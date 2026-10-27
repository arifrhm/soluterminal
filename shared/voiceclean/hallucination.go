package voiceclean

import (
	"encoding/json"
	"strings"
)

// Whisper answers silence and room noise with stock phrases ("Thank you."),
// and a prompted run can turn noise into a string of names. Measured on Groq
// whisper-large-v3-turbo (2026-10-22, launch/reviews/soluterminal-voice/data/
// parity/silence_probe.jsonl):
//   - silence: "Thank you." with a segment ending at 29.98 s of a 1-4 s clip;
//   - hiss: "Thank you." spanning the whole 1 s clip;
//   - room noise, prompted: "Sli, Mica, Sulu, Cale, Cose, Pnpm…" (avg logprob
//     -0.99, spanning the whole clip) and "B" ending at 20.3 s of a 4 s clip;
//   - a real "Thank you." ends ~1.5 s before the clip ends (both apps keep
//     recording ~0.3 s after release), with avg logprob around -0.2 to -0.45.
// Groq reports no_speech_prob as 0 for all of these, so it is not used.

// Segment is the part of a Whisper verbose_json segment the checks use.
type Segment struct {
	Start      float64 `json:"start"`
	End        float64 `json:"end"`
	AvgLogprob float64 `json:"avg_logprob"`
}

// Verbose is a Whisper verbose_json answer.
type Verbose struct {
	Language string    `json:"language"`
	Text     string    `json:"text"`
	Duration float64   `json:"duration"`
	Segments []Segment `json:"segments"`
}

// ParseVerbose reads a verbose_json (or plain json) answer. A plain answer
// has no duration or segments, and the silence checks then say nothing.
func ParseVerbose(raw []byte) (Verbose, error) {
	var v Verbose
	err := json.Unmarshal(raw, &v)
	return v, err
}

// stockPhrases are what Whisper says over silence. Compared after lowercasing
// and dropping punctuation.
var stockPhrases = map[string]bool{
	"thank you":                            true,
	"thank you very much":                  true,
	"thanks":                               true,
	"thanks for watching":                  true,
	"thank you for watching":               true,
	"thanks for listening":                 true,
	"thank you for listening":              true,
	"please subscribe":                     true,
	"subscribe":                            true,
	"you":                                  true,
	"bye":                                  true,
	"bye bye":                              true,
	"see you next time":                    true,
	"ill see you next time":                true,
	"subtitles by the amaraorg community":  true,
	"subtitles by the amara org community": true,
	"transcription by castingwords":        true,
	"thank you so much for watching":       true,
	"thanks for watching and see you next time": true,
}

func normalizePhrase(s string) string {
	var b strings.Builder
	for _, r := range strings.ToLower(s) {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		case r == ' ', r == '-':
			b.WriteRune(' ')
		}
	}
	return strings.Join(strings.Fields(b.String()), " ")
}

// IsStockPhrase reports a transcript that is only one of Whisper's
// silence phrases.
func IsStockPhrase(text string) bool {
	return stockPhrases[normalizePhrase(text)]
}

const (
	// overrunSeconds: a segment ending this far past the audio was invented.
	overrunSeconds = 1.0
	// tailSeconds: real dictation stops before the clip does (both apps
	// record ~0.3 s after release), so speech running to within this of the
	// end with nothing after it is noise being read as words.
	tailSeconds = 0.15
	// shortClipSeconds: a stock phrase in a clip this short is a tap, not
	// speech.
	shortClipSeconds = 1.2
	// garbledLogprob: an answer this unsure that also runs to the end of the
	// clip is noise.
	garbledLogprob = -0.9
	// garbledMaxWords bounds the garbled check to short answers.
	garbledMaxWords = 12
)

// SilenceHallucination reports a transcript Whisper made up from silence or
// noise, with a short reason for logs. It returns false when the answer has
// no duration or segments (plain json, or an older provider): it never
// guesses without evidence.
func SilenceHallucination(v Verbose) (bool, string) {
	if v.Duration <= 0 || len(v.Segments) == 0 || IsNoSpeech(v.Text) {
		return false, ""
	}
	invented := 0
	lastEnd := 0.0
	sumLP := 0.0
	for _, s := range v.Segments {
		if s.Start >= v.Duration || s.End > v.Duration+overrunSeconds {
			invented++
		}
		if s.End > lastEnd {
			lastEnd = s.End
		}
		sumLP += s.AvgLogprob
	}
	if invented == len(v.Segments) {
		return true, "overrun"
	}
	runsToEnd := lastEnd >= v.Duration-tailSeconds
	if IsStockPhrase(v.Text) && (runsToEnd || v.Duration < shortClipSeconds) {
		return true, "stock_phrase"
	}
	avgLP := sumLP / float64(len(v.Segments))
	if runsToEnd && avgLP < garbledLogprob && len(strings.Fields(v.Text)) <= garbledMaxWords {
		return true, "garbled"
	}
	return false, ""
}
