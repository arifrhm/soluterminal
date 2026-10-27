//go:build !windows

package main

import (
	"bufio"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"os/exec"
	"strconv"
	"strings"
	"testing"
	"time"
)

func TestPortIPv6AddressDecoding(t *testing.T) {
	for raw, want := range map[string]string{
		"00000000000000000000000001000000:1451": "::1",
		"0000000000000000FFFF00000100007F:1451": "127.0.0.1",
		"00000000000000000000000000000000:1451": "::",
		"0100007F:1451":                         "127.0.0.1",
	} {
		addr, port := parseHexAddr(raw)
		if addr != want || port != 5201 {
			t.Errorf("%s -> %s:%d want %s:5201", raw, addr, port, want)
		}
	}
}

func TestPortListenerChildProcess(t *testing.T) {
	if os.Getenv("SOLU_PORT_REVIEW_CHILD") == "" {
		return
	}
	var listener net.Listener
	var err error
	if os.Getenv("SOLU_PORT_REVIEW_SHARED_FD") == "1" {
		file := os.NewFile(3, "shared-listener")
		listener, err = net.FileListener(file)
		file.Close()
	} else {
		listener, err = net.Listen("tcp", "127.0.0.1:"+os.Getenv("SOLU_PORT_REVIEW_PORT"))
	}
	if err != nil {
		os.Exit(2)
	}
	fmt.Println(listener.Addr().(*net.TCPAddr).Port)
	http.Serve(listener, http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) { w.WriteHeader(200) }))
	os.Exit(0)
}

func TestPortScannerSharedSocketWorkerReloadKeepsPreview(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("Linux /proc required")
	}
	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer listener.Close()
	port := listener.Addr().(*net.TCPAddr).Port
	file, err := listener.(*net.TCPListener).File()
	if err != nil {
		t.Fatal(err)
	}
	defer file.Close()
	start := func() *exec.Cmd {
		cmd := exec.Command(os.Args[0], "-test.run=^TestPortListenerChildProcess$")
		cmd.Env = append(os.Environ(), "SOLU_PORT_REVIEW_CHILD=1", "SOLU_PORT_REVIEW_SHARED_FD=1")
		cmd.ExtraFiles = []*os.File{file}
		out, err := cmd.StdoutPipe()
		if err != nil {
			t.Fatal(err)
		}
		if err = cmd.Start(); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
		if _, err = bufio.NewReader(out).ReadString('\n'); err != nil {
			t.Fatal(err)
		}
		return cmd
	}
	first, second := start(), start()
	listener.Close()
	file.Close() // Only the two child processes now hold it.
	pane := testPaneWithTail(t, "reload-pane", "Reload server", "ready")
	pane.Pid = os.Getpid()
	pr := NewPortRegistry(testManagerWithPane(pane))
	events := make(chan Port, 2)
	pr.onPaneServerUp = func(p Port) { events <- p }
	// Earlier tests may have populated the one-second process-tree cache
	// before these workers existed. Discovery intentionally tolerates that
	// attribution lag; this fixture needs both children in its first scan.
	snapshotProcessTreeMaxAge(0)
	pr.scan()
	key := fmt.Sprintf("tcp|127.0.0.1|%d", port)
	old := pr.seen[key]
	if old == nil || old.WebStatus != "ready" {
		t.Fatalf("initial shared service missing or unready: %+v", old)
	}
	selector := portSelector(old)
	transport := pr.proxyTransport(old)
	winner := first
	if old.Pid == second.Process.Pid {
		winner = second
	}
	winner.Process.Kill()
	winner.Wait()
	pr.scan()
	current := pr.seen[key]
	if current == nil || current.Pid == old.Pid || current.identity != old.identity {
		t.Fatalf("shared socket was not retained after worker exit: old=%+v current=%+v", old, current)
	}
	if portSelector(current) != selector || !current.FirstAt.Equal(old.FirstAt) || current.probeAttempts != old.probeAttempts || current.WebStatus != "ready" {
		t.Fatalf("worker reload reset preview/readiness: old=%+v current=%+v", old, current)
	}
	if pr.proxyTransport(current) != transport {
		t.Fatal("worker reload discarded the socket connection pool")
	}
	w := httptest.NewRecorder()
	(&apiServer{ports: pr}).proxyPort(w, httptest.NewRequest(http.MethodGet, fmt.Sprintf("/api/proxy/%d~%s/", port, selector), nil))
	if w.Code != 200 {
		t.Fatalf("existing preview failed after reload: %d %s", w.Code, w.Body.String())
	}
	select {
	case <-events:
		t.Fatal("worker reload emitted another port-up event")
	case <-time.After(20 * time.Millisecond):
	}
}

func startPortListenerChild(t *testing.T, dir string, port int) (*exec.Cmd, int) {
	t.Helper()
	cmd := exec.Command(os.Args[0], "-test.run=^TestPortListenerChildProcess$")
	cmd.Dir = dir
	cmd.Env = append(os.Environ(), "SOLU_PORT_REVIEW_CHILD=1", "SOLU_PORT_REVIEW_PORT="+strconv.Itoa(port))
	out, err := cmd.StdoutPipe()
	if err != nil {
		t.Fatal(err)
	}
	if err = cmd.Start(); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { cmd.Process.Kill(); cmd.Wait() })
	line, err := bufio.NewReader(out).ReadString('\n')
	if err != nil {
		t.Fatal(err)
	}
	n, err := strconv.Atoi(strings.TrimSpace(line))
	if err != nil {
		t.Fatal(err)
	}
	return cmd, n
}

func TestPortScannerReplacementRefreshesMetadata(t *testing.T) {
	if _, err := os.Stat("/proc/net/tcp"); err != nil {
		t.Skip("Linux /proc required")
	}
	firstDir, secondDir := t.TempDir(), t.TempDir()
	first, port := startPortListenerChild(t, firstDir, 0)
	pr := NewPortRegistry(NewManager(""))
	pr.scan()
	key := fmt.Sprintf("tcp|127.0.0.1|%d", port)
	old := pr.seen[key]
	if old == nil {
		t.Fatal("first listener missing")
	}
	oldSelector := portSelector(old)
	first.Process.Kill()
	first.Wait()
	time.Sleep(time.Millisecond)
	second, _ := startPortListenerChild(t, secondDir, port)
	pr.scan()
	got := pr.seen[key]
	if got == nil || got.Pid != second.Process.Pid || got.Cwd != secondDir {
		t.Fatalf("replacement metadata %+v", got)
	}
	if portSelector(got) == oldSelector {
		t.Fatal("replacement retained the old identity")
	}
	if got.WebStatus != "unverified" || got.Scheme != "" {
		t.Fatalf("unowned service was probed automatically: %+v", got)
	}
}
