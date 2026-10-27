package main

import (
	"bytes"
	"encoding/json"
	"os"
	"os/exec"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

func snapshotFixtureTree(t *testing.T, root string) map[string]string {
	t.Helper()
	out := map[string]string{}
	if err := filepath.Walk(root, func(path string, info os.FileInfo, err error) error {
		if err != nil {
			return err
		}
		rel, _ := filepath.Rel(root, path)
		if info.IsDir() {
			out[rel] = "dir"
			return nil
		}
		b, err := os.ReadFile(path)
		if err != nil {
			return err
		}
		out[rel] = string(b)
		return nil
	}); err != nil {
		t.Fatal(err)
	}
	return out
}

func TestUninstallPlanWindowsSeparatesNativeShellAndWSLState(t *testing.T) {
	home := filepath.Join(t.TempDir(), "profile")
	local := filepath.Join(home, "AppData", "Local")
	env := uninstallEnv{
		HomeDir: home, GOOS: "windows", LocalAppData: local,
		Executable: filepath.Join(local, "soluterminal", "solud.exe"),
		CodexHome:  filepath.Join(home, ".codex"),
		StatFn:     func(string) (os.FileInfo, error) { return nil, os.ErrNotExist },
	}
	report := uninstallPlan(env)
	entries := make(map[string]uninstallEntry, len(report.Entries))
	for _, entry := range report.Entries {
		entries[entry.Path] = entry
	}
	for _, path := range []string{
		filepath.Join(home, ".solu"),
		filepath.Join(local, "soluterminal"),
		`WSL:$HOME/.solu`,
	} {
		entry, ok := entries[path]
		if !ok || entry.Category != "state" || entry.Default != "keep" {
			t.Fatalf("Windows state entry %q = %+v, present=%v", path, entry, ok)
		}
	}
}

func TestUnixRemovalCommandQuotesLiteralHostilePath(t *testing.T) {
	if _, err := exec.LookPath("sh"); err != nil {
		t.Skip("sh unavailable")
	}
	dir := t.TempDir()
	capture := filepath.Join(dir, "captured")
	touchCapture := filepath.Join(dir, "command-substitution-ran")
	stub := filepath.Join(dir, "rm")
	if err := os.WriteFile(stub, []byte("#!/bin/sh\nprintf '%s' \"$3\" > \"$CAPTURE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dir, "touch"), []byte("#!/bin/sh\nprintf '%s' \"$1\" > \"$TOUCH_CAPTURE\"\n"), 0o755); err != nil {
		t.Fatal(err)
	}
	hostile := "/tmp/codex-$USER-`touch nope`-$(touch nope)-O'Brien\nsecond line"
	cmd := removalCommand("linux", hostile, false)
	run := exec.Command("sh", "-c", cmd)
	run.Dir = dir
	run.Env = append(os.Environ(), "PATH="+dir, "CAPTURE="+capture, "TOUCH_CAPTURE="+touchCapture)
	if out, err := run.CombinedOutput(); err != nil {
		t.Fatalf("quoted command failed: %v: %s\ncommand: %s", err, out, cmd)
	}
	got, err := os.ReadFile(capture)
	if err != nil {
		t.Fatal(err)
	}
	if string(got) != hostile {
		t.Fatalf("stub received %q, want literal %q (command %s)", got, hostile, cmd)
	}
	if _, err := os.Stat(touchCapture); !os.IsNotExist(err) {
		t.Fatalf("command substitution executed: %v", err)
	}
}

func TestUninstallPlanPlatformsAreReadOnlyAndCredentialsKeep(t *testing.T) {
	for _, goos := range []string{"linux", "darwin", "windows"} {
		t.Run(goos, func(t *testing.T) {
			home := t.TempDir()
			dataDir := filepath.Join(home, ".solu")
			bin := filepath.Join(home, ".local", "bin", "solud")
			fixtures := map[string]string{
				bin: "binary", bin + ".old": "old", filepath.Join(dataDir, "claim.json"): "secret",
				filepath.Join(home, ".claude.json"):                 "credential-must-not-print",
				filepath.Join(home, ".codex", "auth.json"):          "credential-must-not-print",
				filepath.Join(home, ".config", "muse", "auth.json"): "credential-must-not-print",
			}
			switch goos {
			case "linux":
				fixtures[filepath.Join(home, ".config", "systemd", "user", "soluterminal.service")] = "unit"
			case "darwin":
				fixtures[filepath.Join(home, "Library", "LaunchAgents", "com.soluterminal.daemon.plist")] = "plist"
			}
			for path, body := range fixtures {
				if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
					t.Fatal(err)
				}
				if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
					t.Fatal(err)
				}
			}
			before := snapshotFixtureTree(t, home)
			statFn := os.Stat
			if goos == "windows" {
				statFn = func(path string) (os.FileInfo, error) {
					if path == `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\soluterminal` {
						return registryArtifactInfo("soluterminal"), nil
					}
					return os.Stat(path)
				}
			}
			env := uninstallEnv{HomeDir: home, DataDir: dataDir, Executable: bin, GOOS: goos, CodexHome: filepath.Join(home, ".codex"), StatFn: statFn}
			report := uninstallPlan(env)
			if !report.ReadOnly || len(report.Entries) == 0 {
				t.Fatalf("report = %+v", report)
			}
			entries := make(map[string]uninstallEntry, len(report.Entries))
			for _, entry := range report.Entries {
				entries[entry.Path] = entry
				if entry.Category == "credential" && entry.Default != "keep" {
					t.Fatalf("credential default = %q for %s", entry.Default, entry.Path)
				}
			}
			expected := map[string]struct{ category, def string }{
				bin: {"binary", "delete"}, bin + ".old": {"binary", "delete"},
				bin + ".old.1": {"binary", "delete"}, bin + ".old.2": {"binary", "delete"},
				bin + ".version": {"binary", "delete"}, bin + ".update-pending": {"binary", "delete"},
				bin + ".successor-ok": {"binary", "delete"}, bin + ".restart-intent": {"binary", "delete"},
				bin + ".rollback": {"binary", "delete"}, bin + ".rollback.tmp": {"binary", "delete"},
				filepath.Join(filepath.Dir(bin), "successor.log"): {"binary", "delete"},
				dataDir: {"state", "keep"}, filepath.Join(dataDir, "shell-update"): {"state", "keep"},
				filepath.Join(dataDir, "logs"): {"state", "keep"}, filepath.Join(dataDir, "workflow"): {"state", "keep"},
				filepath.Join(dataDir, "compute"): {"state", "keep"}, filepath.Join(dataDir, "claim.json"): {"state", "keep"},
				filepath.Join(dataDir, "service.env"): {"state", "keep"}, filepath.Join(dataDir, "update_rollback.json"): {"state", "keep"},
				filepath.Join(home, ".claude"): {"credential", "keep"}, filepath.Join(home, ".claude.json"): {"credential", "keep"},
				filepath.Join(home, ".codex"): {"credential", "keep"}, filepath.Join(home, ".codex", "auth.json"): {"credential", "keep"},
				filepath.Join(home, ".config", "muse", "auth.json"): {"credential", "keep"},
			}
			servicePath := filepath.Join(home, ".config", "systemd", "user", "soluterminal.service")
			if goos == "darwin" {
				servicePath = filepath.Join(home, "Library", "LaunchAgents", "com.soluterminal.daemon.plist")
			} else if goos == "windows" {
				servicePath = `HKCU\Software\Microsoft\Windows\CurrentVersion\Uninstall\soluterminal`
			}
			expected[servicePath] = struct{ category, def string }{"service", "delete"}
			for path, want := range expected {
				entry, ok := entries[path]
				if !ok || entry.Category != want.category || entry.Default != want.def {
					t.Fatalf("entry %q = %+v, present=%v, want category=%s default=%s", path, entry, ok, want.category, want.def)
				}
			}
			if !entries[bin].Exists || !entries[bin+".old"].Exists || !entries[servicePath].Exists || !entries[filepath.Join(dataDir, "claim.json")].Exists {
				t.Fatalf("representative artifacts not reported present: %+v", entries)
			}
			if entries[bin+".old.2"].Exists {
				t.Fatal("missing sidecar was omitted or reported present")
			}

			var jsonOut, textOut, errOut bytes.Buffer
			if code := runUninstallPlanCommandWithEnv([]string{"--json"}, env, &jsonOut, &errOut); code != 0 {
				t.Fatalf("json command = %d: %s", code, errOut.String())
			}
			var decoded uninstallReport
			if err := json.Unmarshal(jsonOut.Bytes(), &decoded); err != nil || !reflect.DeepEqual(decoded, report) {
				t.Fatalf("json report mismatch: %v, %+v", err, decoded)
			}
			if code := runUninstallPlanCommandWithEnv(nil, env, &textOut, &errOut); code != 0 {
				t.Fatalf("text command = %d: %s", code, errOut.String())
			}
			if !strings.Contains(textOut.String(), "READ ONLY") || strings.Contains(textOut.String(), "credential-must-not-print") {
				t.Fatalf("unsafe text report: %s", textOut.String())
			}
			for path := range expected {
				if !strings.Contains(textOut.String(), path) {
					t.Fatalf("text report omitted %q", path)
				}
			}
			after := snapshotFixtureTree(t, home)
			if len(after) != len(before) {
				t.Fatalf("fixture entry count changed: before=%v after=%v", before, after)
			}
			for path, body := range before {
				if after[path] != body {
					t.Fatalf("fixture changed at %s", path)
				}
			}
		})
	}
}
