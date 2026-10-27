package main

import (
	"fmt"
	"os"
	"sort"
	"strings"
)

// FleetComponent is one process in the debug inventory (GET /api/debug/fleet).
// Stale means "a restart would change behavior": for a pane agent that is an
// older agentProtoVersion, NOT merely a different binary. BinaryDiffers keeps
// the raw inode comparison for diagnosis only.
type FleetComponent struct {
	Role              string `json:"role"`
	PaneID            string `json:"paneId,omitempty"`
	PaneTitle         string `json:"paneTitle,omitempty"`
	PID               int    `json:"pid"`
	Version           string `json:"version"`
	Executable        string `json:"executable"`
	AgentProto        int    `json:"agentProto,omitempty"`
	Stale             bool   `json:"stale"`
	BinaryDiffers     bool   `json:"binaryDiffers"`
	Reason            string `json:"reason,omitempty"`
	RuntimeCompatible bool   `json:"runtimeCompatible,omitempty"`
}

type FleetStatus struct {
	Supported         bool             `json:"supported"`
	UnsupportedReason string           `json:"unsupportedReason,omitempty"`
	RunningVersion    string           `json:"runningVersion,omitempty"`
	TargetVersion     string           `json:"targetVersion"`
	TargetExecutable  string           `json:"targetExecutable"`
	AgentProtoVersion int              `json:"agentProtoVersion"`
	StaleCount        int              `json:"staleCount"`
	InventoryError    string           `json:"inventoryError"`
	Components        []FleetComponent `json:"components"`
}

type fleetFileIdentity struct {
	Device uint64
	Inode  uint64
}

func (i fleetFileIdentity) same(other fleetFileIdentity) bool {
	return i.Device != 0 && i.Inode != 0 && i == other
}

type fleetTargetBinary struct {
	Path     string
	Version  string
	Identity fleetFileIdentity
	Runtime  string
}

type fleetProcessBinary struct {
	PID        int
	StartTime  uint64
	Identity   fleetFileIdentity
	Executable string
	Version    string
	Deleted    bool
	Runtime    string
}

type fleetBinaryPlatform interface {
	Supported() bool
	Target(path string) (fleetTargetBinary, error)
	Process(pid int, reportedVersion string) (fleetProcessBinary, error)
}

// fleetBinaryInspector inventories only the serving process and the panes in
// this Manager. It never walks /proc for unrelated solud processes, which is
// what keeps separate data directories isolated from one another. It backs the
// debug endpoint only; nothing polls it.
type fleetBinaryInspector struct {
	targetExecutable string
	servingVersion   string
	mgr              *Manager
	platform         fleetBinaryPlatform
}

func newFleetBinaryInspector(targetExecutable, servingVersion string, mgr *Manager) *fleetBinaryInspector {
	if strings.TrimSpace(targetExecutable) == "" {
		targetExecutable, _ = os.Executable()
	}
	return &fleetBinaryInspector{
		targetExecutable: targetExecutable,
		servingVersion:   strings.TrimSpace(servingVersion),
		mgr:              mgr,
		platform:         newFleetBinaryPlatform(),
	}
}

func (i *fleetBinaryInspector) target() (fleetTargetBinary, error) {
	if i == nil || i.platform == nil || !i.platform.Supported() {
		return fleetTargetBinary{}, fmt.Errorf("fleet binary inventory is unsupported on this platform")
	}
	return i.platform.Target(i.targetExecutable)
}

func (i *fleetBinaryInspector) Status() FleetStatus {
	status := FleetStatus{
		Supported:         i != nil && i.platform != nil && i.platform.Supported(),
		AgentProtoVersion: agentProtoVersion,
		Components:        []FleetComponent{},
	}
	if i == nil {
		status.InventoryError = "fleet inspector is unavailable"
		return status
	}
	if !status.Supported {
		status.UnsupportedReason = "fleet binary inventory requires Linux /proc"
		return status
	}
	target, err := i.target()
	if err != nil {
		status.InventoryError = err.Error()
		return status
	}
	status.TargetExecutable = target.Path
	status.TargetVersion = target.Version
	status.RunningVersion = i.servingVersion
	if strings.TrimSpace(target.Version) == "" {
		status.InventoryError = "installed executable version metadata is unavailable"
	}

	serving, err := i.platform.Process(os.Getpid(), i.servingVersion)
	if err != nil {
		status.InventoryError = "serving daemon: " + err.Error()
	} else {
		status.Components = append(status.Components, classifyFleetComponent("daemon", "", "Serving daemon", serving, target, 0))
	}

	if i.mgr != nil {
		panes := i.mgr.List()
		sort.Slice(panes, func(a, b int) bool { return panes[a].ID < panes[b].ID })
		for _, pane := range panes {
			snap := pane.Snapshot()
			if snap.Status != PaneRunning {
				continue
			}
			reportedVersion, pid, proto := pane.agentRuntimeInfo()
			proc, procErr := i.platform.Process(pid, reportedVersion)
			if procErr != nil {
				proc = fleetProcessBinary{PID: pid, Version: reportedVersion, Executable: fmt.Sprintf("/proc/%d/exe (unavailable)", pid)}
				if status.InventoryError == "" {
					status.InventoryError = fmt.Sprintf("pane %s: %v", snap.ID, procErr)
				}
			}
			status.Components = append(status.Components, classifyFleetComponent("pane-agent", snap.ID, displayTitle(pane), proc, target, proto))
		}
	}

	for _, component := range status.Components {
		if component.Stale {
			status.StaleCount++
		}
	}
	return status
}

// classifyFleetComponent labels one inventoried process. The serving daemon is
// stale when it executes a binary other than the installed one (the updater
// restarts it). A pane agent is stale ONLY when it reports an older
// agentProtoVersion: agent binaries differ after nearly every release, but the
// PTY relay they run rarely changes, so a differing binary alone is not a
// reason to ask for a restart.
func classifyFleetComponent(role, paneID, paneTitle string, proc fleetProcessBinary, target fleetTargetBinary, agentProto int) FleetComponent {
	component := FleetComponent{
		Role: role, PaneID: paneID, PaneTitle: paneTitle, PID: proc.PID,
		Version: proc.Version, Executable: proc.Executable, AgentProto: agentProto,
	}
	component.BinaryDiffers = !proc.Identity.same(target.Identity)
	component.RuntimeCompatible = proc.Runtime != "" && proc.Runtime == target.Runtime
	if role == "pane-agent" {
		component.Stale = agentProtoStale(agentProto)
		if component.Stale {
			component.Reason = fmt.Sprintf("agent protocol %d; this daemon ships %d", agentProto, agentProtoVersion)
		}
		return component
	}
	component.Stale = component.BinaryDiffers
	if component.Stale {
		switch {
		case proc.Deleted:
			component.Reason = "executing a replaced/deleted binary inode"
		case proc.Version != "" && target.Version != "" && proc.Version != target.Version:
			component.Reason = fmt.Sprintf("running version %s; installed version is %s", proc.Version, target.Version)
		default:
			component.Reason = "executable inode differs from the installed binary"
		}
	}
	return component
}
