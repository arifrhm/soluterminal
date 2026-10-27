package main

import (
	"time"
)

const (
	adaptiveGreenCooldown = 120 * time.Second
	// Above-advisory spare probes require a small warm completion sample at
	// the current cap before workstation telemetry may overrule the count-only
	// machine formula.
	adaptiveSpareWarmMinSuccesses = 4
	// A configured Windows-host probe that remains stale while the controller
	// is above its advisory ceiling conservatively gives back one slot at a
	// time. This is intentionally shorter than a failed-cap retry hold because
	// missing telemetry is not evidence that the cap itself failed.
	adaptiveSpareHostStaleDemote = 5 * time.Minute
	adaptiveRedPersist           = 10 * time.Second
	adaptiveSampleCreditMax      = 10 * time.Second
	adaptiveFitPressureMaxAge    = 20 * time.Second
	adaptiveShadowObserveMin     = 4 * time.Hour
	adaptiveShadowTailGreen      = time.Hour
	// Promotion evidence grows linearly with the target cap: target T needs
	// 30m*(T-2) of loaded, saturated green telemetry at the current cap.
	adaptivePromoteSaturatedBase = 30 * time.Minute
	// Target T also needs 8*(T-2) successful heavy completions at the current cap.
	adaptivePromoteSuccessBase = 8
	// A cap invalidated by rollback cannot be retried until this bounded hold ends.
	adaptiveCapRetryHold = 24 * time.Hour
	// Throughput comparisons need representative busy time and completions at
	// both adjacent caps before they can govern capacity.
	adaptiveThroughputMinBusySec = int64((20 * time.Minute) / time.Second)
	adaptiveThroughputMinSamples = 8
	// Roll back when the higher cap realizes less than 90% of prior throughput.
	adaptiveThroughputRegressionRatio = 0.9
	// Successful completion durations are retained as a bounded per-cap ring.
	adaptiveCapCompletionSamples         = 16
	adaptiveRollbackHold                 = 30 * time.Minute
	adaptiveSoftRollbackHold             = 10 * time.Minute
	adaptiveSoftHoldRecoveryGreen        = 2 * time.Minute
	adaptiveMemoryFullWindow             = 30 * time.Second
	adaptiveMemoryFullHighWindow         = 60 * time.Second
	adaptiveMemoryFullMaxGap             = 10 * time.Second
	adaptiveMemoryHighHeadroomMB   int64 = 20 * 1024
	adaptiveRecoveryPromotionSec   int64 = 120
	adaptiveRecoverySoftPenaltySec int64 = 60
	adaptiveVerifyBaselineSamples        = 8
	adaptiveVerifyRegressionRatio        = 1.25
	// A ratio alone cannot separate cap-induced contention from host noise on
	// short verify commands: a 95ms webdist copy that takes 123ms, or a 770ms
	// package test that takes 2.5s, clears 1.25x on ordinary jitter and rolled
	// the heavy cap back under a 30-minute hold. Require the slowdown to also be
	// large in absolute terms. Past a ~20s baseline the ratio already implies
	// this floor, so genuinely long suites keep their existing protection.
	adaptiveVerifyRegressionMinDeltaMs int64 = 5_000
	hostFreeGreenMB                    int64 = 4096
	hostFreeRedMB                      int64 = 2048
	adaptivePressureRing                     = 720
	adaptivePressurePersist                  = 120
	adaptiveRampHistory                      = 64
	adaptiveRollbackHistory                  = 32
	// Swap reclaim is routine under WSL2 autoMemoryReclaim. Treat it as host
	// pressure only at a material rate, or when another memory signal agrees.
	adaptiveSwapRedPagesPerSec = 1000.0
	// One noisy pressure burst may remove at most one slot per minute. OOM
	// evidence bypasses this limiter because it is already a confirmed failure.
	adaptivePressureStepCooldown = 60 * time.Second
	rollbackClassSoft            = "soft"
	rollbackClassTransient       = "transient"
	rollbackClassHard            = "hard"
)
