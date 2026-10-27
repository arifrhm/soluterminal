package grid

import "testing"

func TestConditionalSubmitTrySnapshotDoesNotWait(t *testing.T) {
	// The busy path must not inspect the emulator while its writer owns it.
	e := &Emulator{}
	e.writeMu.Lock()
	defer e.writeMu.Unlock()
	if _, ok := e.TrySnapshot(); ok {
		t.Fatal("snapshot acquired busy emulator")
	}
}
