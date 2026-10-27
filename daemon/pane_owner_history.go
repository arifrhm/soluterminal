package main

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"
	"time"
)

// This boot recipe lives outside the workflow reader: ordinary panes restore
// before workflow startup, including in older binaries. Ordinary panes.json is
// removed only after this record and the task's closing intent are durable.
type ownerRetainedHistory struct {
	ExitConfirmed      bool                 `json:"exitConfirmed,omitempty"`
	ReopenRequested    bool                 `json:"reopenRequested,omitempty"`
	RestoreEnabled     bool                 `json:"restoreEnabled,omitempty"`
	TaskID             string               `json:"taskId"`
	Pane               PersistedPane        `json:"pane"`
	State              ownerRetirement      `json:"state"`
	Proof              ownerRetirementProof `json:"proof"`
	TranscriptArtifact string               `json:"transcriptArtifact"`
	TranscriptSHA256   string               `json:"transcriptSha256"`
	RetainedAt         time.Time            `json:"retainedAt"`
}

func ownerHistoryPath(dataDir, paneID string) string {
	sum := sha256.Sum256([]byte(paneID))
	return filepath.Join(dataDir, "owner-retirements", hex.EncodeToString(sum[:])+".json")
}
func writeOwnerHistory(dataDir string, history ownerRetainedHistory) error {
	if history.TaskID == "" || history.Pane.ID == "" || history.State.ConversationID == "" || history.TranscriptArtifact == "" {
		return fmt.Errorf("incomplete retained owner history")
	}
	path := ownerHistoryPath(dataDir, history.Pane.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(history)
	if err != nil {
		return err
	}
	return writeFileAtomicSynced(path, raw, 0600)
}
func loadOwnerHistories(dataDir string) (map[string]ownerRetainedHistory, error) {
	out := map[string]ownerRetainedHistory{}
	entries, err := os.ReadDir(filepath.Join(dataDir, "owner-retirements"))
	if os.IsNotExist(err) {
		return out, nil
	}
	if err != nil {
		return out, err
	}
	var diagnostics []error
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".json") {
			continue
		}
		history, err := loadOwnerHistoryFile(dataDir, entry.Name())
		if err != nil {
			diagnostics = append(diagnostics, err)
			continue
		}
		out[history.Pane.ID] = history
	}
	return out, errors.Join(diagnostics...)
}
func loadOwnerHistoryFile(dataDir, name string) (h ownerRetainedHistory, err error) {
	raw, err := os.ReadFile(filepath.Join(dataDir, "owner-retirements", name))
	if err != nil {
		return h, err
	}
	if err = json.Unmarshal(raw, &h); err != nil {
		return h, fmt.Errorf("owner retirement boot record %s: %w", name, err)
	}
	if h.TaskID == "" || h.Pane.ID == "" || filepath.Base(ownerHistoryPath(dataDir, h.Pane.ID)) != name || h.State.PaneID != h.Pane.ID || h.State.ConversationID == "" || !h.State.HistoryAvailable {
		return h, fmt.Errorf("invalid owner retirement boot record %s", name)
	}
	if raw, readErr := os.ReadFile(ownerViewerHistoryPath(dataDir, h.Pane.ID)); readErr == nil && !h.RestoreEnabled {
		var latest PersistedPane
		if err = json.Unmarshal(raw, &latest); err != nil {
			return h, fmt.Errorf("retained viewer history is unreadable: %w", err)
		}
		if latest.ID != h.Pane.ID {
			return h, fmt.Errorf("retained viewer pane identity mismatch")
		}
		if latest.AgentPid != h.Pane.AgentPid || persistedPaneConversationID(latest) != persistedPaneConversationID(h.Pane) {
			h.Pane.OwnerRetirementViewersKnown = false
			h.Pane.OwnerRetirementPendingDevices = []string{"unknown-retained-process-generation"}
		} else {
			h.Pane.OwnerRetirementViewersKnown = latest.OwnerRetirementViewersKnown
			h.Pane.OwnerRetirementPendingDevices = latest.OwnerRetirementPendingDevices
		}
	} else if readErr != nil && !os.IsNotExist(readErr) {
		return h, readErr
	}
	h.Pane.OwnerRetirementRestoreDisabled = true
	return h, nil
}
func readExactRetirementTranscript(ctx context.Context, proof ownerRetirementProof) ([]byte, error) {
	if proof.TranscriptSize < 0 || proof.TranscriptSize > 64<<20 {
		return nil, fmt.Errorf("transcript exceeds bounded automatic retention; keep owner for explicit archival")
	}
	f, err := os.Open(proof.TranscriptPath)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	before, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !before.Mode().IsRegular() || before.Size() != proof.TranscriptSize || before.ModTime().UnixNano() != proof.TranscriptModifiedNs {
		return nil, fmt.Errorf("proved transcript changed before retention")
	}
	// Agent proof validates device/inode immediately before this callback. The
	// opened descriptor must still identify that same source, never a replaced path.
	if !ownerRetirementTranscriptIdentity(before, proof) {
		return nil, fmt.Errorf("proved transcript identity changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != proof.TranscriptSize || after.Size() != before.Size() || !after.ModTime().Equal(before.ModTime()) {
		return nil, fmt.Errorf("transcript changed during retention")
	}
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	return raw, nil
}

// Viewer uncertainty keeps changing after retention if a close is deferred.
// Keep it separate from immutable transcript/closing records so a later state
// update can never overwrite a newly disconnected page's pending marker.
func ownerViewerHistoryPath(dataDir, paneID string) string {
	return ownerHistoryPath(dataDir, paneID) + ".viewers"
}
func writeOwnerViewerHistory(dataDir string, pane PersistedPane) error {
	path := ownerViewerHistoryPath(dataDir, pane.ID)
	if err := os.MkdirAll(filepath.Dir(path), 0700); err != nil {
		return err
	}
	raw, err := json.Marshal(pane)
	if err != nil {
		return err
	}
	return writeFileAtomicSynced(path, raw, 0600)
}

func readPostExitOwnerTranscript(history ownerRetainedHistory, old []byte) ([]byte, error) {
	path := history.Proof.TranscriptSourcePath
	if path == "" {
		return nil, fmt.Errorf("stable proved transcript pathname is unavailable")
	}
	f, err := os.Open(path)
	if err != nil {
		return nil, err
	}
	defer f.Close()
	stat, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if !stat.Mode().IsRegular() || !ownerRetirementTranscriptIdentity(stat, history.Proof) || stat.Size() < int64(len(old)) || stat.Size() > 64<<20 {
		return nil, fmt.Errorf("post-exit transcript identity/size changed")
	}
	raw, err := io.ReadAll(io.LimitReader(f, (64<<20)+1))
	if err != nil {
		return nil, err
	}
	after, err := f.Stat()
	if err != nil {
		return nil, err
	}
	if int64(len(raw)) != stat.Size() || after.Size() != stat.Size() || !after.ModTime().Equal(stat.ModTime()) || len(raw) < len(old) {
		return nil, fmt.Errorf("post-exit transcript is still changing")
	}
	if sha256.Sum256(raw[:len(old)]) != sha256.Sum256(old) {
		return nil, fmt.Errorf("post-exit transcript rewrote retained history")
	}
	return raw, nil
}

// The standard resume path persists the spawned generation before returning.
// An explicit reopen intent diverts that first write into the disabled recipe,
// so a crash before the HTTP handler returns still reconnects the new exact
// socket without creating permission to automatically spawn another process.
func persistOwnerReopenGeneration(dataDir string, pane PersistedPane) (bool, error) {
	if pane.Role != paneRoleTaskOwner {
		return false, nil
	}
	history, err := loadOwnerHistoryFile(dataDir, filepath.Base(ownerHistoryPath(dataDir, pane.ID)))
	if os.IsNotExist(err) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	if !history.ReopenRequested {
		return false, nil
	}
	if history.Pane.ID != pane.ID || persistedPaneConversationID(pane) != history.State.ConversationID || pane.Kind != "" || pane.Cwd != history.Pane.Cwd {
		return false, fmt.Errorf("reopened owner generation changed binding")
	}
	history.Pane = pane
	history.State.Phase = "reactivated"
	history.State.Reason = "Explicit reopen is retaining the new exact owner process; normal restore remains disabled until committed."
	history.ExitConfirmed = false
	history.Pane.OwnerRetirementRestoreDisabled = true
	if err := writeOwnerHistory(dataDir, history); err != nil {
		return false, err
	}
	if err := writeOwnerViewerHistory(dataDir, pane); err != nil {
		return false, err
	}
	return true, nil
}
