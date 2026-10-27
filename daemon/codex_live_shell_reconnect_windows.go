//go:build windows

package main

// Windows shell panes run PowerShell, whose Codex invocation cannot be proven
// from the process table the same way; they keep the pane-command path.
type codexForeground struct {
	cwd        string
	underShell bool
	args       []string
}

func codexForegroundOf(p *Pane, thread string) (codexForeground, bool) {
	return codexForeground{}, false
}
