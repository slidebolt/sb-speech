package tts

import "math"

// Resample converts mono samples between rates with a windowed-sinc low-pass
// filter, so downsampling to telephone rate does not alias.
func Resample(in []int16, from, to int) []int16 {
	if from == to || len(in) == 0 {
		return append([]int16(nil), in...)
	}
	const halfTaps = 16
	ratio := float64(to) / float64(from)
	cutoff := math.Min(1, ratio) * 0.9 // fraction of the input Nyquist, with headroom
	n := int(float64(len(in)) * ratio)
	out := make([]int16, n)
	for i := range out {
		center := float64(i) / ratio
		lo := int(math.Floor(center)) - halfTaps
		var sum, weight float64
		for j := lo; j <= lo+2*halfTaps; j++ {
			if j < 0 || j >= len(in) {
				continue
			}
			x := float64(j) - center
			w := cutoff * sinc(cutoff*x) * blackman(x, halfTaps)
			sum += float64(in[j]) * w
			weight += w
		}
		if weight != 0 {
			sum /= weight
		}
		out[i] = int16(math.Max(-32768, math.Min(32767, math.Round(sum))))
	}
	return out
}

func sinc(x float64) float64 {
	if x == 0 {
		return 1
	}
	return math.Sin(math.Pi*x) / (math.Pi * x)
}

func blackman(x float64, half int) float64 {
	t := (x + float64(half)) / float64(2*half)
	if t < 0 || t > 1 {
		return 0
	}
	return 0.42 - 0.5*math.Cos(2*math.Pi*t) + 0.08*math.Cos(4*math.Pi*t)
}
