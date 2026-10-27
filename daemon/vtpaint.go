package main

import (
	"bytes"
	"fmt"
	"strconv"
	"strings"

	"github.com/hinshun/vt10x"

	"soluterminal/grid"
)

// vt10x's attr bit positions (private in upstream — re-declared
// locally). Order MUST match state.go's const block: Reverse,
// Underline, Bold, Gfx, Italic, Blink.
const (
	vtAttrReverse = 1 << iota
	vtAttrUnderline
	vtAttrBold
	vtAttrGfx
	vtAttrItalic
	vtAttrBlink
)

// vt10x's default-color sentinel values. Colors 1<<24 and above are
// reserved markers for "use the terminal's default" — they must not
// be emitted as SGR color codes; let them map to SGR reset instead.
const (
	vtDefaultFG vt10x.Color = 1<<24 + 0
	vtDefaultBG vt10x.Color = 1<<24 + 1
)

// serializeVT walks the VT's cell grid and emits a self-contained
// byte stream that, when written to a fresh alt-screen xterm, will
// render exactly what the VT currently shows. Used to override the
// raw-byte ringbuf replay on reattach and to repair the xterm state
// after a live resize — in both cases we can't trust Codex/Ratatui's
// diff-only output to fully paint every cell, so we paint from our
// own authoritative model of the terminal state.
//
// Emits:
//
//  1. \x1b[?1049h — enter alt-screen (if the app itself is in alt-
//     screen; caller decides whether to include this).
//  2. \x1b[2J\x1b[H — clear + home so every cell starts at default.
//  3. For each row: cursor-position, then cell-by-cell writes with
//     SGR state tracking. We reset SGR and re-emit from scratch when
//     any of fg / bg / attrs change, which is cheap for typical
//     Ratatui output and foolproof.
//  4. Final cursor position matching the VT's current cursor.
//
// vt10x's Lock/Unlock guards consistent reads across the whole
// grid — we hold it for the duration.
func serializeVT(vt vt10x.Terminal, includeAltScreenEnter bool) []byte {
	vt.Lock()
	defer vt.Unlock()

	cols, rows := vt.Size()
	if cols <= 0 || rows <= 0 {
		return nil
	}

	var buf bytes.Buffer
	if includeAltScreenEnter {
		buf.WriteString("\x1b[?1049h")
	}
	buf.WriteString("\x1b[2J\x1b[H")

	// SGR state we've emitted so far; -2 forces the first cell to
	// emit a full SGR reset+set (any Mode value is >= 0, so -2 never
	// matches a real glyph).
	var curFG, curBG vt10x.Color = vtDefaultFG - 9999, vtDefaultBG - 9999
	var curMode int16 = -2

	resetAndSetSGR := func(g vt10x.Glyph) {
		if g.FG == curFG && g.BG == curBG && g.Mode == curMode {
			return
		}
		buf.WriteString("\x1b[0m")
		if g.Mode&vtAttrBold != 0 {
			buf.WriteString("\x1b[1m")
		}
		if g.Mode&vtAttrItalic != 0 {
			buf.WriteString("\x1b[3m")
		}
		if g.Mode&vtAttrUnderline != 0 {
			buf.WriteString("\x1b[4m")
		}
		if g.Mode&vtAttrBlink != 0 {
			buf.WriteString("\x1b[5m")
		}
		if g.Mode&vtAttrReverse != 0 {
			buf.WriteString("\x1b[7m")
		}
		writeColor(&buf, g.FG, true)
		writeColor(&buf, g.BG, false)
		curFG, curBG, curMode = g.FG, g.BG, g.Mode
	}

	for y := 0; y < rows; y++ {
		buf.WriteString("\x1b[")
		buf.WriteString(strconv.Itoa(y + 1))
		buf.WriteString(";1H")
		for x := 0; x < cols; x++ {
			g := vt.Cell(x, y)
			resetAndSetSGR(g)
			ch := g.Char
			if ch == 0 {
				ch = ' '
			}
			buf.WriteRune(ch)
		}
	}
	buf.WriteString("\x1b[0m")

	cur := vt.Cursor()
	if cur.Y >= 0 && cur.X >= 0 {
		buf.WriteString("\x1b[")
		buf.WriteString(strconv.Itoa(cur.Y + 1))
		buf.WriteString(";")
		buf.WriteString(strconv.Itoa(cur.X + 1))
		buf.WriteString("H")
	}
	return buf.Bytes()
}

