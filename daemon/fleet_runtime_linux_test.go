//go:build linux

package main

import (
	"crypto/sha256"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestFleetRuntimeMetadataAndExactArtifactMigration(t *testing.T) {
	dir := t.TempDir()
	source := filepath.Join(dir, "main.go")
	if err := os.WriteFile(source, []byte("package main\nvar version, paneRuntimeSourceLinux, behavior string\nfunc main(){println(version, paneRuntimeSourceLinux, behavior)}\n"), 0600); err != nil {
		t.Fatal(err)
	}
	stamp := strings.Repeat("a", 64)
	build := func(name, flags string, overrides ...string) string {
		t.Helper()
		path := filepath.Join(dir, name)
		cmd := exec.Command("go", "build", "-buildvcs=false", "-ldflags="+flags, "-o", path, source)
		cmd.Env = append(os.Environ(), "CGO_ENABLED=0", "GOOS=linux", "GOARCH=amd64", "GOAMD64=v1")
		cmd.Env = append(cmd.Env, overrides...)
		if output, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("build: %v: %s", err, output)
		}
		return path
	}
	old := build("old", "-s -w -X main.version=1.0.0")
	current := build("current", "-s -w -X main.version=1.0.1 -X main.paneRuntimeSourceLinux="+stamp)
	want := fleetReadPaneRuntime(current, nil)
	if want == "" || fleetReadPaneRuntime(old, nil) != "" {
		t.Fatal("missing fingerprint was not fail-closed")
	}
	b, _ := os.ReadFile(old)
	artifact := fmt.Sprintf("%x", sha256.Sum256(b))
	if got := fleetReadPaneRuntime(old, map[string]string{artifact: stamp}); got != want {
		t.Fatalf("exact legacy receipt = %q, want %q", got, want)
	}
	if fleetReadPaneRuntime(old, map[string]string{strings.Repeat("b", 64): stamp}) != "" {
		t.Fatal("wrong artifact receipt accepted")
	}
	changed := build("changed", "-s -w -X main.version=1.0.1 -X main.paneRuntimeSourceLinux="+strings.Repeat("b", 64))
	if fleetReadPaneRuntime(changed, nil) == want {
		t.Fatal("changed runtime accepted")
	}
	unstripped := build("unstripped", "-X main.version=1.0.1 -X main.paneRuntimeSourceLinux="+stamp)
	if fleetReadPaneRuntime(unstripped, nil) != want {
		t.Fatal("debug symbol stripping changed executable compatibility")
	}
	semantic := build("semantic", "-X main.version=1.0.1 -X main.paneRuntimeSourceLinux="+stamp+" -X main.other=changed")
	if fleetReadPaneRuntime(semantic, nil) == want {
		t.Fatal("semantic linker override was ignored")
	}
	flags := "-s -w -X main.version=1.0.1 -X main.paneRuntimeSourceLinux=" + stamp
	for _, variant := range []struct {
		name, flags string
		env         []string
		unsupported bool
	}{
		{name: "cpu-v2", flags: flags, env: []string{"GOAMD64=v2"}},
		{name: "semantic-linker", flags: flags + " -X main.behavior=changed"},
		{name: "arm64", flags: flags, env: []string{"GOARCH=arm64"}, unsupported: true},
		{name: "cgo", flags: flags, env: []string{"CGO_ENABLED=1"}, unsupported: true},
	} {
		t.Run(variant.name, func(t *testing.T) {
			path := build(variant.name, variant.flags, variant.env...)
			got := fleetReadPaneRuntime(path, nil)
			if variant.unsupported {
				if got != "" {
					t.Fatalf("unsupported runtime accepted: %q", got)
				}
			} else if got == "" || got == want {
				t.Fatalf("changed build settings did not produce a distinct runtime: %q", got)
			}
		})
	}
	// Wrappers launched before and during the daemon-only shortcut release
	// remain equivalent; this exact source mapping cannot waive compiler flags
	// or a subsequent real runtime change.
	preShortcut := "a6a8750476f5da672873e521a4e515ed9de14c671e92a9546d558aa96a13c21f"
	shortcutOnly := "63569694593fdb92a5d6ba15f7f3c235601d31aeb394f81b1e2a44bc98690eae"
	beforeShortcut := build("before-shortcut", "-X main.version=0.0.1081 -X main.paneRuntimeSourceLinux="+preShortcut)
	duringShortcut := build("during-shortcut", "-X main.version=0.0.1082 -X main.paneRuntimeSourceLinux="+shortcutOnly)
	compat := fleetReadPaneRuntime(beforeShortcut, nil)
	if compat == "" || fleetReadPaneRuntime(duringShortcut, nil) != compat {
		t.Fatal("audited shortcut-only transition forced a pane restart")
	}
	duringSemanticChange := build("during-semantic", "-X main.version=0.0.1082 -X main.paneRuntimeSourceLinux="+shortcutOnly+" -X main.other=changed")
	if fleetReadPaneRuntime(duringSemanticChange, nil) == compat || fleetReadPaneRuntime(changed, nil) == compat {
		t.Fatal("shortcut transition waived a semantic change")
	}
	if fleetCanonicalPaneRuntimeSource(stamp) != stamp {
		t.Fatal("unknown runtime source was normalized")
	}
	for _, body := range []string{`{}`, `{"schema":2,"artifacts":{}}`, `{"schema":1,"artifacts":{"bad":"bad"}}`} {
		p := filepath.Join(dir, "receipts.json")
		_ = os.WriteFile(p, []byte(body), 0600)
		if len(fleetReadLegacyRuntimeReceipts(p)) != 0 {
			t.Fatal("malformed receipt accepted")
		}
	}
}
