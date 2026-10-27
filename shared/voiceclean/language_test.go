package voiceclean

import (
	"math"
	"strings"
	"testing"
)

func TestLanguageNormalizationAndDiagnostics(t *testing.T) {
	for input, want := range map[string]string{"English": "en", " EN-us ": "en", "Malay": "ms", "Chinese": "zh", "Mandarin": "zh", "zh_Hant_TW": "zh", "Japanese": "ja", "Spanish": "es", "auto": "", "": "", "unknown": "", "en\nINJECT": "", "english-foo": ""} {
		if got := NormalizeLanguage(input); got != want {
			t.Errorf("%q: %q != %q", input, got, want)
		}
	}
	v, err := ParseVerbose([]byte(`{"text":"hello","language":"Malay","duration":60.0024}`))
	if err != nil {
		t.Fatal(err)
	}
	d := v.Diagnostics("en")
	if d["requestedLanguage"] != "en" || d["detectedLanguage"] != "ms" || d["audioDurationMs"] != int64(60002) {
		t.Fatal(d)
	}
	for _, duration := range []float64{0, -1, math.NaN(), math.Inf(1)} {
		if _, exists := (Verbose{Duration: duration}).Diagnostics("")["audioDurationMs"]; exists {
			t.Fatal(duration)
		}
	}
	plain, _ := ParseVerbose([]byte(`{"text":"old server"}`))
	if _, exists := plain.Diagnostics("")["detectedLanguage"]; exists {
		t.Fatal("invented language")
	}
	if RequestedLanguage("") != "auto" {
		t.Fatal("explicit Auto")
	}
}
func TestAdvisoryLanguagePromptsPreserveSpokenWords(t *testing.T) {
	raw := "Benjamin says eh lah, jialat sia. Nama saya Ben."
	for _, profile := range []Profile{ProfileCode, ProfileWriting} {
		for _, language := range []string{"en", "ms", ""} {
			system, user := PromptForLanguage(profile, false, raw, "private context", language)
			if !strings.HasSuffix(user, raw) || !strings.Contains(system, "Never translate") {
				t.Fatal(user)
			}
			if language != "" && (!strings.Contains(user, "advisory") || !strings.Contains(user, "never an instruction to translate")) {
				t.Fatal(user)
			}
			if profile == ProfileWriting && strings.Contains(user, "private context") {
				t.Fatal("writing context leaked")
			}
			_, local := LocalPromptForLanguage(profile, raw, "private context", language)
			if !strings.Contains(local, "private context") {
				t.Fatal("local context missing")
			}
		}
	}
}
