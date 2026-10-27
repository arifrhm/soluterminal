//go:build !windows

package main

import (
	"bytes"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"regexp"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/creack/pty"
)

// The composer repaints by moving the cursor back up over its own frame from
// the parked input cell. It used to use DECSC/DECRC (\x1b7/\x1b8), which saves
// an absolute screen row — but the composer is drawn against the bottom of the
// pane, so drawing it scrolls the screen and the saved row is never adjusted.
// The restore then landed below the real top and every keystroke left the
// previous frame behind: the duplicated input row. Pipes never scroll and the
// raw-mode path is TTY-only, so the pipe-based client tests could not see it.
// Drive the real client under a PTY, type at human speed, and check the
// finished screen holds exactly one composer row.
const composerFakeRuntime = `import readline from 'node:readline';
const input=readline.createInterface({input:process.stdin});let promptId;
const send=x=>process.stdout.write(JSON.stringify({jsonrpc:'2.0',...x})+'\n');
input.on('line',line=>{const m=JSON.parse(line);
if(m.method==='initialize')send({id:m.id,result:{protocolVersion:1,agentCapabilities:{promptCapabilities:{image:false}}}});
else if(m.method==='session/new')send({id:m.id,result:{sessionId:'s',configOptions:[{id:'model',category:'model',currentValue:'deepseek-flash',options:[{value:'deepseek-flash',name:'DeepSeek V4.1 Flash'}]}]}});
else if(m.method==='session/prompt'){promptId=m.id;
send({method:'session/update',params:{sessionId:'s',update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'filler output\n'}}}});
send({id:promptId,result:{stopReason:'end_turn'}});}
else if(m.method==='session/close')send({id:m.id,result:{}});
});input.on('close',()=>process.exit(0));`

// harnessClientEnv is the process environment with the managed-harness and pane
// hints removed. `go test` run from inside a SoluTerminal pane inherits
// SOLU_HARNESS_RESUME_SESSION / SOLU_PANE_ID / DSH_*; the client would then try
// to resume a real session against the test's fake runtime and hang until its
// kill timer. Tests that exercise resume pass their own values.
func harnessClientEnv(extra ...string) []string {
	env := make([]string, 0, len(os.Environ())+len(extra))
	for _, kv := range os.Environ() {
		name, _, _ := strings.Cut(kv, "=")
		if strings.HasPrefix(name, "SOLU_HARNESS_") || strings.HasPrefix(name, "SOLU_PANE_") || strings.HasPrefix(name, "DSH_") {
			continue
		}
		env = append(env, kv)
	}
	return append(env, extra...)
}

func TestDeepSeekComposerRepaintsInPlace(t *testing.T) {
	node, err := resolveNodeBin()
	if err != nil || node == "" {
		t.Skipf("node unavailable: %v", err)
	}
	dir := t.TempDir()
	runtime := filepath.Join(dir, "fake.mjs")
	if err := os.WriteFile(runtime, []byte(composerFakeRuntime), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("harness_assets", "deepseek-terminal.mjs"), runtime, "deepseek-flash")
	cmd.Env = harnessClientEnv("DSH_HOME="+dir, "DEEPSEEK_API_KEY=synthetic-key", "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	const cols, rows = 60, 12
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		t.Fatal(err)
	}

	var mu sync.Mutex
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
	waitFor := func(needle string, d time.Duration) {
		t.Helper()
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if strings.Contains(snapshot(), needle) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q in client output", needle)
	}

	waitFor("▸ ", 20*time.Second)
	// Enough output to scroll the pane so the composer is drawn at the very
	// bottom — the condition that exposed the stale saved row.
	_, _ = ptmx.WriteString("/help\r")
	waitFor("and elicitation are not exposed.", 20*time.Second)
	time.Sleep(250 * time.Millisecond)
	for _, ch := range "hello" {
		if _, err := ptmx.WriteString(string(ch)); err != nil {
			t.Fatalf("write %q: %v", ch, err)
		}
		time.Sleep(120 * time.Millisecond)
	}
	time.Sleep(350 * time.Millisecond)
	// Kill the child so the slave closes and the reader sees EOF. Closing the
	// master here instead would NOT unblock a read() already parked on it.
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = ptmx.Close()

	screen := newVTScreen(cols, rows)
	screen.feed(snapshot())
	var composers []string
	for _, line := range screen.lines() {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			composers = append(composers, line)
		}
	}
	if len(composers) != 1 {
		t.Fatalf("composer painted %d rows, want 1:\n%s\n--- screen ---\n%s",
			len(composers), strings.Join(composers, "\n"), screen.dump())
	}
	if got := strings.TrimSpace(composers[0]); got != "▸ hello" {
		t.Fatalf("composer row = %q, want %q", got, "▸ hello")
	}
}

