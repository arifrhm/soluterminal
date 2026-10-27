package main

import (
	"context"
	"crypto/sha256"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"soluterminal-shared/typesafe"
)

const jevNovelToolMenu = "Tool: read_file\n  src/app.go\nGrant this action?\n❯ 1. Yes\n  2. No (esc)"

func TestJevAmbiguousCandidateKeepsDeterministicDenials(t *testing.T) {
	if reason := autoApprovalDeclineReason(jevNovelToolMenu, false); reason != "choice menu, not a permission prompt" {
		t.Fatalf("novel permission must exercise ambiguous path: %q", reason)
	}
	if !jevAmbiguousCandidateText(jevNovelToolMenu) {
		t.Fatal("specific novel tool permission never reaches Jev")
	}
	for _, tc := range []struct{ name, text string }{
		{"no action payload", "Tool: read_file\nGrant this action?\n❯ 1. Yes\n  2. No (esc)"},
		{"stale action", jevNovelToolMenu + "\n\nGrant this action?\n❯ 1. Yes\n  2. No (esc)"},
		{"plan", "Tool: read_file\n  src/app.go\nReady to code?\n❯ 1. Yes\n  2. No (esc)"},
		{"billing", "Tool: read_file\n  src/app.go\nUse your reset?\n❯ 1. Yes\n  2. No (esc)"},
		{"question", "Tool: read_file\n  src/app.go\nWhich option?\n❯ 1. Yes\n  2. No (esc)\nEnter to select"},
		{"persistent", "Tool: read_file\n  src/app.go\nGrant this action?\n❯ 1. Yes, and don't ask again\n  2. No (esc)"},
		{"secret", strings.Replace(jevNovelToolMenu, "src/app.go", ".env", 1)},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if jevAmbiguousCandidateText(tc.text) {
				t.Fatal("unsafe or contextless menu reached Jev")
			}
		})
	}
}

func TestJevAmbiguousDetectorRequiresExactPaneAllowlist(t *testing.T) {
	dir := t.TempDir()
	marker := filepath.Join(dir, ariaJevDetectMarker)
	if err := os.WriteFile(marker, []byte("pane-a\n"), 0600); err != nil {
		t.Fatal(err)
	}
	if !ariaJevDetectEnabled(dir, "pane-a") || ariaJevDetectEnabled(dir, "pane-b") {
		t.Fatal("marker must enable only exact pane IDs")
	}
	t.Setenv(ariaJevDetectEnv, "0")
	if ariaJevDetectEnabled(dir, "pane-a") {
		t.Fatal("explicit zero did not disable detector")
	}
	t.Setenv(ariaJevDetectEnv, "1")
	t.Setenv(ariaJevDetectPanesEnv, "pane-b")
	if ariaJevDetectEnabled(dir, "pane-a") || !ariaJevDetectEnabled(dir, "pane-b") {
		t.Fatal("env allowlist did not scope detector")
	}
}

func TestJevAmbiguousDetectorCachesNegativePerMenu(t *testing.T) {
	a, p := ariaJevSafetyFixture(t, jevNovelToolMenu, true)
	p.AutoMode = "always"
	if err := os.WriteFile(filepath.Join(a.dataDir, ariaJevDetectMarker), []byte(p.ID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	var calls atomic.Int32
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(_ context.Context, _ string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			if len(questions) != 1 {
				t.Fatalf("always mode asked risk question: %v", questions)
			}
			low := 0.1
			return &typesafe.Response{Model: ariaJevModel, Answers: map[string]typesafe.Answer{"permission_menu": {Type: "noul", Noul: &low}}, Usage: typesafe.Usage{InputTokens: 20}}, nil
		}}
	}
	for range 2 {
		if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
			t.Fatal("low-confidence unrecognized menu must stop this bound observation")
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("same menu cost %d provider calls, want 1", calls.Load())
	}
	// A new occurrence with identical text has a new absolute prompt position.
	// It must be classified again; a cached answer belongs to one menu only.
	appendDirectAutoMenuRedraw(t, p, jevNovelToolMenu)
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("low-confidence unrecognized replacement must remain a human gate")
	}
	if calls.Load() != 2 {
		t.Fatalf("new same-looking menu cost %d provider calls, want 2", calls.Load())
	}
}

