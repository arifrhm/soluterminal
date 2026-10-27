package main

import (
	"strings"
	"testing"
	"time"
)

func TestWSLCrashDumpScriptsRefuseReparseAndChangedFiles(t *testing.T) {
	root := `C:\Users\operator\AppData\Local\Temp\wsl-crashes`
	file := crashDumpFile{Path: root + `\dump.dmp`, Bytes: 100, ModifiedAt: time.Date(2026, 9, 24, 0, 0, 0, 0, time.UTC)}
	for _, script := range []string{crashDumpListScript(root), crashDumpDeleteScript(file)} {
		for _, guard := range []string{"$ErrorActionPreference='Stop'", "[IO.FileAttributes]::ReparsePoint", "Split-Path -LiteralPath"} {
			if !strings.Contains(script, guard) {
				t.Fatalf("missing path guard %q: %s", guard, script)
			}
		}
	}
	script := crashDumpDeleteScript(file)
	for _, guard := range []string{"Assert-NoReparse $p", "$item.Length -ne 100", "$item.LastWriteTimeUtc.Ticks", "$item.PSIsContainer"} {
		if strings.Index(script, guard) < 0 || strings.Index(script, guard) > strings.Index(script, "Remove-Item") {
			t.Fatalf("delete before guard %q: %s", guard, script)
		}
	}
}
