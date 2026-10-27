//go:build linux

package main

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal/grid"

	"github.com/creack/pty"
)

// Opt-in installed-CLI acceptance fixture. Every model request targets the
// localhost synthetic Responses server. A temporary CODEX_HOME and sanitized
// child environment isolate it from owner/team credentials and configuration.
// Identity is deliberately stubbed: this tests the real TUI consuming the
// production guarded transport, not a second claim about process ownership.
func TestConditionalSubmitRealCodexPTY(t *testing.T) {
	if os.Getenv("SOLU_CONDITIONAL_TUI_INTEGRATION") != "1" {
		t.Skip("set SOLU_CONDITIONAL_TUI_INTEGRATION=1 for the installed CLI fixture")
	}
	codex, err := exec.LookPath("codex")
	if err != nil {
		t.Fatal(err)
	}
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	home := filepath.Join(root, "codex-home")
	if err := os.Mkdir(home, 0700); err != nil {
		t.Fatal(err)
	}
	const nonce = "CONDITIONAL_TUI_LOCAL_FIXTURE_OK"
	accepted := make(chan bool, 8)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if !strings.HasSuffix(r.URL.Path, "/responses") {
			w.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(w, `{"data":[]}`)
			return
		}
		body, _ := io.ReadAll(io.LimitReader(r.Body, 8<<20))
		select {
		case accepted <- strings.Contains(string(body), nonce):
		default:
		}
		w.Header().Set("Content-Type", "text/event-stream")
		for _, event := range []string{
			`{"type":"response.created","response":{"id":"resp_fixture","status":"in_progress","output":[]}}`,
			`{"type":"response.output_item.added","output_index":0,"item":{"type":"message","id":"msg_fixture","role":"assistant","content":[]}}`,
			`{"type":"response.output_text.delta","item_id":"msg_fixture","output_index":0,"content_index":0,"delta":"fixture conditional accepted"}`,
			`{"type":"response.output_item.done","output_index":0,"item":{"type":"message","id":"msg_fixture","role":"assistant","content":[{"type":"output_text","text":"fixture conditional accepted"}]}}`,
			`{"type":"response.completed","response":{"id":"resp_fixture","status":"completed","output":[],"usage":{"input_tokens":1,"output_tokens":1,"total_tokens":2}}}`,
		} {
			fmt.Fprintf(w, "data: %s\n\n", event)
		}
	}))
	defer server.Close()
	config := fmt.Sprintf("model = \"gpt-5.6-sol\"\nmodel_provider = \"fixture\"\ncheck_for_update_on_startup = false\ncli_auth_credentials_store = \"ephemeral\"\n[features]\napps = false\n[model_providers.fixture]\nname = \"Local fixture\"\nbase_url = %q\nwire_api = \"responses\"\nrequires_openai_auth = false\nenv_key = \"CONDITIONAL_FIXTURE_KEY\"\nsupports_websockets = false\n[projects.%q]\ntrust_level = \"trusted\"\n", server.URL+"/v1", root)
	if err := os.WriteFile(filepath.Join(home, "config.toml"), []byte(config), 0600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(codex, "--no-daemon", "--no-alt-screen", "--sandbox", "read-only")
	cmd.Dir = root
	cmd.Env = []string{"PATH=" + os.Getenv("PATH"), "HOME=" + root, "TMPDIR=" + root,
		"CODEX_HOME=" + home, "TERM=xterm-256color", "LANG=C.UTF-8", "CONDITIONAL_FIXTURE_KEY=synthetic-only",
		"HTTP_PROXY=http://127.0.0.1:9", "HTTPS_PROXY=http://127.0.0.1:9", "ALL_PROXY=http://127.0.0.1:9",
		"NO_PROXY=127.0.0.1,localhost"}
	terminal, err := pty.StartWithSize(cmd, &pty.Winsize{Rows: 35, Cols: 120})
	if err != nil {
		t.Fatal(err)
	}
	done := make(chan error, 1)
	go func() { done <- cmd.Wait() }()
	var workers sync.WaitGroup
	var emulator *grid.Emulator
	defer func() {
		stopCodexLiveProcess(cmd, done)
		_ = terminal.Close()
		workers.Wait()
		if emulator != nil {
			_ = emulator.Close()
		}
	}()
	a, p, _, req, _ := conditionalFixture(t)
	p.Pid, p.Cwd = cmd.Process.Pid, root
	p.replayEmu = grid.New(120, 35)
	emulator = p.replayEmu
	p.buffer = newTestRing()
	a.conditionalSubmitProbe = func(context.Context, int) (conditionalSubmitProcess, error) {
		return conditionalSubmitProcess{Provider: "codex", Pid: cmd.Process.Pid, StartTime: "12345", Cwd: root}, nil
	}
	client, agent := net.Pipe()
	defer client.Close()
	defer agent.Close()
	p.agentConn = client
	var ptyMu sync.Mutex
	workers.Add(2)
	go func() {
		defer workers.Done()
		for {
			kind, payload, err := ReadFrame(agent)
			if err != nil || kind != FrameData {
				return
			}
			ptyMu.Lock()
			_, err = terminal.Write(payload)
			ptyMu.Unlock()
			if err != nil {
				return
			}
		}
	}()
	go func() {
		defer workers.Done()
		buf := make([]byte, 8192)
		for {
			n, err := terminal.Read(buf)
			if n > 0 {
				p.buffer.Write(buf[:n])
				p.replayEmu.Feed(buf[:n])
				atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI(buf[:n]))))
				if strings.Contains(string(buf[:n]), "\x1b[6n") {
					// Same input lease as real browser replies, outside emulator
					// locks. A paste gap can delay it, never invert lock order.
					_, _ = p.Write([]byte("\x1b[1;1R"))
				}
			}
			if err != nil {
				return
			}
		}
	}()
	deadline := time.Now().Add(25 * time.Second)
	var ready conditionalSubmitResult
	declinedFixtureModelMigration := false
	for time.Now().Before(deadline) {
		// Fresh temporary homes can show a one-time model promotion. Choose
		// ONLY the explicitly offered existing model in this isolated fixture;
		// never accept an upsell or modify the owner's configuration.
		if snapshot, ok := p.replayEmu.TrySnapshot(); ok && !declinedFixtureModelMigration {
			text, _ := snapshotVisibleText(snapshot)
			if strings.Contains(text, "Meet GPT-6 Sol") && strings.Contains(text, "2. Use existing model") {
				ptyMu.Lock()
				_, _ = terminal.Write([]byte("\x1b[B"))
				ptyMu.Unlock()
				time.Sleep(100 * time.Millisecond)
				ptyMu.Lock()
				_, _ = terminal.Write([]byte("\r"))
				ptyMu.Unlock()
				declinedFixtureModelMigration = true
			}
		}
		ready = a.runConditionalSubmit(context.Background(), p.ID, nil)
		if ready.outcome == "ready" {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if ready.outcome != "ready" {
		tail, _, _ := p.buffer.TrySnapshotTail(3000)
		t.Fatalf("real TUI never reached guarded readiness: %+v; %s", ready, stripANSI(tail))
	}
	req.conditionalSubmitCondition, req.Text = ready.condition, nonce+". Reply fixture only; do not call tools."
	result := a.runConditionalSubmit(context.Background(), p.ID, &req)
	if result.outcome != "transport_written" {
		t.Fatal(result)
	}
	timer := time.NewTimer(5 * time.Second)
	defer timer.Stop()
	for {
		select {
		case matched := <-accepted:
			if matched {
				t.Log("real installed Codex TUI submitted the exact guarded nonce to localhost; no real account/provider call")
				return
			}
		case <-timer.C:
			t.Fatal("socket write succeeded but the real TUI never submitted a local model request")
		}
	}
}
