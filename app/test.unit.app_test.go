package app

import (
	"encoding/json"
	"strings"
	"testing"

	messenger "github.com/slidebolt/sb-messenger-sdk"
	speech "github.com/slidebolt/sb-speech-sdk"
)

func TestHello(t *testing.T) {
	h := New().Hello()
	if h.ID != ServiceID || len(h.DependsOn) != 1 {
		t.Fatalf("hello = %+v", h)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

// A failed OnStart would abort startAll in sb-manager and stop everything.
func TestOnStart_MissingWhisperStaysUpAndReportsDisabled(t *testing.T) {
	t.Setenv("SPEECH_RTP_HOST", "127.0.0.1")
	t.Setenv("SPEECH_WHISPER_PATH", "no-such-binary")
	msg, payload, err := messenger.MockWithPayload()
	if err != nil {
		t.Fatal(err)
	}
	defer msg.Close()
	a := New()
	if _, err := a.OnStart(map[string]json.RawMessage{"messenger": payload}); err != nil {
		t.Fatalf("OnStart must not fail: %v", err)
	}
	defer a.OnShutdown()
	client := speech.NewClient(msg)
	if _, err := client.Start(speech.StartRequest{}); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("start err = %v", err)
	}
	if err := client.Stop("x"); err == nil || !strings.Contains(err.Error(), "disabled") {
		t.Fatalf("stop err = %v", err)
	}
}
