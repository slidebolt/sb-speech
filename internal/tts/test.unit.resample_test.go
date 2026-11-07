package tts

import (
	"math"
	"testing"
)

func tone(freq float64, rate, n int) []int16 {
	out := make([]int16, n)
	for i := range out {
		out[i] = int16(10000 * math.Sin(2*math.Pi*freq*float64(i)/float64(rate)))
	}
	return out
}

// power of freq in samples at rate, by correlation.
func power(s []int16, freq float64, rate int) float64 {
	var re, im float64
	for i, v := range s {
		a := 2 * math.Pi * freq * float64(i) / float64(rate)
		re += float64(v) * math.Cos(a)
		im += float64(v) * math.Sin(a)
	}
	return math.Hypot(re, im) / float64(len(s))
}

func TestResampleKeepsSpeechBandTone(t *testing.T) {
	out := Resample(tone(1000, 22050, 22050), 22050, 8000)
	if len(out) != 8000 {
		t.Fatalf("length: got %d want 8000", len(out))
	}
	if p := power(out[100:7900], 1000, 8000); p < 4000 {
		t.Fatalf("1 kHz tone lost: power %.0f", p)
	}
}

func TestResampleRemovesToneAboveNyquist(t *testing.T) {
	// 6 kHz cannot exist at 8 kHz; without a filter it aliases to 2 kHz.
	out := Resample(tone(6000, 22050, 22050), 22050, 8000)
	if p := power(out[100:7900], 2000, 8000); p > 300 {
		t.Fatalf("6 kHz aliased to 2 kHz: power %.0f", p)
	}
}

func TestResampleSameRateCopies(t *testing.T) {
	in := []int16{1, 2, 3}
	out := Resample(in, 8000, 8000)
	out[0] = 9
	if in[0] != 1 || len(out) != 3 {
		t.Fatal("same-rate resample should return a copy")
	}
}
