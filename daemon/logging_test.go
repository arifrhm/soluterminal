package main

import (
	"bytes"
	"log"
	"log/slog"
	"strings"
	"testing"
)

func TestStdlibLogBridgeRoutesThroughSlog(t *testing.T) {
	var buf bytes.Buffer
	prev := slog.Default()
	slog.SetDefault(slog.New(slog.NewTextHandler(&buf, &slog.HandlerOptions{Level: slog.LevelInfo})))
	defer slog.SetDefault(prev)

	prevFlags := log.Flags()
	prevWriter := log.Writer()
	log.SetFlags(0)
	log.SetOutput(slogStdlibBridge{})
	defer func() {
		log.SetFlags(prevFlags)
		log.SetOutput(prevWriter)
	}()

	log.Printf("hello %d %s", 42, "world")

	out := buf.String()
	if !strings.Contains(out, "msg=") {
		t.Fatalf("bridged stdlib log line missing slog msg field: %q", out)
	}
	if !strings.Contains(out, "hello 42 world") {
		t.Fatalf("bridged line missing payload: %q", out)
	}
	if !strings.Contains(out, "level=INFO") {
		t.Fatalf("bridged line missing level=INFO: %q", out)
	}
}

func TestParseLogLevelDefaults(t *testing.T) {
	cases := map[string]slog.Level{
		"":        slog.LevelInfo,
		"info":    slog.LevelInfo,
		"DEBUG":   slog.LevelDebug,
		"warn":    slog.LevelWarn,
		"warning": slog.LevelWarn,
		"error":   slog.LevelError,
		"err":     slog.LevelError,
		"garbage": slog.LevelInfo,
	}
	for in, want := range cases {
		if got := parseLogLevel(in); got != want {
			t.Errorf("parseLogLevel(%q) = %v, want %v", in, got, want)
		}
	}
}
