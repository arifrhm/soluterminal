package main

import (
	"time"
)

type statsPressureHost struct {
	Status     string `json:"status"`
	FreeMB     *int64 `json:"freeMB,omitempty"`
	TotalMB    *int64 `json:"totalMB,omitempty"`
	AgeSec     int64  `json:"ageSec,omitempty"`
	Provenance string `json:"provenance,omitempty"`
	Error      string `json:"error,omitempty"`
}

type statsGit struct {
	SlowCalls               int64           `json:"slowCalls"`
	Timeouts                int64           `json:"timeouts"`
	TimeoutRetriesSucceeded int64           `json:"timeoutRetriesSucceeded"`
	TimeoutFailures         int64           `json:"timeoutFailures"`
	LastWedge               *statsGitWedge  `json:"lastWedge,omitempty"`
	LandLocks               []statsLandLock `json:"landLocks,omitempty"`
	LandLockForcedReleases  int64           `json:"landLockForcedReleases"`
}

type statsLandLock struct {
	RepoKey    string `json:"repoKey"`
	Owner      string `json:"owner"`
	HeldForSec int64  `json:"heldForSec"`
	Stuck      bool   `json:"stuck"`
}

type statsAdmissionClaims struct {
	Queued       int                   `json:"queued"`
	Admitted     int                   `json:"admitted"`
	Claims       []statsAdmissionClaim `json:"claims"`
	GrantedTotal int64                 `json:"grantedTotal"`
	ExpiredTotal int64                 `json:"expiredTotal"`
	DeniedTotal  int64                 `json:"deniedTotal"`
}

type statsAdmissionClaim struct {
	ID          string    `json:"id"`
	Class       string    `json:"class"`
	Origin      string    `json:"origin"`
	Owner       string    `json:"owner"`
	State       string    `json:"state"`
	Cause       string    `json:"cause,omitempty"`
	CPU         float64   `json:"cpu"`
	MemMB       int64     `json:"memMB"`
	CostSource  string    `json:"costSource"`
	QueuedAt    time.Time `json:"queuedAt"`
	AdmittedAt  time.Time `json:"admittedAt,omitempty"`
	HeartbeatAt time.Time `json:"heartbeatAt,omitempty"`
}
