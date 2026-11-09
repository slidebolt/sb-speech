// Package tts turns text into speech, at the call's sample rate, for sending back into a call.
package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// Piper runs the piper binary once per utterance.
type Piper struct {
	Binary string // e.g. piper, or a wrapper script
	Voice  string // path to the .onnx voice; its .onnx.json sits beside it
	rate   int
}

// Check confirms the binary and voice exist and reads the voice's sample rate.
func (p *Piper) Check() error {
	if _, err := exec.LookPath(p.Binary); err != nil {
		return fmt.Errorf("piper binary: %w", err)
	}
	if _, err := os.Stat(p.Voice); err != nil {
		return fmt.Errorf("piper voice: %w", err)
	}
	raw, err := os.ReadFile(p.Voice + ".json")
	if err != nil {
		return fmt.Errorf("piper voice config: %w", err)
	}
	var cfg struct {
		Audio struct {
			SampleRate int `json:"sample_rate"`
		} `json:"audio"`
	}
	if err := json.Unmarshal(raw, &cfg); err != nil || cfg.Audio.SampleRate <= 0 {
		return fmt.Errorf("piper voice config: no sample rate")
	}
	p.rate = cfg.Audio.SampleRate
	return nil
}

// Synthesize returns text as mono samples at rate.
func (p *Piper) Synthesize(ctx context.Context, text string, rate int) ([]int16, error) {
	if p.rate == 0 {
		if err := p.Check(); err != nil {
			return nil, err
		}
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.Binary, "--model", p.Voice, "--output_raw", "--quiet")
	cmd.Stdin = strings.NewReader(strings.ReplaceAll(text, "\n", " ") + "\n")
	cmd.Stderr = &stderr
	out, err := cmd.Output()
	if err != nil {
		return nil, fmt.Errorf("piper: %w: %s", err, lastLine(stderr.String()))
	}
	samples := make([]int16, len(out)/2)
	if err := binary.Read(bytes.NewReader(out[:len(samples)*2]), binary.LittleEndian, samples); err != nil {
		return nil, err
	}
	return Resample(samples, p.rate, rate), nil
}

func lastLine(s string) string {
	lines := strings.Split(strings.TrimSpace(s), "\n")
	return lines[len(lines)-1]
}
