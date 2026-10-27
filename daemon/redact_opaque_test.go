package main

import (
	"math/rand"
	"testing"
)

// Auto-approval kept declining ordinary prompts as "sensitive permission
// action" because any 32+ char [A-Za-z0-9_-] run counted as a secret.
func TestOpaqueSecretLike(t *testing.T) {
	notSecret := []string{
		"node /tmp/shinoque-files-850f559d-move-check-attempt2-20261003.mjs",
		"cd /tmp/solu-four-role-repaired-rehearsal-20261002.lArHyj/study/worktrees/writer",
		"codex resume 01a0ecf7-f3ab-7c73-aa9a-1ac74c3670b0",
		"git push origin fix/terminal-uplift-v2-review-round-three-fixes",
		"git show 9fc9a9af0",
		"internationalization_configuration_file_reference",
		// Long CamelCase identifiers are words, not keys (a Go test name in a
		// `go test -run` approval was declined as sensitive).
		"go test -p=2 -count=1 -run '^(TestSharedSnapshot|TestComputeState|TestWorkflowBootHealsWrittenDepCycle|TestWorkflowHealthBaseline)' .",
		"npx vitest run src/components/PreviewChannelSectionKeyboardNavigation.test.tsx",
		"grep -rn useAgentsStatusPollingWithBackoff web/src",
		"HTTPServerConfigurationLoaderForTests",
		"go test -run TestTeamWatchWakesOnlyOnRealCredentialChange .",
	}
	for _, s := range notSecret {
		if opaqueSecretLike(s) {
			t.Errorf("ordinary text treated as a secret: %q", s)
		}
	}
	secret := []string{
		"export GH_TOKEN=ghp_1A2b3C4d5E6f7G8h9I0jKlMnOpQrStUvWxYz",
		"curl -H 'X-Key: 9fJ2kQ7xLm4Pz8Rt1Vb6Nc3Hs5Gd0WaYe'",
		"git checkout 9fc9a9af0473419d78be35539273a919aed27b50",
		"aws configure set secret wJalrXUtnFEMI-K7MDENGbPxRfiCYEXAMPLEKEY12",
		// Letters-only mixed-case keys still look random.
		"export API_KEY=aZbYcXdWeVfUgThSiRjQkPlOmNnMoLpKqJ",
		"Authorization: Bearer xKqPzLmWvRtYbNcJhGfDsAeUoIpLkJhGfD",
		// Word-shaped passphrases in a password context stay flagged.
		"sshpass -p CorrectHorseBatteryStapleOrangeJuice ssh host",
		"curl -u admin:CorrectHorseBatteryStapleOrangeJuice https://x.test",
		"echo CorrectHorseBatteryStapleOrangeJuice | docker login -u me --password-stdin",
	}
	for _, s := range secret {
		if !opaqueSecretLike(s) {
			t.Errorf("secret-looking token not flagged: %q", s)
		}
	}
}

// The CamelCase exemption must not let random mixed-case keys through.
func TestOpaqueSecretLikeRandomLettersStillFlagged(t *testing.T) {
	const letters = "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ"
	rng := rand.New(rand.NewSource(1))
	missed := 0
	const trials = 10000
	for i := 0; i < trials; i++ {
		b := make([]byte, 32+rng.Intn(16))
		for j := range b {
			b[j] = letters[rng.Intn(len(letters))]
		}
		if !opaqueSecretLike(string(b)) {
			missed++
		}
	}
	if missed > trials/1000 {
		t.Fatalf("%d of %d random letter keys passed as words", missed, trials)
	}
	t.Logf("random letter keys passed as words: %d/%d", missed, trials)
}

func TestHuggingFaceTokenIsSensitive(t *testing.T) {
	// Split in the source so secret scanners don't flag the fake.
	cmd := "huggingface-cli login --token " + "hf_" + "AbCdEfGhIjKlMnOpQrStUvWxYzAbCdEfGh"
	if redactTerminalTail(cmd) == cmd {
		t.Fatalf("hf_ token not caught by the terminal-tail rules: %q", cmd)
	}
}

// The observed approval was a go-test command, not a credential. Its word
// Owns exposed the capital-initial-vowel blind spot in the identifier parser.
func TestOpaqueCodeWordsIncludeInitialVowels(t *testing.T) {
	for _, name := range []string{
		"TestVerificationPlaneLiveChildOwnsHealthLiveness",
		"TestVerificationPlaneLiveChildEndsCleanly",
		"TestVerificationHTTPAuthOwnsLifecycle",
	} {
		command := "go test -run '^" + name + "$' -count=40"
		if opaqueSecretLike(command) || redactTerminalTail(command) != command {
			t.Errorf("code identifier rejected: %s", name)
		}
		// Word-shaped passphrases still stay local; this is no credential exemption.
		if !opaqueSecretLike("login --password " + name) {
			t.Errorf("password context lost for %s", name)
		}
	}
	for _, word := range []string{"Owns", "Ends", "HTTPAuth"} {
		if !camelCaseWords(word) {
			t.Errorf("vowel-initial word rejected: %s", word)
		}
	}
}
