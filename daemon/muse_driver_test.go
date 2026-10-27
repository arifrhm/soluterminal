package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// pasteFixture writes name (a solu-paste-* image) under a real
// ~/.soluterminal/pastes-shaped tree and returns its absolute path. The
// extractor is pinned to that shape, so the fixture must carry it.
func pasteFixture(t *testing.T, name string, size int) string {
	t.Helper()
	dir := filepath.Join(t.TempDir(), ".soluterminal", "pastes")
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatal(err)
	}
	p := filepath.Join(dir, name)
	if err := os.WriteFile(p, []byte(strings.Repeat("x", size)), 0o600); err != nil {
		t.Fatal(err)
	}
	return p
}

func TestMusePasteImagePaths(t *testing.T) {
	t.Parallel()
	a := pasteFixture(t, "solu-paste-abc123.png", 16)
	b := pasteFixture(t, "solu-paste-def456.jpg", 16)

	t.Run("at-mention and plain path both attach", func(t *testing.T) {
		got := musePasteImagePaths("look at @" + a + " and " + b + " please")
		if len(got) != 2 || got[0] != a || got[1] != b {
			t.Fatalf("paths = %v, want [%s %s]", got, a, b)
		}
	})

	t.Run("duplicates collapse to first-seen order", func(t *testing.T) {
		got := musePasteImagePaths(b + " then @" + a + " then " + b)
		if len(got) != 2 || got[0] != b || got[1] != a {
			t.Fatalf("paths = %v, want [%s %s]", got, b, a)
		}
	})

	t.Run("trailing punctuation is not part of the path", func(t *testing.T) {
		got := musePasteImagePaths("see (" + a + "), ok?")
		if len(got) != 1 || got[0] != a {
			t.Fatalf("paths = %v, want [%s]", got, a)
		}
	})

	t.Run("missing files are skipped, text mention stays", func(t *testing.T) {
		missing := filepath.Join(filepath.Dir(a), "solu-paste-000000.png")
		got := musePasteImagePaths("see " + missing + " and @" + a)
		if len(got) != 1 || got[0] != a {
			t.Fatalf("paths = %v, want [%s]", got, a)
		}
	})

	t.Run("non-paste image paths are ignored", func(t *testing.T) {
		other := pasteFixture(t, "screenshot.png", 16)
		plainDir := filepath.Dir(filepath.Dir(other))
		system := filepath.Join(plainDir, "icons", "app.png")
		if err := os.MkdirAll(filepath.Dir(system), 0o755); err != nil {
			t.Fatal(err)
		}
		if err := os.WriteFile(system, []byte("x"), 0o600); err != nil {
			t.Fatal(err)
		}
		// other has an image ext and exists but is not a solu-paste upload;
		// system is outside the paste tree entirely.
		if got := musePasteImagePaths("see " + other + " and " + system); len(got) != 0 {
			t.Fatalf("paths = %v, want []", got)
		}
	})

	t.Run("empty uploads are skipped", func(t *testing.T) {
		empty := pasteFixture(t, "solu-paste-empty00.png", 0)
		// Zero-byte file: created on disk but skipped by the extractor.
		if got := musePasteImagePaths("see " + empty); len(got) != 0 {
			t.Fatalf("paths = %v, want []", got)
		}
		if got := musePasteImagePaths("no images here"); len(got) != 0 {
			t.Fatalf("paths = %v, want []", got)
		}
	})
}
