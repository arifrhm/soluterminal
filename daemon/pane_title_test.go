package main

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// Titles run on the user's own `claude -p`: tools off, no MCP, no session
// file, Haiku. The prompt rides stdin and the output is sanitized.
func TestGenerateTitleViaCLI(t *testing.T) {
	prev := titleRunner
	t.Cleanup(func() { titleRunner = prev })

	t.Run("sanitizes the CLI output", func(t *testing.T) {
		var gotPrompt string
		titleRunner = func(_ context.Context, prompt string) (string, error) {
			gotPrompt = prompt
			return "  \"Wire up title endpoint.\"\n", nil
		}
		got, err := generateTitleViaCLI(context.Background(), "User: add a title endpoint")
		if err != nil || got != "Wire up title endpoint" {
			t.Fatalf("got %q err=%v", got, err)
		}
		if !strings.HasPrefix(gotPrompt, titleSystemTask) || !strings.Contains(gotPrompt, "User: add a title endpoint") {
			t.Fatalf("prompt=%q", gotPrompt)
		}
	})

	t.Run("empty output errors", func(t *testing.T) {
		titleRunner = func(context.Context, string) (string, error) { return " \n ", nil }
		if _, err := generateTitleViaCLI(context.Background(), "x"); err == nil {
			t.Fatal("expected an error for empty output")
		}
	})

	t.Run("runner failure passes through", func(t *testing.T) {
		titleRunner = func(context.Context, string) (string, error) { return "", errTitleCLIUnavailable }
		if _, err := generateTitleViaCLI(context.Background(), "x"); !errors.Is(err, errTitleCLIUnavailable) {
			t.Fatalf("err=%v", err)
		}
	})
}

func TestTitleCLIArgsAreInert(t *testing.T) {
	args := strings.Join(titleCLIArgs(), " ")
	for _, want := range []string{"-p", "--tools ", "--strict-mcp-config", "--no-session-persistence", "--model haiku", "--safe-mode"} {
		if !strings.Contains(args, want) {
			t.Fatalf("args %q missing %q", args, want)
		}
	}
	for i, a := range titleCLIArgs() {
		if a == "--tools" && titleCLIArgs()[i+1] != "" {
			t.Fatalf("--tools must be empty, got %q", titleCLIArgs()[i+1])
		}
	}
}

// sanitizeTitle is the pure normalizer; cover the messy inputs the model emits.
func TestSanitizeTitle(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"plain", "Add OAuth refresh", "Add OAuth refresh"},
		{"surrounding straight quotes", `"Add OAuth refresh"`, "Add OAuth refresh"},
		{"surrounding single quotes", `'Add OAuth refresh'`, "Add OAuth refresh"},
		{"smart quotes", "“Add OAuth refresh”", "Add OAuth refresh"},
		{"trailing period", "Add OAuth refresh.", "Add OAuth refresh"},
		{"trailing exclamation", "Done!", "Done"},
		{"newline + tabs collapse to single spaces", "Add\n\tOAuth   refresh", "Add OAuth refresh"},
		{"leading/trailing whitespace", "   trim me   ", "trim me"},
		{"empty", "", ""},
		{"whitespace only", "  \n\t ", ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := sanitizeTitle(c.in); got != c.want {
				t.Fatalf("sanitizeTitle(%q) = %q, want %q", c.in, got, c.want)
			}
		})
	}

	t.Run("overlong clamps to <=60 chars on a valid boundary", func(t *testing.T) {
		long := "This is an absurdly long title that the model definitely should not have produced at all"
		got := sanitizeTitle(long)
		if len(got) > 60 {
			t.Fatalf("len %d > 60: %q", len(got), got)
		}
		if got == "" {
			t.Fatal("clamped to empty")
		}
	})

	t.Run("overlong multibyte does not split a rune", func(t *testing.T) {
		// 40 three-byte runes = 120 bytes; clamping to 60 bytes must land on a
		// rune boundary (no invalid UTF-8 in the output).
		long := ""
		for i := 0; i < 40; i++ {
			long += "界"
		}
		got := sanitizeTitle(long)
		if len(got) > 60 {
			t.Fatalf("len %d > 60", len(got))
		}
		for _, r := range got {
			if r == '�' {
				t.Fatal("output contains the replacement rune (split a multibyte rune)")
			}
		}
	})
}

// buildTitleConvText flattens turns and keeps the NEWEST within the budget.
func TestBuildTitleConvText(t *testing.T) {
	t.Parallel()
	t.Run("flattens roles + tool_use", func(t *testing.T) {
		turns := []convTurn{
			{Role: "user", Kind: "text", Text: "fix the env bug"},
			{Role: "assistant", Kind: "text", Text: "I'll read the file."},
			{Role: "assistant", Kind: "tool_use", Tool: "Read", ToolHint: "config.go"},
		}
		got := buildTitleConvText(turns, titleConvMaxChars)
		want := "User: fix the env bug\nAssistant: I'll read the file.\n[tool: Read config.go]"
		if got != want {
			t.Fatalf("got %q want %q", got, want)
		}
	})

	t.Run("tool_use without hint", func(t *testing.T) {
		turns := []convTurn{{Role: "assistant", Kind: "tool_use", Tool: "Bash"}}
		if got := buildTitleConvText(turns, titleConvMaxChars); got != "[tool: Bash]" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("empty turns → empty string", func(t *testing.T) {
		if got := buildTitleConvText(nil, titleConvMaxChars); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("blank text turns are skipped", func(t *testing.T) {
		turns := []convTurn{
			{Role: "user", Kind: "text", Text: "   "},
			{Role: "assistant", Kind: "text", Text: ""},
		}
		if got := buildTitleConvText(turns, titleConvMaxChars); got != "" {
			t.Fatalf("got %q", got)
		}
	})

	t.Run("keeps the NEWEST turns within the budget", func(t *testing.T) {
		turns := []convTurn{
			{Role: "user", Kind: "text", Text: "oldest line"},
			{Role: "user", Kind: "text", Text: "middle line"},
			{Role: "user", Kind: "text", Text: "newest line"},
		}
		// Budget only fits the last line (+ its newline).
		got := buildTitleConvText(turns, len("User: newest line")+1)
		if got != "User: newest line" {
			t.Fatalf("got %q", got)
		}
	})
}
