package main

import "testing"

func TestIsMuseCmdline(t *testing.T) {
	t.Parallel()
	for _, cmd := range []string{
		// Launcher phase: a bash script, so comm says "bash".
		"/home/u/.local/bin/muse",
		"muse exec --json --session-id 11111111-2222-4333-8444-555555555555",
		"/home/u/.local/bin/muse exec --json --prompt-file /tmp/solu-muse-prompt-1.txt --disable-approval",
		// Pinned versioned binary (comm truncates to 15 bytes).
		"/home/u/.local/bin/muse-bin-1.3.0-R3401.1 exec --json",
		"muse-bin-1.3.0-R3401.1",
	} {
		if !isMuseCmdline(cmd) {
			t.Errorf("muse argv %q was not recognized", cmd)
		}
	}
	for _, cmd := range []string{
		"",
		"bash",
		"node /usr/bin/some-server --port 3000",
		"node /home/u/.solu/harness-launchers/launch.mjs deepseek --data-dir /home/u/.solu",
		// A model id in args must not mark the pane by itself.
		"somelauncher --model muse-spark-1.2-contributor",
		// Substring traps: neither basename is the muse binary.
		"/opt/amusement/run --serve",
		"/home/u/museum/catalog --index",
	} {
		if isMuseCmdline(cmd) {
			t.Errorf("non-muse argv %q was misclassified as muse", cmd)
		}
	}
}

func TestAdoptForegroundMusePreservesProviderConversations(t *testing.T) {
	p := deferredTestPane("muse-identity", "")
	p.Agent = agentCodex
	p.CodexThreadID = "old-codex"
	p.ClaudeSessionID = "old-claude"
	sessionID := "01a09087-edca-75a0-863d-7134199b1397"
	if !adoptForegroundMuse(p, sessionID) || p.Agent != agentMuse || p.MuseSessionID != sessionID {
		t.Fatal("foreground Muse was not adopted")
	}
	if p.CodexThreadID != "old-codex" || p.ClaudeSessionID != "old-claude" {
		t.Fatal("adoption discarded another provider's durable conversation")
	}
	if adoptForegroundMuse(p, sessionID) {
		t.Fatal("unchanged identity must not repeatedly persist")
	}
	adoptForegroundMuse(p, "")
	if p.MuseSessionID != sessionID || fleetRespawnEligibility(p).Blocker == "" {
		t.Fatal("unknown foreground Muse must preserve identity but block replacement")
	}
	adoptForegroundMuse(p, sessionID)
	if p.codexIdentityBlocked.Load() {
		t.Fatal("verified Muse did not clear the ambiguity latch")
	}
}

func TestUnidentifiedMuseCannotResumeStaleSpawnRecipe(t *testing.T) {
	p := deferredTestPane("muse-unknown", "")
	p.Cmd = []string{"claude", "--resume", "old-claude"}
	p.ClaudeSessionID = "old-claude"
	adoptForegroundMuse(p, "")
	if got := fleetRespawnEligibility(p); got.Blocker == "" || got.Snapshot.Agent != agentMuse {
		t.Fatal("identity healing must not replace unknown Muse with an old Claude session")
	}
}
