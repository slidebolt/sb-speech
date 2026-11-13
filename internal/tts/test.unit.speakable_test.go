package tts

import "testing"

func TestSpeakable(t *testing.T) {
	cases := []struct{ in, want string }{
		{"Basement lights dimmed to 25%.", "Basement lights dimmed to 25 percent."},
		{`Here's one. (I didn't catch "looks back on.")`, "Here's one. I didn't catch looks back on."},
		{"The **bar** lights are on & the deck is off.", "The bar lights are on and the deck is off."},
		{"It's 72°F outside.", "It's 72 degrees Fahrenheit outside."},
		{"Done 👍", "Done."},
		{"Lights on:\n- kitchen\n- bar", "Lights on: kitchen. bar."},
		{"See https://example.com/x for more", "See a link for more."},
		{"## Summary\nAll off", "Summary. All off."},
		{"Basement (most of it) is red now.", "Basement, most of it, is red now."},
		{"Well... maybe", "Well... maybe."},
	}
	for _, c := range cases {
		if got := Speakable(c.in); got != c.want {
			t.Errorf("Speakable(%q)\n  got  %q\n  want %q", c.in, got, c.want)
		}
	}
}
