// Package whisper contains the engine-neutral audio encoding and transcript
// cleanup used when talking to an isolated transcription provider.
package whisper

import (
	"bytes"
	"encoding/binary"
	"regexp"
	"strings"
)

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