// newVTScreen is a deliberately small VT: enough of CR/LF, scrolling,
// save/restore, ED and CUP/cursor movement to replay what this client writes.
type vtScreen struct {
	cols, rows     int
	grid           [][]rune
	r, c           int
	wrap           bool
	savedR, savedC int
}

func newVTScreen(cols, rows int) *vtScreen {
	grid := make([][]rune, rows)
	for i := range grid {
		grid[i] = blankVTRow(cols)
	}
	return &vtScreen{cols: cols, rows: rows, grid: grid}
}

func blankVTRow(cols int) []rune {
	row := make([]rune, cols)
	for i := range row {
		row[i] = ' '
	}
	return row
}

func (s *vtScreen) scroll() {
	copy(s.grid, s.grid[1:])
	s.grid[s.rows-1] = blankVTRow(s.cols)
}

func (s *vtScreen) nl() {
	s.r++
	if s.r >= s.rows {
		s.r = s.rows - 1
		s.scroll()
	}
}

func (s *vtScreen) put(ch rune) {
	if s.wrap {
		s.nl()
		s.c = 0
		s.wrap = false
	}
	s.grid[s.r][s.c] = ch
	if s.c == s.cols-1 {
		s.wrap = true
	} else {
		s.c++
	}
}

func (s *vtScreen) applyCSI(params string, final rune) {
	nums := []int{}
	for _, part := range strings.Split(params, ";") {
		n := 0
		ok := part != ""
		for _, ch := range part {
			if ch < '0' || ch > '9' {
				ok = false
				break
			}
			n = n*10 + int(ch-'0')
		}
		if ok {
			nums = append(nums, n)
		}
	}
	arg := func(i, def int) int {
		if i < len(nums) && nums[i] != 0 {
			return nums[i]
		}
		return def
	}
	switch final {
	case 'A':
		s.r = max(0, s.r-arg(0, 1))
	case 'B':
		s.r = min(s.rows-1, s.r+arg(0, 1))
	case 'C':
		s.c = min(s.cols-1, s.c+arg(0, 1))
	case 'D':
		s.c = max(0, s.c-arg(0, 1))
	case 'H', 'f':
		s.r = min(s.rows-1, arg(0, 1)-1)
		s.c = min(s.cols-1, arg(1, 1)-1)
	case 'J':
		switch arg(0, 0) {
		case 2:
			for i := range s.grid {
				s.grid[i] = blankVTRow(s.cols)
			}
		case 0:
			for cc := s.c; cc < s.cols; cc++ {
				s.grid[s.r][cc] = ' '
			}
			for rr := s.r + 1; rr < s.rows; rr++ {
				s.grid[rr] = blankVTRow(s.cols)
			}
		}
	case 'K':
		switch arg(0, 0) {
		case 0:
			for cc := s.c; cc < s.cols; cc++ {
				s.grid[s.r][cc] = ' '
			}
		case 2:
			s.grid[s.r] = blankVTRow(s.cols)
		}
	}
	s.wrap = false
}

func (s *vtScreen) feed(data string) {
	r := []rune(data)
	for i := 0; i < len(r); i++ {
		ch := r[i]
		if ch == 0x1b {
			// OSC (ESC ]) — string-terminated by BEL or ST; consumed whole so a
			// client control report never lands on screen. xterm.js does the same.
			if i+1 < len(r) && r[i+1] == ']' {
				j := i + 2
				for j < len(r) && r[j] != 0x07 && !(r[j] == 0x1b && j+1 < len(r) && r[j+1] == '\\') {
					j++
				}
				if j < len(r) {
					if r[j] == 0x1b {
						j++
					}
					i = j
					continue
				}
				return
			}
			if i+1 < len(r) && r[i+1] == '[' {
				j := i + 2
				for j < len(r) && !(r[j] >= '@' && r[j] <= '~') {
					j++
				}
				if j < len(r) {
					s.applyCSI(string(r[i+2:j]), r[j])
					i = j
					continue
				}
				return
			}
			if i+1 < len(r) && (r[i+1] == '7' || r[i+1] == '8') {
				if r[i+1] == '7' {
					s.savedR, s.savedC = s.r, s.c
				} else {
					s.r, s.c = s.savedR, s.savedC
				}
				i++
				continue
			}
			i++
			continue
		}
		switch ch {
		case '\r':
			s.c = 0
			s.wrap = false
		case '\n':
			s.nl()
			s.c = 0
			s.wrap = false
		case '\b':
			s.c = max(0, s.c-1)
		case '\t':
			s.c = min(s.cols-1, (s.c/8+1)*8)
		default:
			if ch >= ' ' && ch != 0x7f {
				s.put(ch)
			}
		}
	}
}