func TestJevAmbiguousEvidenceBindsActionPayload(t *testing.T) {
	a, p := ariaJevSafetyFixture(t, jevNovelToolMenu, true)
	p.AutoMode = "always"
	if err := os.WriteFile(filepath.Join(a.dataDir, ariaJevDetectMarker), []byte(p.ID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tail := tailOfPane(p, 30)
	_, pos, fp, ok := currentChoicePromptIdentity(p, tail)
	if !ok || fp == 0 {
		t.Fatal("fixture lacks prompt identity")
	}
	proof := &jevAmbiguousProof{dataDir: a.dataDir, mode: "always", pos: pos, fingerprint: fp, tailDigest: sha256.Sum256([]byte(ariaJevActiveMenuEvidence(tail)))}
	bindJevTestProof(t, a, p, proof)
	if !proof.valid(p) {
		t.Fatal("unchanged action failed proof")
	}
	changed := strings.Replace(jevNovelToolMenu, "src/app.go", "src/other.go", 1)
	redraw := "\n" + changed + "\n"
	if _, err := p.buffer.Write([]byte(redraw)); err != nil {
		t.Fatal(err)
	}
	atomic.AddInt64(&p.TotalStrippedChars, int64(len(stripANSI([]byte(redraw)))))
	if proof.valid(p) {
		t.Fatal("changed action retained Jev permission proof")
	}
	// Case in paths and flags is meaningful to a shell and filesystem.
	a2, p2 := ariaJevSafetyFixture(t, jevNovelToolMenu, true)
	p2.AutoMode = "always"
	if err := os.WriteFile(filepath.Join(a2.dataDir, ariaJevDetectMarker), []byte(p2.ID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	tail2 := tailOfPane(p2, 30)
	_, pos2, fp2, ok := currentChoicePromptIdentity(p2, tail2)
	if !ok || fp2 == 0 {
		t.Fatal("case-change fixture lacks prompt identity")
	}
	proof2 := &jevAmbiguousProof{dataDir: a2.dataDir, mode: "always", pos: pos2, fingerprint: fp2, tailDigest: sha256.Sum256([]byte(ariaJevActiveMenuEvidence(tail2)))}
	bindJevTestProof(t, a2, p2, proof2)
	if !proof2.valid(p2) {
		t.Fatal("unchanged case-change fixture failed proof")
	}
	caseOnly := "\n" + strings.Replace(jevNovelToolMenu, "src/app.go", "src/App.go", 1) + "\n"
	if _, err := p2.buffer.Write([]byte(caseOnly)); err != nil {
		t.Fatal(err)
	}
	atomic.AddInt64(&p2.TotalStrippedChars, int64(len(stripANSI([]byte(caseOnly)))))
	if proof2.valid(p2) {
		t.Fatal("case-only action change retained Jev permission proof")
	}
}

func newJevDetectHarness(t *testing.T, mode string) (*Aria, *Pane, <-chan directAutoFrameRead) {
	t.Helper()
	p, frames := newTypingHoldPane(t, "pane-jev-detect")
	p.AutoMode = mode
	appendDirectAutoMenuRedraw(t, p, jevNovelToolMenu)
	seedGridMenu(t, p, jevNovelToolMenu)
	a := testAriaWithPane(t, p)
	seedJevTestProcess(a, p)
	a.dataDir = t.TempDir()
	a.sweepStreaks = map[string]*sweepStreak{}
	a.evalLog = newAriaEvalLog()
	if err := os.WriteFile(filepath.Join(a.dataDir, ariaJevDetectMarker), []byte(p.ID+"\n"), 0600); err != nil {
		t.Fatal(err)
	}
	seedRuntimeSecret(t, a.dataDir, jevTypesafeRuntimeID, jevTypesafeSecretEnv, "jev-test-key")
	if !jevAmbiguousCandidateText(tailOfPane(p, 30)) {
		t.Fatalf("harness menu not eligible: %q", tailOfPane(p, 30))
	}
	return a, p, frames
}

func jevDetectYesResponse(cautious bool) *typesafe.Response {
	high := .99
	answers := map[string]typesafe.Answer{"permission_menu": {Type: "noul", Noul: &high}}
	if cautious {
		answers["safe_action"] = typesafe.Answer{Type: "noul", Noul: &high}
	}
	return &typesafe.Response{Model: ariaJevModel, Answers: answers, Usage: typesafe.Usage{InputTokens: 40}}
}

func TestJevAmbiguousAlwaysFiresWithoutRiskQuestion(t *testing.T) {
	a, p, frames := newJevDetectHarness(t, "always")
	var calls atomic.Int32
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(_ context.Context, _ string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			if len(questions) != 1 {
				t.Fatalf("always mode questions = %v", questions)
			}
			return jevDetectYesResponse(false), nil
		}}
	}
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("positive Jev permission was not handled")
	}
	expectFrame(t, frames, "Jev always fire", "\r")
	if calls.Load() != 1 {
		t.Fatalf("provider calls = %d, want 1", calls.Load())
	}
}

func TestJevAmbiguousPositiveCacheRetriesAfterTypingHold(t *testing.T) {
	a, p, frames := newJevDetectHarness(t, "always")
	var calls atomic.Int32
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			atomic.StoreInt64(&p.LastInputMs, time.Now().UnixMilli())
			return jevDetectYesResponse(false), nil
		}}
	}
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("held positive decision was not handled")
	}
	expectNoFrame(t, frames, "typing hold")
	atomic.StoreInt64(&p.LastInputMs, time.Now().Add(-10*ariaAutoTypingSettle).UnixMilli())
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("cached positive decision did not retry")
	}
	expectFrame(t, frames, "cached positive retry", "\r")
	if calls.Load() != 1 {
		t.Fatalf("positive retry made %d provider calls, want 1", calls.Load())
	}
}

