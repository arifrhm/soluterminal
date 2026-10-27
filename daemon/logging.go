package main

import (
	"context"
	"io"
	"log"
	"log/slog"
	"os"
	"strings"
)

// initLogging configures the daemon's logging stack. Two independent
// knobs:
//
//	LOG_FORMAT = "text" (default) | "json"
//	  Picks the slog handler. JSON is what you want when piping into
//	  a log aggregator; text is what you want when reading the
//	  terminal yourself.
//
//	LOG_LEVEL = "debug" | "info" (default) | "warn" | "error"
//	  Filters records below the chosen level out at the handler. Note
//	  that stdlib log.Printf calls bridged in below all enter at
//	  INFO — if you raise LOG_LEVEL above INFO, you'll lose them.
//
// The stdlib `log` package is bridged through slog so existing
// log.Printf / log.Fatalf call sites inherit the chosen format
// without each one needing to be migrated by hand. As call sites
// move to slog directly they pick up structured fields; until then
// they still emit, just consistently.
func initLogging() {
	slog.SetDefault(slog.New(buildLogHandler(os.Stderr)))

	// Bridge stdlib log. log.Printf used to write directly to stderr
	// with its own date/time prefix; redirecting through slog puts
	// every call site under the same handler. log.Fatalf still calls
	// os.Exit(1) after Output, so it keeps fatal semantics.
	log.SetFlags(0)
	log.SetOutput(slogStdlibBridge{})
}

// buildLogHandler constructs the daemon's slog handler over w, applying the
// LOG_FORMAT / LOG_LEVEL knobs documented on initLogging and wrapping the
// result so every record is also captured (redacted) in the in-memory log
// ring before reaching w. The stdlib log bridge funnels through
// slog.Default() too, so it lands in the ring as well.
//
// Factored out of initLogging so attachDurableLog can rebuild the same
// handler over a MultiWriter (stderr + durable file) without the two paths
// drifting on format, level, or ring capture.
func buildLogHandler(w io.Writer) slog.Handler {
	opts := &slog.HandlerOptions{Level: parseLogLevel(os.Getenv("LOG_LEVEL"))}
	var handler slog.Handler
	switch strings.ToLower(strings.TrimSpace(os.Getenv("LOG_FORMAT"))) {
	case "json":
		handler = slog.NewJSONHandler(w, opts)
	default:
		handler = slog.NewTextHandler(w, opts)
	}
	return &ringHandler{inner: handler, ring: globalLogRing}
}

// parseLogLevel mirrors slog's level naming. Anything unknown falls
// back to Info — the goal is "the daemon always boots cleanly", not
// "config typos halt startup".
func parseLogLevel(s string) slog.Level {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "debug":
		return slog.LevelDebug
	case "warn", "warning":
		return slog.LevelWarn
	case "error", "err":
		return slog.LevelError
	default:
		return slog.LevelInfo
	}
}

// slogStdlibBridge is an io.Writer that takes whatever stdlib log
// writes and re-emits it through slog.Default() at INFO.
type slogStdlibBridge struct{}

func (slogStdlibBridge) Write(p []byte) (int, error) {
	msg := strings.TrimRight(string(p), "\n")
	if msg != "" {
		slog.Default().Log(context.Background(), slog.LevelInfo, msg)
	}
	return len(p), nil
}
