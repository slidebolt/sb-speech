package app

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"strconv"
	"strings"
	"time"

	contract "github.com/slidebolt/sb-contract"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	providercontract "github.com/slidebolt/sb-speech-provider-sdk"
	speech "github.com/slidebolt/sb-speech-sdk"
	"github.com/slidebolt/sb-speech/internal/hub"
	"github.com/slidebolt/sb-speech/internal/provider"
)

// ServiceID is the identifier other binaries use in DependsOn.
const ServiceID = "speech"

// App is the sb-speech service.
type App struct {
	msg         messenger.Messenger
	subs        []messenger.Subscription
	hub         *hub.Hub
	disabled    error
	transcriber *provider.Client
	synthesizer *provider.Client
}

func New() *App {
	return &App{}
}

func (a *App) Hello() contract.HelloResponse {
	return contract.HelloResponse{
		ID:              ServiceID,
		Kind:            contract.KindService,
		ContractVersion: contract.ContractVersion,
		DependsOn:       []string{"messenger"},
		Setup: &contract.SetupDefinition{Mode: contract.SetupDependency, Category: "Voice", Fields: []contract.SetupField{
			{Key: "rtpHost", Environment: "SPEECH_RTP_HOST", Name: "RTP host", Control: contract.SetupText, Default: json.RawMessage(`"127.0.0.1"`), Advanced: true},
			{Key: "transcriberURL", Environment: "SPEECH_TRANSCRIBER_URL", Name: "Transcription provider URL", Description: "Optional generic speech-provider endpoint.", Control: contract.SetupText, Format: "url", Advanced: true},
			{Key: "transcriberToken", Environment: "SPEECH_TRANSCRIBER_TOKEN", Name: "Transcription provider token", Control: contract.SetupSecret, Sensitive: true, Advanced: true},
			{Key: "synthesizerURL", Environment: "SPEECH_SYNTHESIZER_URL", Name: "Synthesis provider URL", Description: "Optional generic speech-provider endpoint.", Control: contract.SetupText, Format: "url", Advanced: true},
			{Key: "synthesizerToken", Environment: "SPEECH_SYNTHESIZER_TOKEN", Name: "Synthesis provider token", Control: contract.SetupSecret, Sensitive: true, Advanced: true},
		}},
	}
}

// OnStart stays up when it cannot serve. A failed start aborts sb-manager's
// startAll, which would stop every other binary.
func (a *App) OnStart(deps map[string]json.RawMessage) (json.RawMessage, error) {
	msg, err := messenger.Connect(deps)
	if err != nil {
		return nil, fmt.Errorf("sb-speech: connect messenger: %w", err)
	}
	a.msg = msg
	if err := a.configure(); err != nil {
		a.disabled = err
		log.Printf("sb-speech: disabled: %v", err)
	}
	handlers := map[string]func(*messenger.Message){
		speech.SubjectCapabilities: a.handleCapabilities,
		speech.SubjectStart:        a.handleStart,
		speech.SubjectStop:         a.handleStop,
		speech.SubjectSay:          a.handleSay,
	}
	for subject, handler := range handlers {
		sub, err := msg.Subscribe(subject, handler)
		if err != nil {
			return nil, fmt.Errorf("sb-speech: subscribe: %w", err)
		}
		a.subs = append(a.subs, sub)
	}
	if err := msg.Flush(); err != nil {
		return nil, fmt.Errorf("sb-speech: flush: %w", err)
	}
	if err := msg.Publish(speech.SubjectServiceStarted, nil); err != nil {
		log.Printf("sb-speech: announce: %v", err)
	}
	log.Println("sb-speech: started")
	return nil, nil
}

func (a *App) configure() error {
	host := envString("SPEECH_RTP_HOST", "127.0.0.1")
	portMin, portMax := envInt("SPEECH_RTP_PORT_MIN", 41100), envInt("SPEECH_RTP_PORT_MAX", 41199)
	if portMin <= 0 || portMax < portMin || portMax > 65535 {
		return errors.New("SPEECH_RTP_PORT_MIN and SPEECH_RTP_PORT_MAX are not a valid range")
	}
	var engine hub.Engine
	if url := strings.TrimSpace(os.Getenv("SPEECH_TRANSCRIBER_URL")); url != "" {
		a.transcriber = provider.New(url, os.Getenv("SPEECH_TRANSCRIBER_TOKEN"), providercontract.CapabilityTranscription)
		engine = a.transcriber
	}
	var voice hub.Voice
	if url := strings.TrimSpace(os.Getenv("SPEECH_SYNTHESIZER_URL")); url != "" {
		a.synthesizer = provider.New(url, os.Getenv("SPEECH_SYNTHESIZER_TOKEN"), providercontract.CapabilitySynthesis)
		voice = a.synthesizer
	}
	a.hub = hub.New(hub.Config{
		RTPHost: host, PortMin: portMin, PortMax: portMax,
		SilenceMillis: envInt("SPEECH_SILENCE_MILLIS", 700),
		Engine:        engine,
		Voice:         voice,
		Publish:       a.publish,
	})
	return nil
}

