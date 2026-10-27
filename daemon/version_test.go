package main

import (
	"bytes"
	"strings"
	"testing"
)

func TestIsVersionCommand(t *testing.T) {
	tests := []struct {
		name string
		args []string
		want bool
	}{
		{
			name: "version subcommand",
			args: []string{"solud", "version"},
			want: true,
		},
		{
			name: "long flag version",
			args: []string{"solud", "--version"},
			want: true,
		},
		{
			name: "single dash flag version",
			args: []string{"solud", "-version"},
			want: true,
		},
		{
			name: "short flag version",
			args: []string{"solud", "-v"},
			want: true,
		},
		{
			name: "no arguments",
			args: []string{"solud"},
			want: false,
		},
		{
			name: "other subcommand",
			args: []string{"solud", "agent"},
			want: false,
		},
		{
			name: "help flag",
			args: []string{"solud", "--help"},
			want: false,
		},
		{
			name: "empty args",
			args: []string{},
			want: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if got := isVersionCommand(tt.args); got != tt.want {
				t.Errorf("isVersionCommand(%v) = %v, want %v", tt.args, got, tt.want)
			}
		})
	}
}

func TestRunVersionCommand(t *testing.T) {
	var buf bytes.Buffer
	code := runVersionCommand(&buf)
	if code != 0 {
		t.Fatalf("runVersionCommand exit code = %d, want 0", code)
	}

	got := strings.TrimSpace(buf.String())
	if got != version {
		t.Fatalf("runVersionCommand output = %q, want %q", got, version)
	}
}

func TestRunVersionCommand_CustomVersion(t *testing.T) {
	oldVersion := version
	version = "v1.2.3-custom"
	t.Cleanup(func() { version = oldVersion })

	var buf bytes.Buffer
	code := runVersionCommand(&buf)
	if code != 0 {
		t.Fatalf("runVersionCommand exit code = %d, want 0", code)
	}

	got := strings.TrimSpace(buf.String())
	if got != "v1.2.3-custom" {
		t.Fatalf("runVersionCommand output = %q, want %q", got, "v1.2.3-custom")
	}
}