func (s *vtScreen) lines() []string {
	out := make([]string, s.rows)
	for i, row := range s.grid {
		out[i] = strings.TrimRight(string(row), " ")
	}
	return out
}

func (s *vtScreen) dump() string {
	return strings.Join(s.lines(), "\n")
}

// /clear must wipe the visible screen and the scrollback and then let the
// composer repaint at home. A stale frame or a composer parked on top of old
// output is the failure mode this locks down.
func TestDeepSeekClearWipesScreenAndRepositionsComposer(t *testing.T) {
	node, err := resolveNodeBin()
	if err != nil || node == "" {
		t.Skipf("node unavailable: %v", err)
	}
	dir := t.TempDir()
	runtime := filepath.Join(dir, "fake.mjs")
	if err := os.WriteFile(runtime, []byte(composerFakeRuntime), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("harness_assets", "deepseek-terminal.mjs"), runtime, "deepseek-flash")
	cmd.Env = harnessClientEnv("DSH_HOME="+dir, "DEEPSEEK_API_KEY=synthetic-key", "TERM=xterm-256color")
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	defer func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	}()
	const cols, rows = 60, 12
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: rows, Cols: cols}); err != nil {
		t.Fatal(err)
	}
	var mu sync.Mutex
	var out strings.Builder
	done := make(chan struct{})
	go func() {
		defer close(done)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				mu.Lock()
				out.Write(buf[:n])
				mu.Unlock()
			}
			if rerr != nil {
				return
			}
		}
	}()
	snapshot := func() string {
		mu.Lock()
		defer mu.Unlock()
		return out.String()
	}
	waitFor := func(needle string, d time.Duration) {
		t.Helper()
		deadline := time.Now().Add(d)
		for time.Now().Before(deadline) {
			if strings.Contains(snapshot(), needle) {
				return
			}
			time.Sleep(20 * time.Millisecond)
		}
		t.Fatalf("timed out waiting for %q in client output", needle)
	}

	waitFor("▸ ", 20*time.Second)
	_, _ = ptmx.WriteString("/help\r")
	waitFor("and elicitation are not exposed.", 20*time.Second)
	time.Sleep(200 * time.Millisecond)
	_, _ = ptmx.WriteString("/clear\r")
	time.Sleep(300 * time.Millisecond)
	_, _ = ptmx.WriteString("hi")
	time.Sleep(300 * time.Millisecond)
	_ = cmd.Process.Kill()
	select {
	case <-done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = ptmx.Close()

	screen := newVTScreen(cols, rows)
	screen.feed(snapshot())
	dump := screen.dump()
	for _, stale := range []string{"and elicitation are not exposed.", "quit (also /quit)", "DeepSeek Harness"} {
		if strings.Contains(dump, stale) {
			t.Fatalf("/clear left %q on screen:\n%s", stale, dump)
		}
	}
	var composers []string
	rules := 0
	for _, line := range screen.lines() {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			composers = append(composers, line)
		}
		if line != "" && strings.Trim(line, "─") == "" {
			rules++
		}
	}
	if rules != 2 {
		t.Fatalf("composer has %d rule rows, want 2 (bracketed frame):\n%s", rules, dump)
	}
	if len(composers) != 1 {
		t.Fatalf("composer painted %d rows, want 1:\n%s", len(composers), dump)
	}
	if got := strings.TrimSpace(composers[0]); got != "▸ hi" {
		t.Fatalf("composer row = %q, want %q\n%s", got, "▸ hi", dump)
	}
}

