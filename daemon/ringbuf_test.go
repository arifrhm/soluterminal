package main

import "testing"

func TestSnapshotTail(t *testing.T) {
	t.Parallel()
	cases := []struct {
		name   string
		size   int
		writes []string
		n      int
		want   string
	}{
		{"empty", 16, nil, 8, ""},
		{"fewer than n", 16, []string{"abc"}, 8, "abc"},
		{"exactly n", 16, []string{"abcdefgh"}, 8, "abcdefgh"},
		{"more than n, no wrap", 32, []string{"0123456789"}, 4, "6789"},
		{"after wraparound", 8, []string{"abcdef", "ghij"}, 4, "ghij"},
		{"tail spans wrap seam", 8, []string{"abcde", "fghi"}, 6, "defghi"},
		{"n larger than buffer", 8, []string{"abcdefghijk"}, 100, "defghijk"},
		{"n is zero", 16, []string{"abcdef"}, 0, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := NewRingBuffer(c.size)
			for _, w := range c.writes {
				if _, err := r.Write([]byte(w)); err != nil {
					t.Fatalf("write: %v", err)
				}
			}
			got := string(r.SnapshotTail(c.n))
			if got != c.want {
				t.Fatalf("SnapshotTail(%d) = %q, want %q", c.n, got, c.want)
			}
			// SnapshotTail must agree with the tail of a full Snapshot.
			full := r.Snapshot()
			tail := full
			if c.n < len(tail) {
				tail = tail[len(tail)-c.n:]
			}
			if string(r.SnapshotTail(c.n)) != string(tail) {
				t.Fatalf("SnapshotTail disagrees with Snapshot tail: %q vs %q", got, tail)
			}
		})
	}
}

func TestRingBufferReadFrom(t *testing.T) {
	r := NewRingBuffer(16)
	_, _ = r.Write([]byte("abcdefghij")) // offsets 0..9

	if data, start := r.ReadFrom(2, 4); string(data) != "cdef" || start != 2 {
		t.Fatalf("ReadFrom(2,4) = %q@%d", data, start)
	}
	if data, start := r.ReadFrom(10, 4); len(data) != 0 || start != 10 {
		t.Fatalf("ReadFrom(end) = %q@%d, want empty at 10", data, start)
	}
	if data, start := r.ReadFrom(99, 4); len(data) != 0 || start != 10 {
		t.Fatalf("ReadFrom(future) = %q@%d, want empty at 10", data, start)
	}

	_, _ = r.Write([]byte("KLMNOPQRSTUV")) // total 22; ring keeps offsets 6..21
	if data, start := r.ReadFrom(0, 100); string(data) != "ghijKLMNOPQRSTUV" || start != 6 {
		t.Fatalf("ReadFrom(overwritten) = %q@%d, want resume at oldest 6", data, start)
	}
	if data, start := r.ReadFrom(18, 100); string(data) != "STUV" || start != 18 {
		t.Fatalf("ReadFrom across wrap = %q@%d", data, start)
	}
}
