package relaymux

import (
	"io"
	"testing"
	"time"
)

func TestYamuxConfig(t *testing.T) {
	c := YamuxConfig()
	if c.KeepAliveInterval != 15*time.Second {
		t.Fatalf("KeepAliveInterval = %s, want 15s", c.KeepAliveInterval)
	}
	if c.ConnectionWriteTimeout != 20*time.Second {
		t.Fatalf("ConnectionWriteTimeout = %s, want 20s", c.ConnectionWriteTimeout)
	}
	if c.LogOutput != io.Discard {
		t.Fatalf("LogOutput should discard yamux logs")
	}
}

func TestAddr(t *testing.T) {
	if (Addr{}).Network() != "yamux" {
		t.Fatalf("Network = %q, want yamux", (Addr{}).Network())
	}
	if (Addr{}).String() != "relay" {
		t.Fatalf("String = %q, want relay", (Addr{}).String())
	}
}
