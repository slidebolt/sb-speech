// Package hub receives RTP and turns it into text. Each session has its own
// segmenter, and transcribes one utterance at a time.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"strings"
	"sync"
	"time"

	speech "github.com/slidebolt/sb-speech-sdk"
)

const (
	rtpHeaderSize = 12
	queuedMax     = 8
)

// Engine turns 16 kHz mono samples into text.
type Engine interface {
	Transcribe(ctx context.Context, samples []int16) (string, error)
}

// Voice turns text into mono samples at the requested rate: the session's,
// 8000 or 16000.
type Voice interface {
	Synthesize(ctx context.Context, text string, rate int) ([]int16, error)
}

// ErrNoVoice is returned by Say when speech output is not configured.
var ErrNoVoice = errors.New("speech output is not configured")

const (
	// muteTail is how long incoming audio stays ignored after speaking, so the
	// end of our own voice coming back is not transcribed.
	muteTail = 300 * time.Millisecond
)

// senderWait is how long Say waits for the call's audio, whose source is where
// speech is sent. A variable so tests can shorten it.
var senderWait = 3 * time.Second

// Config configures a Hub.
type Config struct {
	RTPHost       string
	PortMin       int
	PortMax       int
	SilenceMillis int
	Engine        Engine
	Voice         Voice // optional; without it Say returns ErrNoVoice
	Publish       func(speech.Event)
}

// Hub owns all sessions.
type Hub struct {
	cfg Config

	mu       sync.Mutex
	sessions map[string]*session
	closed   bool
}

// New creates a Hub.
func New(cfg Config) *Hub {
	return &Hub{cfg: cfg, sessions: map[string]*session{}}
}

type session struct {
	hub    *Hub
	id     string
	format format
	conn   *net.UDPConn
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan []int16
	done   sync.WaitGroup
	once   sync.Once
	seq    int

	mu         sync.Mutex
	sender     *net.UDPAddr
	mutedUntil time.Time
	saySeq     int
	sayCancel  context.CancelFunc
}