// writeColor emits the SGR color code for `c`. `foreground` true
// picks the 3x/9x/38;5;... codes, false picks 4x/10x/48;5;...
//
// vt10x stores colors as: [0,16) = ANSI (0-7 standard, 8-15 bright),
// [16,256) = xterm 256-color palette, and 1<<24+ = default-color
// sentinels. Truecolor is not supported by this fork. The default
// sentinels get no SGR emitted (we already reset above so default
// applies); truecolor would fall into the 1<<24 range and also get
// skipped, rendering in the terminal default — imperfect but keeps
// the paint self-consistent.
func writeColor(buf *bytes.Buffer, c vt10x.Color, foreground bool) {
	if c >= 1<<24 {
		return // default — \x1b[0m above already covers this
	}
	if c < 8 {
		base := 30
		if !foreground {
			base = 40
		}
		buf.WriteString("\x1b[")
		buf.WriteString(strconv.Itoa(base + int(c)))
		buf.WriteString("m")
		return
	}
	if c < 16 {
		base := 90
		if !foreground {
			base = 100
		}
		buf.WriteString("\x1b[")
		buf.WriteString(strconv.Itoa(base + int(c) - 8))
		buf.WriteString("m")
		return
	}
	if c < 256 {
		prefix := "\x1b[38;5;"
		if !foreground {
			prefix = "\x1b[48;5;"
		}
		buf.WriteString(prefix)
		buf.WriteString(strconv.Itoa(int(c)))
		buf.WriteString("m")
		return
	}
	// Out of palette range; let default apply.
	_ = fmt.Sprintf // keep fmt import defensibly
}

func replayLooksBlank(raw []byte) bool {
	return strings.TrimSpace(stripANSI(raw)) == ""
}

func gridSnapshotHasText(s grid.Snapshot) bool {
	for _, c := range s.Cells {
		if strings.TrimSpace(c.Content) != "" {
			return true
		}
	}
	return false
}

func appendGridSnapshotPaint(raw []byte, snap grid.Snapshot) []byte {
	if !gridSnapshotHasText(snap) {
		return raw
	}
	paint := serializeGridSnapshot(snap, -1)
	if len(paint) == 0 {
		return raw
	}
	out := make([]byte, 0, len(raw)+len(paint))
	out = append(out, raw...)
	out = append(out, paint...)
	return out
}

// serializeGridSnapshot emits a self-contained normal-buffer paint from the
// charmbracelet/x/vt snapshot used by replayEmu. It is used after raw replay
// for Codex normal-buffer panes: raw bytes keep scrollback/history, then this
// paint repairs the current screen when Ratatui's diff-heavy byte stream does
// not fully reconstruct visible cells from a fresh xterm.
//
// skipRow (-1 = none) excludes one row from the paint, leaving whatever
// the raw replay tail drew there. Used for the TUI's cursor row on
// non-Codex panes: the grid model accumulates drift in cells the TUI
// believes are unchanged and therefore never re-emits — the wellness-
// pane phantom prompt lived in the model's input row for weeks
// (v0.0.510 diagnosis, re-seeded from the agent's grid on every daemon
// restart). The TUI's own recent frame bytes in the raw tail are the
// most authoritative source for the live input row, so the model must
// not overwrite it. When a row is skipped, the leading 2J clear is
// omitted too (it would blank the preserved row); every other cell is
// painted explicitly, so the clear is redundant there.
func serializeGridSnapshot(s grid.Snapshot, skipRow int) []byte {
	return serializeGridSnapshotClamped(s, skipRow, 0, 0)
}

