package whisper

import (
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestCleanText(t *testing.T) {
	cases := map[string]string{
		" Turn the lights on.\n":         "Turn the lights on.",
		"[BLANK_AUDIO]\n":                "",
		"(wind blowing) hello *coughs* ": "hello",
		"  [ Silence ]  ":                "",
		"line one\nline two":             "line one line two",
	}
	for in, want := range cases {
		if got := CleanText(in); got != want {
			t.Errorf("%q: got %q, want %q", in, got, want)
		}
	}
}

func TestEncodeWAV(t *testing.T) {
	w := EncodeWAV([]int16{1, -1})
	if string(w[:4]) != "RIFF" || string(w[8:12]) != "WAVE" || string(w[36:40]) != "data" {
		t.Fatalf("header = %q", w[:44])
	}
	if got := binary.LittleEndian.Uint32(w[24:]); got != 16000 {
		t.Fatalf("rate = %d", got)
	}
	if got := binary.LittleEndian.Uint32(w[40:]); got != 4 || len(w) != 48 {
		t.Fatalf("data length %d, file %d", got, len(w))
	}
}

func script(t *testing.T, body string) string {
	t.Helper()
	p := filepath.Join(t.TempDir(), "whisper-cli")
	if err := os.WriteFile(p, []byte("#!/bin/sh\n"+body), 0o755); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestTranscribe(t *testing.T) {
	bin := script(t, `case "$*" in *--no-timestamps*) ;; *) exit 9;; esac; echo ' [BLANK_AUDIO] Turn the lights on.'`)
	got, err := Engine{Binary: bin, Model: "m", Threads: 1}.Transcribe(context.Background(), []int16{1})
	if err != nil || got != "Turn the lights on." {
		t.Fatalf("got %q, %v", got, err)
	}
}

func TestTranscribe_FailureIncludesWhy(t *testing.T) {
	bin := script(t, `echo 'bad model' >&2; exit 3`)
	_, err := Engine{Binary: bin, Model: "m", Threads: 1}.Transcribe(context.Background(), []int16{1})
	if err == nil || !strings.Contains(err.Error(), "bad model") {
		t.Fatalf("err = %v", err)
	}
}

func TestCheck(t *testing.T) {
	bin := script(t, "exit 0")
	model := filepath.Join(t.TempDir(), "m.bin")
	os.WriteFile(model, nil, 0o644)
	if err := (Engine{Binary: bin, Model: model}).Check(); err != nil {
		t.Fatal(err)
	}
	if err := (Engine{Binary: "no-such-binary", Model: model}).Check(); err == nil {
		t.Error("missing binary")
	}
	if err := (Engine{Binary: bin, Model: "/no/such"}).Check(); err == nil {
		t.Error("missing model")
	}
}