func TestJevAmbiguousCautiousRequiresRiskAndHonorsCancel(t *testing.T) {
	a, p, frames := newJevDetectHarness(t, "cautious")
	var calls atomic.Int32
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(_ context.Context, _ string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
			calls.Add(1)
			if len(questions) != 2 {
				t.Fatalf("cautious questions = %v", questions)
			}
			return jevDetectYesResponse(true), nil
		}}
	}
	result := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { result <- a.tryJevAmbiguousTerminal(ctx, p, p.ID, p.Title, time.Now()) }()
	var confirmID string
	deadline := time.After(time.Second)
	for confirmID == "" {
		a.mu.Lock()
		for id := range a.confirms {
			confirmID = id
			break
		}
		a.mu.Unlock()
		if confirmID != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cautious decision did not open confirm")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !a.signalConfirm(confirmID, false, false) {
		t.Fatal("could not cancel cautious Jev decision")
	}
	select {
	case handled := <-result:
		if !handled {
			t.Fatal("cancelled decision fell through")
		}
	case <-time.After(time.Second):
		t.Fatal("cautious decision did not finish after cancel")
	}
	expectNoFrame(t, frames, "cancelled Jev decision")
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("cached cancel fell through")
	}
	if calls.Load() != 1 {
		t.Fatalf("cancelled prompt made %d provider calls, want 1", calls.Load())
	}
}

func TestJevAmbiguousCautiousFiresAfterConfirm(t *testing.T) {
	a, p, frames := newJevDetectHarness(t, "cautious")
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(_ context.Context, _ string, _ any, questions map[string]typesafe.Question) (*typesafe.Response, error) {
			if len(questions) != 2 {
				t.Fatalf("cautious questions = %v", questions)
			}
			return jevDetectYesResponse(true), nil
		}}
	}
	result := make(chan bool, 1)
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
	defer cancel()
	go func() { result <- a.tryJevAmbiguousTerminal(ctx, p, p.ID, p.Title, time.Now()) }()
	var confirmID string
	deadline := time.After(time.Second)
	for confirmID == "" {
		a.mu.Lock()
		for id := range a.confirms {
			confirmID = id
			break
		}
		a.mu.Unlock()
		if confirmID != "" {
			break
		}
		select {
		case <-deadline:
			t.Fatal("cautious approval did not open confirm")
		default:
			time.Sleep(time.Millisecond)
		}
	}
	if !a.signalConfirm(confirmID, true, false) {
		t.Fatal("could not approve cautious Jev confirmation")
	}
	select {
	case handled := <-result:
		if !handled {
			t.Fatal("approved Jev decision fell through")
		}
	case <-time.After(time.Second):
		t.Fatal("approved Jev decision did not finish")
	}
	expectFrame(t, frames, "cautious Jev Enter", "\r")
}

func TestJevAmbiguousStaleProviderResultNeverWrites(t *testing.T) {
	a, p, frames := newJevDetectHarness(t, "always")
	a.jevClientFactory = func(string) jevSystemOneClient {
		return ariaJevSafetyClient{call: func(context.Context, string, any, map[string]typesafe.Question) (*typesafe.Response, error) {
			changed := strings.Replace(jevNovelToolMenu, "src/app.go", "src/other.go", 1)
			appendDirectAutoMenuRedraw(t, p, changed)
			return jevDetectYesResponse(false), nil
		}}
	}
	if !a.tryJevAmbiguousTerminal(context.Background(), p, p.ID, p.Title, time.Now()) {
		t.Fatal("stale result should terminate observation")
	}
	expectNoFrame(t, frames, "stale Jev result")
}