// The composer is the single always-on bottom UI: while a turn runs it stays on
// screen (bracketed frame + input row) with the spinner in its status row, so a
// steering draft is visible instead of hidden behind a separate live line.
func TestDeepSeekComposerStaysVisibleDuringTurn(t *testing.T) {
	const cols, rows = 70, 14
	const slowRuntime = `import readline from 'node:readline';
const input=readline.createInterface({input:process.stdin});
const send=x=>process.stdout.write(JSON.stringify({jsonrpc:'2.0',...x})+'\n');
input.on('line',line=>{const m=JSON.parse(line);
if(m.method==='initialize')send({id:m.id,result:{protocolVersion:1,agentCapabilities:{promptCapabilities:{image:false}}}});
else if(m.method==='session/new')send({id:m.id,result:{sessionId:'busy',configOptions:[{id:'model',category:'model',currentValue:'deepseek-flash',options:[{value:'deepseek-flash',name:'Ready Flash'}]}]}});
else if(m.method==='session/prompt'){const id=m.id;setTimeout(()=>send({id,result:{stopReason:'end_turn'}}),3000);}
else if(m.method==='session/close')send({id:m.id,result:{}});
});input.on('close',()=>process.exit(0));`
	p := startHarnessPTYWith(t, cols, rows, slowRuntime, nil, 0)
	// rawInit paints a composer while session/new is still in flight. Waiting
	// only for the prompt can therefore submit into the boot-time busy state and
	// queue "go" instead of starting the turn on a loaded verifier. The model
	// label arrives with session/new and is painted only by the ready prompt.
	p.waitFor(t, "Ready Flash", 20*time.Second)
	if _, err := p.ptmx.WriteString("go\r"); err != nil {
		t.Fatal(err)
	}
	// The fake holds the turn for 3s; type a steering draft while it runs.
	time.Sleep(400 * time.Millisecond)
	if _, err := p.ptmx.WriteString("draft text"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(500 * time.Millisecond)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = p.ptmx.Close()

	screen := newVTScreen(cols, rows)
	screen.feed(p.snapshot())
	dump := screen.dump()
	rules, drafts, spinner := 0, 0, false
	for _, line := range screen.lines() {
		if line != "" && strings.Trim(line, "─") == "" {
			rules++
		}
		if strings.Contains(line, "draft text") {
			drafts++
		}
		if strings.Contains(line, "Ctrl+C cancels") {
			spinner = true
		}
	}
	if rules != 2 {
		t.Fatalf("mid-turn composer frame = %d rule rows, want 2 (always-on bracket):\n%s", rules, dump)
	}
	if drafts == 0 {
		t.Fatalf("mid-turn draft is not on the composer input row:\n%s", dump)
	}
	if !spinner {
		t.Fatalf("mid-turn status row is missing the live indicator:\n%s", dump)
	}
}

// fakeHarnessPTY runs the real client against composerFakeRuntime on a PTY and
// exposes the raw byte stream, so a test can assert on the exact escapes.
type fakeHarnessPTY struct {
	cmd  *exec.Cmd
	ptmx *os.File
	dir  string
	mu   sync.Mutex
	out  strings.Builder
	done chan struct{}
	// respondCols can change mid-test: a pane that narrows answers the cursor
	// probe with its new width. Guarded because the reader goroutine reads it
	// while the test writes it.
	respondMu   sync.Mutex
	respondCols int
}

func (p *fakeHarnessPTY) setRespondCols(cols int) {
	p.respondMu.Lock()
	p.respondCols = cols
	p.respondMu.Unlock()
}

func (p *fakeHarnessPTY) getRespondCols() int {
	p.respondMu.Lock()
	defer p.respondMu.Unlock()
	return p.respondCols
}

func startFakeHarnessPTY(t *testing.T, cols, rows int) *fakeHarnessPTY {
	return startHarnessPTYWith(t, cols, rows, composerFakeRuntime, nil, 0)
}

// respondCols > 0 makes the harness answer the client's cursor-position probe
// (ESC[6n) with that column, standing in for a terminal whose real width differs
// from the PTY winsize.
func startHarnessPTYWith(t *testing.T, cols, rows int, runtimeSrc string, extraEnv []string, respondCols int) *fakeHarnessPTY {
	t.Helper()
	node, err := resolveNodeBin()
	if err != nil || node == "" {
		t.Skipf("node unavailable: %v", err)
	}
	dir := t.TempDir()
	runtime := filepath.Join(dir, "fake.mjs")
	if err := os.WriteFile(runtime, []byte(runtimeSrc), 0o600); err != nil {
		t.Fatal(err)
	}
	cmd := exec.Command(node, filepath.Join("harness_assets", "deepseek-terminal.mjs"), runtime, "deepseek-flash")
	cmd.Env = harnessClientEnv(append([]string{"DSH_HOME=" + dir, "DEEPSEEK_API_KEY=synthetic-key", "TERM=xterm-256color"}, extraEnv...)...)
	ptmx, err := pty.Start(cmd)
	if err != nil {
		t.Skipf("pty unavailable: %v", err)
	}
	p := &fakeHarnessPTY{cmd: cmd, ptmx: ptmx, dir: dir, done: make(chan struct{}), respondCols: respondCols}
	if err := pty.Setsize(ptmx, &pty.Winsize{Rows: uint16(rows), Cols: uint16(cols)}); err != nil {
		t.Fatal(err)
	}
	go func() {
		defer close(p.done)
		buf := make([]byte, 32*1024)
		for {
			n, rerr := ptmx.Read(buf)
			if n > 0 {
				chunk := buf[:n]
				p.mu.Lock()
				p.out.Write(chunk)
				p.mu.Unlock()
				if rc := p.getRespondCols(); rc > 0 {
					// Prefer the non-moving text-area report (CSI 18 t), which is
					// what a current xterm.js answers after Pane.tsx enables
					// windowOptions.getWinSizeChars. Keep the legacy CPR response
					// too so the one-shot fallback path stays covered.
					if bytes.Contains(chunk, []byte("\x1b[18t")) {
						_, _ = ptmx.WriteString(fmt.Sprintf("\x1b[8;%d;%dt", rows, rc))
					}
					if bytes.Contains(chunk, []byte("\x1b[6n")) {
						_, _ = ptmx.WriteString(fmt.Sprintf("\x1b[1;%dR", rc))
					}
				}
			}
			if rerr != nil {
				return
			}
		}
	}()
	t.Cleanup(func() {
		_ = ptmx.Close()
		_ = cmd.Process.Kill()
		_, _ = cmd.Process.Wait()
	})
	return p
}

func (p *fakeHarnessPTY) snapshot() string {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.out.String()
}

func (p *fakeHarnessPTY) waitFor(t *testing.T, needle string, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		if strings.Contains(p.snapshot(), needle) {
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatalf("timed out waiting for %q in client output:\n%s", needle, p.snapshot())
}

// Narrowing a pane reflows the composer's rule (drawn at the old width) into
// more rows. The next erase must walk back over the reflowed height, or the
// stale top of the old frame is left behind. Asserted on the raw escape, not a
// screen dump: our emulator is a fixed grid and does not model reflow.
func TestDeepSeekComposerRecomputesRowDistanceAfterResize(t *testing.T) {
	p := startFakeHarnessPTY(t, 60, 12)
	p.waitFor(t, "▸ ", 20*time.Second)
	if _, err := p.ptmx.WriteString("hi"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	mark := len(p.snapshot())
	if err := pty.Setsize(p.ptmx, &pty.Winsize{Rows: 12, Cols: 40}); err != nil {
		t.Fatal(err)
	}
	time.Sleep(600 * time.Millisecond)
	after := p.snapshot()[mark:]
	// Frame at width 60 has one row above the input cell: the 60-char rule.
	// At width 40 that rule reflows to two rows, so the erase moves up 2.
	if !strings.Contains(after, "\r\x1b[2A\x1b[J") {
		t.Fatalf("narrowing did not recompute the reflowed row distance (want \\r\\x1b[2A\\x1b[J):\n%q", after)
	}
	if !strings.Contains(after, strings.Repeat("─", 40)) {
		t.Fatalf("client did not repaint the rule at the new width:\n%q", after)
	}
}

// While the "/" list is up, ↓ moves the selection and Tab accepts it. Tab on
// its own completes the first hit (/new), so completing the second (/sessions)
// only happens when navigation worked.
func TestDeepSeekSuggestionListNavigatesWithArrows(t *testing.T) {
	const cols, rows = 60, 12
	p := startFakeHarnessPTY(t, cols, rows)
	p.waitFor(t, "▸ ", 20*time.Second)
	if _, err := p.ptmx.WriteString("/"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := p.ptmx.WriteString("\x1b[B"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(150 * time.Millisecond)
	if _, err := p.ptmx.WriteString("\t"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = p.ptmx.Close()

	screen := newVTScreen(cols, rows)
	screen.feed(p.snapshot())
	var composers []string
	for _, line := range screen.lines() {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			composers = append(composers, line)
		}
	}
	got := ""
	if len(composers) == 1 {
		got = strings.TrimSpace(composers[0])
	}
	if len(composers) != 1 || got != "▸ /sessions" {
		t.Fatalf("composer rows = %v, want one '▸ /sessions':\n%s", composers, screen.dump())
	}
}

// A slow turn: records each prompt it receives, then answers after a delay.
const busyFakeRuntime = `import readline from 'node:readline';
import fs from 'node:fs';
const input=readline.createInterface({input:process.stdin});let promptId;
const send=x=>process.stdout.write(JSON.stringify({jsonrpc:'2.0',...x})+'\n');
input.on('line',line=>{const m=JSON.parse(line);
if(m.method==='initialize')send({id:m.id,result:{protocolVersion:1,agentCapabilities:{promptCapabilities:{image:false}}}});
else if(m.method==='session/new')send({id:m.id,result:{sessionId:'s',configOptions:[{id:'model',category:'model',currentValue:'deepseek-flash',options:[{value:'deepseek-flash',name:'DeepSeek V4.1 Flash'}]}]}});
else if(m.method==='session/prompt'){promptId=m.id;
fs.appendFileSync(process.env.PROMPT_LOG, (m.params.prompt||[]).map(p=>p.text).join('')+'\n');
setTimeout(()=>{send({method:'session/update',params:{sessionId:'s',update:{sessionUpdate:'agent_message_chunk',content:{type:'text',text:'TURN_DONE'}}}});send({id:promptId,result:{stopReason:'end_turn'}});},700);}
else if(m.method==='session/close')send({id:m.id,result:{}});
});input.on('close',()=>process.exit(0));`

// A message typed and Entered while a turn is running is queued, not silently
// echoed and dropped: the queued line is visible, the runtime receives the
// original prompt first, and the queued message is delivered only after the
// running turn settles. That is Pi's steering order; the old behavior kept the
// draft in the composer and never sent it.
func TestDeepSeekBusyInputQueuesWithHint(t *testing.T) {
	dir := t.TempDir()
	logPath := filepath.Join(dir, "prompts.log")
	p := startHarnessPTYWith(t, 60, 12, busyFakeRuntime, []string{"PROMPT_LOG=" + logPath}, 0)
	p.waitFor(t, "▸ ", 20*time.Second)
	if _, err := p.ptmx.WriteString("go\r"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := p.ptmx.WriteString("draft"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(200 * time.Millisecond)
	if _, err := p.ptmx.WriteString("\r"); err != nil {
		t.Fatal(err)
	}
	p.waitFor(t, "queued (1)", 10*time.Second)
	p.waitFor(t, "TURN_DONE", 10*time.Second)
	// Give the post-turn drain a moment to hand the queued message over.
	time.Sleep(500 * time.Millisecond)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = p.ptmx.Close()

	out := p.snapshot()
	if !strings.Contains(out, "queued (1)") {
		t.Fatalf("no visible hint that the busy message was queued:\n%q", out)
	}
	log, err := os.ReadFile(logPath)
	if err != nil {
		t.Fatal(err)
	}
	logLines := strings.Split(strings.TrimSpace(string(log)), "\n")
	if len(logLines) == 0 || logLines[0] != "go" {
		t.Fatalf("runtime received %q, want the original prompt first", log)
	}
	for _, line := range logLines[1:] {
		if line != "draft" {
			t.Fatalf("runtime received unexpected prompt %q after the first turn", line)
		}
	}
}

// Leaves one tool call in progress when the turn settles — the running-work
// signal the close guard keys on.
const idleTaskFakeRuntime = `import readline from 'node:readline';
const input=readline.createInterface({input:process.stdin});let promptId;
const send=x=>process.stdout.write(JSON.stringify({jsonrpc:'2.0',...x})+'\n');
input.on('line',line=>{const m=JSON.parse(line);
if(m.method==='initialize')send({id:m.id,result:{protocolVersion:1,agentCapabilities:{promptCapabilities:{image:false}}}});
else if(m.method==='session/new')send({id:m.id,result:{sessionId:'s',configOptions:[{id:'model',category:'model',currentValue:'deepseek-flash',options:[{value:'deepseek-flash',name:'DeepSeek V4.1 Flash'}]}]}});
else if(m.method==='session/prompt'){promptId=m.id;
send({method:'session/update',params:{sessionId:'s',update:{sessionUpdate:'tool_call',toolCallId:'t1',title:'Bash',kind:'execute',status:'in_progress',rawInput:{command:'x',background:true}}}});
send({method:'session/update',params:{sessionId:'s',update:{sessionUpdate:'tool_call_update',toolCallId:'t1',status:'completed',content:[{type:'content',content:{type:'text',text:'started background job bash-9'}}]}}});
send({id:promptId,result:{stopReason:'end_turn'}});}
else if(m.method==='session/close')send({id:m.id,result:{}});
});input.on('close',()=>process.exit(0));`

// Ctrl+C must not close the session while a task is still running: the first
// press warns, the second confirms.
func TestDeepSeekCtrlCWarnsBeforeClosingWithRunningTask(t *testing.T) {
	p := startHarnessPTYWith(t, 60, 12, idleTaskFakeRuntime, nil, 0)
	p.waitFor(t, "▸ ", 20*time.Second)
	if _, err := p.ptmx.WriteString("go\r"); err != nil {
		t.Fatal(err)
	}
	p.waitFor(t, "end_turn", 10*time.Second)
	time.Sleep(250 * time.Millisecond)
	if _, err := p.ptmx.WriteString("\x03"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(300 * time.Millisecond)
	if !strings.Contains(p.snapshot(), "Ctrl+C again") {
		t.Fatalf("first Ctrl+C did not warn about the running task:\n%q", p.snapshot())
	}
	// The pane's close dialog learns about the same task over OSC 7658.
	if !strings.Contains(p.snapshot(), "\x1b]7658;SoluWork=1\x07") {
		t.Fatalf("client did not report running work to the pane:\n%q", p.snapshot())
	}
	select {
	case <-p.done:
		t.Fatal("client closed on the first Ctrl+C despite a running task")
	default:
	}
	if _, err := p.ptmx.WriteString("\x03"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(6 * time.Second):
		t.Fatal("second Ctrl+C did not close the client")
	}
	_ = p.ptmx.Close()
}

// When the PTY winsize and the terminal's real width disagree — the desync that
// produced the stale-rule "staircase" — the composer must be sized from the
// width the terminal reports, not the PTY. Here the PTY says 100 columns while
// the fake terminal answers the cursor probe with 60.
func TestDeepSeekComposerUsesTerminalMeasuredWidth(t *testing.T) {
	const screenCols, ptyCols, rows = 60, 100, 24
	p := startHarnessPTYWith(t, ptyCols, rows, composerFakeRuntime, nil, screenCols)
	p.waitFor(t, "▸ ", 20*time.Second)
	time.Sleep(400 * time.Millisecond) // let the probe answer land and redraw
	if _, err := p.ptmx.WriteString("hi"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = p.ptmx.Close()

	screen := newVTScreen(screenCols, rows)
	raw := p.snapshot()
	// The client also tells the pane's web client about the disagreement so it
	// can re-assert its measured width (OSC 7658).
	if !strings.Contains(raw, "\x1b]7658;SoluWidth=100;60\x07") {
		t.Fatalf("client did not report the width mismatch to the pane:\n%q", raw)
	}
	// The measurement must come from the non-moving text-area report (CSI 18 t),
	// not the old caret-walking probe — and because the fake terminal answers
	// 18 t, the one-shot fallback must never fire.
	if !strings.Contains(raw, "\x1b[18t") {
		t.Fatalf("client did not ask for the width with the non-moving CSI 18 t report:\n%q", raw)
	}
	if strings.Contains(raw, "\x1b[9999C") {
		t.Fatalf("client moved the caret to the right margin (the flashing-indicator bug):\n%q", raw)
	}
	screen.feed(raw)
	dump := screen.dump()
	rules := 0
	var composers []string
	for _, line := range screen.lines() {
		if line != "" && strings.Trim(line, "─") == "" {
			rules++
		}
		if strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			composers = append(composers, line)
		}
	}
	if rules != 2 {
		t.Fatalf("composer drew %d rule rows at the terminal width, want 2 (bracketed frame):\n%s", rules, dump)
	}
	got := ""
	if len(composers) == 1 {
		got = strings.TrimSpace(composers[0])
	}
	if len(composers) != 1 || got != "▸ hi" {
		t.Fatalf("composer rows = %v, want one '▸ hi':\n%s", composers, dump)
	}
}

// Enter with the "/" suggestion list up accepts the highlighted command instead
// of submitting a half-typed one — "/ses" would otherwise print "Unknown
// command" and lose the input.
func TestDeepSeekEnterAcceptsHighlightedSuggestion(t *testing.T) {
	const cols, rows = 60, 12
	p := startHarnessPTYWith(t, cols, rows, composerFakeRuntime, nil, 0)
	p.waitFor(t, "▸ ", 20*time.Second)
	if _, err := p.ptmx.WriteString("/ses"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(250 * time.Millisecond)
	if _, err := p.ptmx.WriteString("\r"); err != nil {
		t.Fatal(err)
	}
	time.Sleep(400 * time.Millisecond)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = p.ptmx.Close()

	screen := newVTScreen(cols, rows)
	screen.feed(p.snapshot())
	dump := screen.dump()
	if strings.Contains(dump, "Unknown command") {
		t.Fatalf("Enter submitted the partial command instead of accepting it:\n%s", dump)
	}
	lines := screen.lines()
	// The input row now sits between the frame's top and bottom rules, so find
	// the `▸` row directly rather than looking below the last rule.
	composer := ""
	for _, line := range lines {
		if strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			composer = strings.TrimSpace(line)
			break
		}
	}
	if composer != "▸ /sessions" {
		t.Fatalf("composer = %q, want '▸ /sessions':\n%s", composer, dump)
	}
}

// A prompt that contains the API key must not survive in the on-disk input
// history (which returns on up-arrow), matching the transcript's redaction.
func TestDeepSeekHistoryRedactsAPIKey(t *testing.T) {
	// The PTY helper launches the client with DEEPSEEK_API_KEY=synthetic-key, so
	// that literal is what safe() must replace.
	p := startHarnessPTYWith(t, 60, 12, composerFakeRuntime, nil, 0)
	p.waitFor(t, "▸ ", 20*time.Second)
	if _, err := p.ptmx.WriteString("my key is synthetic-key\r"); err != nil {
		t.Fatal(err)
	}
	p.waitFor(t, "filler output", 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	if _, err := p.ptmx.WriteString("/exit\r"); err != nil {
		t.Fatal(err)
	}
	select {
	case <-p.done:
	case <-time.After(10 * time.Second):
		t.Fatal("pty reader did not finish after /exit")
	}
	b, err := os.ReadFile(filepath.Join(p.dir, "terminal-history"))
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(b), "synthetic-key") {
		t.Fatalf("history leaked the API key: %q", b)
	}
	if !strings.Contains(string(b), "[redacted]") {
		t.Fatalf("history did not record the redacted prompt: %q", b)
	}
}

var ruleRunRe = regexp.MustCompile("─+")

// A stale cursor-position probe must not make the composer draw wider than the
// PTY. The pane is 60 cols but the probe (mis)reports 100; before the width fix
// every rule and status line drew at 100 and wrapped into rows the erase math
// did not account for, leaving the "staircase" of stale rules above the input.
func TestDeepSeekComposerNeverDrawsWiderThanPTY(t *testing.T) {
	const cols = 60
	p := startHarnessPTYWith(t, cols, 12, composerFakeRuntime, nil, 100)
	p.waitFor(t, "▸ ", 20*time.Second)
	time.Sleep(300 * time.Millisecond)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = p.ptmx.Close()

	longest := 0
	for _, run := range ruleRunRe.FindAllString(p.snapshot(), -1) {
		if n := len([]rune(run)); n > longest {
			longest = n
		}
	}
	if longest == 0 {
		t.Fatal("no composer rule was drawn")
	}
	if longest > cols {
		t.Fatalf("composer drew a %d-col rule into a %d-col PTY (wrap/staircase)", longest, cols)
	}
}

// A pane that narrows without a PTY SIGWINCH (a non-owner viewer, or a resize
// the daemon declines to apply) answers the cursor probe with its new width but
// never signals the client. Before the on-input re-probe the client kept the
// old, wider measurement: each keystroke's rule hard-wrapped in the narrower
// terminal and the erase walked back over only the last frame, so stale rules
// piled up as a staircase while the user typed. The client must re-measure from
// input and repaint at the real width.
func TestDeepSeekComposerRecoversFromStaleWidthWithoutSigwinch(t *testing.T) {
	const ptyCols, screenCols, rows = 100, 60, 24
	// The first probe answers the original, now-stale width; the fake pane starts
	// answering the new width only after the test flips it below.
	p := startHarnessPTYWith(t, ptyCols, rows, composerFakeRuntime, nil, ptyCols)
	p.waitFor(t, "▸ ", 20*time.Second)
	time.Sleep(300 * time.Millisecond) // let the stale measurement land

	// The pane narrowed to 60: the PTY winch is NOT changed and no SIGWINCH is
	// delivered, exactly the desync the client cannot see from a signal.
	p.setRespondCols(screenCols)
	for _, ch := range "hello world" {
		if _, err := p.ptmx.WriteString(string(ch)); err != nil {
			t.Fatal(err)
		}
		time.Sleep(60 * time.Millisecond)
	}
	time.Sleep(500 * time.Millisecond)
	_ = p.cmd.Process.Kill()
	select {
	case <-p.done:
	case <-time.After(5 * time.Second):
		t.Fatal("pty reader did not finish after the client was killed")
	}
	_ = p.ptmx.Close()

	screen := newVTScreen(screenCols, rows)
	screen.feed(p.snapshot())
	rules := 0
	var composers []string
	for _, line := range screen.lines() {
		if line != "" && strings.Trim(line, "─") == "" {
			rules++
		}
		if strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			composers = append(composers, strings.TrimSpace(line))
		}
	}
	if rules != 2 {
		t.Fatalf("stale-width repaint left %d rule rows, want 2 (bracketed frame, no staircase):\n%s", rules, screen.dump())
	}
	if len(composers) != 1 || composers[0] != "▸ hello world" {
		t.Fatalf("composer rows = %v, want one '▸ hello world':\n%s", composers, screen.dump())
	}
}
