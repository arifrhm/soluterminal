package grid

import (
	"reflect"
	"unsafe"

	"github.com/charmbracelet/x/vt"
)

// altScreen reaches the alternate Screen in the pinned vt implementation.
// vt exposes Screen.SetScrollbackSize, Screen.Scrollback, and
// Scrollback.MaxLines publicly, but Emulator.scrs is unexported and
// Emulator.SetScrollbackSize hard-codes scrs[0]. The same limitation remains
// in the newer v0.0.0-20260705004817. This narrowly-scoped accessor follows
// the registerMarginGuards precedent: it avoids vendoring the roughly
// six-thousand-line module while an upstream API is unavailable. Shape checks
// and recovery make a future vt layout change degrade to primary-only capping
// instead of taking down the daemon.
func altScreen(e *vt.Emulator) (screen *vt.Screen) {
	defer func() {
		if recover() != nil {
			screen = nil
		}
	}()
	if e == nil {
		return nil
	}
	v := reflect.ValueOf(e).Elem().FieldByName("scrs")
	return screenFromVTArray(v, 1)
}

func screenFromVTArray(v reflect.Value, index int) *vt.Screen {
	if !v.IsValid() || v.Kind() != reflect.Array || v.Len() != 2 ||
		index < 0 || index >= v.Len() || v.Type().Elem() != reflect.TypeOf(vt.Screen{}) ||
		!v.Index(index).CanAddr() {
		return nil
	}
	return (*vt.Screen)(unsafe.Pointer(v.Index(index).UnsafeAddr()))
}
