package app

import (
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"os"
	"runtime"
	"strconv"

	contract "github.com/slidebolt/sb-contract"
	messenger "github.com/slidebolt/sb-messenger-sdk"
	speech "github.com/slidebolt/sb-speech-sdk"
	"github.com/slidebolt/sb-speech/internal/hub"
	"github.com/slidebolt/sb-speech/internal/whisper"
)

// ServiceID is the identifier other binaries use in DependsOn.
const ServiceID = "speech"

// App is the sb-speech service.
type App struct {
	msg      messenger.Messenger
	subs     []messenger.Subscription
	hub      *hub.Hub
	disabled error
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
		speech.SubjectStart: a.handleStart,
		speech.SubjectStop:  a.handleStop,
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
	host := os.Getenv("SPEECH_RTP_HOST")
	if host == "" {
		return errors.New("SPEECH_RTP_HOST is required")
	}
	engine := whisper.Engine{
		Binary:  envString("SPEECH_WHISPER_PATH", "whisper-cli"),
		Model:   envString("SPEECH_WHISPER_MODEL", "/app/models/ggml-base.en.bin"),
		Threads: envInt("SPEECH_THREADS", max(1, runtime.NumCPU()/2)),
	}
	if err := engine.Check(); err != nil {
		return err
	}
	portMin, portMax := envInt("SPEECH_RTP_PORT_MIN", 41100), envInt("SPEECH_RTP_PORT_MAX", 41199)
	if portMin <= 0 || portMax < portMin || portMax > 65535 {
		return errors.New("SPEECH_RTP_PORT_MIN and SPEECH_RTP_PORT_MAX are not a valid range")
	}
	a.hub = hub.New(hub.Config{
		RTPHost: host, PortMin: portMin, PortMax: portMax,
		SilenceMillis: envInt("SPEECH_SILENCE_MILLIS", 700),
		Engine:        engine,
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
