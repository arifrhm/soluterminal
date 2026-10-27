package voiceclean

import (
	"strings"
	"testing"
)

func TestVocabularyPromptHasBaseAndCustomWords(t *testing.T) {
	p := VocabularyPrompt([]string{"Mara Ellison", "  ledger  "})
	for _, w := range []string{"SoluTerminal", "Shinoque", "kubectl", "Mara Ellison", "ledger"} {
		if !strings.Contains(p, w) {
			t.Fatalf("prompt %q is missing %q", p, w)
		}
	}
	if !strings.HasSuffix(p, ".") {
		t.Fatalf("prompt should end with a full stop: %q", p)
	}
}

func TestSanitizeWordsDropsJunkDuplicatesAndCaps(t *testing.T) {
	in := []string{"", "Shinoque", "shinoque", "rm -rf /; echo", "<script>", strings.Repeat("a", MaxWordLen+1), "123", "Mara", "mara"}
	got := SanitizeWords(in)
	if len(got) != 1 || got[0] != "Mara" {
		t.Fatalf("SanitizeWords = %q, want [Mara]", got)
	}
	many := make([]string, 0, MaxCustomWords+10)
	for i := 0; i < MaxCustomWords+10; i++ {
		many = append(many, "word"+strings.Repeat("x", i%5)+string(rune('a'+i%26))+string(rune('a'+i/26)))
	}
	if n := len(SanitizeWords(many)); n != MaxCustomWords {
		t.Fatalf("cap: got %d words, want %d", n, MaxCustomWords)
	}
}

func TestParseWordList(t *testing.T) {
	got := ParseWordList("Mara, Ellison\nLedger;Kai")
	if len(got) != 4 {
		t.Fatalf("ParseWordList = %q", got)
	}
}

func TestForeignScript(t *testing.T) {
	cases := []struct {
		text, lang string
		want       bool
	}{
		{"open Shinoque and check the mail", "en", false},
		{"café résumé naïve", "en", false},
		{"字幕由Amara.org社区提供", "en", true},
		{"字幕由Amara.org社区提供", "", true},
		{"字幕由Amara.org社区提供", "zh", false},
		{"Привет", "en", true},
		{"Привет", "ru", false},
	}
	for _, c := range cases {
		if got := ForeignScript(c.text, c.lang); got != c.want {
			t.Errorf("ForeignScript(%q, %q) = %v, want %v", c.text, c.lang, got, c.want)
		}
	}
}

func TestEchoesPrompt(t *testing.T) {
	if !EchoesPrompt("SoluTerminal, Shinoque, SoluFlow, Claude Code.", nil) {
		t.Fatal("a bare list of prompt words is an echo")
	}
	if !EchoesPrompt("Mara Ellison, Claude Code.", []string{"Mara Ellison"}) {
		t.Fatal("custom words count toward the echo")
	}
	if EchoesPrompt("open Shinoque and check the mail space", nil) {
		t.Fatal("a real sentence with one name is not an echo")
	}
	if EchoesPrompt("Codex", nil) {
		t.Fatal("a single word is too little to call an echo")
	}
}

func TestPromptedResultSuspect(t *testing.T) {
	if bad, why := PromptedResultSuspect("字幕", "en", nil); !bad || why != "script" {
		t.Fatalf("script: got %v %q", bad, why)
	}
	if bad, why := PromptedResultSuspect("Git, GitHub, WSL, Tauri.", "en", nil); !bad || why != "echo" {
		t.Fatalf("echo: got %v %q", bad, why)
	}
	if bad, _ := PromptedResultSuspect("restart SoluTerminal and reconnect the panes", "en", nil); bad {
		t.Fatal("a normal sentence is not suspect")
	}
	if bad, _ := PromptedResultSuspect("", "en", nil); bad {
		t.Fatal("empty text is handled by the no-speech check, not here")
	}
}

func TestNeedsUnpromptedCheck(t *testing.T) {
	for _, junk := range []string{"Sosu.", "Riturna", "Slep, tauri.", "Scefie, Kudel, GitHub, Tauri.", "Rol, Rol, Rol, Rol, Rol, Rol", "3"} {
		if !NeedsUnpromptedCheck(junk, "en", nil) {
			t.Errorf("%q should be checked", junk)
		}
	}
	if NeedsUnpromptedCheck("Restart SoluTerminal and reconnect the panes now please.", "en", nil) {
		t.Error("a long sentence needs no check")
	}
	if NeedsUnpromptedCheck(".", "en", nil) || NeedsUnpromptedCheck("", "en", nil) {
		t.Error("no speech needs no check")
	}
}

func TestChooseTranscript(t *testing.T) {
	cases := []struct {
		prompted, unprompted, want, reason string
	}{
		{"Sosu.", ".", ".", "no_speech"},
		{"Scefie, Kudel, GitHub, Tauri.", ".", ".", "no_speech"},
		{"Yes, do it.", "Yes do it.", "Yes, do it.", ""},
		{"Deploy Shinoque.", "Deploy shinoki.", "Deploy Shinoque.", ""},
		{"Git, GitHub, WSL.", "get the hub", "get the hub", "echo"},
		{"字幕", "hello", "hello", "script"},
	}
	for _, c := range cases {
		got, _, why := ChooseTranscript(c.prompted, c.unprompted, "en", nil)
		if got != c.want || why != c.reason {
			t.Errorf("ChooseTranscript(%q, %q) = %q (%q), want %q (%q)", c.prompted, c.unprompted, got, why, c.want, c.reason)
		}
	}
}
