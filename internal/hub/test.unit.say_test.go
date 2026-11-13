package hub

import (
	"context"
	"encoding/binary"
	"errors"
	"math"
	"net"
	"strings"
	"sync"
	"testing"
	"time"

	speech "github.com/slidebolt/sb-speech-sdk"
)

// fakeVoice returns a 1 kHz tone of millis length for any text, at the
// requested rate.
type fakeVoice struct {
	mu     sync.Mutex
	millis int
	texts  []string
	rates  []int
	err    error
	gap    time.Duration // pause after the second piece
}

func (v *fakeVoice) Synthesize(ctx context.Context, text string, rate int, emit func([]int16)) error {
	v.mu.Lock()
	v.texts = append(v.texts, text)
	v.rates = append(v.rates, rate)
	millis, err, gap := v.millis, v.err, v.gap
	v.mu.Unlock()
	if err != nil {
		return err
	}
	out := make([]int16, millis*rate/1000)
	for i := range out {
		out[i] = int16(8000 * math.Sin(2*math.Pi*1000*float64(i)/float64(rate)))
	}
	// In uneven pieces, as a real voice produces them.
	half := len(out) / 2
	for _, p := range [][]int16{out[:half/3], out[half/3 : half], out[half:]} {
		if len(p) == 0 {
			continue
		}
		emit(p)
		if gap > 0 && &p[0] == &out[half/3] {
			select {
			case <-time.After(gap): // the voice falls behind mid-reply
			case <-ctx.Done():
				return ctx.Err()
			}
		}
	}
	return nil
}

func newSayHarness(t *testing.T, millis int) (*harness, *fakeVoice) {
	t.Helper()
	h := newHarness(t)
	v := &fakeVoice{millis: millis}
	h.hub.cfg.Voice = v
	return h, v
}

type rtpPacket struct {
	at       time.Time
	marker   bool
	pt       byte
	seq      uint16
	ts, ssrc uint32
	payload  []byte
}

// readPackets collects RTP arriving on conn until quiet for gap.
func readPackets(t *testing.T, conn *net.UDPConn, gap time.Duration) []rtpPacket {
	t.Helper()
	var out []rtpPacket
	buf := make([]byte, 2048)
	for {
		conn.SetReadDeadline(time.Now().Add(gap))
		n, err := conn.Read(buf)
		if err != nil {
			return out
		}
		p := buf[:n]
		out = append(out, rtpPacket{
			at: time.Now(), marker: p[1]&0x80 != 0, pt: p[1] & 0x7f,
			seq: binary.BigEndian.Uint16(p[2:]), ts: binary.BigEndian.Uint32(p[4:]), ssrc: binary.BigEndian.Uint32(p[8:]),
			payload: append([]byte(nil), p[rtpHeaderSize:]...),
		})
	}
}

func (h *harness) eventsOf(typ string) []speech.Event {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []speech.Event
	for _, ev := range h.events {
		if ev.Type == typ {
			out = append(out, ev)
		}
	}
	return out
}

func (h *harness) waitEvents(typ string, n int) []speech.Event {
	h.t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		if evs := h.eventsOf(typ); len(evs) >= n {
			return evs
		}
		time.Sleep(5 * time.Millisecond)
	}
	h.t.Fatalf("wanted %d %s events, got %d", n, typ, len(h.eventsOf(typ)))
	return nil
}

func TestSaySendsPacedRTPToTheCallsSender(t *testing.T) {
	h, _ := newSayHarness(t, 400)
	r := h.start()
	conn := dial(t, r)
	sendAudio(t, conn, 0, 60, payloadTypePCMU) // silence, so the hub knows who to reply to
	time.Sleep(50 * time.Millisecond)

	seq, err := h.hub.Say(r.SessionID, "eight lights are on")
	if err != nil || seq != 1 {
		t.Fatalf("Say: seq=%d err=%v", seq, err)
	}
	pkts := readPackets(t, conn, 300*time.Millisecond)
	if len(pkts) != 20 {
		t.Fatalf("400 ms of speech should be 20 packets, got %d", len(pkts))
	}
	for i, p := range pkts {
		if p.pt != payloadTypePCMU || len(p.payload) != frameSamples || p.ssrc != pkts[0].ssrc {
			t.Fatalf("packet %d: pt=%d len=%d ssrc=%x", i, p.pt, len(p.payload), p.ssrc)
		}
		if p.marker != (i == 0) {
			t.Fatalf("packet %d: marker=%v", i, p.marker)
		}
		if i > 0 && (p.seq != pkts[i-1].seq+1 || p.ts != pkts[i-1].ts+frameSamples) {
			t.Fatalf("packet %d: seq/ts not consecutive", i)
		}
	}
	// Real-time pacing: 20 packets span about 380 ms, not a burst.
	if span := pkts[len(pkts)-1].at.Sub(pkts[0].at); span < 300*time.Millisecond || span > 600*time.Millisecond {
		t.Fatalf("20 packets took %v; want about 380ms", span)
	}
	// The audio is the tone, not silence.
	if rms(DecodeULaw(pkts[5].payload)) < 2000 {
		t.Fatal("sent audio is silent")
	}
	ev := h.waitEvents(speech.EventSpoken, 1)[0]
	if ev.Seq != 1 || ev.Error != "" || ev.Text != "eight lights are on" {
		t.Fatalf("spoken event: %+v", ev)
	}
	if sp := h.eventsOf(speech.EventSpeaking); len(sp) != 1 || sp[0].Seq != 1 {
		t.Fatalf("speaking events: %+v", sp)
	}
}

