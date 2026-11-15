package whisper

import (
	"encoding/binary"
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
