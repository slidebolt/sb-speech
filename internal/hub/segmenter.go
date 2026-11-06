package hub

import "math"

const (
	rate           = 8000
	frameSamples   = 160 // 20 ms
	speechFrames   = 3   // frames above threshold before speech counts as started
	preRollFrames  = 15  // audio kept from before speech started, so its start is not lost
	minVoiced      = 300 * rate / 1000 // loud audio an utterance needs, so a click is not speech
	maxUtterance   = 15 * rate
	minSpeechRMS   = 300.0
	noiseMultiple  = 3.0
	noiseSmoothing = 0.95
)

// Segmenter splits a stream of samples into utterances using energy. Speech is
// audio louder than a noise floor it tracks during silence.
type Segmenter struct {
	silenceFrames int

	frame     []int16
	preRoll   [][]int16
	utterance []int16
	noise     float64
	loud      int
	quiet     int
	voiced    int
	inSpeech  bool
}

// NewSegmenter ends an utterance after silenceMillis of quiet.
func NewSegmenter(silenceMillis int) *Segmenter {
	return &Segmenter{silenceFrames: max(1, silenceMillis/20), noise: minSpeechRMS / noiseMultiple}
}

// Write adds samples and returns any utterances they completed.
func (s *Segmenter) Write(samples []int16) [][]int16 {
	var out [][]int16
	for _, v := range samples {
		s.frame = append(s.frame, v)
		if len(s.frame) == frameSamples {
			if u := s.processFrame(s.frame); u != nil {
				out = append(out, u)
			}
			s.frame = nil
		}
	}
	return out
}

// Flush ends any utterance in progress, as when the audio stops arriving.
func (s *Segmenter) Flush() []int16 {
	s.frame = nil
	if !s.inSpeech {
		return nil
	}
	return s.finish()
}

func (s *Segmenter) processFrame(frame []int16) []int16 {
	level := rms(frame)
	threshold := math.Max(minSpeechRMS, s.noise*noiseMultiple)
	isLoud := level > threshold

	if !s.inSpeech {
		s.preRoll = append(s.preRoll, frame)
		if len(s.preRoll) > preRollFrames {
			s.preRoll = s.preRoll[1:]
		}
		if !isLoud {
			s.loud = 0
			s.noise = s.noise*noiseSmoothing + level*(1-noiseSmoothing)
			return nil
		}
		s.loud++
		if s.loud < speechFrames {
			return nil
		}
		s.inSpeech = true
		s.quiet = 0
		s.voiced = s.loud * frameSamples
		for _, f := range s.preRoll {
			s.utterance = append(s.utterance, f...)
		}
		s.preRoll = nil
		return nil
	}

	s.utterance = append(s.utterance, frame...)
	if isLoud {
		s.quiet = 0
		s.voiced += frameSamples
	} else {
		s.quiet++
	}
	if s.quiet >= s.silenceFrames || len(s.utterance) >= maxUtterance {
		return s.finish()
	}
	return nil
}

func (s *Segmenter) finish() []int16 {
	u := s.utterance
	s.utterance = nil
	s.inSpeech = false
	voiced := s.voiced
	s.loud = 0
	s.quiet = 0
	s.voiced = 0
	if voiced < minVoiced {
		return nil
	}
	return u
}

func rms(samples []int16) float64 {
	var sum float64
	for _, v := range samples {
		sum += float64(v) * float64(v)
	}
	return math.Sqrt(sum / float64(len(samples)))
}

// ulawTable decodes G.711 mu-law to 16-bit samples.
var ulawTable = func() (t [256]int16) {
	for i := range t {
		u := ^byte(i)
		sample := (int(u&0x0f)<<3 + 0x84) << ((u & 0x70) >> 4)
		if u&0x80 != 0 {
			t[i] = int16(0x84 - sample)
		} else {
			t[i] = int16(sample - 0x84)
		}
	}
	return t
}()

// DecodeULaw converts a mu-law payload to samples.
func DecodeULaw(payload []byte) []int16 {
	out := make([]int16, len(payload))
	for i, b := range payload {
		out[i] = ulawTable[b]
	}
	return out
}

// Upsample2x doubles the sample rate by interpolation, since whisper needs 16 kHz.
func Upsample2x(in []int16) []int16 {
	if len(in) == 0 {
		return nil
	}
	out := make([]int16, 0, len(in)*2)
	for i, v := range in {
		out = append(out, v)
		next := v
		if i+1 < len(in) {
			next = in[i+1]
		}
		out = append(out, int16((int(v)+int(next))/2))
	}
	return out
}
