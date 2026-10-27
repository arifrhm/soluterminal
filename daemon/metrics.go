package main

import (
	"fmt"
	"io"
	"net/http"
	"runtime"
	"sort"
	"sync"
	"sync/atomic"
)

// Metrics holds the daemon's process-local counters and gauges in
// Prometheus exposition format. The serializer is hand-rolled so we
// don't pull in the whole prometheus/client_golang dependency just to
// publish ~20 numbers — when the metric set grows past what's easy to
// hand-curate, swap in the real client.
//
// All values are atomic int64s. Counters are monotonically increasing;
// gauges can move both ways. To add a new counter, declare a field +
// add a one-line entry in WriteText below.

type Metrics struct {
	// Updater outcomes. checks_total - errors_total = successful checks.
	UpdaterChecksTotal  atomic.Int64
	UpdaterErrorsTotal  atomic.Int64
	UpdaterAppliesTotal atomic.Int64

	// Push notifications fanned out to phones. Total = sum of attempted
	// sends; deliveries reflect what the cloud confirmed actually went
	// out (since fan-out happens in goroutines, this lags slightly but
	// is the operational truth).
	PushFireTotal     atomic.Int64
	PushDeliveriesSum atomic.Int64
	PushErrorsTotal   atomic.Int64

	// ARIA autoresponder.
	AriaEvalsTotal           atomic.Int64
	AriaEvalFiredTotal       atomic.Int64 // eval delivered terminal input or a native permission response
	AriaEvalSkippedTotal     atomic.Int64 // eval skipped (paused / cap / in-flight dedup)
	AriaEvalErrorsTotal      atomic.Int64
	AriaSweeperSuspendsTotal atomic.Int64

	// Pane lifecycle. Spawn = new pane created; Exit = pane process
	// exited and was reaped. Diff = currently running panes (also
	// available live via /api/panes, but this lets dashboards graph it).
	PaneSpawnTotal atomic.Int64
	PaneExitTotal  atomic.Int64

	// Custom labelled counters that don't justify their own field. Keys
	// are pre-formatted "name{label=value}" strings; values are the
	// running counts. Locked because Prometheus exposition needs sorted
	// output and atomic.Map doesn't sort cheaply.
	mu       sync.Mutex
	labelled map[string]int64
}

func NewMetrics() *Metrics {
	return &Metrics{labelled: make(map[string]int64)}
}

// IncLabelled adds 1 to the counter identified by name + a stable
// labelset. Use sparingly — every distinct label combination becomes a
// new time series, and a high-cardinality label (e.g. paneID) blows
// up cost on any scrape target. Reserve for low-cardinality dimensions
// like outcome, kind, http status class.
func (m *Metrics) IncLabelled(name string, labels map[string]string) {
	if m == nil {
		return
	}
	key := formatSeriesKey(name, labels)
	m.mu.Lock()
	m.labelled[key]++
	m.mu.Unlock()
}

func formatSeriesKey(name string, labels map[string]string) string {
	if len(labels) == 0 {
		return name
	}
	keys := make([]string, 0, len(labels))
	for k := range labels {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	var b []byte
	b = append(b, name...)
	b = append(b, '{')
	for i, k := range keys {
		if i > 0 {
			b = append(b, ',')
		}
		b = append(b, k...)
		b = append(b, '=', '"')
		// Prom label values can't contain unescaped " or \. In our use
		// the labels are constants from the daemon so this is mostly
		// belt-and-braces, but keep it correct.
		for _, r := range labels[k] {
			switch r {
			case '"', '\\':
				b = append(b, '\\', byte(r))
			case '\n':
				b = append(b, '\\', 'n')
			default:
				b = append(b, byte(r))
			}
		}
		b = append(b, '"')
	}
	b = append(b, '}')
	return string(b)
}

// WriteText emits the exposition. Format spec:
// https://github.com/prometheus/docs/blob/main/content/docs/instrumenting/exposition_formats.md
func (m *Metrics) WriteText(w io.Writer, paneCount int) {
	type entry struct {
		name, help, kind string
		value            int64
	}
	rows := []entry{
		{"solud_updater_checks_total", "Total auto-update manifest checks attempted.", "counter", m.UpdaterChecksTotal.Load()},
		{"solud_updater_errors_total", "Total auto-update checks that returned an error.", "counter", m.UpdaterErrorsTotal.Load()},
		{"solud_updater_applies_total", "Total auto-update binary swaps applied.", "counter", m.UpdaterAppliesTotal.Load()},
		{"solud_push_fire_total", "Times the daemon attempted to notify cloud of a pane attention state.", "counter", m.PushFireTotal.Load()},
		{"solud_push_deliveries_sum", "Sum of subscription deliveries reported by the cloud across all push fires.", "counter", m.PushDeliveriesSum.Load()},
		{"solud_push_errors_total", "Times a push notify call to the cloud failed.", "counter", m.PushErrorsTotal.Load()},
		{"solud_aria_evals_total", "Total ARIA autonomous evaluations started.", "counter", m.AriaEvalsTotal.Load()},
		{"solud_aria_eval_fired_total", "Evaluations that delivered terminal input or a native permission response.", "counter", m.AriaEvalFiredTotal.Load()},
		{"solud_aria_eval_skipped_total", "Evaluations skipped (paused / cap / in-flight dedup).", "counter", m.AriaEvalSkippedTotal.Load()},
		{"solud_aria_eval_errors_total", "Evaluations that failed (cloud / model / tool error).", "counter", m.AriaEvalErrorsTotal.Load()},
		{"solud_aria_sweeper_suspends_total", "Times the sweeper backed off after consecutive non-fires.", "counter", m.AriaSweeperSuspendsTotal.Load()},
		{"solud_pane_spawn_total", "Total panes spawned over the daemon's lifetime.", "counter", m.PaneSpawnTotal.Load()},
		{"solud_pane_exit_total", "Total panes that exited (process died and was reaped).", "counter", m.PaneExitTotal.Load()},
		{"solud_pane_running", "Currently running panes.", "gauge", int64(paneCount)},
		{"solud_goroutines", "Live goroutines in the daemon process.", "gauge", int64(runtime.NumGoroutine())},
	}
	for _, r := range rows {
		fmt.Fprintf(w, "# HELP %s %s\n", r.name, r.help)
		fmt.Fprintf(w, "# TYPE %s %s\n", r.name, r.kind)
		fmt.Fprintf(w, "%s %d\n", r.name, r.value)
	}

	// Labelled rows last so the file's grouping is predictable. Sort
	// keys so the output is byte-stable across scrapes (helpful when
	// diff-ing two snapshots by hand).
	m.mu.Lock()
	keys := make([]string, 0, len(m.labelled))
	for k := range m.labelled {
		keys = append(keys, k)
	}
	sort.Strings(keys)
	for _, k := range keys {
		fmt.Fprintf(w, "%s %d\n", k, m.labelled[k])
	}
	m.mu.Unlock()
}

// metricsHandler exposes /metrics. Loopback-only — the metrics surface
// is operational telemetry, not a public API; no token gate is offered
// because there's nothing for a remote browser session to do with it.
func metricsHandler(m *Metrics, mgr *Manager) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		if !isLoopback(r) {
			http.Error(w, "metrics is loopback-only", http.StatusForbidden)
			return
		}
		w.Header().Set("Content-Type", "text/plain; version=0.0.4; charset=utf-8")
		paneCount := 0
		if mgr != nil {
			paneCount = len(mgr.List())
		}
		m.WriteText(w, paneCount)
	}
}
