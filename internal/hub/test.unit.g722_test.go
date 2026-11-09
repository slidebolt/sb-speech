package hub

import (
	"math"
	"testing"
)

// g722RoundTripSNR encodes and decodes signal and returns the best SNR in dB
// over the codec's delay, which is a few tens of samples.
func g722RoundTripSNR(signal []int16) (float64, int) {
	enc, dec := NewG722Encoder(), NewG722Decoder()
	var out []int16
	for f := 0; f+320 <= len(signal); f += 320 { // in 20 ms packets, as on a call
		out = dec.Decode(out, enc.Encode(nil, signal[f:f+320]))
	}
	best, bestLag := math.Inf(-1), 0
	skip := 1600 // let both adapt
	for lag := 0; lag < 64; lag++ {
		var sig, noise float64
		for i := skip; i+lag < len(out); i++ {
			s, o := float64(signal[i]), float64(out[i+lag])
			sig += s * s
			noise += (s - o) * (s - o)
		}
		if snr := 10 * math.Log10(sig/noise); snr > best {
			best, bestLag = snr, lag
		}
	}
	return best, bestLag
}

func TestG722RoundTripKeepsWidebandAudio(t *testing.T) {
	for _, hz := range []float64{300, 1000, 3000, 5000, 6500} {
		sig := make([]int16, 16000)
		for i := range sig {
			sig[i] = int16(8000 * math.Sin(2*math.Pi*hz*float64(i)/16000))
		}
		snr, lag := g722RoundTripSNR(sig)
		t.Logf("%5.0f Hz: SNR %.1f dB at delay %d samples", hz, snr, lag)
		// Above 4 kHz is what telephone audio cannot carry; G.722 must.
		if snr < 15 {
			t.Errorf("%v Hz: SNR %.1f dB, the codec is not reproducing it", hz, snr)
		}
	}
}

func TestG722Sizes(t *testing.T) {
	enc := NewG722Encoder()
	b := enc.Encode(nil, make([]int16, 320))
	if len(b) != 160 {
		t.Fatalf("20 ms at 16 kHz encoded to %d bytes, want 160", len(b))
	}
	if s := NewG722Decoder().Decode(nil, b); len(s) != 320 {
		t.Fatalf("160 bytes decoded to %d samples, want 320", len(s))
	}
}
