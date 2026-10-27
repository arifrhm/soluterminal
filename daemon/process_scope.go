package main

// Process scopes own a command's descendants independently of its original PID
// and process group. A successful command may intentionally leave background
// work: scopes are stopped only by an explicit cancellation/teardown, and are
// otherwise collected after the kernel reports that they are empty.
import (
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"sync"
	"syscall"
	"time"
	"weak"
)

const processScopeHintEnv = "SOLUTERMINAL_PROCESS_SCOPE"
const processScopeStateHintEnv = "SOLUTERMINAL_PROCESS_SCOPE_STATE"

var errProcessScopeUnavailable = errors.New("worker process containment unavailable")

type processScopeIdentity struct {
	Version int    `json:"version"`
	Kind    string `json:"kind"`
	ID      string `json:"id"`
	Boot    string `json:"boot"`
	Path    string `json:"path"`
	Inode   uint64 `json:"inode,omitempty"`
}

type processScopeRecord struct {
	Scope        processScopeIdentity `json:"scope"`
	Phase        string               `json:"phase"`
	PID          int                  `json:"pid,omitempty"`
	Birth        string               `json:"birth,omitempty"`
	OwnerTask    string               `json:"ownerTask,omitempty"`
	OwnerLaunch  string               `json:"ownerLaunch,omitempty"`
	CreatorPID   int                  `json:"creatorPid,omitempty"`
	CreatorBirth string               `json:"creatorBirth,omitempty"`
}

func claimTaskProcessScope(identity processScopeIdentity, taskID, launchID string) error {
	if !processScopeHasContainment(identity) {
		return fmt.Errorf("workflow shutdown requires a migration-contained scope")
	}
	root, err := processScopeRoot()
	if err != nil {
		return err
	}
	scope := &ownedProcessScope{identity: identity, root: root}
	unlock, err := lockProcessScope(root, identity.ID)
	if err != nil {
		return err
	}
	defer unlock()
	record, err := scope.readRecord()
	if err != nil {
		return err
	}
	if record.Phase != "prepared" || (record.OwnerTask != "" && (record.OwnerTask != taskID || record.OwnerLaunch != launchID)) {
		return fmt.Errorf("process scope is already owned or launched")
	}
	record.OwnerTask, record.OwnerLaunch = taskID, launchID
	return scope.writeRecord(record)
}

func taskProcessScopeOwner(identity processScopeIdentity, taskID string) error {
	root, err := processScopeRoot()
	if err != nil {
		return err
	}
	record, err := (&ownedProcessScope{identity: identity, root: root}).readRecord()
	if err != nil {
		return err
	}
	if record.OwnerTask != taskID {
		return fmt.Errorf("process scope belongs to a different task")
	}
	return nil
}

type ownedProcessScope struct {
	identity     processScopeIdentity
	root         string
	resource     *os.File             // creation-bound platform handle, closed after Start
	originalAttr *syscall.SysProcAttr // caller policy restored after fork/discard
}

var commandProcessScopes sync.Map   // *exec.Cmd -> *ownedProcessScope, only until Start
var legacyWorkflowCommands sync.Map // *exec.Cmd -> bool, exact fallback launch only
var processScopeRegistry = struct {
	sync.Mutex
	processes map[weak.Pointer[os.Process]]*ownedProcessScope
	active    map[string]*ownedProcessScope
	once      sync.Once
}{processes: map[weak.Pointer[os.Process]]*ownedProcessScope{}, active: map[string]*ownedProcessScope{}}

func validProcessScopeID(id string) bool {
	if len(id) != 32 {
		return false
	}
	_, err := hex.DecodeString(id)
	return err == nil
}

func newProcessScopeID() (string, error) {
	var raw [16]byte
	if _, err := rand.Read(raw[:]); err != nil {
		return "", err
	}
	return hex.EncodeToString(raw[:]), nil
}

func processScopeRoot() (string, error) {
	// Child agents may intentionally use another HOME/cache profile. Keep
	// nested ownership receipts beside their parent's durable receipts. This
	// is a locator, not authority: it must already be a private owned directory.
	if hint := os.Getenv(processScopeStateHintEnv); hint != "" {
		if !filepath.IsAbs(hint) || filepath.Clean(hint) != hint || filepath.Base(hint) != "process-scopes-v1" {
			return "", fmt.Errorf("invalid process scope state locator")
		}
		return hint, checkProcessScopeDirectory(hint)
	}
	cache, err := os.UserCacheDir()
	if err != nil {
		return "", err
	}
	root := filepath.Join(cache, "soluterminal", "process-scopes-v1")
	if err := os.MkdirAll(root, 0700); err != nil {
		return "", err
	}
	if err := checkProcessScopeDirectory(root); err != nil {
		return "", err
	}
	return root, nil
}

func (s *ownedProcessScope) recordPath(closed bool) string {
	dir := "active"
	if closed {
		dir = "closed"
	}
	return filepath.Join(s.root, dir, s.identity.ID+".json")
}

