package tts

import "math"

const halfTaps = 16

// Resample converts mono samples between rates with a windowed-sinc low-pass
// filter, so downsampling does not alias.
func Resample(in []int16, from, to int) []int16 {
	if from == to || len(in) == 0 {
		return append([]int16(nil), in...)
	}
	r := NewResampler(from, to)
	return append(r.Write(in), r.Flush()...)
}

// Resampler is Resample for a stream that arrives in pieces: it keeps the
// filter's history between writes, so the joins are seamless.
type Resampler struct {
	from, to int
	ratio    float64
	cutoff   float64
	buf      []int16 // input from absolute index base on
	base     int
	total    int // input samples written
	next     int // index of the next output sample
}

func NewResampler(from, to int) *Resampler {
	ratio := float64(to) / float64(from)
	return &Resampler{
		from: from, to: to, ratio: ratio,
		cutoff: math.Min(1, ratio) * 0.9, // fraction of the input Nyquist, with headroom
	}
}

// Write adds input and returns the output it completes.
func (r *Resampler) Write(in []int16) []int16 {
	if r.from == r.to {
		return append([]int16(nil), in...)
	}
	r.buf = append(r.buf, in...)
	r.total += len(in)
	return r.emit(false)
}

// Flush returns the rest of the output, treating the input as ended.
func (r *Resampler) Flush() []int16 {
	if r.from == r.to {
		return nil
	}
	return r.emit(true)
}

func (r *Resampler) emit(final bool) []int16 {
	var out []int16
	end := int(float64(r.total) * r.ratio)
	for r.next < end {
		center := float64(r.next) / r.ratio
		lo := int(math.Floor(center)) - halfTaps
		if !final && lo+2*halfTaps >= r.total {
			break // needs input that has not arrived yet
		}
		var sum, weight float64
		for j := lo; j <= lo+2*halfTaps; j++ {
			if j < 0 || j >= r.total {
				continue
			}
			x := float64(j) - center
			w := r.cutoff * sinc(r.cutoff*x) * blackman(x, halfTaps)
			sum += float64(r.buf[j-r.base]) * w
			weight += w
		}
		if weight != 0 {
			sum /= weight
		}
		out = append(out, int16(math.Max(-32768, math.Min(32767, math.Round(sum)))))
		r.next++
	}
	// Keep only the input the next output still needs.
	keep := int(math.Floor(float64(r.next)/r.ratio)) - halfTaps
	if drop := keep - r.base; drop > 0 && drop <= len(r.buf) {
		r.buf = r.buf[drop:]
		r.base = keep
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
