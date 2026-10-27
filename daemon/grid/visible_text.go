package grid

// VisibleText projects the current screen without allocating styled cells.
// It has the same cell/whitespace semantics as Snapshot's plain-text readers.
// Feed and Resize invalidate the cached immutable string under writeMu, so
// polling an unchanged screen allocates nothing and cannot return a torn frame.
func (e *Emulator) VisibleText() (string, bool) {
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if e.w <= 0 || e.h <= 0 {
		return "", false
	}
	if e.textValid {
		return e.visibleText, true
	}
	text := make([]byte, 0, (e.w+1)*e.h)
	for y := 0; y < e.h; y++ {
		start := len(text)
		for x := 0; x < e.w; x++ {
			cell := e.vt.CellAt(x, y)
			if cell == nil || cell.Content == "" {
				text = append(text, ' ')
			} else {
				text = append(text, cell.Content...)
			}
		}
		for len(text) > start && (text[len(text)-1] == ' ' || text[len(text)-1] == '\t') {
			text = text[:len(text)-1]
		}
		text = append(text, '\n')
	}
	e.visibleText = string(text)
	e.textValid = true
	return e.visibleText, true
}
