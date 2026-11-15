// Package provider adapts the generic HTTP speech-provider contract to the
// session hub's sample-oriented interfaces.
package provider

import (
	"bytes"
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"net/http"
	"time"

	engine "github.com/slidebolt/sb-speech-provider-sdk"
	"github.com/slidebolt/sb-speech/internal/tts"
	"github.com/slidebolt/sb-speech/internal/whisper"
)

type Client struct {
	client     engine.Client
	capability engine.Capability
}

func New(url, token string, capability engine.Capability) *Client {
	return &Client{client: engine.Client{
		BaseURL: url,
		Token:   token,
		HTTP:    &http.Client{Timeout: 2 * time.Minute},
	}, capability: capability}
}

func (c *Client) Ready(ctx context.Context) error {
	capabilities, err := c.client.Capabilities(ctx)
	if err != nil {
		return err
	}
	if !capabilities.Has(c.capability) {
		return fmt.Errorf("provider does not offer %s", c.capability)
	}
	health, err := c.client.Health(ctx)
	if err != nil {
		return err
	}
	if !health.Ready {
		if health.Error != "" {
			return errors.New(health.Error)
		}
		return errors.New("provider is not ready")
	}
	return nil
}

func (c *Client) Transcribe(ctx context.Context, samples []int16) (string, error) {
	result, err := c.client.Transcribe(ctx, whisper.EncodeWAV(samples))
	if err != nil {
		return "", err
	}
	return whisper.CleanText(result.Text), nil
}

func (c *Client) Synthesize(ctx context.Context, text string, rate int, emit func([]int16)) error {
	text = tts.Speakable(text)
	if text == "" {
		return nil
	}
	audio, err := c.client.Synthesize(ctx, engine.SynthesisRequest{Text: text})
	if err != nil {
		return err
	}
	samples, sourceRate, err := decodeWAV(audio)
	if err != nil {
		return err
	}
	resampler := tts.NewResampler(sourceRate, rate)
	const chunk = 4096
	for len(samples) > 0 {
		n := min(chunk, len(samples))
		if output := resampler.Write(samples[:n]); len(output) > 0 {
			emit(output)
		}
		samples = samples[n:]
	}
	if output := resampler.Flush(); len(output) > 0 {
		emit(output)
	}
	return nil
}

func decodeWAV(raw []byte) ([]int16, int, error) {
	if len(raw) < 12 || string(raw[:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, errors.New("speech provider returned an invalid WAV file")
	}
	var rate int
	var data []byte
	for offset := 12; offset+8 <= len(raw); {
		length := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		start, end := offset+8, offset+8+length
		if length < 0 || end > len(raw) {
			return nil, 0, errors.New("speech provider returned a truncated WAV file")
		}
		switch string(raw[offset : offset+4]) {
		case "fmt ":
			if length < 16 || binary.LittleEndian.Uint16(raw[start:start+2]) != 1 ||
				binary.LittleEndian.Uint16(raw[start+2:start+4]) != 1 ||
				binary.LittleEndian.Uint16(raw[start+14:start+16]) != 16 {
				return nil, 0, errors.New("speech provider WAV must be mono 16-bit PCM")
			}
			rate = int(binary.LittleEndian.Uint32(raw[start+4 : start+8]))
		case "data":
			data = raw[start:end]
		}
		offset = end + length%2
	}
	if rate <= 0 || len(data) == 0 || len(data)%2 != 0 {
		return nil, 0, errors.New("speech provider WAV has no usable audio")
	}
	samples := make([]int16, len(data)/2)
	if err := binary.Read(bytes.NewReader(data), binary.LittleEndian, samples); err != nil {
		return nil, 0, err
	}
	return samples, rate, nil
}