func (s *ownedProcessScope) readRecord() (processScopeRecord, error) {
	var record processScopeRecord
	for _, closed := range []bool{true, false} {
		data, err := readProcessScopeRecord(s.recordPath(closed))
		if errors.Is(err, os.ErrNotExist) {
			continue
		}
		if err != nil {
			return record, err
		}
		if len(data) > 4096 {
			return record, fmt.Errorf("process scope record exceeds bound")
		}
		if err := json.Unmarshal(data, &record); err != nil {
			return record, err
		}
		if record.Scope != s.identity || (closed && record.Phase != "closed") {
			return record, fmt.Errorf("process scope identity changed")
		}
		if closed {
			// A prior fsync failure may have left visible, uncommitted receipt
			// bytes. Re-establish durability before anyone consumes the proof.
			f, err := os.OpenFile(s.recordPath(true), os.O_RDWR, 0)
			if err != nil {
				return record, err
			}
			err = f.Sync()
			_ = f.Close()
			if err == nil {
				err = syncProcessScopeDirectory(filepath.Dir(s.recordPath(true)))
			}
			if err != nil {
				return record, err
			}
		}
		return record, nil
	}
	return record, os.ErrNotExist
}

func readProcessScopeRecord(path string) ([]byte, error) {
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	data, err := io.ReadAll(io.LimitReader(f, 4097))
	if err == nil && len(data) > 4096 {
		err = fmt.Errorf("process scope record exceeds bound")
	}
	return data, err
}

func waitOwnedCommand(cmd *exec.Cmd) error {
	err := cmd.Wait()
	if scope, lookupErr := scopeForProcess(cmd.Process); lookupErr == nil && scope != nil {
		_, _ = closeEmptyProcessScope(scope)
	}
	return err
}

func stopProcessScopeIdentity(identity processScopeIdentity) (bool, error) {
	root, err := processScopeRoot()
	if err != nil {
		return false, err
	}
	return stopOwnedProcessScope(&ownedProcessScope{identity: identity, root: root}, true)
}

// A closed receipt is synced BEFORE removing the empty kernel scope. Missing
// paths alone are never proof: a live cgroup can have been renamed. Creation
// and cancellation share the same cross-process lock, so no delayed Start can
// cross a durable closing/closed fence, including after a daemon crash.
func (s *ownedProcessScope) writeRecord(record processScopeRecord) error {
	if record.Scope != s.identity {
		return fmt.Errorf("process scope record identity mismatch")
	}
	path := s.recordPath(record.Phase == "closed")
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	if err := checkProcessScopeDirectory(filepath.Dir(path)); err != nil {
		return err
	}
	f, err := os.CreateTemp(filepath.Dir(path), ".scope-")
	if err != nil {
		return err
	}
	defer os.Remove(f.Name())
	data, err := json.Marshal(record)
	if err == nil {
		_, err = f.Write(data)
	}
	if err == nil {
		err = f.Sync()
	}
	closeErr := f.Close()
	if err == nil {
		err = closeErr
	}
	if err == nil {
		err = os.Rename(f.Name(), path)
	}
	if err == nil {
		err = syncProcessScopeDirectory(filepath.Dir(path))
	}
	if err == nil && record.Phase == "closed" {
		_ = os.Remove(s.recordPath(false))
	}
	return err
}

func prepareOwnedCommand(cmd *exec.Cmd, required bool) (*ownedProcessScope, error) {
	if value, ok := commandProcessScopes.Load(cmd); ok {
		scope := value.(*ownedProcessScope)
		if required && !processScopeHasContainment(scope.identity) {
			return nil, fmt.Errorf("prepared command lacks required migration containment")
		}
		return scope, nil
	}
	if cmd == nil || cmd.Process != nil || cmd.Err != nil {
		return nil, nil
	}
	scope, err := preparePlatformProcessScope(cmd, required)
	if err == nil && required && scope == nil {
		err = fmt.Errorf("command has no owned process scope")
	}
	if err != nil {
		if required {
			return nil, fmt.Errorf("%w: %w", errProcessScopeUnavailable, err)
		}
		// Legacy/nondelegated environments retain existing execution behavior;
		// they never receive a containment identity or a false shutdown proof.
		return nil, nil
	}
	if scope != nil {
		commandProcessScopes.Store(cmd, scope)
	}
	return scope, nil
}

func discardPreparedProcessScope(cmd *exec.Cmd) {
	if value, ok := commandProcessScopes.LoadAndDelete(cmd); ok {
		scope := value.(*ownedProcessScope)
		cmd.SysProcAttr, scope.originalAttr = scope.originalAttr, nil
		if scope.resource != nil {
			scope.resource.Close()
			scope.resource = nil
		}
		_, _ = stopOwnedProcessScope(scope, true)
	}
}

