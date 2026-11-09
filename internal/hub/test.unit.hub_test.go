package hub

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"sync"
	"testing"
	"time"

	speech "github.com/slidebolt/sb-speech-sdk"
)

// Telephone-rate sizes the PCMU tests are written in.
const (
	rate         = 8000
	frameSamples = rate * frameMillis / 1000
	minVoiced    = minVoicedMillis * rate / 1000
	maxUtterance = maxUtteranceSec * rate
)

type fakeEngine struct {
	mu    sync.Mutex
	calls [][]int16
	text  string
	err   error
	block chan struct{}
}

func (f *fakeEngine) Transcribe(ctx context.Context, samples []int16) (string, error) {
	f.mu.Lock()
	f.calls = append(f.calls, samples)
	text, err, block := f.text, f.err, f.block
	f.mu.Unlock()
	if block != nil {
		select {
		case <-block:
		case <-ctx.Done():
			return "", ctx.Err()
		}
	}
	return text, err
}

func (f *fakeEngine) count() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

type harness struct {
	t      *testing.T
	hub    *Hub
	engine *fakeEngine

	mu     sync.Mutex
	events []speech.Event
}

func newHarness(t *testing.T) *harness {
	t.Helper()
	h := &harness{t: t, engine: &fakeEngine{text: "turn the lights on"}}
	h.hub = New(Config{
		RTPHost: "127.0.0.1", PortMin: 43000, PortMax: 43050, SilenceMillis: 100,
		Engine: h.engine,
		Publish: func(ev speech.Event) {
			h.mu.Lock()
			h.events = append(h.events, ev)
			h.mu.Unlock()
		},
	})
	t.Cleanup(h.hub.Close)
	return h
}

func (h *harness) start() speech.StartReply {
	return h.startInput(speech.Input{Type: speech.InputRTP, Codec: speech.CodecPCMU, SampleRate: 8000})
}

// startWide opens a wideband G.722 session, as asterisk-host does.
func (h *harness) startWide() speech.StartReply {
	return h.startInput(speech.Input{Type: speech.InputRTP, Codec: speech.CodecG722, SampleRate: 16000})
}

func (h *harness) startInput(in speech.Input) speech.StartReply {
	h.t.Helper()
	r, err := h.hub.Start(speech.StartRequest{Input: in})
	if err != nil {
		h.t.Fatal(err)
	}
	return r
}

func (h *harness) waitEvent(typ string) speech.Event {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		h.mu.Lock()
		for _, ev := range h.events {
			if ev.Type == typ {
				h.mu.Unlock()
				return ev
			}
		}
		h.mu.Unlock()
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("no %s event", typ)
	return speech.Event{}
}

func (h *harness) transcripts() []speech.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []speech.Event
	for _, ev := range h.events {
		if ev.Type == speech.EventTranscript {
			out = append(out, ev)
		}
	}
	return out
}

