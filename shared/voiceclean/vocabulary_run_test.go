package voiceclean

import (
	"context"
	"sync"
	"testing"
)

type fakeAnswer struct {
	text string
	ok   bool
}

func fakeRun(prompted, unprompted fakeAnswer, calls *[]string, mu *sync.Mutex) func(context.Context, string) fakeAnswer {
	return func(_ context.Context, prompt string) fakeAnswer {
		mu.Lock()
		*calls = append(*calls, prompt)
		mu.Unlock()
		if prompt != "" {
			return prompted
		}
		return unprompted
	}
}

func textOfFake(a fakeAnswer) (string, bool) { return a.text, a.ok }

func TestRunWithVocabulary(t *testing.T) {
	cases := []struct {
		name               string
		bytes              int
		prompted, unprompt fakeAnswer
		want, note         string
		calls              int
	}{
		{"room tone becomes no speech", 8 << 10, fakeAnswer{"Sosu.", true}, fakeAnswer{".", true}, ".", "no_speech", 2},
		{"short real speech keeps the names", 8 << 10, fakeAnswer{"Deploy Shinoque.", true}, fakeAnswer{"Deploy shinoki.", true}, "Deploy Shinoque.", "", 2},
		{"long answer needs no check on big audio", 200 << 10, fakeAnswer{"Restart SoluTerminal and reconnect all of the panes now.", true}, fakeAnswer{"x", true}, "Restart SoluTerminal and reconnect all of the panes now.", "", 1},
		{"short answer on big audio checks serially", 200 << 10, fakeAnswer{"GitHub, Tauri.", true}, fakeAnswer{".", true}, ".", "no_speech", 2},
		{"prompt failure falls back when the check already ran", 8 << 10, fakeAnswer{"", false}, fakeAnswer{"hello there", true}, "hello there", "prompt_failed", 2},
		{"prompt failure on big audio is returned as is", 200 << 10, fakeAnswer{"", false}, fakeAnswer{"hello", true}, "", "", 1},
		{"failed check keeps the prompted answer", 8 << 10, fakeAnswer{"Yes, do it.", true}, fakeAnswer{"", false}, "Yes, do it.", "", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			var mu sync.Mutex
			got, note := RunWithVocabulary(context.Background(), c.bytes, "en", nil, "SoluTerminal.",
				fakeRun(c.prompted, c.unprompt, &calls, &mu), textOfFake)
			if got.text != c.want || note != c.note {
				t.Fatalf("got %q (%q), want %q (%q)", got.text, note, c.want, c.note)
			}
			mu.Lock()
			n := len(calls)
			mu.Unlock()
			// A parallel check that wasn't needed may still be in flight.
			if c.calls == 1 && n != 1 {
				t.Fatalf("calls = %d, want 1", n)
			}
			if c.calls == 2 && n != 2 {
				t.Fatalf("calls = %d, want 2", n)
			}
		})
	}
	t.Run("no prompt is one plain run", func(t *testing.T) {
		var calls []string
		var mu sync.Mutex
		got, _ := RunWithVocabulary(context.Background(), 1, "en", nil, "", fakeRun(fakeAnswer{"p", true}, fakeAnswer{"u", true}, &calls, &mu), textOfFake)
		if got.text != "u" || len(calls) != 1 {
			t.Fatalf("got %q calls=%d", got.text, len(calls))
		}
	})
}

func TestRunWithVocabularySingleSmallRun(t *testing.T) {
	cases := []struct {
		name               string
		bytes              int
		prompted, unprompt fakeAnswer
		want, note         string
		calls              int
	}{
		{"short command sends one request", 8 << 10, fakeAnswer{"Deploy Shinoque.", true}, fakeAnswer{"Deploy shinoki.", true}, "Deploy Shinoque.", "", 1},
		{"short name list is kept without a check", 8 << 10, fakeAnswer{"Sosu.", true}, fakeAnswer{".", true}, "Sosu.", "", 1},
		{"prompt echo still checks serially", 8 << 10, fakeAnswer{"SoluTerminal, Claude, Codex.", true}, fakeAnswer{".", true}, ".", "no_speech", 2},
		{"prompt failure is returned as is", 8 << 10, fakeAnswer{"", false}, fakeAnswer{"hello", true}, "", "", 1},
		{"big audio keeps the short-answer check", 200 << 10, fakeAnswer{"GitHub, Tauri.", true}, fakeAnswer{".", true}, ".", "no_speech", 2},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			var calls []string
			var mu sync.Mutex
			got, note := RunWithVocabularyMode(context.Background(), c.bytes, "en", nil, "SoluTerminal.", false,
				fakeRun(c.prompted, c.unprompt, &calls, &mu), textOfFake)
			if got.text != c.want || note != c.note {
				t.Fatalf("got %q (%q), want %q (%q)", got.text, note, c.want, c.note)
			}
			mu.Lock()
			n := len(calls)
			mu.Unlock()
			if n != c.calls {
				t.Fatalf("calls = %d, want %d", n, c.calls)
			}
		})
	}
}
