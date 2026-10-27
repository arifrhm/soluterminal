package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverySettingsDefaultsToManual(t *testing.T) {
	t.Setenv("SOLU_AUTO_RESUME", "")
	st, err := newRecoverySettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("newRecoverySettingsStore: %v", err)
	}
	if got := st.Snapshot().ResumeAfterRestart; got != recoveryResumeManual {
		t.Fatalf("default resume mode = %q, want %q", got, recoveryResumeManual)
	}
	if st.AutoResumeAfterRestart() {
		t.Fatal("AutoResumeAfterRestart true for manual default")
	}
}

func TestRecoverySettingsEnvSeedsAutoWhenNoFile(t *testing.T) {
	t.Setenv("SOLU_AUTO_RESUME", "1")
	st, err := newRecoverySettingsStore(t.TempDir())
	if err != nil {
		t.Fatalf("newRecoverySettingsStore: %v", err)
	}
	if got := st.Snapshot().ResumeAfterRestart; got != recoveryResumeAuto {
		t.Fatalf("default resume mode = %q, want %q", got, recoveryResumeAuto)
	}
	if !st.AutoResumeAfterRestart() {
		t.Fatal("AutoResumeAfterRestart false for auto default")
	}
}

func TestRecoverySettingsFileWinsOverEnv(t *testing.T) {
	dir := t.TempDir()
	t.Setenv("SOLU_AUTO_RESUME", "1")
	st, err := newRecoverySettingsStore(dir)
	if err != nil {
		t.Fatalf("newRecoverySettingsStore: %v", err)
	}
	if _, err := st.Set(recoverySettings{ResumeAfterRestart: recoveryResumePaused}); err != nil {
		t.Fatalf("Set paused: %v", err)
	}

	t.Setenv("SOLU_AUTO_RESUME", "")
	reloaded, err := newRecoverySettingsStore(dir)
	if err != nil {
		t.Fatalf("reload recovery settings: %v", err)
	}
	if got := reloaded.Snapshot().ResumeAfterRestart; got != recoveryResumePaused {
		t.Fatalf("reloaded resume mode = %q, want %q", got, recoveryResumePaused)
	}
}

func TestRecoverySettingsRejectsInvalidMode(t *testing.T) {
	st := defaultRecoverySettingsStore(t.TempDir())
	if _, err := st.Set(recoverySettings{ResumeAfterRestart: "soon"}); !errors.Is(err, errInvalidRecoverySettings) {
		t.Fatalf("Set invalid error = %v, want errInvalidRecoverySettings", err)
	}
}

func TestRecoverySettingsRejectsInvalidFileMode(t *testing.T) {
	dir := t.TempDir()
	if err := os.WriteFile(filepath.Join(dir, "recovery_settings.json"), []byte(`{"resumeAfterRestart":"soon"}`), 0o600); err != nil {
		t.Fatalf("write settings: %v", err)
	}
	if _, err := newRecoverySettingsStore(dir); !errors.Is(err, errInvalidRecoverySettings) {
		t.Fatalf("load invalid error = %v, want errInvalidRecoverySettings", err)
	}
}
