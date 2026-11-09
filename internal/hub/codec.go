package hub

import (
	speech "github.com/slidebolt/sb-speech-sdk"
)

const (
	frameMillis     = 20
	payloadTypePCMU = 0
	payloadTypeG722 = 9
	// rtpClockRate is the RTP timestamp rate of both codecs: G.722 uses 8000
	// although it samples at 16 kHz (RFC 3551).
	rtpClockRate = 8000
)

// format is how a session's audio is carried: telephone-quality mu-law at
// 8 kHz, or wideband G.722 at 16 kHz. Both send one byte per 8 kHz tick, so a
// 20 ms packet is always 160 bytes.
type format struct {
	codec string
	rate  int
}

func formatFor(in speech.Input) (format, bool) {
	if !speech.SupportedInput(in) {
		return format{}, false
	}
	return format{codec: in.Codec, rate: in.SampleRate}, true
}

// frameSamples is the number of audio samples in one 20 ms packet.
func (f format) frameSamples() int { return f.rate * frameMillis / 1000 }

// frameBytes is the payload size of one 20 ms packet, and its timestamp step.
func (f format) frameBytes() int { return rtpClockRate * frameMillis / 1000 }

func (f format) payloadType() byte {
	if f.codec == speech.CodecG722 {
		return payloadTypeG722
	}
	return payloadTypePCMU
}

// decoder returns a function that turns one stream's payloads into samples.
// G.722 is stateful, so each stream needs its own.
func (f format) decoder() func([]byte) []int16 {
	if f.codec == speech.CodecG722 {
		d := NewG722Decoder()
		return func(p []byte) []int16 { return d.Decode(nil, p) }
	}
	return DecodeULaw
}

// encoder returns a function that turns one stream's samples into a payload.
func (f format) encoder() func(dst []byte, samples []int16) []byte {
	if f.codec == speech.CodecG722 {
		e := NewG722Encoder()
		return e.Encode
	}
	return func(dst []byte, samples []int16) []byte {
		for _, v := range samples {
			dst = append(dst, encodeULaw(v))
		}
		return dst
	}
}
