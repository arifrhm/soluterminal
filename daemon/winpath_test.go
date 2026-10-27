package main

import (
	"strings"
	"testing"
)

// pathOf returns the value of the (case-insensitive) PATH entry in env, or "" .
func pathOf(env []string) (string, string) {
	for _, kv := range env {
		if eq := strings.IndexByte(kv, '='); eq >= 0 && strings.EqualFold(kv[:eq], "PATH") {
			return kv[:eq], kv[eq+1:]
		}
	}
	return "", ""
}

func TestUnionWindowsPath_EmptyLiveIsNoop(t *testing.T) {
	t.Parallel()
	in := []string{"Path=C:\\Windows", "FOO=bar"}
	out := unionWindowsPath(in, "")
	if _, got := pathOf(out); got != "C:\\Windows" {
		t.Fatalf("blank live should leave PATH untouched, got %q", got)
	}
	if len(out) != 2 {
		t.Fatalf("blank live should not add entries, got %v", out)
	}
}

func TestUnionWindowsPath_UnionAndOrder(t *testing.T) {
	t.Parallel()
	// Live (registry) dirs come first, then inherited dirs not already present.
	in := []string{`Path=C:\Windows;C:\Old`}
	live := `C:\Windows;C:\Users\x\.local\bin`
	_, got := pathOf(unionWindowsPath(in, live))
	want := `C:\Windows;C:\Users\x\.local\bin;C:\Old`
	if got != want {
		t.Fatalf("union/order wrong:\n got %q\nwant %q", got, want)
	}
}

func TestUnionWindowsPath_CaseAndSlashDedup(t *testing.T) {
	t.Parallel()
	// Same dir differing only by case or a trailing slash must not duplicate.
	in := []string{`Path=c:\windows\`}
	live := `C:\Windows`
	_, got := pathOf(unionWindowsPath(in, live))
	if got != `C:\Windows` {
		t.Fatalf("expected case/slash-insensitive dedup to keep one entry, got %q", got)
	}
}

func TestUnionWindowsPath_PreservesKeySpelling(t *testing.T) {
	t.Parallel()
	// Must reuse the existing key ("Path") — not introduce a second "PATH=".
	in := []string{`Path=C:\Windows`}
	out := unionWindowsPath(in, `C:\New`)
	key, _ := pathOf(out)
	if key != "Path" {
		t.Fatalf("expected key spelling preserved as Path, got %q", key)
	}
	n := 0
	for _, kv := range out {
		if eq := strings.IndexByte(kv, '='); eq >= 0 && strings.EqualFold(kv[:eq], "PATH") {
			n++
		}
	}
	if n != 1 {
		t.Fatalf("expected exactly one PATH-like entry, got %d in %v", n, out)
	}
}

func TestUnionWindowsPath_AppendsWhenNoPath(t *testing.T) {
	t.Parallel()
	in := []string{"FOO=bar"}
	out := unionWindowsPath(in, `C:\New`)
	key, got := pathOf(out)
	if key != "Path" || got != `C:\New` {
		t.Fatalf("expected appended Path=C:\\New, got key=%q val=%q", key, got)
	}
}

func TestUnionWindowsPath_EmptyInheritedKeepsLive(t *testing.T) {
	t.Parallel()
	in := []string{"Path="}
	_, got := pathOf(unionWindowsPath(in, `C:\A;C:\B`))
	if got != `C:\A;C:\B` {
		t.Fatalf("expected live dirs when inherited PATH empty, got %q", got)
	}
}
