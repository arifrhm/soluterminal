package main

import "os"

// Updater retains a small rolling ring of previous binaries so the
// operator has more than one rollback target. Layout, with `current`
// being the running binary path:
//
//	current        — the just-installed version
//	current.old    — the version we replaced one update ago
//	current.old.1  — the version we replaced two updates ago
//	current.old.2  — the version we replaced three updates ago
//
// On every swapBinary, we shift the ring one slot older before parking
// the outgoing binary at .old. On every RestorePrevious, we promote
// .old back to current and shift the ring one slot newer so a chained
// restore can keep walking back through history.
//
// Three slots is enough to recover from a short string of bad
// releases without wasting much disk (each slot is whatever the
// daemon binary weighs — currently ~14MB).
const oldRingDepth = 3

// rotateOldRingForward drops the oldest .old.N and shifts every
// remaining slot one step older to make room for a fresh .old. Called
// before parking the outgoing binary in swapBinary. Best-effort: a
// rename failure inside the ring (e.g., a Windows AV briefly holding
// .old.1 open) doesn't fail the update — we just lose that slot from
// the rollback history. The caller still has a workable .old after
// the swap completes.
func rotateOldRingForward(currentPath string) {
	// Drop the slot that's about to fall off the end.
	_ = os.Remove(ringSlot(currentPath, oldRingDepth-1))
	// Shift each remaining slot one older. Walk from oldest-still-kept
	// down to newest so we never overwrite a slot we still need to read.
	for i := oldRingDepth - 1; i >= 1; i-- {
		src := ringSlot(currentPath, i-1)
		dst := ringSlot(currentPath, i)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		_ = os.Remove(dst)
		_ = os.Rename(src, dst)
	}
}

// rotateOldRingBackward is the reverse: after RestorePrevious has
// consumed .old (promoted it to current), pull every other slot one
// step newer so a chained restore goes back further. Best-effort.
func rotateOldRingBackward(currentPath string) {
	for i := 1; i < oldRingDepth; i++ {
		src := ringSlot(currentPath, i)
		dst := ringSlot(currentPath, i-1)
		if _, err := os.Stat(src); err != nil {
			continue
		}
		_ = os.Remove(dst)
		_ = os.Rename(src, dst)
	}
	// The newest slot is now empty (or holds whatever was there before
	// — but rotateOldRingBackward is only called from RestorePrevious,
	// and there's no fresher binary to put in slot 0; that's already
	// the running one). Nothing to do for slot oldRingDepth-1.
}

// ringSlot returns the on-disk name for slot `i` in the ring.
//
//	i=0 → "<current>.old"      (most recent rollback target)
//	i=1 → "<current>.old.1"
//	i=2 → "<current>.old.2"
func ringSlot(currentPath string, i int) string {
	if i == 0 {
		return currentPath + ".old"
	}
	return currentPath + ".old." + itoa(i)
}

// Tiny inline itoa — avoids pulling strconv into this file just for
// single-digit numbers (the ring depth is bounded at oldRingDepth).
func itoa(n int) string {
	if n < 0 || n > 9 {
		// Fallback: this shouldn't happen given oldRingDepth, but if
		// someone ever bumps the ring past 9 we'd want a real strconv
		// import. Until then, single-digit fast path.
		return "?"
	}
	return string(rune('0' + n))
}
