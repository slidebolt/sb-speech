// Package hub receives RTP and turns it into text. Each session has its own
// segmenter, and transcribes one utterance at a time.
package hub

import (
	"context"
	"crypto/rand"
	"encoding/hex"
	"errors"
	"fmt"
	"log"
	"net"
	"sync"
	"time"

	speech "github.com/slidebolt/sb-speech-sdk"
)

const (
	rtpHeaderSize   = 12
	payloadTypePCMU = 0
	queuedMax       = 8
)

// Engine turns 16 kHz mono samples into text.
type Engine interface {
	Transcribe(ctx context.Context, samples []int16) (string, error)
}

// Config configures a Hub.
type Config struct {
	RTPHost       string
	PortMin       int
	PortMax       int
	SilenceMillis int
	Engine        Engine
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
	conn   *net.UDPConn
	ctx    context.Context
	cancel context.CancelFunc
	queue  chan []int16
	done   sync.WaitGroup
	once   sync.Once
	seq    int
}

// Start opens a session. The caller sends RTP to the returned address.
func (h *Hub) Start(req speech.StartRequest) (speech.StartReply, error) {
	in := req.Input
	if in.Type != speech.InputRTP || in.Codec != speech.CodecPCMU || in.SampleRate != 8000 {
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
	s := &session{hub: h, id: hex.EncodeToString(b), conn: conn, ctx: ctx, cancel: cancel, queue: make(chan []int16, queuedMax)}
	h.sessions[s.id] = s
	s.done.Add(2)
	go s.readRTP()
	go s.transcribe()
	return speech.StartReply{SessionID: s.id, RTPHost: h.cfg.RTPHost, RTPPort: port}, nil
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
	seg := NewSegmenter(s.hub.cfg.SilenceMillis)
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
		payload, ok := rtpPayload(buf[:n])
		if !ok {
			continue
		}
		if sender == nil {
			sender = from
		} else if !sender.IP.Equal(from.IP) || sender.Port != from.Port {
			continue
		}
		last = time.Now()
		for _, u := range seg.Write(DecodeULaw(payload)) {
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
		text, err := s.hub.cfg.Engine.Transcribe(s.ctx, Upsample2x(u))
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

func rtpPayload(p []byte) ([]byte, bool) {
	if len(p) < rtpHeaderSize || p[0]>>6 != 2 || p[1]&0x7f != payloadTypePCMU {
		return nil, false
	}
	offset := rtpHeaderSize + int(p[0]&0x0f)*4
	if p[0]&0x10 != 0 {
		if len(p) < offset+4 {
			return nil, false
		}
		offset += 4 + (int(p[offset+2])<<8|int(p[offset+3]))*4
	}
	end := len(p)
	if p[0]&0x20 != 0 {
		end -= int(p[len(p)-1])
	}
	if offset >= end {
		return nil, false
	}
	return p[offset:end], true
}
