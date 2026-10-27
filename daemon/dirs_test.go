package main

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// A fresh home with no project folders must still answer [] (not null): the
// new-session folder picker iterates the result.
func TestListDirsEmptyHomeEncodesAsEmptyArray(t *testing.T) {
	home := t.TempDir()
	if err := os.WriteFile(filepath.Join(home, ".bashrc"), nil, 0o600); err != nil {
		t.Fatal(err)
	}
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	for _, q := range []string{"", "proj"} {
		got, err := json.Marshal(ListDirs(nil, q, 20))
		if err != nil {
			t.Fatal(err)
		}
		if string(got) != "[]" {
			t.Fatalf("ListDirs(q=%q) = %s, want []", q, got)
		}
	}
}
