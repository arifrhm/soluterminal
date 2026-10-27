package main

import (
	"os"
	"path/filepath"
	"testing"
)

func TestReadDevWebMarker(t *testing.T) {
	dir := t.TempDir()
	cases := []struct {
		name string
		body string
		want string
	}{
		{"plain", "/srv/web/dist\n", "/srv/web/dist"},
		{"leading comments/blank", "# dev override\n\n  /srv/web/dist  \n\n", "/srv/web/dist"},
		{"only comments", "# nothing here\n", ""},
		{"empty", "", ""},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			p := filepath.Join(t.TempDir(), "dev-web")
			if err := os.WriteFile(p, []byte(tc.body), 0o600); err != nil {
				t.Fatal(err)
			}
			if got := readDevWebMarker(p); got != tc.want {
				t.Fatalf("readDevWebMarker(%q) = %q, want %q", tc.body, got, tc.want)
			}
		})
	}

	if got := readDevWebMarker(filepath.Join(dir, "missing")); got != "" {
		t.Fatalf("missing marker = %q, want empty", got)
	}
}

func TestResolveDevWebDirPrecedence(t *testing.T) {
	dataDir := t.TempDir()

	// 1. Flag wins over everything.
	t.Setenv("SOLU_WEB_DEV_DIR", "/env/dist")
	if err := os.WriteFile(filepath.Join(dataDir, "dev-web"), []byte("/marker/dist\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	if got := resolveDevWebDir("/flag/dist", dataDir); got != "/flag/dist" {
		t.Fatalf("flag should win: got %q", got)
	}

	// 2. Env when no flag.
	if got := resolveDevWebDir("", dataDir); got != "/env/dist" {
		t.Fatalf("env should win over marker: got %q", got)
	}

	// 3. Data-dir marker when no flag/env.
	t.Setenv("SOLU_WEB_DEV_DIR", "")
	if got := resolveDevWebDir("", dataDir); got != "/marker/dist" {
		t.Fatalf("marker should apply: got %q", got)
	}

	// 4. No override at all.
	if err := os.Remove(filepath.Join(dataDir, "dev-web")); err != nil {
		t.Fatal(err)
	}
	if got := resolveDevWebDir("", dataDir); got != "" {
		t.Fatalf("no override should be empty: got %q", got)
	}
}
