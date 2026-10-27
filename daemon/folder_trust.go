package main

import (
	"regexp"
	"strings"
)

const folderTrustHint = "Waiting for you to trust this folder. Choose Yes, I trust this folder in the pane to continue."

// Claude's first-run menu has unnumbered options. Require both exact options,
// a focus marker and its confirmation footer; prose mentioning trust is not a
// menu. Both option orders and the older numbered form are accepted.
var folderTrustMenuRE = regexp.MustCompile(`(?m)^[ \t]*[❯❭›▶]?[ \t]*(?:[12]\.[ \t]*)?(?:No, exit|Yes, I trust this folder)[ \t]*\n[ \t]*[❯❭›▶]?[ \t]*(?:[12]\.[ \t]*)?(?:No, exit|Yes, I trust this folder)[ \t]*\n[ \t\n]*Enter to confirm[^\n]*`)

func folderTrustPromptMatches(screen string) [][]int {
	if !strings.Contains(screen, "Yes, I trust this folder") {
		return nil
	}
	var matches [][]int
	for _, loc := range folderTrustMenuRE.FindAllStringIndex(screen, -1) {
		menu := screen[loc[0]:loc[1]]
		if !strings.Contains(menu, "No, exit") || !strings.Contains(menu, "Yes, I trust this folder") || !strings.ContainsAny(menu, "❯❭›▶") {
			continue
		}
		// A later composer proves this is historical output, not a live menu.
		if newPromptFooterRE.MatchString(screen[loc[1]:]) {
			continue
		}
		matches = append(matches, loc)
	}
	return matches
}

func paneWaitingForFolderTrust(p *Pane) bool {
	return len(folderTrustPromptMatches(paneVisibleScreen(p))) > 0
}