// serializeGridSnapshotClamped is serializeGridSnapshot bounded to the
// SUBSCRIBER's geometry. maxCols/maxRows <= 0 mean "no clamp" (paint the
// full model grid — the desktop/legacy path). When a mobile subscriber
// reattaches, the live replayEmu is still at the departed owner's wide cols
// (the PTY hasn't been resized to the phone yet — recomputeSize only fires
// for the owner, and for normal-buffer panes the cols-1 bump is NOT run; see
// pane_subscribe.go). Emitting CUP for the wide grid into a narrow xterm
// overflows it. Clamping each row to the subscriber's cols and dropping rows
// beyond its height keeps every CUP coordinate inside the client grid; the
// client's post-attach Ctrl+L + real resize SIGWINCH then converge the
// screen to the exact reflowed frame.
func serializeGridSnapshotClamped(s grid.Snapshot, skipRow, maxCols, maxRows int) []byte {
	return serializeGridSnapshotAt(s, skipRow, maxCols, maxRows, 0, 0)
}

// serializeGridSnapshotAt is serializeGridSnapshotClamped with the grid
// shifted rowOffset rows down the client's screen (negative = up). The reattach
// paint uses it when the client's height differs from the model grid: the raw
// tail ahead of the paint leaves the client's cursor at a different row than
// the model's, and a top-anchored paint then left the client's extra bottom
// rows holding a second copy of the last lines. Rows shifted above the top
// are not painted.
// clientRows > 0 also drops rows that would land below the client's last
// row: xterm clamps such CUPs onto its bottom row, so painting them overwrote
// the prompt there with the model's lower lines.
func serializeGridSnapshotAt(s grid.Snapshot, skipRow, maxCols, maxRows, rowOffset, clientRows int) []byte {
	if s.Cols <= 0 || s.Rows <= 0 || len(s.Cells) == 0 {
		return nil
	}
	cols := s.Cols
	if maxCols > 0 && maxCols < cols {
		cols = maxCols
	}
	rows := s.Rows
	if maxRows > 0 && maxRows < rows {
		rows = maxRows
	}
	// If the cursor row was clamped out of the painted grid, fall back to a
	// full 2J clear — skipRow's no-clear branch would otherwise leave wide
	// raw-replay residue on the phone's actual rows.
	if maxRows > 0 && skipRow >= rows {
		skipRow = -1
	}

	var buf bytes.Buffer
	// Make the repaint independent from parser state left by raw replay.
	// Codex normal-buffer rendering uses synchronized output and scroll
	// regions; if either is still active, a clear/home paint can be clipped
	// or delayed and the repaired screen may remain blank.
	if skipRow >= 0 {
		buf.WriteString("\x1b[?2026l\x1b[r\x1b[?6l\x1b[0m\x1b[H")
	} else {
		buf.WriteString("\x1b[?2026l\x1b[r\x1b[?6l\x1b[0m\x1b[2J\x1b[H")
	}
	var curFG, curBG string
	var curAttrs uint16 = ^uint16(0)

	setSGR := func(c grid.SnapshotCell) {
		if c.FG == curFG && c.BG == curBG && c.Attrs == curAttrs {
			return
		}
		buf.WriteString("\x1b[0m")
		if c.Attrs&grid.AttrBold != 0 {
			buf.WriteString("\x1b[1m")
		}
		if c.Attrs&grid.AttrFaint != 0 {
			buf.WriteString("\x1b[2m")
		}
		if c.Attrs&grid.AttrItalic != 0 {
			buf.WriteString("\x1b[3m")
		}
		if c.Attrs&grid.AttrUnderline != 0 {
			buf.WriteString("\x1b[4m")
		}
		if c.Attrs&grid.AttrBlink != 0 {
			buf.WriteString("\x1b[5m")
		}
		if c.Attrs&grid.AttrReverse != 0 {
			buf.WriteString("\x1b[7m")
		}
		if c.Attrs&grid.AttrStrikethrough != 0 {
			buf.WriteString("\x1b[9m")
		}
		writeHexColor(&buf, c.FG, true)
		writeHexColor(&buf, c.BG, false)
		curFG, curBG, curAttrs = c.FG, c.BG, c.Attrs
	}

	for y := 0; y < rows; y++ {
		if y == skipRow {
			continue
		}
		target := y + 1 + rowOffset
		if target < 1 || (clientRows > 0 && target > clientRows) {
			continue
		}
		buf.WriteString("\x1b[")
		buf.WriteString(strconv.Itoa(target))
		buf.WriteString(";1H")
		for x := 0; x < cols; x++ {
			idx := y*s.Cols + x
			if idx >= len(s.Cells) {
				break
			}
			c := s.Cells[idx]
			setSGR(c)
			text := c.Content
			if text == "" {
				text = " "
			}
			buf.WriteString(text)
		}
	}
	buf.WriteString("\x1b[0m")
	if s.CursorX >= 0 && s.CursorY >= 0 {
		cy := s.CursorY
		if cy >= rows {
			cy = rows - 1
		}
		cx := s.CursorX
		if cx >= cols {
			cx = cols - 1
		}
		cy += rowOffset
		if cy < 0 {
			cy = 0
		}
		if clientRows > 0 && cy > clientRows-1 {
			cy = clientRows - 1
		}
		buf.WriteString("\x1b[")
		buf.WriteString(strconv.Itoa(cy + 1))
		buf.WriteString(";")
		buf.WriteString(strconv.Itoa(cx + 1))
		buf.WriteString("H")
	}
	return buf.Bytes()
}

