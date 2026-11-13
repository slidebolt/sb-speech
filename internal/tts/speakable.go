package tts

import (
	"regexp"
	"strings"
	"unicode"
)

var (
	urlPattern     = regexp.MustCompile(`https?://\S+`)
	bulletPattern  = regexp.MustCompile(`(?m)^\s*(?:[-*•]|\d+[.)])\s+`)
	headingPattern = regexp.MustCompile(`(?m)^\s*#+\s*`)
	spacePattern   = regexp.MustCompile(`\s+`)
	commaPattern   = regexp.MustCompile(`\s*,(?:\s*,)+`)
	spaceBefore    = regexp.MustCompile(`\s+([,.;:!?])`)
	degreePattern  = regexp.MustCompile(`°\s*([FC])\b`)
	doubleStop     = regexp.MustCompile(`([^.])\.\.([^.]|$)`) // but not an ellipsis
)

// Speakable turns written text into text to read aloud: markdown, links,
// emoji and brackets are not spoken, symbols become words, and each line is
// its own sentence.
func Speakable(text string) string {
	text = urlPattern.ReplaceAllString(text, "a link")
	text = headingPattern.ReplaceAllString(text, "")
	text = bulletPattern.ReplaceAllString(text, "")
	// A line break is a pause: end a line that has no punctuation of its own.
	lines := strings.Split(text, "\n")
	for i, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" && !strings.ContainsRune(".!?:;,", rune(l[len(l)-1])) {
			l += "."
		}
		lines[i] = l
	}
	text = strings.Join(lines, " ")

	text = degreePattern.ReplaceAllStringFunc(text, func(m string) string {
		if strings.HasSuffix(m, "F") {
			return " degrees Fahrenheit"
		}
		return " degrees Celsius"
	})
	text = strings.NewReplacer(
		"°", " degrees",
		"%", " percent",
		"&", " and ",
		"+", " plus ",
		"=", " equals ",
		"~", "about ",
		"→", ", then ",
		"—", ", ", "–", ", ",
		// Brackets are asides: a short pause either side, not read out.
		"(", ", ", ")", ", ", "[", ", ", "]", ", ", "{", ", ", "}", ", ",
		// Markdown emphasis and code.
		"**", "", "__", "", "`", "", "*", "",
		"\"", "", "“", "", "”", "",
	).Replace(text)

	text = strings.Map(func(r rune) rune {
		if unicode.Is(unicode.So, r) || unicode.Is(unicode.Sk, r) || unicode.Is(unicode.Cs, r) || r == '️' || r == '‍' {
			return -1 // emoji and other pictographs
		}
		return r
	}, text)

	text = spacePattern.ReplaceAllString(text, " ")
	text = commaPattern.ReplaceAllString(text, ",")
	text = spaceBefore.ReplaceAllString(text, "$1")
	text = strings.ReplaceAll(text, ",.", ".")
	text = strings.ReplaceAll(text, ".,", ".")
	text = doubleStop.ReplaceAllString(text, "$1.$2")
	return strings.Trim(text, " ,")
}
