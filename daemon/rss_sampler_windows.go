//go:build windows

package main

import "context"

// startRSSSampler is a no-op on Windows: the sampler reads /proc/<pid>/status,
// which only exists on Linux. The Windows-native backend does not expose the
// same cheap per-process RSS source used by the Focus memory chip.
func startRSSSampler(ctx context.Context, mgr *Manager) {}