// sendAudio sends RTP of a tone at the given amplitude, faster than real time.
func sendAudio(t *testing.T, conn *net.UDPConn, amplitude float64, millis int, pt byte) {
	t.Helper()
	for f := range millis / 20 {
		p := make([]byte, rtpHeaderSize+frameSamples)
		p[0], p[1] = 0x80, pt
		binary.BigEndian.PutUint16(p[2:], uint16(f))
		for i := range frameSamples {
			v := amplitude * math.Sin(2*math.Pi*440*float64(f*frameSamples+i)/rate)
			p[rtpHeaderSize+i] = encodeULaw(int16(v))
		}
		if _, err := conn.Write(p); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

// sendWide sends G.722 RTP of a 16 kHz tone, faster than real time.
func sendWide(t *testing.T, conn *net.UDPConn, amplitude float64, millis int) {
	t.Helper()
	enc := NewG722Encoder()
	for f := range millis / 20 {
		tone := make([]int16, 320)
		for i := range tone {
			tone[i] = int16(amplitude * math.Sin(2*math.Pi*440*float64(f*320+i)/16000))
		}
		p := make([]byte, rtpHeaderSize, rtpHeaderSize+160)
		p[0], p[1] = 0x80, payloadTypeG722
		binary.BigEndian.PutUint16(p[2:], uint16(f))
		binary.BigEndian.PutUint32(p[4:], uint32(f*160))
		p = enc.Encode(p, tone)
		if _, err := conn.Write(p); err != nil {
			t.Fatal(err)
		}
		time.Sleep(time.Millisecond)
	}
}

func dial(t *testing.T, r speech.StartReply) *net.UDPConn {
	t.Helper()
	conn, err := net.DialUDP("udp", nil, &net.UDPAddr{IP: net.ParseIP(r.RTPHost), Port: r.RTPPort})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { conn.Close() })
	return conn
}

func TestSpeechThenSilenceProducesTranscript(t *testing.T) {
	h := newHarness(t)
	r := h.start()
	conn := dial(t, r)
	sendAudio(t, conn, 0, 200, 0)
	sendAudio(t, conn, 8000, 500, 0)
	sendAudio(t, conn, 0, 300, 0)
	ev := h.waitEvent(speech.EventTranscript)
	if ev.Text != "turn the lights on" || ev.Seq != 1 || ev.SessionID != r.SessionID {
		t.Fatalf("event = %+v", ev)
	}
	if got := h.engine.calls[0]; len(got) < 2*minVoiced {
		t.Fatalf("engine got %d samples, want 16 kHz audio", len(got))
	}
}

func TestSeqRisesSoRepeatedPhraseIsAChange(t *testing.T) {
	h := newHarness(t)
	conn := dial(t, h.start())
	for range 2 {
		sendAudio(t, conn, 8000, 400, 0)
		sendAudio(t, conn, 0, 300, 0)
	}
	deadline := time.Now().Add(3 * time.Second)
	for len(h.transcripts()) < 2 && time.Now().Before(deadline) {
		time.Sleep(5 * time.Millisecond)
	}
	got := h.transcripts()
	if len(got) != 2 || got[1].Seq != 2 || got[0].Text != got[1].Text {
		t.Fatalf("events = %+v", got)
	}
}

func TestSilenceAndShortNoiseAreIgnored(t *testing.T) {
	h := newHarness(t)
	conn := dial(t, h.start())
	sendAudio(t, conn, 0, 500, 0)
	sendAudio(t, conn, 8000, 100, 0) // a click, too short to be speech
	sendAudio(t, conn, 0, 400, 0)
	time.Sleep(150 * time.Millisecond)
	if h.engine.count() != 0 {
		t.Fatal("engine should not run on silence or a click")
	}
}

func TestSenderStoppingEndsUtterance(t *testing.T) {
	h := newHarness(t)
	conn := dial(t, h.start())
	sendAudio(t, conn, 8000, 500, 0)
	h.waitEvent(speech.EventTranscript) // no silence was ever sent
}

func TestOtherSendersAndCodecsAreDropped(t *testing.T) {
	h := newHarness(t)
	r := h.start()
	first := dial(t, r)
	sendAudio(t, first, 0, 100, 0)
	sendAudio(t, dial(t, r), 8000, 500, 0) // a second sender
	sendAudio(t, first, 8000, 500, 111)    // the wrong codec
	time.Sleep(300 * time.Millisecond)
	if h.engine.count() != 0 {
		t.Fatal("injected audio reached the engine")
	}
}

func TestEmptyTextProducesNoTranscript(t *testing.T) {
	h := newHarness(t)
	h.engine.text = ""
	conn := dial(t, h.start())
	sendAudio(t, conn, 8000, 400, 0)
	sendAudio(t, conn, 0, 300, 0)
	h.waitFor(func() bool { return h.engine.count() == 1 })
	if got := h.transcripts(); len(got) != 0 {
		t.Fatalf("events = %+v", got)
	}
}

func (h *harness) waitFor(ok func() bool) {
	h.t.Helper()
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if ok() {
			return
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatal("timed out")
}

func TestEngineFailureIsReported(t *testing.T) {
	h := newHarness(t)
	h.engine.err = errors.New("model broken")
	conn := dial(t, h.start())
	sendAudio(t, conn, 8000, 400, 0)
	sendAudio(t, conn, 0, 300, 0)
	if ev := h.waitEvent(speech.EventError); ev.Error != "model broken" {
		t.Fatalf("event = %+v", ev)
	}
}

func TestStopEndsSessionEvenMidTranscription(t *testing.T) {
	h := newHarness(t)
	h.engine.block = make(chan struct{})
	r := h.start()
	conn := dial(t, r)
	sendAudio(t, conn, 8000, 400, 0)
	sendAudio(t, conn, 0, 300, 0)
	h.waitFor(func() bool { return h.engine.count() == 1 })
	done := make(chan struct{})
	go func() { h.hub.Stop(r.SessionID); close(done) }()
	select {
	case <-done:
	case <-time.After(3 * time.Second):
		t.Fatal("stop hung")
	}
	h.hub.Stop(r.SessionID) // a repeat is safe
	h.hub.Stop("nope")
	if len(h.transcripts()) != 0 {
		t.Fatal("no text should appear after stopping")
	}
}

func TestStartValidation(t *testing.T) {
	h := newHarness(t)
	bad := []speech.Input{{Type: "rtsp", Codec: speech.CodecPCMU, SampleRate: 8000}, {Type: speech.InputRTP, Codec: "opus", SampleRate: 8000}, {Type: speech.InputRTP, Codec: speech.CodecPCMU, SampleRate: 48000}}
	for _, in := range bad {
		if _, err := h.hub.Start(speech.StartRequest{Input: in}); err == nil {
			t.Errorf("%+v should fail", in)
		}
	}
	h.hub.Close()
	if _, err := h.hub.Start(speech.StartRequest{Input: speech.Input{Type: speech.InputRTP, Codec: speech.CodecPCMU, SampleRate: 8000}}); err == nil {
		t.Error("start after close should fail")
	}
}

func TestSegmenter_MaxLengthSplitsLongSpeech(t *testing.T) {
	s := NewSegmenter(700, rate)
	loud := make([]int16, frameSamples)
	for i := range loud {
		loud[i] = 8000
		if i%2 == 1 {
			loud[i] = -8000
		}
	}
	var got [][]int16
	for range maxUtterance/frameSamples + 20 {
		got = append(got, s.Write(loud)...)
	}
	if len(got) != 1 || len(got[0]) < maxUtterance {
		t.Fatalf("utterances = %d", len(got))
	}
}

func TestSegmenter_NoiseFloorAdapts(t *testing.T) {
	s := NewSegmenter(100, rate)
	noise := make([]int16, frameSamples)
	for i := range noise {
		noise[i] = 200
		if i%2 == 1 {
			noise[i] = -200
		}
	}
	for range 500 {
		if got := s.Write(noise); len(got) != 0 {
			t.Fatal("steady room noise must not count as speech")
		}
	}
	if s.inSpeech {
		t.Fatal("noise started an utterance")
	}
}

func TestDecodeULaw(t *testing.T) {
	cases := map[int16]int16{0: 0, 8000: 8000, -8000: -8000, 1000: 1000}
	for in, want := range cases {
		got := DecodeULaw([]byte{encodeULaw(in)})[0]
		tolerance := max(int(math.Abs(float64(want)))/8, 40)
		if d := int(got) - int(want); d > tolerance || d < -tolerance {
			t.Errorf("%d decoded to %d", in, got)
		}
	}
}

func TestUpsample2x(t *testing.T) {
	got := Upsample2x([]int16{0, 100})
	want := []int16{0, 50, 100, 100}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("got %v, want %v", got, want)
		}
	}
	if Upsample2x(nil) != nil {
		t.Fatal("nil input")
	}
}

func TestWidebandAudioReachesEngineAt16kHz(t *testing.T) {
	h := newHarness(t)
	conn := dial(t, h.startWide())
	sendWide(t, conn, 0, 200)
	sendWide(t, conn, 8000, 500)
	sendWide(t, conn, 0, 300)
	h.waitEvent(speech.EventTranscript)
	got := h.engine.calls[0]
	// 500 ms of speech at 16 kHz is 8000 samples, plus pre-roll and trailing
	// silence; upsampling it again would double that.
	if len(got) < 8000 || len(got) > 16000 {
		t.Fatalf("engine got %d samples; want the 16 kHz audio as sent", len(got))
	}
	if rms(got[len(got)/3:len(got)/2]) < 4000 {
		t.Fatal("engine audio is not the tone: G.722 decoded wrongly")
	}
}

func TestWidebandDropsMuLaw(t *testing.T) {
	h := newHarness(t)
	conn := dial(t, h.startWide())
	sendAudio(t, conn, 8000, 500, payloadTypePCMU) // narrowband on a wideband session
	time.Sleep(300 * time.Millisecond)
	if h.engine.count() != 0 {
		t.Fatal("mu-law audio reached a wideband session's engine")
	}
}