func writeHexColor(buf *bytes.Buffer, hex string, foreground bool) {
	if len(hex) != 7 || hex[0] != '#' {
		return
	}
	if code, ok := ansiHexSGR(hex, foreground); ok {
		buf.WriteString(code)
		return
	}
	r, okR := parseHexByte(hex[1:3])
	g, okG := parseHexByte(hex[3:5])
	b, okB := parseHexByte(hex[5:7])
	if !okR || !okG || !okB {
		return
	}
	prefix := "\x1b[38;2;"
	if !foreground {
		prefix = "\x1b[48;2;"
	}
	buf.WriteString(prefix)
	buf.WriteString(strconv.Itoa(r))
	buf.WriteByte(';')
	buf.WriteString(strconv.Itoa(g))
	buf.WriteByte(';')
	buf.WriteString(strconv.Itoa(b))
	buf.WriteByte('m')
}

func ansiHexSGR(hex string, foreground bool) (string, bool) {
	switch strings.ToLower(hex) {
	case "#000000":
		return sgrColorCode(0, foreground), true
	case "#800000":
		return sgrColorCode(1, foreground), true
	case "#008000":
		return sgrColorCode(2, foreground), true
	case "#808000":
		return sgrColorCode(3, foreground), true
	case "#000080":
		return sgrColorCode(4, foreground), true
	case "#800080":
		return sgrColorCode(5, foreground), true
	case "#008080":
		return sgrColorCode(6, foreground), true
	case "#c0c0c0":
		return sgrColorCode(7, foreground), true
	case "#808080":
		return sgrColorCode(8, foreground), true
	case "#ff0000":
		return sgrColorCode(9, foreground), true
	case "#00ff00":
		return sgrColorCode(10, foreground), true
	case "#ffff00":
		return sgrColorCode(11, foreground), true
	case "#0000ff":
		return sgrColorCode(12, foreground), true
	case "#ff00ff":
		return sgrColorCode(13, foreground), true
	case "#00ffff":
		return sgrColorCode(14, foreground), true
	case "#ffffff":
		return sgrColorCode(15, foreground), true
	default:
		return "", false
	}
}

func sgrColorCode(idx int, foreground bool) string {
	if idx < 8 {
		base := 30
		if !foreground {
			base = 40
		}
		return "\x1b[" + strconv.Itoa(base+idx) + "m"
	}
	base := 90
	if !foreground {
		base = 100
	}
	return "\x1b[" + strconv.Itoa(base+idx-8) + "m"
}

func parseHexByte(s string) (int, bool) {
	if len(s) != 2 {
		return 0, false
	}
	hi, ok := fromHex(s[0])
	if !ok {
		return 0, false
	}
	lo, ok := fromHex(s[1])
	if !ok {
		return 0, false
	}
	return hi*16 + lo, true
}

func fromHex(c byte) (int, bool) {
	switch {
	case c >= '0' && c <= '9':
		return int(c - '0'), true
	case c >= 'a' && c <= 'f':
		return int(c-'a') + 10, true
	case c >= 'A' && c <= 'F':
		return int(c-'A') + 10, true
	default:
		return 0, false
	}
}
