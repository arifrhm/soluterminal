package grid

import (
	"bytes"
	"strings"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func TestLazyParserKeepsGraphemesAcrossInternalWrites(t *testing.T) {
	for _, cluster := range []string{"👩‍💻", "🇸🇬", "é\u0301", "✊🏽"} {
		for offset := -3; offset <= 3; offset++ {
			first := []rune(cluster)[0]
			input := []byte(strings.Repeat("\r", parserFeedBytes-len(string(first))+offset) + cluster + "X")
			e := New(80, 24)
			baseline := vt.NewEmulator(80, 24)
			baseline.Write(input)
			e.Feed(input)
			for x := 0; x < 12; x++ {
				got, want := e.vt.CellAt(x, 0), baseline.CellAt(x, 0)
				if got.Content != want.Content || got.Width != want.Width {
					t.Errorf("cluster %q offset %d cell %d: got %+v want %+v", cluster, offset, x, got, want)
				}
			}
			e.Close()
			baseline.Close()
		}
	}
}

func TestLazyParserPreservesLargeControlStrings(t *testing.T) {
	for _, size := range []int{65537, 200000, parserLimitBytes - 8, parserLimitBytes + 100} {
		e := New(80, 24)
		if e.parser == nil || cap(e.parser.Data()) != parserInitialBytes {
			e.Close()
			t.Fatal("pinned parser accessor did not enable bounded lazy allocation")
		}
		var got []byte
		e.vt.RegisterOscHandler(1337, func(data []byte) bool {
			got = bytes.Clone(data)
			return true
		})
		payload := append([]byte("1337;"), bytes.Repeat([]byte("z"), size)...)
		input := append([]byte("\x1b]"), payload...)
		input = append(input, '\a')
		for len(input) > 0 {
			n := min(len(input), 7919)
			e.Feed(input[:n])
			input = input[n:]
		}
		want := payload[:min(len(payload), parserLimitBytes)]
		if !bytes.Equal(got, want) {
			t.Errorf("size %d: control string changed: got %d bytes, want %d", size, len(got), len(want))
		}
		if cap(e.parser.Data()) > parserLimitBytes {
			t.Fatal("parser exceeded its original memory limit")
		}
		e.Feed([]byte("\x1bc\x1b[?1049h\xe4\xbd\xa0\x1b[?1049lX"))
		if cellChar(e.Snapshot(), 0, 0) != "X" {
			t.Fatal("reset/alternate-screen parsing changed after large control string")
		}
		e.Close()
	}
}

func BenchmarkLazyParserIdlePane(b *testing.B) {
	for b.Loop() {
		e := New(80, 24)
		e.Feed([]byte("ready\r\n"))
		e.Close()
	}
}
