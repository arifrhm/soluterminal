package grid

import (
	"reflect"
	"unsafe"

	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/vt"
)

const (
	parserInitialBytes = 64 << 10
	parserLimitBytes   = 4 << 20 // Preserve the pinned vt implementation's limit.
	parserFeedBytes    = 32 << 10
)

// The pinned vt constructor eagerly allocates 4 MiB for every parser, even
// when a pane only prints text. Like altScreen, this checked accessor stays
// local to the pinned implementation; an incompatible layout keeps vt's
// original buffer and behavior instead of disabling parsing.
func lazyParser(e *vt.Emulator) (p *ansi.Parser) {
	defer func() {
		if recover() != nil {
			p = nil
		}
	}()
	v := reflect.ValueOf(e).Elem().FieldByName("parser")
	if !v.IsValid() || !v.CanAddr() || v.Type() != reflect.TypeOf((*ansi.Parser)(nil)) {
		return nil
	}
	p = *(**ansi.Parser)(unsafe.Pointer(v.UnsafeAddr()))
	if p == nil || len(p.Data()) != 0 || cap(p.Data()) != parserLimitBytes {
		return nil
	}
	p.SetDataSize(parserInitialBytes)
	return p
}

// Called under writeMu before each bounded write. A pending control string
// can grow by at most len(chunk), so no byte is truncated by lazy allocation.
// SetDataSize preserves dataLen; copying restores the pending sequence.
func growParser(p *ansi.Parser, incoming int) {
	data := p.Data()
	needed := min(parserLimitBytes, len(data)+incoming)
	if needed <= cap(data) {
		return
	}
	p.SetDataSize(min(parserLimitBytes, max(needed, 2*cap(data))))
	copy(p.Data(), data)
}
