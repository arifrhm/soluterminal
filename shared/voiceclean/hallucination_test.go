package voiceclean

import (
	"strings"
	"testing"
)

// Cases are the measured Groq answers in launch/reviews/soluterminal-voice/
// data/parity/silence_probe.jsonl (2026-10-22).
func TestSilenceHallucinationMeasuredCases(t *testing.T) {
	seg := func(end, lp float64) []Segment { return []Segment{{Start: 0, End: end, AvgLogprob: lp}} }
	cases := []struct {
		name string
		v    Verbose
		want bool
		why  string
	}{
		{"silence 1s thank you", Verbose{Text: "Thank you.", Duration: 1, Segments: seg(29.98, -0.53)}, true, "overrun"},
		{"silence 4s thank you", Verbose{Text: "Thank you.", Duration: 4, Segments: seg(29.98, -0.29)}, true, "overrun"},
		{"room 4s prompted B", Verbose{Text: "B", Duration: 4, Segments: seg(20.28, -2.66)}, true, "overrun"},
		{"hiss 1s prompted thank you", Verbose{Text: "Thank you.", Duration: 1, Segments: seg(1, -0.38)}, true, "stock_phrase"},
		{"room 2s prompted names", Verbose{Text: "Sli, Mica, Sulu, Cale, Cose, Pnpm, Pnpm, Pnpm, Pnpm, Pn.", Duration: 2, Segments: seg(2, -0.99)}, true, "garbled"},
		{"room 1s prompted Sli", Verbose{Text: "Sli.", Duration: 1, Segments: seg(1, -1.35)}, true, "garbled"},
		{"real thank you us", Verbose{Text: "Thank you.", Duration: 2.672, Segments: seg(0.86, -0.21)}, false, ""},
		{"real thank you sg", Verbose{Text: "Thank you.", Duration: 2.672, Segments: seg(1.1, -0.45)}, false, ""},
		{"real thank you noisy", Verbose{Text: "Thank you.", Duration: 2.672, Segments: seg(0.84, -0.27)}, false, ""},
		{"real yes do it", Verbose{Text: "Yes, do it.", Duration: 3.488, Segments: seg(2.02, -0.31)}, false, ""},
		{"real on my way", Verbose{Text: "On my way.", Duration: 2.672, Segments: seg(1, -0.22)}, false, ""},
		{"plain json has no evidence", Verbose{Text: "Thank you."}, false, ""},
		{"punctuation only is no-speech, not this check", Verbose{Text: ".", Duration: 2, Segments: seg(2, -0.43)}, false, ""},
	}
	for _, c := range cases {
		got, why := SilenceHallucination(c.v)
		if got != c.want || why != c.why {
			t.Errorf("%s: got (%v, %q), want (%v, %q)", c.name, got, why, c.want, c.why)
		}
	}
}

func TestLongRealSpeechThatRunsToTheEndIsKept(t *testing.T) {
	// An older client without the release tail: speech runs to the end, but
	// it is confident and not a stock phrase.
	v := Verbose{Text: "Can you move the call to Thursday at 3:30 pm instead?", Duration: 3.1, Segments: []Segment{{Start: 0, End: 3.1, AvgLogprob: -0.25}}}
	if bad, why := SilenceHallucination(v); bad {
		t.Fatalf("real speech flagged: %s", why)
	}
}

func TestStockPhraseNormalisation(t *testing.T) {
	for _, s := range []string{"Thank you.", "thank you", "  THANKS FOR WATCHING!  ", "Bye-bye."} {
		if !IsStockPhrase(s) {
			t.Errorf("%q should be a stock phrase", s)
		}
	}
	for _, s := range []string{"Thank you, Mara.", "Okay.", "Yes, do it."} {
		if IsStockPhrase(s) {
			t.Errorf("%q should not be a stock phrase", s)
		}
	}
}

func TestParseVerboseAcceptsPlainJSON(t *testing.T) {
	v, err := ParseVerbose([]byte(`{"text":"hello"}`))
	if err != nil || v.Text != "hello" || v.Duration != 0 || len(v.Segments) != 0 {
		t.Fatalf("got %+v, %v", v, err)
	}
	v, err = ParseVerbose([]byte(`{"text":"Thank you.","duration":1,"segments":[{"start":0,"end":29.98,"avg_logprob":-0.5,"no_speech_prob":0}]}`))
	if err != nil || len(v.Segments) != 1 || v.Segments[0].End != 29.98 {
		t.Fatalf("got %+v, %v", v, err)
	}
	if !strings.Contains(v.Text, "Thank") {
		t.Fatal("text lost")
	}
}
