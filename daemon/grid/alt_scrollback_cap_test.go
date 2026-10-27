package grid

import (
	"reflect"
	"testing"

	"github.com/charmbracelet/x/vt"
)

func assertBothScrollbackCaps(t *testing.T, e *Emulator) {
	t.Helper()
	if got := e.vt.Scrollback().MaxLines(); got != scrollbackLines {
		t.Fatalf("primary scrollback MaxLines = %d, want %d", got, scrollbackLines)
	}
	alt := altScreen(e.vt)
	if alt == nil {
		t.Fatal("altScreen returned nil; pinned vt internals changed")
	}
	if got := alt.Scrollback().MaxLines(); got != scrollbackLines {
		t.Fatalf("alternate scrollback MaxLines = %d, want %d", got, scrollbackLines)
	}
}

func TestScrollbackCapAppliesToBothScreens(t *testing.T) {
	e := New(80, 24)
	defer e.Close()
	assertBothScrollbackCaps(t, e)
}

func TestAltScrollbackCapSurvivesRIS(t *testing.T) {
	e := New(80, 24)
	defer e.Close()
	_, _ = e.vt.Write([]byte("\x1bc"))
	assertBothScrollbackCaps(t, e)
}

func TestAltScrollbackCapSurvivesResize(t *testing.T) {
	e := New(80, 24)
	defer e.Close()
	e.Resize(132, 43)
	assertBothScrollbackCaps(t, e)
}

func TestAltScreenRejectsPointerElementArray(t *testing.T) {
	wrongShape := struct {
		Screens [2]*vt.Screen
	}{}
	field := reflect.ValueOf(&wrongShape).Elem().FieldByName("Screens")
	if got := screenFromVTArray(field, 1); got != nil {
		t.Fatalf("pointer-element array returned %p, want nil fallback", got)
	}
}

// This canary documents the upstream gap. If a dependency bump makes it
// fail, vt now caps the alternate screen itself and altScreen plus New's
// direct alternate-screen call can be retired.
func TestUpstreamSetScrollbackSizeStillMissesAltScreen(t *testing.T) {
	vte := vt.NewEmulator(80, 24)
	vte.SetScrollbackSize(scrollbackLines)
	alt := altScreen(vte)
	if alt == nil {
		t.Fatal("altScreen returned nil; pinned vt internals changed")
	}
	if got := alt.Scrollback().MaxLines(); got != vt.DefaultScrollbackSize {
		t.Fatalf("raw vt alternate scrollback MaxLines = %d, want upstream default %d; retire workaround if upstream now caps it", got, vt.DefaultScrollbackSize)
	}
}