func envString(name, fallback string) string {
	if v := os.Getenv(name); v != "" {
		return v
	}
	return fallback
}

func envInt(name string, fallback int) int {
	if v, err := strconv.Atoi(os.Getenv(name)); err == nil {
		return v
	}
	return fallback
}

func (a *App) publish(ev speech.Event) {
	data, err := json.Marshal(ev)
	if err != nil {
		return
	}
	if err := a.msg.Publish(speech.EventSubject(ev.SessionID), data); err != nil {
		log.Printf("sb-speech: publish: %v", err)
	}
}

func (a *App) handleStart(m *messenger.Message) {
	var req speech.StartRequest
	var reply speech.StartReply
	switch {
	case a.disabled != nil:
		reply.Error = "speech service is disabled: " + a.disabled.Error()
	case a.transcriber == nil:
		reply.Error = "speech transcription provider is not configured"
	case !a.ready(a.transcriber, &reply.Error):
	case json.Unmarshal(m.Data, &req) != nil:
		reply.Error = "invalid request"
	default:
		r, err := a.hub.Start(req)
		reply = r
		if err != nil {
			reply.Error = err.Error()
		}
	}
	respond(m, reply)
}

func (a *App) handleCapabilities(m *messenger.Message) {
	if a.disabled != nil {
		detail := "speech service is disabled: " + a.disabled.Error()
		respond(m, speech.CapabilitiesReply{
			Transcription: speech.CapabilityStatus{Error: detail},
			Synthesis:     speech.CapabilityStatus{Error: detail},
		})
		return
	}
	reply := speech.CapabilitiesReply{
		Transcription: a.capability(a.transcriber, "speech transcription provider is not configured"),
		Synthesis:     a.capability(a.synthesizer, "speech synthesis provider is not configured"),
	}
	respond(m, reply)
}

func (a *App) capability(client *provider.Client, missing string) speech.CapabilityStatus {
	if client == nil {
		return speech.CapabilityStatus{Error: missing}
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := client.Ready(ctx); err != nil {
		return speech.CapabilityStatus{Error: err.Error()}
	}
	return speech.CapabilityStatus{Available: true}
}

func (a *App) ready(client *provider.Client, detail *string) bool {
	status := a.capability(client, "speech provider is not configured")
	if !status.Available {
		*detail = status.Error
	}
	return status.Available
}

func (a *App) handleStop(m *messenger.Message) {
	var req speech.StopRequest
	var reply speech.StopReply
	switch {
	case a.disabled != nil:
		reply.Error = "speech service is disabled: " + a.disabled.Error()
	case json.Unmarshal(m.Data, &req) != nil:
		reply.Error = "invalid request"
	default:
		a.hub.Stop(req.SessionID)
	}
	respond(m, reply)
}

func (a *App) handleSay(m *messenger.Message) {
	var req speech.SayRequest
	var reply speech.SayReply
	switch {
	case a.disabled != nil:
		reply.Error = "speech service is disabled: " + a.disabled.Error()
	case a.synthesizer == nil:
		reply.Error = "speech synthesis provider is not configured"
	case !a.ready(a.synthesizer, &reply.Error):
	case json.Unmarshal(m.Data, &req) != nil:
		reply.Error = "invalid request"
	default:
		seq, err := a.hub.Say(req.SessionID, req.Text)
		reply.Seq = seq
		if err != nil {
			reply.Error = err.Error()
		}
	}
	respond(m, reply)
}

func respond(m *messenger.Message, reply any) {
	data, err := json.Marshal(reply)
	if err != nil {
		return
	}
	if err := m.Respond(data); err != nil {
		log.Printf("sb-speech: respond: %v", err)
	}
}

func (a *App) OnShutdown() error {
	for _, sub := range a.subs {
		sub.Unsubscribe()
	}
	if a.hub != nil {
		a.hub.Close()
	}
	if a.msg != nil {
		a.msg.Close()
	}
	return nil
}
