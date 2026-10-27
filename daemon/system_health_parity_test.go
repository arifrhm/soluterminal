package main

import (
	"encoding/json"
	"net/http/httptest"
	"testing"
	"time"
)

func TestSystemHealthAndStatusUseSameCachedVerdict(t *testing.T) {
	now := time.Now().UTC()
	healthyFree, total, healthyAvailable := int64(50_000), int64(100_000), int64(10_000)
	healthyRSS, healthyCPU := 100.0, 2.0
	sample := systemSample{
		SampledAt: now, DiskFreeMB: &healthyFree, DiskTotalMB: &total,
		MemAvailableMB: &healthyAvailable, DaemonRssMB: &healthyRSS, DaemonCpuPct: &healthyCPU,
		Levels: systemLevelsDTO{
			Overall: "critical", Reason: "disk: cached critical verdict",
			Areas: map[string]string{
				"disk": "critical", "hostMemory": "ok", "daemonMemory": "ok", "daemonCpu": "ok",
				"engine": "ok", "runtimes": "ok", "automation": "ok", "cloud": "ok",
			},
			Reasons: []systemLevelReason{{Key: "disk", Level: "critical", Text: "disk: cached critical verdict"}},
		},
	}
	sampler := seededSystemSampler(sample)
	// A health/status request must not call probe dependencies to refresh the
	// cached sample or its hysteresis verdict.
	sampler.deps = systemSamplerDeps{
		diskUsage: func(string) (int64, int64, bool) { t.Fatal("request path called disk probe"); return 0, 0, false },
		procTicks: func(int) (uint64, uint64, bool) { t.Fatal("request path called process probe"); return 0, 0, false },
		hostTicks: func() (uint64, uint64, bool) { t.Fatal("request path called host probe"); return 0, 0, false },
	}
	a := &apiServer{system: sampler, startT: now, dataDir: t.TempDir()}

	statusRecorder := httptest.NewRecorder()
	a.systemStatus(statusRecorder, httptest.NewRequest("GET", "/api/system/status", nil))
	var statusBody struct {
		Level systemLevelsDTO `json:"level"`
	}
	if err := json.Unmarshal(statusRecorder.Body.Bytes(), &statusBody); err != nil {
		t.Fatal(err)
	}
	health := a.systemHealthSummary()
	if health == nil {
		t.Fatal("health summary was omitted with an active sampler")
	}
	if statusBody.Level.Overall != "critical" || health.Level != statusBody.Level.Overall || health.Reason != statusBody.Level.Reason {
		t.Fatalf("cached verdict diverged: status=%+v health=%+v", statusBody.Level, health)
	}
	if len(health.Reasons) != 1 || health.Reasons[0] != statusBody.Level.Reasons[0] {
		t.Fatalf("health reasons diverged: status=%+v health=%+v", statusBody.Level.Reasons, health.Reasons)
	}
}
