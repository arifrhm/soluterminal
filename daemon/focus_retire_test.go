package main

import (
	"testing"
)

func TestNativeRendererRetiredByDefault(t *testing.T) {
	t.Setenv("SOLU_NATIVE_RENDERER", "")
	if nativeRendererEnabled() {
		t.Fatal("Focus/native renderer must be off by default")
	}
	t.Setenv("SOLU_NATIVE_RENDERER", "1")
	if !nativeRendererEnabled() {
		t.Fatal("SOLU_NATIVE_RENDERER=1 must re-enable it")
	}
}

func TestFocusPaneNeedsRetiring(t *testing.T) {
	native := deferredTestPane("pane_native", "native")
	if !focusPaneNeedsRetiring(native) {
		t.Fatal("a running native pane must be retired")
	}

	native.setPendingSwitch("")
	if focusPaneNeedsRetiring(native) {
		t.Fatal("a native pane already switching to terminal must not be re-armed")
	}

	toNative := deferredTestPane("pane_native2", "native")
	toNative.setPendingSwitch("native")
	if !focusPaneNeedsRetiring(toNative) {
		t.Fatal("a stale pending switch INTO native must be replaced by a switch to terminal")
	}

	if focusPaneNeedsRetiring(deferredTestPane("pane_term", "")) {
		t.Fatal("terminal panes are never touched")
	}

	exited := deferredTestPane("pane_exited", "native")
	exited.Status = PaneExited
	if focusPaneNeedsRetiring(exited) {
		t.Fatal("exited native panes are left alone (nothing to switch)")
	}
}
