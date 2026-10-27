package main

import (
	"io"
	"net"
	"net/http"
	"strconv"
	"testing"
	"time"
)

func TestDaemonListenerNetwork(t *testing.T) {
	for _, tc := range []struct {
		name string
		addr string
		wsl  bool
		want string
	}{
		{"wsl wildcard IPv4", "0.0.0.0:8787", true, "tcp4"},
		{"wsl loopback IPv4", "127.0.0.1:8787", true, "tcp4"},
		{"wsl LAN IPv4", "192.168.1.2:8787", true, "tcp4"},
		{"ordinary wildcard", "0.0.0.0:8787", false, "tcp"},
		{"ordinary loopback", "127.0.0.1:8787", false, "tcp"},
		{"IPv6 wildcard", "[::]:8787", true, "tcp"},
		{"IPv6 loopback", "[::1]:8787", true, "tcp"},
		{"mapped IPv6", "[::ffff:127.0.0.1]:8787", true, "tcp"},
		{"hostname", "localhost:8787", true, "tcp"},
		{"unspecified host", ":8787", true, "tcp"},
		{"invalid address", "invalid", true, "tcp"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if got := daemonListenerNetwork(tc.addr, tc.wsl); got != tc.want {
				t.Fatalf("network = %q, want %q", got, tc.want)
			}
		})
	}
}

func TestDaemonWSLWildcardAcceptsIPv4Loopback(t *testing.T) {
	const addr = "0.0.0.0:0"
	ln, err := net.Listen(daemonListenerNetwork(addr, true), addr)
	if err != nil {
		t.Fatal(err)
	}
	server := &http.Server{Handler: http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = io.WriteString(w, "ready")
	})}
	defer server.Close()
	go func() { _ = server.Serve(ln) }()
	port := ln.Addr().(*net.TCPAddr).Port
	url := "http://" + net.JoinHostPort("127.0.0.1", strconv.Itoa(port)) + "/"
	client := &http.Client{Timeout: 3 * time.Second, Transport: &http.Transport{Proxy: nil}}
	defer client.CloseIdleConnections()
	response, err := client.Get(url)
	if err != nil {
		t.Fatalf("IPv4 loopback cannot reach the daemon listener: %v", err)
	}
	defer response.Body.Close()
	body, err := io.ReadAll(response.Body)
	if err != nil || response.StatusCode != http.StatusOK || string(body) != "ready" {
		t.Fatalf("readiness response: status=%d body=%q err=%v", response.StatusCode, body, err)
	}
}
