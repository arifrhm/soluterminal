package main

import (
	"os"
	"strings"
	"testing"
)

func rustShellUpdateSource(t *testing.T) string {
	t.Helper()
	body, err := os.ReadFile("../desktop/src-tauri/src/shell_update.rs")
	if err != nil {
		t.Fatal(err)
	}
	return string(body)
}

func TestNativeLaunchLoggerAvoidsPowerShellAutomaticVariables(t *testing.T) {
	source := rustShellUpdateSource(t)
	for _, reserved := range []string{"$pid", "$args", "$input", "$this"} {
		if strings.Contains(source, "[string]"+reserved) {
			t.Fatalf("PowerShell logger binds reserved automatic variable %s, so parameter binding prevents launch.log writes", reserved)
		}
	}
}