func TestSayNeverSendsToAnotherHost(t *testing.T) {
	h, _ := newSayHarness(t, 200)
	r := h.start()
	caller := dial(t, r)
	sendAudio(t, caller, 0, 40, payloadTypePCMU)
	time.Sleep(30 * time.Millisecond)
	intruder := dial(t, r)
	sendAudio(t, intruder, 0, 40, payloadTypePCMU)

	if _, err := h.hub.Say(r.SessionID, "hello"); err != nil {
		t.Fatal(err)
	}
	if got := readPackets(t, intruder, 400*time.Millisecond); len(got) != 0 {
		t.Fatalf("intruder received %d packets", len(got))
	}
	h.waitEvents(speech.EventSpoken, 1)
}

func TestOwnVoiceIsNotTranscribed(t *testing.T) {
	h, _ := newSayHarness(t, 500)
	r := h.start()
	conn := dial(t, r)
	sendAudio(t, conn, 0, 40, payloadTypePCMU)
	time.Sleep(30 * time.Millisecond)

	if _, err := h.hub.Say(r.SessionID, "hello"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(50 * time.Millisecond)
	// Loud audio while speaking, as an echo of our own voice would be.
	sendAudio(t, conn, 8000, 300, payloadTypePCMU)
	sendAudio(t, conn, 0, 300, payloadTypePCMU)
	h.waitEvents(speech.EventSpoken, 1)
	time.Sleep(muteTail + 100*time.Millisecond)
	if n := h.engine.count(); n != 0 {
		t.Fatalf("own voice reached the engine %d times", n)
	}
	// Once finished, speech is heard again.
	sendAudio(t, conn, 8000, 300, payloadTypePCMU)
	sendAudio(t, conn, 0, 300, payloadTypePCMU)
	h.waitEvent(speech.EventTranscript)
}

func TestNewSayInterruptsTheLastOne(t *testing.T) {
	h, _ := newSayHarness(t, 2000)
	r := h.start()
	conn := dial(t, r)
	sendAudio(t, conn, 0, 40, payloadTypePCMU)
	time.Sleep(30 * time.Millisecond)

	if _, err := h.hub.Say(r.SessionID, "a long answer"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	v := h.hub.cfg.Voice.(*fakeVoice)
	v.mu.Lock()
	v.millis = 100
	v.mu.Unlock()
	if seq, err := h.hub.Say(r.SessionID, "short"); err != nil || seq != 2 {
		t.Fatalf("second Say: seq=%d err=%v", seq, err)
	}
	evs := h.waitEvents(speech.EventSpoken, 2)
	if evs[0].Seq != 1 || evs[0].Error != "interrupted" || evs[1].Seq != 2 || evs[1].Error != "" {
		t.Fatalf("spoken events: %+v", evs)
	}
}

func TestSayRefusals(t *testing.T) {
	h := newHarness(t)
	r := h.start()
	if _, err := h.hub.Say(r.SessionID, "hi"); !errors.Is(err, ErrNoVoice) {
		t.Fatalf("no voice: got %v", err)
	}
	h.hub.cfg.Voice = &fakeVoice{millis: 100}
	for name, c := range map[string]struct{ id, text, want string }{
		"empty":   {r.SessionID, "  ", "nothing to say"},
		"long":    {r.SessionID, strings.Repeat("a", speech.MaxSayLength+1), "longer than"},
		"unknown": {"nope", "hi", "no such session"},
	} {
		if _, err := h.hub.Say(c.id, c.text); err == nil || !strings.Contains(err.Error(), c.want) {
			t.Errorf("%s: got %v, want %q", name, err, c.want)
		}
	}
}

func TestSayWithoutCallAudioReportsIt(t *testing.T) {
	old := senderWait
	senderWait = 200 * time.Millisecond
	t.Cleanup(func() { senderWait = old })
	h, _ := newSayHarness(t, 100)
	r := h.start()
	if _, err := h.hub.Say(r.SessionID, "hello"); err != nil {
		t.Fatal(err)
	}
	ev := h.waitEvents(speech.EventSpoken, 1)[0]
	if !strings.Contains(ev.Error, "no audio") {
		t.Fatalf("spoken event: %+v", ev)
	}
}

func TestSynthesisFailureIsReported(t *testing.T) {
	h, v := newSayHarness(t, 100)
	v.mu.Lock()
	v.err = errors.New("voice broke")
	v.mu.Unlock()
	r := h.start()
	sendAudio(t, dial(t, r), 0, 40, payloadTypePCMU)
	if _, err := h.hub.Say(r.SessionID, "hello"); err != nil {
		t.Fatal(err)
	}
	if ev := h.waitEvents(speech.EventSpoken, 1)[0]; ev.Error != "voice broke" {
		t.Fatalf("spoken event: %+v", ev)
	}
}

func TestStopDuringSpeechEndsCleanly(t *testing.T) {
	h, _ := newSayHarness(t, 3000)
	r := h.start()
	conn := dial(t, r)
	sendAudio(t, conn, 0, 40, payloadTypePCMU)
	time.Sleep(30 * time.Millisecond)
	if _, err := h.hub.Say(r.SessionID, "long"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(100 * time.Millisecond)
	done := make(chan struct{})
	go func() { h.hub.Stop(r.SessionID); close(done) }()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Stop hung while speaking")
	}
}

func TestWidebandSaySendsG722(t *testing.T) {
	h, v := newSayHarness(t, 400)
	r := h.startWide()
	conn := dial(t, r)
	sendWide(t, conn, 0, 60)
	time.Sleep(50 * time.Millisecond)

	if _, err := h.hub.Say(r.SessionID, "the basement is on"); err != nil {
		t.Fatal(err)
	}
	pkts := readPackets(t, conn, 300*time.Millisecond)
	if len(pkts) != 20 {
		t.Fatalf("400 ms of speech should be 20 packets, got %d", len(pkts))
	}
	dec := NewG722Decoder()
	var heard []int16
	for i, p := range pkts {
		// G.722 is 16 kHz audio, but its RTP clock is 8 kHz (RFC 3551).
		if p.pt != payloadTypeG722 || len(p.payload) != 160 {
			t.Fatalf("packet %d: pt=%d len=%d; want G.722, 160 bytes", i, p.pt, len(p.payload))
		}
		if i > 0 && p.ts != pkts[i-1].ts+160 {
			t.Fatalf("packet %d: timestamp step %d, want 160", i, p.ts-pkts[i-1].ts)
		}
		heard = dec.Decode(heard, p.payload)
	}
	if rms(heard[1600:4800]) < 4000 {
		t.Fatal("sent audio does not decode to the tone")
	}
	v.mu.Lock()
	defer v.mu.Unlock()
	if len(v.rates) != 1 || v.rates[0] != 16000 {
		t.Fatalf("voice asked for rates %v, want 16000", v.rates)
	}
}

// When the voice falls behind mid-reply, speech pauses and then carries on at
// the normal pace, with the timestamp moved on over the gap: no burst of late
// packets for the phone to rush through.
func TestVoiceFallingBehindPausesInsteadOfBursting(t *testing.T) {
	h, v := newSayHarness(t, 600)
	v.mu.Lock()
	v.gap = 600 * time.Millisecond // longer than the 300 ms already buffered
	v.mu.Unlock()
	r := h.start()
	conn := dial(t, r)
	sendAudio(t, conn, 0, 40, payloadTypePCMU)
	time.Sleep(50 * time.Millisecond)
	if _, err := h.hub.Say(r.SessionID, "a long reply"); err != nil {
		t.Fatal(err)
	}
	pkts := readPackets(t, conn, 400*time.Millisecond)
	if len(pkts) != 30 {
		t.Fatalf("600 ms of speech should be 30 packets, got %d", len(pkts))
	}
	resumed := -1
	for i := 1; i < len(pkts); i++ {
		gap := pkts[i].at.Sub(pkts[i-1].at)
		if gap > 100*time.Millisecond {
			resumed = i
			// The timestamp covers the pause, and a new talkspurt begins.
			if d := pkts[i].ts - pkts[i-1].ts; d < 160*5 {
				t.Fatalf("timestamp moved %d over a %v pause", d, gap)
			}
			if !pkts[i].marker {
				t.Fatal("no marker on the packet after the pause")
			}
			continue
		}
		if pkts[i].ts != pkts[i-1].ts+160 {
			t.Fatalf("packet %d: timestamp step %d without a pause", i, pkts[i].ts-pkts[i-1].ts)
		}
	}
	if resumed < 0 {
		t.Fatal("the voice paused, but the packets show no pause")
	}
	// After the pause, packets are paced, not sent as a burst.
	rest := pkts[resumed:]
	if span := rest[len(rest)-1].at.Sub(rest[0].at); span < time.Duration(len(rest)-2)*15*time.Millisecond {
		t.Fatalf("%d packets after the pause took only %v: a burst", len(rest), span)
	}
}
