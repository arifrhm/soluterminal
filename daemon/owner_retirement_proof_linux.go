//go:build linux

package main

import (
	"fmt"
	"os"
	"path/filepath"
	"syscall"
	"time"
)

func prepareOwnerIdleProof(req ownerRetirementRequest, inputFence ownerRetirementInputFence, deadline time.Time) (ownerRetirementFrozen, ownerRetirementProof, error) {
	empty := ownerRetirementProof{}
	if req.AgentPID != os.Getpid() || req.PaneID == "" || req.ConversationID == "" {
		return nil, empty, fmt.Errorf("exact owner process/conversation required")
	}
	pane := &Pane{ID: req.PaneID, Pid: os.Getpid(), ClaudeSessionID: req.ConversationID, ClaudeTranscriptPath: req.ClaudeTranscriptPath}
	// Reject known active/unknown sessions before sending any stop signal.
	initial, err := readOwnerIdleBoundary(pane, req.ConversationID, inputFence)
	if err != nil {
		return nil, empty, err
	}
	sourceKey, providerPID := initial.SourceKey, initial.ProviderPID
	frozen, err := freezeOwnerChildren(os.Getpid(), deadline)
	if err != nil {
		return nil, empty, err
	}
	fail := func(err error) (ownerRetirementFrozen, ownerRetirementProof, error) {
		frozen.release()
		return nil, empty, err
	}
	proof, err := readOwnerIdleBoundary(pane, req.ConversationID, inputFence)
	if err != nil {
		return fail(err)
	}
	if proof.SourceKey != sourceKey || proof.ProviderPID != providerPID {
		return fail(fmt.Errorf("provider identity changed at close boundary"))
	}
	// A completed main turn does not prove detached/background descendants are
	// idle. Only the provider and its ancestor launch chain are eligible.
	for _, pid := range snapshotProcessTreeMaxAge(0).descendantPids(os.Getpid()) {
		if pid == os.Getpid() || pid == frozen.watchdogPID || pid == providerPID {
			continue
		}
		if !turnProcessDescends("/proc", providerPID, pid) {
			return fail(fmt.Errorf("owner has an unresolved child process"))
		}
	}
	frozen.mu.Lock()
	for pid := range frozen.fds {
		proof.ProcessIDs = append(proof.ProcessIDs, pid)
	}
	expired := frozen.released || !time.Now().Before(deadline)
	frozen.mu.Unlock()
	if expired {
		return fail(fmt.Errorf("retirement proof exceeded its bounded lease"))
	}
	return frozen, proof, nil
}

func readOwnerIdleBoundary(pane *Pane, conversation string, inputFence ownerRetirementInputFence) (ownerRetirementProof, error) {
	empty := ownerRetirementProof{}
	if inputFence.UnresolvedSubmission {
		return empty, fmt.Errorf("terminal submission was queued during active or unknown provider state; keep this owner for explicit inspection or reopening")
	}

	source := openTurnProviderSource(pane)
	if source == nil {
		return empty, fmt.Errorf("provider ownership cannot be proved")
	}
	if source.session != conversation || source.pid <= 0 {
		source.file.Close()
		return empty, fmt.Errorf("provider conversation changed")
	}
	providerPID := source.pid
	info, err := source.file.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Size() > 64<<20 {
		source.file.Close()
		return empty, fmt.Errorf("exact transcript cannot be retained within the history bound")
	}
	proof := ownerRetirementProof{AgentPID: os.Getpid(), ProviderPID: providerPID, SourceKey: source.key, Agent: source.agent, ConversationID: source.session, TranscriptPath: source.file.Name(), TranscriptSize: info.Size()}
	stat, ok := info.Sys().(*syscall.Stat_t)
	if !ok {
		source.file.Close()
		return empty, fmt.Errorf("transcript identity is unavailable")
	}
	stablePath, err := filepath.EvalSymlinks(source.file.Name())
	if err != nil || !filepath.IsAbs(stablePath) {
		source.file.Close()
		return empty, fmt.Errorf("stable transcript path is unavailable")
	}
	stableInfo, err := os.Stat(stablePath)
	if err != nil || !os.SameFile(info, stableInfo) {
		source.file.Close()
		return empty, fmt.Errorf("stable transcript path no longer names the proved file")
	}
	proof.TranscriptSourcePath = stablePath
	proof.TranscriptDevice, proof.TranscriptInode, proof.TranscriptModifiedNs = uint64(stat.Dev), stat.Ino, info.ModTime().UnixNano()
	var reader turnProviderReader
	observation := reader.read(source, time.Now().UnixMilli())
	if !observation.available || reader.offset != info.Size() || observation.uncertain || observation.active != nil || len(observation.completed) == 0 {
		return empty, fmt.Errorf("no conclusive idle provider boundary; retained pane remains open")
	}
	latest := observation.completed[len(observation.completed)-1]
	if latest.EndedMs <= 0 {
		return empty, fmt.Errorf("provider completion is not conclusive")
	}
	if inputFence.Seen && (!inputFence.Known || inputFence.SourceKey != proof.SourceKey || inputFence.Device != proof.TranscriptDevice || inputFence.Inode != proof.TranscriptInode || observation.startedOffsets[providerTurnKey(latest)] <= inputFence.Offset) {
		return empty, fmt.Errorf("last terminal input has no matching subsequent completed provider turn")
	}
	proof.CompletedMs = latest.EndedMs
	return proof, nil
}

func captureOwnerInputFence(paneID string, prior ownerRetirementInputFence) ownerRetirementInputFence {
	fence := ownerRetirementInputFence{Seen: true, UnresolvedSubmission: true}
	pane := &Pane{ID: paneID, Pid: os.Getpid()}
	source := openTurnProviderSource(pane)
	if source == nil {
		return fence
	}
	conversation := source.session
	source.file.Close()
	proof, err := readOwnerIdleBoundary(pane, conversation, prior)
	if err != nil {
		return fence
	}
	fence.Known = true
	fence.UnresolvedSubmission = false
	fence.SourceKey = proof.SourceKey
	fence.Offset = proof.TranscriptSize
	fence.Device = proof.TranscriptDevice
	fence.Inode = proof.TranscriptInode
	return fence
}
