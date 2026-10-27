package main

import (
	"bytes"
	"math/rand"
	"testing"
)

func TestRingLazyAllocationPreservesReplay(t *testing.T) {
	r := NewRingBuffer(8192)
	if len(r.buf) != 0 {
		t.Fatal("empty ring allocated its replay allowance")
	}
	rng := rand.New(rand.NewSource(42))
	var history []byte
	for i := 0; i < 250; i++ {
		chunk := make([]byte, rng.Intn(10000))
		_, _ = rng.Read(chunk)
		r.Write(chunk)
		history = append(history, chunk...)
		want := history[max(0, len(history)-r.size):]
		if !bytes.Equal(r.Snapshot(), want) || r.TotalWritten() != int64(len(history)) {
			t.Fatalf("replay changed after write %d", i)
		}
		tail := min(len(want), 173)
		offset := int64(len(history) - tail)
		since, ok := r.SnapshotSince(offset)
		if !ok || !bytes.Equal(since, want[len(want)-tail:]) || !bytes.Equal(r.SnapshotTail(tail), since) {
			t.Fatalf("reconnect offset/tail changed after write %d", i)
		}
		if len(history) > r.size {
			if _, ok := r.SnapshotSince(int64(len(history) - r.size - 1)); ok {
				t.Fatal("evicted byte offset accepted")
			}
		}
	}
}

func BenchmarkRingIdlePanes(b *testing.B) {
	for b.Loop() {
		rings := make([]*RingBuffer, 83)
		for i := range rings {
			rings[i] = NewRingBuffer(8 << 20)
			rings[i].Write([]byte("ready\r\n"))
		}
	}
}
