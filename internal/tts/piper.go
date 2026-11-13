// Package tts turns text into speech, at the call's sample rate, for sending back into a call.
package tts

import (
	"bytes"
	"context"
	"encoding/binary"
	"encoding/json"
	"fmt"
	"io"
	"math"
	"os"
	"os/exec"
	"strconv"
)

// Piper runs the piper binary once per utterance and streams its audio, which
// piper produces a sentence at a time, so speech can start before the whole
// reply is synthesized.
type Piper struct {
	Binary string // e.g. piper, or a wrapper script
	Voice  string // path to the .onnx voice; its .onnx.json sits beside it

	// Delivery. Zero leaves piper's default.
	LengthScale     float64 // speaking pace: above 1 is slower
	NoiseScale      float64 // variation in delivery
	NoiseW          float64 // variation in phoneme length
	SentenceSilence float64 // seconds of pause after each sentence
	// GainDB is applied to everything spoken. Piper scales every sentence to
	// full scale, so its peaks clip; a negative gain leaves headroom.
	GainDB float64

	rate int
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

func (p *Piper) args() []string {
	args := []string{"--model", p.Voice, "--output_raw", "--quiet"}
	for _, o := range []struct {
		flag string
		v    float64
	}{
		{"--length_scale", p.LengthScale},
		{"--noise_scale", p.NoiseScale},
		{"--noise_w", p.NoiseW},
		{"--sentence_silence", p.SentenceSilence},
	} {
		if o.v > 0 {
			args = append(args, o.flag, strconv.FormatFloat(o.v, 'f', -1, 64))
		}
	}
	return args
}

// Synthesize speaks text at rate, passing audio to emit as piper produces it.
// It returns once all of it has been emitted.
func (p *Piper) Synthesize(ctx context.Context, text string, rate int, emit func([]int16)) error {
	if p.rate == 0 {
		if err := p.Check(); err != nil {
			return err
		}
	}
	text = Speakable(text)
	if text == "" {
		return nil
	}
	var stderr bytes.Buffer
	cmd := exec.CommandContext(ctx, p.Binary, p.args()...)
	cmd.Stdin = bytes.NewReader([]byte(text + "\n"))
	cmd.Stderr = &stderr
	out, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return fmt.Errorf("piper: %w", err)
	}
	gain := math.Pow(10, p.GainDB/20)
	rs := NewResampler(p.rate, rate)
	buf := make([]byte, 8192)
	var odd []byte // half a sample left over from the last read
	for {
		n, rerr := out.Read(buf)
		if n > 0 {
			data := append(odd, buf[:n]...)
			samples := make([]int16, len(data)/2)
			_ = binary.Read(bytes.NewReader(data[:len(samples)*2]), binary.LittleEndian, samples)
			odd = append([]byte(nil), data[len(samples)*2:]...)
			applyGain(samples, gain)
			if s := rs.Write(samples); len(s) > 0 {
				emit(s)
			}
		}
		if rerr == io.EOF {
			break
		}
		if rerr != nil {
			cmd.Wait()
			return fmt.Errorf("piper: read: %w", rerr)
		}
	}
	if err := cmd.Wait(); err != nil {
		return fmt.Errorf("piper: %w: %s", err, lastLine(stderr.String()))
	}
	if s := rs.Flush(); len(s) > 0 {
		emit(s)
	}
	return nil
}

func applyGain(s []int16, gain float64) {
	if gain == 1 {
		return
	}
	for i, v := range s {
		s[i] = int16(math.Max(-32768, math.Min(32767, math.Round(float64(v)*gain))))
	}
}

func lastLine(s string) string {
	for len(s) > 0 && (s[len(s)-1] == '\n' || s[len(s)-1] == ' ') {
		s = s[:len(s)-1]
	}
	if i := bytes.LastIndexByte([]byte(s), '\n'); i >= 0 {
		return s[i+1:]
	}
	return s
}
