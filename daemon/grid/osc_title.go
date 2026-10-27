package grid

import "unicode/utf8"

// OSC 0/1/2 carry window/icon titles, not display cells. The upstream byte
// parser mistakes UTF-8 continuation 0x9c inside titles such as ✳ for ST,
// leaking the remaining title into the grid. Suppress only title payloads
// before that parser; preserve their headers, terminators and other controls.
// Raw PTY output sent to clients is never changed. Other OSCs remain intact.
type oscTitleFilter struct {
	state       uint8
	command     uint8 // saturates above title commands; no unbounded numeric buffer
	digits      bool
	visibleUTF8 int
	titleUTF8   [utf8.UTFMax]byte
	titleN      int
	scratch     []byte
}

const (
	titleGround uint8 = iota
	titleEscape
	titleCommand
	titlePayload
	titleOtherOSC
)

func (f *oscTitleFilter) filter(p []byte) []byte {
	f.scratch = f.scratch[:0]
	for _, b := range p {
		f.advance(b)
	}
	result := f.scratch
	// Replaying a multi-MiB ring must not permanently retain another full
	// ring-sized allocation in every emulator. Ordinary read frames reuse
	// their small scratch; an exceptional large frame releases it afterward.
	if cap(f.scratch) > 64*1024 {
		f.scratch = nil
	}
	return result
}

func (f *oscTitleFilter) beginOSC() {
	f.state = titleCommand
	f.command = 0
	f.digits = false
	f.visibleUTF8 = 0
}

func utf8ContinuationCount(b byte) int {
	switch {
	case b >= 0xc2 && b <= 0xdf:
		return 1
	case b >= 0xe0 && b <= 0xef:
		return 2
	case b >= 0xf0 && b <= 0xf4:
		return 3
	}
	return 0
}

func (f *oscTitleFilter) advance(b byte) {
	if f.state == titlePayload {
		if f.titleN > 0 {
			f.titleUTF8[f.titleN] = b
			f.titleN++
			pending := f.titleUTF8[:f.titleN]
			if !utf8.FullRune(pending) {
				return
			}
			r, size := utf8.DecodeRune(pending)
			copyPending, n := f.titleUTF8, f.titleN
			f.titleN = 0
			if r == utf8.RuneError && size == 1 {
				// Drop the invalid leading byte, but reconsider subsequent
				// bytes as controls. A real standalone ST must still end OSC.
				for _, tail := range copyPending[1:n] {
					f.advance(tail)
				}
			}
			return
		}
		if utf8ContinuationCount(b) > 0 {
			f.titleUTF8[0] = b
			f.titleN = 1
			return
		}
		switch b {
		case 0x07, 0x9c, 0x18, 0x1a:
			f.scratch = append(f.scratch, b)
			f.state = titleGround
		case 0x1b:
			// ESC aborts OSC; let the following escape execute normally,
			// including ST, cursor moves or RIS rather than swallowing it.
			f.scratch = append(f.scratch, b)
			f.state = titleEscape
		}
		return
	}

	f.scratch = append(f.scratch, b)
	if f.visibleUTF8 > 0 {
		if b >= 0x80 && b <= 0xbf {
			f.visibleUTF8--
			return
		}
		f.visibleUTF8 = 0
	}
	f.visibleUTF8 = utf8ContinuationCount(b)
	switch f.state {
	case titleGround:
		if b == 0x1b {
			f.state = titleEscape
		} else if b == 0x9d {
			f.beginOSC()
		}
	case titleEscape:
		if b == ']' || b == 0x9d {
			f.beginOSC()
		} else if b != 0x1b {
			f.state = titleGround
		}
	case titleCommand:
		switch {
		case b >= '0' && b <= '9':
			f.digits = true
			if f.command <= 2 {
				v := int(f.command)*10 + int(b-'0')
				if v > 2 {
					f.command = 3
				} else {
					f.command = uint8(v)
				}
			}
		case b == ';':
			if f.digits && f.command <= 2 {
				f.state = titlePayload
				f.titleN = 0
			} else {
				f.state = titleOtherOSC
			}
		case b == 0x1b:
			f.state = titleEscape
		case b == 0x07 || b == 0x9c || b == 0x18 || b == 0x1a:
			f.state = titleGround
		default:
			f.command = 3
		}
	case titleOtherOSC:
		if b == 0x1b {
			f.state = titleEscape
		} else if b == 0x07 || b == 0x9c || b == 0x18 || b == 0x1a {
			f.state = titleGround
		}
	}
}
