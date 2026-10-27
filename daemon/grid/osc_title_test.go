package grid

import (
	"strings"
	"testing"
)

func TestWindowTitleNeverPaintsUTF8Payload(t *testing.T) {
	for _, title := range []string{"ASCII hidden-title", "✳ hidden-title", "✓ hidden-title", "◐ hidden-title", "你好 hidden-title"} {
		for _, command := range []string{"0", "1", "2", "0002"} {
			for _, end := range []string{"\a", "\x1b\\", "\x9c"} {
				for _, intro := range []string{"\x1b]", "\x9d"} {
					data := []byte(intro + command + ";" + title + end + "X")
					for _, chunked := range []bool{false, true} {
						em := New(80, 24)
						if chunked {
							for _, b := range data {
								em.Feed([]byte{b})
							}
						} else {
							em.Feed(data)
						}
						snap := em.Snapshot()
						em.Close()
						if got := strings.TrimRight(rowAt(snap, 0), " "); got != "X" || snap.CursorX != 1 {
							t.Fatalf("title=%q cmd=%q end=%q intro=%q chunked=%v row=%q cursor=%d", title, command, end, intro, chunked, got, snap.CursorX)
						}
					}
				}
			}
		}
	}
}

func TestWindowTitleDoesNotHideVisibleUTF8OrAbortControls(t *testing.T) {
	for _, text := range []string{"Ý_VISIBLE", "✳_VISIBLE", "✓_VISIBLE"} {
		em := New(80, 24)
		for _, b := range []byte(text) {
			em.Feed([]byte{b})
		}
		snap := em.Snapshot()
		em.Close()
		if got := strings.TrimRight(rowAt(snap, 0), " "); got != text {
			t.Fatalf("visible UTF8: got %q want %q", got, text)
		}
	}
	for _, data := range []string{
		"\x1b]0;hidden\x18X", "\x1b]0;hidden\x1aX",
		"\x1b]0;hidden\x1b[2;3HX",
		"\x1b]0;\xe2\x9cX", // invalid UTF8: standalone ST must not swallow X
	} {
		em := New(80, 24)
		for _, b := range []byte(data) {
			em.Feed([]byte{b})
		}
		snap := em.Snapshot()
		em.Close()
		var all strings.Builder
		for y := 0; y < snap.Rows; y++ {
			all.WriteString(rowAt(snap, y))
		}
		if !strings.Contains(all.String(), "X") || strings.Contains(all.String(), "hidden") {
			t.Fatalf("abort/control %q painted %q", data, all.String())
		}
	}
}

// Sanitized shape of the actual Claude replay: a cursor parked after an empty
// composer, then an OSC title containing the UTF8 continuation byte 0x9c.
// No private replay, account, path or model conversation is committed.
func TestWindowTitleAtClaudeComposerPreservesDraftAndReconnect(t *testing.T) {
	for _, draft := range []string{"", "operator draft"} {
		for _, chunked := range []bool{false, true} {
			data := []byte("Previous response\r\n✻ Brewed for 1s\r\n❯ " + draft + "\r\n status\x1b[3;3H\x1b]0;✳ hidden-window-title\a\x1b]0;✓ hidden-window-title\a")
			for _, reconnect := range []bool{false, true} {
				em := New(120, 35)
				if chunked {
					for _, b := range data {
						em.Feed([]byte{b})
					}
				} else {
					em.Feed(data)
				}
				if reconnect {
					// A fresh replay emulator receives the same bytes, not a
					// carried readiness record from an older pane incarnation.
					em.Close()
					em = New(120, 35)
					em.Feed(data)
				}
				snap := em.Snapshot()
				em.Close()
				if got := strings.TrimRight(rowAt(snap, 2), " "); got != strings.TrimRight("❯ "+draft, " ") || snap.CursorX != 2 || snap.CursorY != 2 {
					t.Fatalf("draft=%q chunked=%v reconnect=%v composer=%q cursor=%d,%d", draft, chunked, reconnect, got, snap.CursorX, snap.CursorY)
				}
			}
		}
	}
}

func TestTitleFilterPreservesOtherOSCAndBoundsScratch(t *testing.T) {
	for _, data := range []string{
		"\x1b]10;?\x1b\\", "\x1b]11;?\a", "\x1b]12;?\x1b\\",
		"\x1b]4;1;?\x1b\\", "\x1b]8;;https://example.com\x1b\\link\x1b]8;;\x1b\\",
		"visible UTF8 ✳ ✓ Ý", "\x1b[31mred\x1b[0m", "\x1b[2;3HX",
	} {
		var f oscTitleFilter
		if got := string(f.filter([]byte(data))); got != data {
			t.Fatalf("non-title bytes changed: got %q want %q", got, data)
		}
	}
	var f oscTitleFilter
	large := strings.Repeat("X", 128*1024)
	if got := string(f.filter([]byte(large))); got != large {
		t.Fatal("large visible frame changed")
	}
	if cap(f.scratch) > 64*1024 {
		t.Fatal("retained oversized scratch")
	}
	if got := string(f.filter([]byte("\x1b]0;" + strings.Repeat("✳", 128*1024) + "\aX"))); got != "\x1b]0;\aX" {
		t.Fatalf("large title leaked: %q", got)
	}
	if cap(f.scratch) > 64*1024 {
		t.Fatal("title retained oversized scratch")
	}
}
