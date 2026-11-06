// Package whisper runs whisper.cpp to turn speech into text.
package whisper

import (
	"bytes"
	"context"
	"encoding/binary"
	"fmt"
	"os"
	"os/exec"
	"regexp"
	"strconv"
	"strings"
)

// Engine runs whisper-cli once per utterance.
type Engine struct {
	Binary  string
	Model   string
	Threads int
}

// Check confirms the binary and model exist.
func (e Engine) Check() error {
	if _, err := exec.LookPath(e.Binary); err != nil {
		return fmt.Errorf("whisper binary: %w", err)
	}
	if _, err := os.Stat(e.Model); err != nil {
		return fmt.Errorf("whisper model: %w", err)
	}
	return nil
}

// Transcribe returns the text of 16 kHz mono samples.
func (e Engine) Transcribe(ctx context.Context, samples []int16) (string, error) {
	f, err := os.CreateTemp("", "utterance-*.wav")
	if err != nil {
		return "", err
	}
	defer os.Remove(f.Name())
	if _, err := f.Write(EncodeWAV(samples)); err != nil {
		f.Close()
		return "", err
	}
	f.Close()

	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, e.Binary, "-m", e.Model, "-f", f.Name(), "-l", "en",
		"-t", strconv.Itoa(e.Threads), "--no-timestamps", "--no-prints")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return "", fmt.Errorf("whisper: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return CleanText(string(out)), nil
}

// nonSpeech matches the markers whisper emits for sounds, such as [BLANK_AUDIO] or (wind).
var nonSpeech = regexp.MustCompile(`\[[^\]]*\]|\([^)]*\)|\*[^*]*\*`)

// CleanText removes non-speech markers and extra whitespace.
func CleanText(s string) string {
	return strings.Join(strings.Fields(nonSpeech.ReplaceAllString(s, " ")), " ")
}

// EncodeWAV wraps samples in a 16 kHz mono 16-bit WAV file.
func EncodeWAV(samples []int16) []byte {
	const sampleRate = 16000
	dataLen := uint32(len(samples) * 2)
	var b bytes.Buffer
	b.WriteString("RIFF")
	binary.Write(&b, binary.LittleEndian, 36+dataLen)
	b.WriteString("WAVEfmt ")
	binary.Write(&b, binary.LittleEndian, uint32(16))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint16(1))
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate))
	binary.Write(&b, binary.LittleEndian, uint32(sampleRate*2))
	binary.Write(&b, binary.LittleEndian, uint16(2))
	binary.Write(&b, binary.LittleEndian, uint16(16))
	b.WriteString("data")
	binary.Write(&b, binary.LittleEndian, dataLen)
	binary.Write(&b, binary.LittleEndian, samples)
	return b.Bytes()
}
