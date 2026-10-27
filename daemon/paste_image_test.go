package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestPasteImageDirIsPrivate(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)

	dir, err := pasteImageDir()
	if err != nil {
		t.Fatal(err)
	}
	if want := filepath.Join(home, ".soluterminal", "pastes"); dir != want {
		t.Fatalf("dir = %q, want %q", dir, want)
	}
	info, err := os.Stat(dir)
	if err != nil {
		t.Fatal(err)
	}
	if got := info.Mode().Perm(); got != 0o700 {
		t.Fatalf("mode = %o, want 700", got)
	}
}

func TestPastedImageExt(t *testing.T) {
	tests := []struct {
		contentType string
		want        string
	}{
		{"image/png", ".png"},
		{"image/png; charset=utf-8", ".png"},
		{"IMAGE/JPEG", ".jpg"},
		{"image/jpg", ".jpg"},
		{"image/gif", ".gif"},
		{"image/webp", ".webp"},
		{"text/plain", ""},
		{"", ""},
	}

	for _, tt := range tests {
		if got := pastedImageExt(tt.contentType); got != tt.want {
			t.Fatalf("pastedImageExt(%q) = %q, want %q", tt.contentType, got, tt.want)
		}
	}
}
