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
	if h.Setup == nil || h.Setup.Mode != "dependency" {
		t.Fatalf("setup = %+v", h.Setup)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestOnStart_TransportOnlyReportsUnavailable(t *testing.T) {
	t.Setenv("SPEECH_RTP_HOST", "127.0.0.1")
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
	capabilities, err := client.Capabilities()
	if err != nil || capabilities.Transcription.Available || capabilities.Synthesis.Available {
		t.Fatalf("capabilities = %+v, %v", capabilities, err)
	}
	if _, err := client.Start(speech.StartRequest{}); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("start err = %v", err)
	}
	if err := client.Stop("x"); err != nil {
		t.Fatalf("stop err = %v", err)
	}
	if _, err := client.Say("x", "hi"); err == nil || !strings.Contains(err.Error(), "not configured") {
		t.Fatalf("say err = %v", err)
	}
}

func TestCapabilitiesReportServiceConfigurationFailure(t *testing.T) {
	t.Setenv("SPEECH_RTP_PORT_MIN", "70000")
	msg, payload, err := messenger.MockWithPayload()
	if err != nil {
		t.Fatal(err)
	}
	defer msg.Close()
	a := New()
	if _, err := a.OnStart(map[string]json.RawMessage{"messenger": payload}); err != nil {
		t.Fatal(err)
	}
	defer a.OnShutdown()
	capabilities, err := speech.NewClient(msg).Capabilities()
	if err != nil || !strings.Contains(capabilities.Transcription.Error, "disabled") || !strings.Contains(capabilities.Synthesis.Error, "disabled") {
		t.Fatalf("capabilities = %+v, %v", capabilities, err)
	}
}
