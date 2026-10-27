package grid

import (
	"encoding/base64"
	"encoding/json"
	"io"
	"os"
	"strings"
	"testing"
)

// Replays a real codex session capture through the emulator, firing
// resize events at the same moments the captured session did. Dumps
// intermediate grid snapshots so we can SEE what the user sees without
// a human in the loop.
//
// Capture lives in the sibling soluterminal-grid/ POC repo (where the
// drive tool was built) at testdata/resize-cycle.cast — two narrow →
// widen cycles against a real booted codex.

const realCodexCastPath = "../../../soluterminal-grid/testdata/resize-cycle.cast"

type castRec struct {
	K string `json:"k"`
	T int64  `json:"t"`
	D string `json:"d,omitempty"`
	C int    `json:"c,omitempty"`
	R int    `json:"r,omitempty"`
}

type castHdr struct {
	V    int `json:"v"`
	Cols int `json:"cols"`
	Rows int `json:"rows"`
}

func TestRealCodexResizeReplay(t *testing.T) {
	t.Parallel()
	f, err := os.Open(realCodexCastPath)
	if err != nil {
		t.Skipf("capture not present at %s: %v", realCodexCastPath, err)
	}
	defer f.Close()

	dec := json.NewDecoder(f)
	var hdr castHdr
	if err := dec.Decode(&hdr); err != nil {
		t.Fatalf("header: %v", err)
	}

	e := New(hdr.Cols, hdr.Rows)
	defer e.Close()

	dumpIdx := 0
	dump := func(label string) {
		dumpIdx++
		s := e.Snapshot()
		var b strings.Builder
		b.WriteString("==== " + label + " (grid " + rowCol(s.Cols, s.Rows) + ") ====\n")
		for y := 0; y < s.Rows; y++ {
			row := rowAt(s, y)
			if strings.TrimSpace(row) == "" {
				continue
			}
			b.WriteString(row + "\n")
		}
		t.Log(b.String())
	}

	for {
		var rec castRec
		if err := dec.Decode(&rec); err != nil {
			if err == io.EOF {
				break
			}
			t.Fatalf("record: %v", err)
		}
		switch rec.K {
		case "out":
			data, err := base64.StdEncoding.DecodeString(rec.D)
			if err != nil {
				continue
			}
			e.Feed(data)
		case "resize":
			// Dump before and after the resize so we can see what
			// happened. This is what ResizeGrid does in production —
			// just em.Resize, no clear, no Ctrl-L.
			dump("BEFORE resize to " + rowCol(rec.C, rec.R))
			e.Resize(rec.C, rec.R)
			dump("AFTER resize to " + rowCol(rec.C, rec.R))
		}
	}

	dump("FINAL")
}
