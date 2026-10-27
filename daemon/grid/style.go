package grid

import (
	"image/color"

	uv "github.com/charmbracelet/ultraviolet"
)

// hexDigits is a nibble->lowercase-hex lookup so styleColor can build the
// "#rrggbb" string with no fmt.Sprintf allocation/parse work. The full grid
// is serialized per frame (two colors per cell), so this is a hot path.
const hexDigits = "0123456789abcdef"

// writeHexByte writes the two lowercase hex digits of b into dst[0:2].
func writeHexByte(dst []byte, b byte) {
	dst[0] = hexDigits[b>>4]
	dst[1] = hexDigits[b&0x0f]
}

// Snapshot-level attr bits. Kept separate from uv.Attr* so the wire format
// stays stable if the underlying lib's bitfield layout ever shifts.
const (
	AttrBold          uint16 = 1 << 0
	AttrFaint         uint16 = 1 << 1
	AttrItalic        uint16 = 1 << 2
	AttrBlink         uint16 = 1 << 3
	AttrReverse       uint16 = 1 << 4
	AttrConceal       uint16 = 1 << 5
	AttrStrikethrough uint16 = 1 << 6
	AttrUnderline     uint16 = 1 << 7
)

// styleColor serializes a color to a stable form. Empty means "default".
func styleColor(c color.Color) string {
	if c == nil {
		return ""
	}
	r, g, b, a := c.RGBA()
	if a == 0 {
		return ""
	}
	// Manual "#rrggbb" writer: byte-identical to the old
	// fmt.Sprintf("#%02x%02x%02x", r>>8, g>>8, b>>8) (lowercase, leading
	// '#') but with no Sprintf format parse per cell. RGBA() returns 16-bit
	// pre-multiplied components; >>8 narrows each to the high byte.
	var buf [7]byte
	buf[0] = '#'
	writeHexByte(buf[1:3], byte(r>>8))
	writeHexByte(buf[3:5], byte(g>>8))
	writeHexByte(buf[5:7], byte(b>>8))
	return string(buf[:])
}

func styleAttrs(s uv.Style) uint16 {
	var out uint16
	if s.Attrs&uv.AttrBold != 0 {
		out |= AttrBold
	}
	if s.Attrs&uv.AttrFaint != 0 {
		out |= AttrFaint
	}
	if s.Attrs&uv.AttrItalic != 0 {
		out |= AttrItalic
	}
	if s.Attrs&uv.AttrBlink != 0 {
		out |= AttrBlink
	}
	if s.Attrs&uv.AttrReverse != 0 {
		out |= AttrReverse
	}
	if s.Attrs&uv.AttrConceal != 0 {
		out |= AttrConceal
	}
	if s.Attrs&uv.AttrStrikethrough != 0 {
		out |= AttrStrikethrough
	}
	if s.Underline != 0 {
		out |= AttrUnderline
	}
	return out
}
