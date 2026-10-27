package main

import (
	"strings"
	"testing"
)

// Review follow-ups: URL/header credentials are scrubbed on bundle surfaces,
// browsing-history lines are dropped, and the auto-approval tail check keeps
// its narrower rule set.
func TestRedactSensitiveURLAndHeaderCredentials(t *testing.T) {
	cases := map[string]string{
		"GET https://bob:Synth-Pa55@example.test/x":                      "Synth-Pa55",
		"cb https://app.test/oauth/cb?code=4/0AeaSynthCode&state=abc123": "4/0AeaSynthCode",
		"https://s3.test/o?X-Amz-Signature=deadbeefcafe&X-Amz-Expires=9": "deadbeefcafe",
		"https://h.test/p?sid=s3ss10n&key=k3yval":                        "s3ss10n",
		"Cookie: sid=abcdef; theme=dark":                                 "sid=abcdef",
		"SOLU_UPDATE_SIGNING_KEY=shortsecret":                            "shortsecret",
	}
	for in, secret := range cases {
		if out := redactSensitive(in); strings.Contains(out, secret) {
			t.Errorf("redactSensitive(%q) = %q, still contains %q", in, out, secret)
		}
	}
	// Ordinary URLs survive.
	plain := "see https://github.com/example-org/soluterminal/pull/395?tab=files"
	if out := redactSensitive(plain); !strings.Contains(out, "pull/395?tab=files") {
		t.Errorf("plain URL mangled: %q", out)
	}
	// The terminal-tail / auto-approval rule set is unchanged by these rules.
	cmd := "curl 'https://api.test/items?code=1&key=2'"
	if redactTerminalTail(cmd) != cmd {
		t.Errorf("redactTerminalTail now changes %q; auto-approval would decline it", cmd)
	}
}

func TestDropBrowsingLines(t *testing.T) {
	in := "a\nINFO open_external_window ENTER target=https://bank.test/?code=x\nb"
	if got := dropBrowsingLines(in); got != "a\nb" {
		t.Fatalf("dropBrowsingLines = %q", got)
	}
}

func TestUnknownSubcommandShape(t *testing.T) {
	for _, w := range []string{"support-bundle", "triage", "gc2"} {
		if !unknownSubcommandRE.MatchString(w) {
			t.Errorf("%q should read as a subcommand", w)
		}
	}
	for _, w := range []string{"/usr/bin/x", "0.0.0.0:8787", "Foo", "-x", "a b"} {
		if unknownSubcommandRE.MatchString(w) {
			t.Errorf("%q should not read as a subcommand", w)
		}
	}
}