// Start opens a session. The caller sends RTP to the returned address.
func (h *Hub) Start(req speech.StartRequest) (speech.StartReply, error) {
	in := req.Input
	f, ok := formatFor(in)
	if !ok {
		return speech.StartReply{}, fmt.Errorf("input %s/%s/%d is not supported", in.Type, in.Codec, in.SampleRate)
	}
	h.mu.Lock()
	defer h.mu.Unlock()
	if h.closed {
		return speech.StartReply{}, errors.New("speech service is shutting down")
	}
	conn, port, err := h.listen()
	if err != nil {
		return speech.StartReply{}, err
	}
	b := make([]byte, 16)
	if _, err := rand.Read(b); err != nil {
		conn.Close()
		return speech.StartReply{}, fmt.Errorf("session id: %w", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	s := &session{hub: h, id: hex.EncodeToString(b), format: f, conn: conn, ctx: ctx, cancel: cancel, queue: make(chan []int16, queuedMax)}
	h.sessions[s.id] = s
	s.done.Add(2)
	go s.readRTP()
	go s.transcribe()
	return speech.StartReply{SessionID: s.id, RTPHost: h.cfg.RTPHost, RTPPort: port}, nil
}

// Say speaks text back to the sender of a session's audio, interrupting anything
// it is still saying. It returns the utterance's seq once accepted.
func (h *Hub) Say(id, text string) (int, error) {
	text = strings.TrimSpace(text)
	switch {
	case h.cfg.Voice == nil:
		return 0, ErrNoVoice
	case text == "":
		return 0, errors.New("nothing to say")
	case len(text) > speech.MaxSayLength:
		return 0, fmt.Errorf("text is longer than %d characters", speech.MaxSayLength)
	}
	h.mu.Lock()
	s := h.sessions[id]
	h.mu.Unlock()
	if s == nil {
		return 0, errors.New("no such session")
	}
	return s.say(text)
}

// Stop ends a session. Stopping one that already ended is not an error.
func (h *Hub) Stop(id string) {
	h.mu.Lock()
	s := h.sessions[id]
	delete(h.sessions, id)
	h.mu.Unlock()
	if s != nil {
		s.stop()
	}
}

// Close ends every session and refuses new ones.
func (h *Hub) Close() {
	h.mu.Lock()
	h.closed = true
	sessions := make([]*session, 0, len(h.sessions))
	for _, s := range h.sessions {
		sessions = append(sessions, s)
	}
	h.sessions = map[string]*session{}
	h.mu.Unlock()
	for _, s := range sessions {
		s.stop()
	}
}

func (s *session) stop() {
	s.once.Do(func() {
		s.cancel()
		s.conn.Close()
	})
	s.done.Wait()
}

func (h *Hub) publish(ev speech.Event) {
	if h.cfg.Publish != nil {
		h.cfg.Publish(ev)
	}
}

func (h *Hub) listen() (*net.UDPConn, int, error) {
	ip := net.ParseIP(h.cfg.RTPHost)
	if ip == nil {
		return nil, 0, fmt.Errorf("invalid RTP host %q", h.cfg.RTPHost)
	}
	var lastErr error
	for port := h.cfg.PortMin; port <= h.cfg.PortMax; port++ {
		conn, err := net.ListenUDP("udp", &net.UDPAddr{IP: ip, Port: port})
		if err == nil {
			return conn, port, nil
		}
		lastErr = err
	}
	return nil, 0, fmt.Errorf("no free RTP port: %w", lastErr)
}

// readRTP splits audio into utterances and queues them. It latches onto the first
// valid sender, so other hosts cannot inject speech.
func (s *session) readRTP() {
	defer s.done.Done()
	defer close(s.queue)
	seg := NewSegmenter(s.hub.cfg.SilenceMillis, s.format.rate)
	decode := s.format.decoder()
	buf := make([]byte, 2048)
	var sender *net.UDPAddr
	last := time.Now()
	gap := time.Duration(s.hub.cfg.SilenceMillis) * time.Millisecond
	for s.ctx.Err() == nil {
		s.conn.SetReadDeadline(time.Now().Add(100 * time.Millisecond))
		n, from, err := s.conn.ReadFromUDP(buf)
		if err != nil {
			var ne net.Error
			if !errors.As(err, &ne) || !ne.Timeout() {
				return
			}
			// Some senders stop sending during silence.
			if time.Since(last) > gap {
				s.enqueue(seg.Flush())
			}
			continue
		}
		payload, pt, ok := rtpPayload(buf[:n])
		if !ok || pt != s.format.payloadType() {
			continue
		}
		if sender == nil {
			sender = from
			s.mu.Lock()
			s.sender = from
			s.mu.Unlock()
		} else if !sender.IP.Equal(from.IP) || sender.Port != from.Port {
			continue
		}
		last = time.Now()
		if s.muted() {
			// Our own voice: drop it, and anything half-heard before it.
			seg.Flush()
			continue
		}
		for _, u := range seg.Write(decode(payload)) {
			s.enqueue(u)
		}
	}
}

func (s *session) enqueue(u []int16) {
	if u == nil {
		return
	}
	select {
	case s.queue <- u:
	case <-s.ctx.Done():
	default:
		log.Printf("sb-speech: %s: dropped an utterance, transcription is falling behind", s.id)
	}
}

func (s *session) transcribe() {
	defer s.done.Done()
	for u := range s.queue {
		if s.format.rate == 8000 {
			u = Upsample2x(u)
		}
		text, err := s.hub.cfg.Engine.Transcribe(s.ctx, u)
		if s.ctx.Err() != nil {
			return
		}
		if err != nil {
			log.Printf("sb-speech: %s: transcribe: %v", s.id, err)
			s.hub.publish(speech.Event{SessionID: s.id, Type: speech.EventError, Error: err.Error()})
			continue
		}
		if text == "" {
			continue
		}
		s.seq++
		s.hub.publish(speech.Event{SessionID: s.id, Type: speech.EventTranscript, Seq: s.seq, Text: text})
	}
}

// rtpPayload returns an RTP packet's payload and payload type.
func rtpPayload(p []byte) ([]byte, byte, bool) {
	if len(p) < rtpHeaderSize || p[0]>>6 != 2 {
		return nil, 0, false
	}
	pt := p[1] & 0x7f
	offset := rtpHeaderSize + int(p[0]&0x0f)*4
	if p[0]&0x10 != 0 {
		if len(p) < offset+4 {
			return nil, 0, false
		}
		offset += 4 + (int(p[offset+2])<<8|int(p[offset+3]))*4
	}
	end := len(p)
	if p[0]&0x20 != 0 {
		end -= int(p[len(p)-1])
	}
	if offset >= end {
		return nil, 0, false
	}
	return p[offset:end], pt, true
}

func (s *session) muted() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return time.Now().Before(s.mutedUntil)
}

func (s *session) say(text string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.ctx.Err() != nil {
		return 0, errors.New("session has ended")
	}
	if s.sayCancel != nil {
		s.sayCancel()
	}
	s.saySeq++
	ctx, cancel := context.WithCancel(s.ctx)
	s.sayCancel = cancel
	s.done.Add(1)
	go s.speak(ctx, s.saySeq, text)
	return s.saySeq, nil
}

// speak synthesizes text and sends it as paced RTP to the session's sender.
func (s *session) speak(ctx context.Context, seq int, text string) {
	defer s.done.Done()
	publish := func(ev speech.Event) {
		if s.ctx.Err() == nil {
			ev.SessionID, ev.Seq = s.id, seq
			s.hub.publish(ev)
		}
	}
	publish(speech.Event{Type: speech.EventSpeaking, Text: text})
	fail := func(err string) { publish(speech.Event{Type: speech.EventSpoken, Text: text, Error: err}) }

	samples, err := s.hub.cfg.Voice.Synthesize(ctx, text, s.format.rate)
	if ctx.Err() != nil {
		fail("interrupted")
		return
	}
	if err != nil {
		log.Printf("sb-speech: %s: synthesize: %v", s.id, err)
		fail(err.Error())
		return
	}
	to := s.waitSender(ctx)
	if to == nil {
		if ctx.Err() != nil {
			fail("interrupted")
		} else {
			fail("no audio from the call to reply to")
		}
		return
	}

	frameSamples := s.format.frameSamples()
	frames := (len(samples) + frameSamples - 1) / frameSamples
	start := time.Now()
	s.mu.Lock()
	s.mutedUntil = start.Add(time.Duration(frames)*20*time.Millisecond + muteTail)
	s.mu.Unlock()

	var hdr [4]byte
	_, _ = rand.Read(hdr[:])
	rtpSeq := binary.BigEndian.Uint16(hdr[:2])
	var tsb, ssrcb [4]byte
	_, _ = rand.Read(tsb[:])
	_, _ = rand.Read(ssrcb[:])
	ts, ssrc := binary.BigEndian.Uint32(tsb[:]), binary.BigEndian.Uint32(ssrcb[:])

	pt, step := s.format.payloadType(), uint32(s.format.frameBytes())
	encode := s.format.encoder()
	packet := make([]byte, 0, rtpHeaderSize+s.format.frameBytes())
	frame := make([]int16, frameSamples)
	for f := 0; f < frames; f++ {
		if wait := time.Until(start.Add(time.Duration(f) * 20 * time.Millisecond)); wait > 0 {
			select {
			case <-time.After(wait):
			case <-ctx.Done():
			}
		}
		if ctx.Err() != nil {
			s.mu.Lock()
			s.mutedUntil = time.Now().Add(muteTail)
			s.mu.Unlock()
			fail("interrupted")
			return
		}
		packet = packet[:rtpHeaderSize]
		packet[0], packet[1] = 0x80, pt
		if f == 0 {
			packet[1] |= 0x80 // marker: start of a talkspurt
		}
		binary.BigEndian.PutUint16(packet[2:], rtpSeq)
		binary.BigEndian.PutUint32(packet[4:], ts)
		binary.BigEndian.PutUint32(packet[8:], ssrc)
		clear(frame)
		copy(frame, samples[min(f*frameSamples, len(samples)):])
		packet = encode(packet, frame)
		if _, err := s.conn.WriteToUDP(packet, to); err != nil && s.ctx.Err() == nil {
			log.Printf("sb-speech: %s: send: %v", s.id, err)
		}
		rtpSeq++
		ts += step
	}
	publish(speech.Event{Type: speech.EventSpoken, Text: text})
}

func (s *session) waitSender(ctx context.Context) *net.UDPAddr {
	deadline := time.Now().Add(senderWait)
	for {
		s.mu.Lock()
		to := s.sender
		s.mu.Unlock()
		if to != nil || time.Now().After(deadline) {
			return to
		}
		select {
		case <-time.After(20 * time.Millisecond):
		case <-ctx.Done():
			return nil
		}
	}
}
