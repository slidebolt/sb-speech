package app

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	messenger "github.com/slidebolt/sb-messenger-sdk"
	runtime "github.com/slidebolt/sb-runtime"
	speech "github.com/slidebolt/sb-speech-sdk"
)

type resourceEvent struct {
	id    string
	state string
}

type resourceRecorder struct{ events []resourceEvent }

func (r *resourceRecorder) Ready(id string) {
	r.events = append(r.events, resourceEvent{id: id, state: "ready"})
}
func (r *resourceRecorder) Degraded(id, _, _ string, _ bool) {
	r.events = append(r.events, resourceEvent{id: id, state: "degraded"})
}

var _ runtime.ResourceReporter = (*resourceRecorder)(nil)

func TestHello(t *testing.T) {
	h := New().Hello()
	if h.ID != ServiceID || len(h.DependsOn) != 1 {
		t.Fatalf("hello = %+v", h)
	}
	if h.Setup == nil || h.Setup.Mode != "dependency" {
		t.Fatalf("setup = %+v", h.Setup)
	}
	if len(h.Setup.Resources) != 2 || h.Setup.Resources[0].ID != transcriptionResource || h.Setup.Resources[1].ID != synthesisResource {
		t.Fatalf("resources = %+v", h.Setup.Resources)
	}
	if err := h.Validate(); err != nil {
		t.Fatal(err)
	}
}

func TestConfiguredProviderReportsHealth(t *testing.T) {
	mux := http.NewServeMux()
	mux.HandleFunc("GET /v1/capabilities", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"contractVersion":"1","capabilities":["transcription"],"audioTypes":["audio/wav"]}`))
	})
	mux.HandleFunc("GET /v1/health", func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"ready":true}`))
	})
	server := httptest.NewServer(mux)
	t.Setenv("SPEECH_TRANSCRIBER_URL", server.URL)
	msg, payload, err := messenger.MockWithPayload()
	if err != nil {
		t.Fatal(err)
	}
	defer msg.Close()
	recorder := &resourceRecorder{}
	a := New()
	a.SetResourceReporter(recorder)
	if _, err := a.OnStart(map[string]json.RawMessage{"messenger": payload}); err != nil {
		t.Fatal(err)
	}
	server.Close()
	if err := a.checkProviders(); err == nil {
		t.Fatal("closed provider unexpectedly healthy")
	}
	_ = a.OnShutdown()
	if len(recorder.events) != 2 || recorder.events[0] != (resourceEvent{transcriptionResource, "ready"}) || recorder.events[1] != (resourceEvent{transcriptionResource, "degraded"}) {
		t.Fatalf("events = %+v", recorder.events)
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
