package main

import (
	"log"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
)

const (
	codexAgentsBeginMarker = "<!-- soluterminal:managed:begin -->"
	codexAgentsEndMarker   = "<!-- soluterminal:managed:end -->"
)

// codexAgentsPublicPolicy selects the public beta's block: installs without
// the team entitlement get only the context-pack note, and a team block left
// by an earlier version is replaced with it on the next sync.
var codexAgentsPublicPolicy atomic.Bool

const codexAgentsContextPackLine = "Context packs: if the environment variable SOLU_CONTEXT_PACK is set, it names a local directory holding a briefing for this session — before your first response, read its AGENTS.md (or CLAUDE.md if AGENTS.md is absent) and follow it, in addition to any AGENTS.md discovered from the working directory. If another instruction already pointed you at that same directory, it is the same pack — read it once.\n"

// renderCodexAgentsPolicy renders the block in the user's GLOBAL
// ~/.codex/AGENTS.md, which the daemon already manages. The orchestrator brief
// must NEVER be written here or into a repository AGENTS.md; pane-role policy
// reaches Codex only through the per-pane transport in pane_role_host.go.
func renderCodexAgentsPolicy() string {
	if codexAgentsPublicPolicy.Load() {
		return "This block is maintained by soluterminal; edits inside the markers are overwritten. Content outside is never touched.\n\n" +
			codexAgentsContextPackLine
	}
	return "This block is maintained by the soluterminal daemon; edits inside the markers are overwritten. Content outside is never touched.\n\n" + mcpDirectServerInstructions + "\n\n" + codexAgentsContextPackLine

}

func codexAgentsManagedBlock() string {
	return codexAgentsBeginMarker + "\n" + renderCodexAgentsPolicy() + "\n" + codexAgentsEndMarker
}

// codexAgentsApply replaces only our marker-delimited block. Files with an
// ambiguous marker shape are deliberately left untouched.
func codexAgentsApply(existing string) (updated string, changed bool, ok bool) {
	beginCount := strings.Count(existing, codexAgentsBeginMarker)
	endCount := strings.Count(existing, codexAgentsEndMarker)
	block := codexAgentsManagedBlock()

	if beginCount == 0 && endCount == 0 {
		if existing == "" {
			updated = block + "\n"
		} else {
			updated = existing
			if !strings.HasSuffix(updated, "\n") {
				updated += "\n"
			}
			updated += "\n" + block + "\n"
		}
		return updated, updated != existing, true
	}
	if beginCount != 1 || endCount != 1 {
		return existing, false, false
	}
	begin := strings.Index(existing, codexAgentsBeginMarker)
	end := strings.Index(existing, codexAgentsEndMarker)
	if begin > end {
		return existing, false, false
	}
	end += len(codexAgentsEndMarker)
	updated = existing[:begin] + block + existing[end:]
	return updated, updated != existing, true
}

func syncCodexAgentsMd(path string) {
	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		log.Printf("codex-agents: could not read %s: %v", path, err)
		return
	}
	next, changed, ok := codexAgentsApply(string(data))
	if !ok {
		log.Printf("codex-agents: %s has an unrecognized soluterminal block shape (leaving as is)", path)
		return
	}
	if !changed {
		return
	}
	if err := os.MkdirAll(filepath.Dir(path), 0755); err != nil {
		log.Printf("codex-agents: could not create directory for %s: %v", path, err)
		return
	}
	tmp := path + ".solu-tmp"
	if err := os.WriteFile(tmp, []byte(next), 0644); err != nil {
		log.Printf("codex-agents: could not write %s: %v", tmp, err)
		return
	}
	if err := os.Rename(tmp, path); err != nil {
		_ = os.Remove(tmp)
		log.Printf("codex-agents: could not replace %s: %v", path, err)
		return
	}
	log.Printf("codex-agents: synchronized soluterminal policy in %s", path)
}
