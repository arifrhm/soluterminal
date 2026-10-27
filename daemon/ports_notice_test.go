package main

import (
	"fmt"
	"testing"
	"time"
)

func TestPreviewNoticeProcessPolicy(t *testing.T) {
	for _, tc := range []struct{ command, want string }{
		{"node /project/node_modules/vite/bin/vite.js --host", "Vite"},
		{"node --conditions codex /project/node_modules/vite/bin/vite.js", "Vite"},
		{"node --import /project/loader.mjs /project/node_modules/next/dist/bin/next dev", "Next.js"},
		{"next-server (v16.9.1)", "Next.js"},
		{"node /project/node_modules/vitest/vitest.mjs run", ""},
		{"node --conditions vite /project/node_modules/vitest/vitest.mjs run", ""},
		{"node --eval 'vite'", ""},
		{"python3.11 -m pytest", ""},
		{"codex --no-daemon", ""},
		{"node /project/custom-server.js", ""},
		{"redis-server", ""},
		{`node C:\project\node_modules\vite\bin\vite.js`, "Vite"},
	} {
		t.Run(tc.command, func(t *testing.T) {
			if got := previewProcessLabel(&Port{Process: "MainThread", Cmdline: tc.command}); got != tc.want {
				t.Fatalf("label=%q want %q", got, tc.want)
			}
		})
	}
}

func TestPreviewNoticesBootStabilityAndInventory(t *testing.T) {
	now := time.Unix(1000, 0)
	pr := NewPortRegistry(nil)
	port := func(pid int, cmd string) *Port {
		return &Port{Port: 43295, WebStatus: "ready", Pid: pid, PaneID: "app", Cmdline: cmd, FirstAt: now}
	}
	fresh := map[string]*Port{"tcp4": port(1, "node vite")}
	if got := pr.previewNotices(fresh, now); len(got) != 0 {
		t.Fatal("boot notified")
	}
	pr.scannedOnce = true
	if got := pr.previewNotices(fresh, now.Add(5*time.Second)); len(got) != 0 {
		t.Fatal("baseline notified later")
	}
	fresh["tcp4"] = port(2, "node vite")
	if got := pr.previewNotices(fresh, now.Add(10*time.Second)); len(got) != 0 {
		t.Fatal("unstable listener notified")
	}
	// Address variants use one notification even with slightly different discovery timestamps.
	cp := *fresh["tcp4"]
	cp.FirstAt = now.Add(time.Millisecond)
	fresh["tcp6"] = &cp
	got := pr.previewNotices(fresh, now.Add(15*time.Second))
	if len(got) != 1 || got[0].Process != "Vite" {
		t.Fatalf("notices=%v", got)
	}
	if len(pr.previewNotices(fresh, now.Add(20*time.Second))) != 0 {
		t.Fatal("duplicate notice")
	}
	fresh["tcp4"] = port(3, "node vite")
	delete(fresh, "tcp6")
	pr.previewNotices(fresh, now.Add(25*time.Second))
	if len(pr.previewNotices(fresh, now.Add(30*time.Second))) != 0 {
		t.Fatal("restart burst")
	}
	pr.mu.Lock()
	pr.commitLocked(map[string]*Port{"test": port(4, "node vitest")})
	pr.mu.Unlock()
	if len(pr.List()) != 1 || !pr.Has(43295) {
		t.Fatal("suppression changed inventory/allowlist")
	}
}

func TestPreviewNoticeBurstAndBoundedHistory(t *testing.T) {
	pr := NewPortRegistry(nil)
	pr.scannedOnce = true
	now := time.Unix(2000, 0)
	fresh := map[string]*Port{}
	for i := 0; i < 400; i++ {
		fresh[fmt.Sprint(i)] = &Port{Port: uint16(30000 + i), WebStatus: "ready", Pid: i + 1, PaneID: "app", Cmdline: "node vite"}
	}
	pr.previewNotices(fresh, now)
	if len(pr.notices) > 256 {
		t.Fatal("unbounded history")
	}
	if len(pr.previewNotices(fresh, now.Add(5*time.Second))) != 1 {
		t.Fatal("burst was not paced")
	}
	if len(pr.previewNotices(fresh, now.Add(5*time.Second))) != 0 {
		t.Fatal("same-scan duplicate")
	}
	if len(pr.previewNotices(fresh, now.Add(35*time.Second))) != 0 {
		t.Fatal("expired listener notified")
	}
	pr.previewNotices(nil, now.Add(2*time.Minute))
	if len(pr.notices) != 0 {
		t.Fatal("history did not expire")
	}
}

func TestPreviewNoticeSlowScansDoNotResetLifetimes(t *testing.T) {
	pr := NewPortRegistry(nil)
	now := time.Unix(1000, 0)
	fresh := map[string]*Port{"p": {PaneID: "app", Port: 5173, WebStatus: "ready", Pid: 5, Cmdline: "node vite"}}
	pr.previewNotices(fresh, now)
	pr.scannedOnce = true
	pr.previewNotices(fresh, now.Add(10*time.Second))
	if len(pr.previewNotices(fresh, now.Add(15*time.Second))) != 0 {
		t.Fatal("delayed baseline scan notified")
	}
	fresh["p"].Pid = 6
	pr.previewNotices(fresh, now.Add(25*time.Second))
	if len(pr.previewNotices(fresh, now.Add(35*time.Second))) != 1 {
		t.Fatal("slow scans never announced a new preview")
	}
	for seconds := 40; seconds <= 100; seconds += 5 {
		pr.previewNotices(fresh, now.Add(time.Duration(seconds)*time.Second))
	}
	pr.previewNotices(fresh, now.Add(110*time.Second))
	if len(pr.previewNotices(fresh, now.Add(115*time.Second))) != 0 {
		t.Fatal("slow scan reannounced a stable listener")
	}
}

func TestPreviewNoticeDoesNotGuessFromBareRuntime(t *testing.T) {
	if got := previewProcessLabel(&Port{Process: "node.exe"}); got != "" {
		t.Fatalf("bare Windows runtime classified as %q", got)
	}
}

func TestPreviewNoticeMinuteLongStallPreservesBaseline(t *testing.T) {
	pr := NewPortRegistry(nil)
	now := time.Unix(1000, 0)
	fresh := map[string]*Port{"p": {PaneID: "app", Port: 5173, WebStatus: "ready", Pid: 5, Cmdline: "node vite"}}
	pr.previewNotices(fresh, now)
	pr.scannedOnce = true
	pr.previewNotices(fresh, now.Add(61*time.Second))
	if len(pr.previewNotices(fresh, now.Add(66*time.Second))) != 0 {
		t.Fatal("long stall reannounced baseline")
	}
}
