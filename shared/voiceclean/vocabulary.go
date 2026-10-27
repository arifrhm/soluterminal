package voiceclean

import (
	"strings"
	"unicode"
)

// Whisper accepts a short "prompt" that biases spelling toward the words in
// it. We send a fixed list of product and tool names (plus the user's own
// words) so names like Shinoque or kubectl come out right. It never carries
// terminal text: that would send the user's screen to the speech provider.

// BaseVocabulary is sent with every transcription.
var BaseVocabulary = []string{
	"SoluTerminal", "Shinoque", "SoluFlow", "Claude", "Claude Code",
	"Codex", "kubectl", "npm", "pnpm", "Git", "GitHub", "WSL", "Tauri",
}

const (
	// MaxCustomWords bounds the user's own list. Whisper reads at most 224
	// prompt tokens, and a long list dilutes the bias anyway.
	MaxCustomWords = 30
	// MaxWordLen bounds one entry ("Claude Code" is an entry, not two).
	MaxWordLen = 40
)

// ParseWordList splits a comma- or newline-separated list.
func ParseWordList(s string) []string {
	return strings.FieldsFunc(s, func(r rune) bool { return r == ',' || r == '\n' || r == ';' })
}

// SanitizeWords trims, drops anything that isn't a plain word or name,
// removes duplicates (including base words) and caps the list.
func SanitizeWords(in []string) []string {
	seen := map[string]bool{}
	for _, w := range BaseVocabulary {
		seen[strings.ToLower(w)] = true
	}
	out := []string{}
	for _, raw := range in {
		w := strings.Join(strings.Fields(raw), " ")
		if w == "" || len(w) > MaxWordLen || !plainWord(w) {
			continue
		}
		key := strings.ToLower(w)
		if seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, w)
		if len(out) == MaxCustomWords {
			break
		}
	}
	return out
}

func plainWord(w string) bool {
	hasLetter := false
	for _, r := range w {
		switch {
		case unicode.IsLetter(r):
			hasLetter = true
		case unicode.IsDigit(r), r == ' ', r == '.', r == '-', r == '_', r == '+', r == '#', r == '/', r == '@', r == '\'':
		default:
			return false
		}
	}
	return hasLetter
}

// VocabularyPrompt is the Whisper prompt: the base list plus the user's words.
func VocabularyPrompt(custom []string) string {
	words := append(append([]string{}, BaseVocabulary...), SanitizeWords(custom)...)
	return strings.Join(words, ", ") + "."
}

// latinLanguages are Whisper language codes written in Latin script. An empty
// language (auto-detect) is treated the same way: the user can't have meant a
// non-Latin script just because the prompt was Latin.
var latinLanguages = map[string]bool{
	"": true, "en": true, "ms": true, "id": true, "es": true, "fr": true, "de": true,
	"it": true, "pt": true, "nl": true, "tl": true, "vi": true, "sv": true, "da": true,
	"no": true, "fi": true, "pl": true, "cs": true, "ro": true, "tr": true,
}

// ForeignScript reports whether a transcript contains letters outside the
// Latin script when the user's language is Latin-script (or auto). A prompted
// Whisper run on near-silence has been seen to answer in Chinese characters;
// the caller then retries without the prompt.
func ForeignScript(text, language string) bool {
	if !latinLanguages[strings.ToLower(strings.TrimSpace(language))] {
		return false
	}
	for _, r := range text {
		if unicode.IsLetter(r) && !unicode.Is(unicode.Latin, r) {
			return true
		}
	}
	return false
}

// EchoesPrompt reports whether a transcript is mostly the prompt's own words,
// the other way a prompted Whisper hallucinates on silence or noise.
func EchoesPrompt(text string, custom []string) bool {
	vocab := map[string]bool{}
	for _, w := range append(append([]string{}, BaseVocabulary...), SanitizeWords(custom)...) {
		for _, t := range tokens(w) {
			vocab[t] = true
		}
	}
	toks := tokens(text)
	if len(toks) < 2 {
		return false
	}
	hits := 0
	for _, t := range toks {
		if vocab[t] {
			hits++
		}
	}
	return hits >= 2 && hits*10 >= len(toks)*7
}

func tokens(s string) []string {
	return strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		return !unicode.IsLetter(r) && !unicode.IsDigit(r)
	})
}

// PromptedResultSuspect is the single check callers use after a prompted run.
func PromptedResultSuspect(text, language string, custom []string) (bool, string) {
	if strings.TrimSpace(text) == "" {
		return false, ""
	}
	if ForeignScript(text, language) {
		return true, "script"
	}
	if EchoesPrompt(text, custom) {
		return true, "echo"
	}
	return false, ""
}

// ShortCheckWords: a prompted answer this short is double-checked against an
// unprompted run. Hallucinations on room tone and silence were all 1-6 words
// ("Sosu.", "Scefie, Kudel, GitHub, Tauri."), where the unprompted run hears
// nothing.
const ShortCheckWords = 6

// SmallAudioBytes: recordings this small are short enough that callers start
// the unprompted check alongside the prompted run, so it costs no extra wait.
const SmallAudioBytes = 48 << 10

// NeedsUnpromptedCheck reports whether a prompted answer must be compared with
// an unprompted run before it is trusted.
func NeedsUnpromptedCheck(prompted, language string, custom []string) bool {
	if IsNoSpeech(prompted) {
		return false
	}
	if bad, _ := PromptedResultSuspect(prompted, language, custom); bad {
		return true
	}
	n := len(strings.Fields(prompted))
	return n > 0 && n <= ShortCheckWords
}

// ChooseTranscript picks between a prompted answer and its unprompted check.
// When the unprompted run hears nothing, nothing was said; when the prompted
// answer looks hallucinated, the unprompted one wins; otherwise the prompted
// answer (better names) is kept.
func ChooseTranscript(prompted, unprompted, language string, custom []string) (text string, usedUnprompted bool, reason string) {
	if IsNoSpeech(unprompted) {
		return unprompted, true, "no_speech"
	}
	if bad, why := PromptedResultSuspect(prompted, language, custom); bad {
		return unprompted, true, why
	}
	return prompted, false, ""
}
