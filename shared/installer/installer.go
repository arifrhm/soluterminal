// Package installer holds the static `curl | sh` installer (install.sh).
//
// The same bytes are attached to every release by publish.sh and served by
// the cloud's /install.sh, so the script a user runs never depends on the
// cloud generating it. The script verifies the release's Ed25519-signed
// update.json itself, with the public key it carries, before installing.
package installer

import (
	_ "embed"
	"regexp"
)

//go:embed install.sh
var Script []byte

var pubKeyLineRE = regexp.MustCompile(`(?m)^SOLU_RELEASE_PUBKEY='([A-Za-z0-9+/=]+)'$`)
var reposLineRE = regexp.MustCompile(`(?m)^SOLU_RELEASE_REPOS='([^']*)'$`)

// PublicKey returns the base64 release signing key embedded in the script,
// or "" when the assignment is missing.
func PublicKey() string {
	return firstGroup(pubKeyLineRE, Script)
}

// Repos returns the space-separated releases repositories the script
// downloads from, current repo first.
func Repos() string {
	return firstGroup(reposLineRE, Script)
}

func firstGroup(re *regexp.Regexp, b []byte) string {
	m := re.FindSubmatch(b)
	if m == nil {
		return ""
	}
	return string(m[1])
}
