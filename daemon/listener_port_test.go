package main

import (
	"net"
	"testing"
)

type translatedPortListener struct {
	net.Listener
	port   int
	closed bool
}

func (ln *translatedPortListener) Addr() net.Addr {
	return &net.TCPAddr{IP: net.IPv4zero, Port: ln.port}
}

func (ln *translatedPortListener) Close() error {
	ln.closed = true
	return nil
}

func TestConfiguredListenerRejectsTranslatedPort(t *testing.T) {
	ln := &translatedPortListener{port: 43217}
	got, err := retainConfiguredPort(ln, "0.0.0.0:8787")
	if err == nil || got != nil || !ln.closed {
		t.Fatalf("translated port accepted: listener=%v error=%v closed=%v", got, err, ln.closed)
	}
}

func TestConfiguredListenerRetainsExactPortAndExplicitEphemeralPort(t *testing.T) {
	for _, addr := range []string{"127.0.0.1:48887", "0.0.0.0:0"} {
		ln := &translatedPortListener{port: 48887}
		got, err := retainConfiguredPort(ln, addr)
		if err != nil || got != ln || ln.closed {
			t.Fatalf("valid port %s rejected: %v", addr, err)
		}
	}
}

func TestConfiguredListenerBindsFixedPort(t *testing.T) {
	// Rebind a port assigned by the host, avoiding assumptions about which
	// fixed ports Windows has reserved or forwarded on the actual test host.
	reservation, err := net.Listen("tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	addr := reservation.Addr().String()
	_ = reservation.Close()
	ln, err := listenOnConfiguredPort("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close()
	if ln.Addr().String() != addr {
		t.Fatalf("requested %s, bound %s", addr, ln.Addr())
	}
	connection, err := net.Dial("tcp4", addr)
	if err != nil {
		t.Fatal(err)
	}
	_ = connection.Close()
}

func TestTranslatedPortCannotAcknowledgeUpdateSuccessor(t *testing.T) {
	t.Setenv("SOLU_SUCCESSOR_OF_UPDATE", "1")
	translated := &translatedPortListener{port: 43217}
	got, err := startupHandoffListenerWithDeps("tcp4", "0.0.0.0:8787", startupHandoffDeps{
		preflight: func(network, addr string) (net.Listener, error) {
			return preflightUpdateSuccessorListenerWithListen(network, addr, func(string, string) (net.Listener, error) {
				return retainConfiguredPort(translated, addr)
			})
		},
		writeMarker:  func() { t.Fatal("wrong port acknowledged to predecessor") },
		clearPending: func() { t.Fatal("wrong port cleared pending update") },
		acquire: func(string, string) (net.Listener, error) {
			t.Fatal("wrong port retried after failed preflight")
			return nil, nil
		},
	})
	if err == nil || got != nil || !translated.closed {
		t.Fatalf("translated successor accepted: listener=%v error=%v closed=%v", got, err, translated.closed)
	}
}