func startInOwnedProcessScope(cmd *exec.Cmd, start func() error) error {
	if _, legacy := legacyWorkflowCommands.LoadAndDelete(cmd); legacy {
		return start()
	}
	scope, err := prepareOwnedCommand(cmd, false)
	if err != nil || scope == nil {
		if err != nil {
			return err
		}
		return start()
	}
	defer commandProcessScopes.Delete(cmd)
	// Cgroup/namespace attributes are fork-time implementation details. Keep
	// the caller's original object (and avoid leaving a closed CgroupFD on Cmd).
	defer func() { cmd.SysProcAttr, scope.originalAttr = scope.originalAttr, nil }()
	defer func() {
		if scope.resource != nil {
			scope.resource.Close()
			scope.resource = nil
		}
	}()
	unlock, err := lockProcessScope(scope.root, scope.identity.ID)
	if err != nil {
		return err
	}
	record, err := scope.readRecord()
	if err != nil || record.Phase != "prepared" {
		unlock()
		return fmt.Errorf("process scope launch is fenced: %w", errors.Join(err, fmt.Errorf("phase %q", record.Phase)))
	}
	if cmd.Cancel != nil {
		cmd.Cancel = func() error {
			// Cancellation can race the post-Start registry insertion. This
			// creation-bound handle already exists and shares the launch fence.
			_, err := stopOwnedProcessScope(scope, true)
			return err
		}
	}
	err = start()
	if err == nil {
		record.Phase, record.PID = "started", cmd.Process.Pid
		record.Birth = processScopeBirth(cmd.Process.Pid)
		if writeErr := scope.writeRecord(record); writeErr != nil {
			err = writeErr
		}
	}
	unlock()
	if err != nil {
		_, _ = stopOwnedProcessScope(scope, true)
		if cmd.Process != nil && cmd.ProcessState == nil {
			_ = cmd.Wait()
		}
		return err
	}
	processScopeRegistry.Lock()
	processScopeRegistry.processes[weak.Make(cmd.Process)] = scope
	processScopeRegistry.active[scope.identity.ID] = scope
	processScopeRegistry.Unlock()
	processScopeRegistry.once.Do(func() { go reapOwnedProcessScopes() })
	return nil
}

func scopeForProcess(proc *os.Process) (*ownedProcessScope, error) {
	if proc == nil {
		return nil, nil
	}
	processScopeRegistry.Lock()
	scope := processScopeRegistry.processes[weak.Make(proc)]
	processScopeRegistry.Unlock()
	if scope != nil {
		return scope, nil
	}
	return recoverPlatformProcessScope(proc.Pid)
}

func stopScopedProcess(proc *os.Process, force bool) (bool, error) {
	scope, err := scopeForProcess(proc)
	if err != nil || scope == nil {
		return err != nil, err
	}
	_, err = stopOwnedProcessScope(scope, force)
	return true, err
}

func ownedScopeForPID(pid int) *processScopeIdentity {
	scope, err := recoverPlatformProcessScope(pid)
	if err != nil || scope == nil {
		return nil
	}
	identity := scope.identity
	return &identity
}

func ownedScopeForProcess(proc *os.Process) *processScopeIdentity {
	scope, err := scopeForProcess(proc)
	if err != nil || scope == nil {
		return nil
	}
	identity := scope.identity
	return &identity
}

func reapOwnedProcessScopes() {
	ticker := time.NewTicker(15 * time.Second)
	defer ticker.Stop()
	lastScan := time.Time{}
	for range ticker.C {
		if time.Since(lastScan) >= time.Minute {
			// Recover only empty scopes. This never stops a surviving pane or
			// intentionally backgrounded process when a daemon is replaced.
			recoverEmptyProcessScopes()
			lastScan = time.Now()
		}
		processScopeRegistry.Lock()
		var scopes []*ownedProcessScope
		for key := range processScopeRegistry.processes {
			if key.Value() == nil {
				delete(processScopeRegistry.processes, key)
			}
		}
		for _, scope := range processScopeRegistry.active {
			scopes = append(scopes, scope)
		}
		processScopeRegistry.Unlock()
		for _, scope := range scopes {
			if empty, err := closeEmptyProcessScope(scope); err == nil && empty {
				processScopeRegistry.Lock()
				delete(processScopeRegistry.active, scope.identity.ID)
				processScopeRegistry.Unlock()
			}
		}
	}
}

func recoverEmptyProcessScopes() {
	root, err := processScopeRoot()
	if err != nil {
		return
	}
	for _, dir := range []string{"active", "closed"} {
		f, err := os.Open(filepath.Join(root, dir))
		if err != nil {
			continue
		}
		for {
			entries, readErr := f.ReadDir(128)
			for _, entry := range entries {
				if entry.IsDir() {
					continue
				}
				data, err := readProcessScopeRecord(filepath.Join(root, dir, entry.Name()))
				var record processScopeRecord
				if err != nil || json.Unmarshal(data, &record) != nil || !validProcessScopeID(record.Scope.ID) || entry.Name() != record.Scope.ID+".json" {
					continue
				}
				_, _ = closeEmptyProcessScope(&ownedProcessScope{identity: record.Scope, root: root})
			}
			if readErr != nil {
				break
			}
		}
		f.Close()
	}
}
