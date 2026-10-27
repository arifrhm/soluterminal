package main

import "testing"

func TestIsObviousMenuReply(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want bool
	}{
		{"1", true},
		{"9", true},
		{"0", false},
		{"10", false},
		{"y", true},
		{"N", true},
		{"yes", true},
		{"No", true},
		{" yes", false},
		{"yes\n", false},
		{"maybe", false},
		{"", false},
	}
	for _, c := range cases {
		if got := isObviousMenuReply(c.in); got != c.want {
			t.Fatalf("isObviousMenuReply(%q) = %v, want %v", c.in, got, c.want)
		}
	}
}

func TestCautiousAutoDenyReason(t *testing.T) {
	t.Parallel()
	menu := "Do you want to proceed?\n› 1. Yes, proceed\n  2. No"
	if got := cautiousAutoDenyReason(menu, "1\n"); got != "" {
		t.Fatalf("cautiousAutoDenyReason(low-risk menu) = %q, want allow", got)
	}
	if got := cautiousAutoDenyReason("Would you like me to continue?", "1\n"); got == "" {
		t.Fatal("cautiousAutoDenyReason allowed prose without focused menu")
	}
	if got := cautiousAutoDenyReason(menu, "approve everything\n"); got == "" {
		t.Fatal("cautiousAutoDenyReason allowed non-menu reply")
	}
	risky := "Run `npm install left-pad`?\n› 1. Yes, proceed\n  2. No"
	if got := cautiousAutoDenyReason(risky, "1\n"); got == "" {
		t.Fatal("cautiousAutoDenyReason allowed risky install prompt")
	}
	genericChoice := "How should I handle this?\n› 1. Fix it now\n  2. Explain tradeoffs\n  3. Chat about this\nEnter to select · ↑/↓ to navigate · Esc to cancel"
	if got := cautiousAutoDenyReason(genericChoice, "1\n"); got == "" {
		t.Fatal("cautiousAutoDenyReason allowed generic choice menu")
	}
}

func TestSplitTrailingEnter(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in         string
		wantBody   string
		wantSubmit bool
	}{
		{"hello\n", "hello", true},
		{"hello\r", "hello", true},
		{"hello\r\n", "hello", true},
		{"hello", "hello", false},
		{"hello\nworld\n", "hello\nworld\n", false},
		{"\n", "", true},
		{"", "", false},
	}
	for _, c := range cases {
		body, submit := splitTrailingEnter(c.in)
		if body != c.wantBody || submit != c.wantSubmit {
			t.Fatalf("splitTrailingEnter(%q) = (%q, %v), want (%q, %v)",
				c.in, body, submit, c.wantBody, c.wantSubmit)
		}
	}
}

func TestShouldSubmitFocusedChoiceWithEnter(t *testing.T) {
	t.Parallel()
	p := testPaneWithTail(t, "pane_1", "Pane 1", "Would you like to proceed?\n› 1. Yes\n  2. No")
	if got := p.computeState(); got != StateChoicePrompt {
		t.Fatalf("computeState = %s, want choice-prompt", got)
	}
	if !shouldSubmitFocusedChoiceWithEnter(p, "1\n") {
		t.Fatal("focused option-1 submit should be reducible to Enter")
	}
	if shouldSubmitFocusedChoiceWithEnter(p, "2\n") {
		t.Fatal("non-focused option should not be reduced to Enter")
	}
	if shouldSubmitFocusedChoiceWithEnter(p, "1") {
		t.Fatal("unsubmitted input should not be reduced to Enter")
	}
}

func TestUnescapeAriaInput(t *testing.T) {
	t.Parallel()
	cases := []struct {
		in   string
		want string
	}{
		{`hello\n`, "hello\n"},
		{`tab\treturn\r`, "tab\treturn\r"},
		{`quote\"slash\\`, "quote\"slash\\"},
		{`nul\0x`, "nul\x00x"},
		{`\x03`, "\x03"},
		{`\u263a`, "☺"},
		{`C:\Users\ben`, `C:\Users\ben`},
		{`\xZZ`, `\xZZ`},
		{`\uZZZZ`, `\uZZZZ`},
	}
	for _, c := range cases {
		if got := unescapeAriaInput(c.in); got != c.want {
			t.Fatalf("unescapeAriaInput(%q) = %q, want %q", c.in, got, c.want)
		}
	}
}
