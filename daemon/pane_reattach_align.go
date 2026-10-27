package main

import "soluterminal/grid"

// reattachPaintRowOffset returns how many rows the reattach grid paint must
// shift so the model's cursor row lands where the raw replay leaves the
// client's cursor. It replays the same bytes into a scratch emulator at the
// client's size, which is what the client's xterm is about to do. Zero when
// the client's height matches the model (the paint already lines up) or when
// there is nothing to measure.
func reattachPaintRowOffset(clientCols, clientRows int, replay []byte, model grid.Snapshot) int {
	if clientCols <= 1 || clientRows <= 0 || clientRows == model.Rows || len(replay) == 0 {
		return 0
	}
	scratch := grid.New(clientCols, clientRows)
	defer func() { _ = scratch.Close() }()
	scratch.Feed(replay)
	return scratch.Snapshot().CursorY - model.CursorY
}
