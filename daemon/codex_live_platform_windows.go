//go:build windows

package main

import (
	"encoding/json"
	"errors"
	"golang.org/x/sys/windows"
	"os"
	"path/filepath"
	"strings"
	"time"
)

func codexLiveSupported() bool { return true }

func codexLiveSpawnCommand(self, dir, daemonURL string, cmd []string, overrides ...[]string) ([]string, error) {
	args := []string{self, "codex-terminal", "--data-dir", dir, "--daemon-url", daemonURL}
	if len(overrides) > 0 && len(overrides[0]) > 0 {
		raw, _ := json.Marshal(overrides[0])
		args = append(args, "--broker-config", string(raw))
	}
	return append(append(args, "--"), cmd...), nil
}

// Windows enforces socket privacy through the directory DACL, not chmod.
// Apply this only to the fresh directory owned by this wrapper.
func codexLivePrivateDirectory(path string) error {
	user, err := windows.GetCurrentProcessToken().GetTokenUser()
	if err != nil {
		return err
	}
	sd, err := windows.SecurityDescriptorFromString("D:P(A;OICI;FA;;;" + user.User.Sid.String() + ")")
	if err != nil {
		return err
	}
	acl, _, err := sd.DACL()
	if err != nil {
		return err
	}
	return windows.SetNamedSecurityInfo(path, windows.SE_FILE_OBJECT, windows.DACL_SECURITY_INFORMATION|windows.PROTECTED_DACL_SECURITY_INFORMATION, nil, nil, acl, nil)
}

func codexLivePaneHasBridge(p *Pane) bool {
	st := readCodexLiveStatus(p.codexLiveDataDir, p.ID)
	if st.PID <= 0 || st.UpdatedAt < p.CreatedAt.UnixMilli() || time.Now().UnixMilli()-st.UpdatedAt > 5000 {
		return false
	}
	self, err := os.Executable()
	if err != nil {
		return false
	}
	expected := strings.TrimSuffix(filepath.Base(self), filepath.Ext(self))
	for _, pid := range p.ChildPids() {
		if pid == st.PID && strings.EqualFold(processName(pid), expected) {
			return true
		}
	}
	return false
}

func lockCodexLiveInput(path string) (*os.File, error) {
	f, err := os.OpenFile(path, os.O_RDWR, 0600)
	if err != nil {
		return nil, err
	}
	deadline := time.Now().Add(250 * time.Millisecond)
	var position windows.Overlapped
	for {
		err = windows.LockFileEx(windows.Handle(f.Fd()), windows.LOCKFILE_EXCLUSIVE_LOCK|windows.LOCKFILE_FAIL_IMMEDIATELY, 0, 1, 0, &position)
		if err == nil {
			return f, nil
		}
		if (err != windows.ERROR_LOCK_VIOLATION && err != windows.ERROR_IO_PENDING) || time.Now().After(deadline) {
			f.Close()
			return nil, errors.New("terminal input coordination busy")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
func unlockCodexLiveInput(f *os.File) {
	var position windows.Overlapped
	_ = windows.UnlockFileEx(windows.Handle(f.Fd()), 0, 1, 0, &position)
	_ = f.Close()
}
