package main

import "testing"

func TestPaneEnvironmentFor(t *testing.T) {
	cases := []struct {
		goos string
		wsl  bool
		want string
	}{
		{"windows", false, "windows"},
		{"linux", true, "wsl"},
		{"linux", false, "linux"},
		{"darwin", false, "macos"},
	}
	for _, c := range cases {
		if got := paneEnvironmentFor(c.goos, c.wsl); got != c.want {
			t.Fatalf("paneEnvironmentFor(%q, %v) = %q, want %q", c.goos, c.wsl, got, c.want)
		}
	}
}
