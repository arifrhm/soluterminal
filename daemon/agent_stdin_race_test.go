package main

import (
	"bytes"
	"io"
	"os"
	"sync"
	"testing"
)

// TestWriteChildLocked_NoInterleave is the regression for the "native pane Session
// ended (exit code 1) on the first message after a daemon update" bug. During an
// auto-update swap the predecessor and successor daemon both hold a live
// connection to the SAME agent for ~600ms, and the agent multiplexes them onto one
// child stdin pipe. A native pane's stdin is a real os.Pipe, where a write larger
// than PIPE_BUF (4096B on Linux) is split into several write(2) calls — so two
// concurrent unsynchronised writers can interleave bytes mid-line, producing a
// corrupt NDJSON line that makes `claude -p --input-format stream-json` self-exit
// code 1. writeChildLocked serialises each frame's payload so every line lands
// whole. This test feeds many >PIPE_BUF lines concurrently and asserts not a single
// byte interleaved.
func TestWriteChildLocked_NoInterleave(t *testing.T) {
	t.Parallel()
	r, w, err := os.Pipe()
	if err != nil {
		t.Fatalf("os.Pipe: %v", err)
	}
	cio := &pipeIO{r: r, w: w}
	var mu sync.Mutex

	const writers = 8
	const lineLen = 8000 // > PIPE_BUF (4096) so a single write is non-atomic
	const repeats = 40   // each writer sends its line this many times

	// Drain the read end concurrently so writers never block on a full pipe.
	got := make(chan []byte, 1)
	go func() {
		data, _ := io.ReadAll(r)
		got <- data
	}()

	var wg sync.WaitGroup
	for i := 0; i < writers; i++ {
		ch := byte('A' + i) // a distinct fill byte per writer
		line := append(bytes.Repeat([]byte{ch}, lineLen), '\n')
		wg.Add(1)
		go func(payload []byte) {
			defer wg.Done()
			for j := 0; j < repeats; j++ {
				if err := writeChildLocked(&mu, cio, payload); err != nil {
					t.Errorf("writeChildLocked: %v", err)
					return
				}
			}
		}(line)
	}
	wg.Wait()
	_ = w.Close() // EOF for io.ReadAll
	data := <-got

	// Every line must be exactly one writer's fill byte repeated lineLen times.
	// An interleave would mix two fill bytes into one line (or split a line),
	// which fails the all-same-byte check or the expected length.
	lines := bytes.Split(data, []byte{'\n'})
	if n := len(lines) - 1; n != writers*repeats { // trailing element after final '\n'
		t.Fatalf("line count = %d, want %d (interleave split/merged lines)", n, writers*repeats)
	}
	if last := lines[len(lines)-1]; len(last) != 0 {
		t.Fatalf("trailing fragment after final newline: %q (a write was torn)", last)
	}
	counts := map[byte]int{}
	for i, ln := range lines[:len(lines)-1] {
		if len(ln) != lineLen {
			t.Fatalf("line %d length = %d, want %d (interleaved/torn write)", i, len(ln), lineLen)
		}
		ch := ln[0]
		if ch < 'A' || ch >= 'A'+writers {
			t.Fatalf("line %d starts with unexpected byte %q", i, ch)
		}
		if !bytes.Equal(ln, bytes.Repeat([]byte{ch}, lineLen)) {
			t.Fatalf("line %d is not a single fill byte — bytes from two writers interleaved", i)
		}
		counts[ch]++
	}
	for i := 0; i < writers; i++ {
		if c := counts[byte('A'+i)]; c != repeats {
			t.Fatalf("writer %q produced %d intact lines, want %d", byte('A'+i), c, repeats)
		}
	}
}
