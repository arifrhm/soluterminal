//go:build linux

package main

import (
	"context"
	"fmt"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// This gate uses a production agent executable and a disposable real PTY.
// The provider fixture owns a real, foreground transcript FD; no fake /proc,
// activity summaries, mocked stop signal or replaced Manager close is used.
func TestOwnerRetirementManagerRealPTY(t *testing.T) {
	binary := os.Getenv("SOLU_OWNER_RETIREMENT_AGENT_BINARY")
	if binary == "" {
		t.Skip("set SOLU_OWNER_RETIREMENT_AGENT_BINARY to a production agent build")
	}
	for _, mode := range []string{"idle", "idle-submission", "queued-input", "active", "retain-failure", "guard-failure", "late-input", "flagged"} {
		t.Run(mode, func(t *testing.T) {
			dir := t.TempDir()
			config := filepath.Join(dir, "claude")
			project := filepath.Join(config, "projects", "fixture")
			if err := os.MkdirAll(project, 0700); err != nil {
				t.Fatal(err)
			}
			transcript := filepath.Join(project, turnProviderSession+".jsonl")
			content := claudeTurnLine("user", "user", "old", turnProviderStart, map[string]any{"promptSource": "typed", "message": map[string]any{"role": "user", "content": "work"}}) + "\n"
			if mode != "active" && mode != "queued-input" {
				content += claudeTurnLine("assistant", "answer", "user", turnProviderStart+1000, map[string]any{"message": map[string]any{"stop_reason": "end_turn"}}) + "\n"
				content += claudeTurnLine("system", "duration", "answer", turnProviderStart+2000, map[string]any{"subtype": "turn_duration"}) + "\n"
			}
			if err := os.WriteFile(transcript, []byte(content), 0600); err != nil {
				t.Fatal(err)
			}
			script := filepath.Join(dir, "provider.py")
			if err := os.WriteFile(script, []byte("import ctypes,os,sys\nctypes.CDLL(None).prctl(15,b'claude',0,0,0)\nf=open(sys.argv[1],'a')\nos.write(1,b'OWNER_READY\\r\\n')\nwhile True:\n data=os.read(0,4096)\n if not data: break\n os.write(1,b'INPUT:'+data)\n"), 0600); err != nil {
				t.Fatal(err)
			}
			socket := filepath.Join(dir, "agent.sock")
			command := exec.Command(binary, "agent", "--socket", socket, "--cwd", dir, "--cols", "80", "--rows", "24", "--data-dir", dir, "--env", "SOLU_PANE_ID=owner,CLAUDE_CONFIG_DIR="+config, "--owner-retirement-pane", "owner", "--", "python3", script, transcript)
			log, err := os.Create(filepath.Join(dir, "agent.log"))
			if err != nil {
				t.Fatal(err)
			}
			defer log.Close()
			command.Stdout = log
			command.Stderr = log
			if err := command.Start(); err != nil {
				t.Fatal(err)
			}
			defer func() { command.Process.Kill(); command.Wait() }()
			var conn net.Conn
			deadline := time.Now().Add(5 * time.Second)
			for time.Now().Before(deadline) {
				conn, err = net.Dial("unix", socket)
				if err == nil {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			if err != nil {
				t.Fatal(err)
			}
			p := &Pane{ownerViewerKnowledgeKnown: true, ID: "owner", Role: paneRoleTaskOwner, Pid: command.Process.Pid, Status: PaneRunning, agentConn: conn, done: make(chan struct{}), buffer: NewRingBuffer(1 << 20), subs: map[int64]*subEntry{}, ClaudeSessionID: turnProviderSession, ClaudeTranscriptPath: transcript, Cols: 80, Rows: 24, SocketPath: socket, Cwd: dir}
			m := NewManager(dir)
			m.panes[p.ID] = p
			go p.readLoop(nil)
			defer func() {
				conn.Close()
				select {
				case <-p.done:
				case <-time.After(time.Second):
					t.Error("pane reader leaked")
				}
			}()
			for time.Now().Before(deadline) {
				p.mu.Lock()
				supported := p.ownerRetirementSupported
				p.mu.Unlock()
				if supported && strings.Contains(string(p.buffer.Snapshot()), "OWNER_READY") {
					break
				}
				time.Sleep(10 * time.Millisecond)
			}
			// Discovery caches are refreshed explicitly before exercising the actual
			// agent proof. The provider has finished startup and keeps the same FD.
			snapshotProcessTreeMaxAge(0)
			if mode == "idle-submission" || mode == "queued-input" {
				if _, err := p.Write([]byte("next\r")); err != nil {
					t.Fatal(err)
				}
				delivered := time.Now().Add(time.Second)
				for !strings.Contains(string(p.buffer.Snapshot()), "INPUT:") && time.Now().Before(delivered) {
					time.Sleep(time.Millisecond)
				}
				if !strings.Contains(string(p.buffer.Snapshot()), "INPUT:") {
					t.Fatal("fixture input not delivered")
				}
				extra := ""
				parent := "user"
				if mode == "idle-submission" {
					extra = claudeTurnLine("user", "user2", "duration", turnProviderStart+3000, map[string]any{"promptSource": "typed", "message": map[string]any{"role": "user", "content": "next"}}) + "\n"
					parent = "user2"
				}
				extra += claudeTurnLine("assistant", "answer2", parent, turnProviderStart+4000, map[string]any{"message": map[string]any{"stop_reason": "end_turn"}}) + "\n"
				extra += claudeTurnLine("system", "duration2", "answer2", turnProviderStart+5000, map[string]any{"subtype": "turn_duration"}) + "\n"
				file, err := os.OpenFile(transcript, os.O_APPEND|os.O_WRONLY, 0600)
				if err != nil {
					t.Fatal(err)
				}
				if _, err = file.WriteString(extra); err != nil {
					t.Fatal(err)
				}
				file.Close()
				content += extra
			}
			retained := false
			guarded := false
			hooks := ownerRetirementHooks{
				Retain: func(ctx context.Context, proof ownerRetirementProof, snapshot PaneSnapshot) error {
					if proof.ConversationID != turnProviderSession || proof.TranscriptSize != int64(len(content)) || proof.ProviderPID <= 0 {
						t.Errorf("bad exact history proof: %+v", proof)
					}
					raw, err := os.ReadFile(proof.TranscriptPath)
					if err != nil || string(raw) != content {
						return fmt.Errorf("history unavailable: %v", err)
					}
					if mode == "retain-failure" {
						return fmt.Errorf("injected durable history failure")
					}
					retained = true
					if mode == "late-input" {
						go func() { _, _ = p.Write([]byte("draft")) }()
						until := time.Now().Add(500 * time.Millisecond)
						for time.Now().Before(until) {
							p.ownerRetirementGate.Lock()
							cancelled := p.ownerViewerLease == nil
							p.ownerRetirementGate.Unlock()
							if cancelled {
								return nil
							}
							time.Sleep(time.Millisecond)
						}
						return fmt.Errorf("input cancellation did not arrive")
					}
					return nil
				},
				CommitGuard: func(PaneSnapshot) (func(), error) {
					guarded = true
					if mode == "guard-failure" {
						return nil, fmt.Errorf("injected lineage change")
					}
					return func() {}, nil
				},
			}
			if mode == "flagged" {
				p.mu.Lock()
				p.Flagged = true
				p.mu.Unlock()
			}
			ctx, cancel := context.WithTimeout(context.Background(), 7*time.Second)
			defer cancel()
			result, err := m.retireOwnerPane(ctx, p.ID, turnProviderSession, hooks)
			if mode == "idle" || mode == "idle-submission" {
				if err != nil || !result.Exited || !retained || !guarded {
					raw, _ := os.ReadFile(filepath.Join(dir, "agent.log"))
					t.Fatalf("idle did not retire after history/guard: result=%+v err=%v retained=%v guarded=%v log=%s", result, err, retained, guarded, raw)
				}
				if _, ok := m.Get(p.ID); ok {
					t.Fatal("retired process remains a live manager pane")
				}
			} else {
				if err == nil || result.Exited || result.CommitSent {
					t.Fatalf("%s closed despite blocker: %+v %v", mode, result, err)
				}
				if _, ok := m.Get(p.ID); !ok {
					t.Fatal("blocked owner removed")
				}
				source := openTurnProviderSource(p)
				if source == nil {
					t.Fatal("blocked provider no longer owns its live transcript")
				}
				source.file.Close()
				if _, err := p.Write([]byte("still here\r")); err != nil {
					t.Fatalf("blocked owner input not restored: %v", err)
				}
			}
		})
	}
}
