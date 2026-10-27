package main

import (
	"context"
	"net"
	"os"
	"path/filepath"
	"sync"
	"testing"
	"time"
)

type retirementFrozenFixture struct{ released, committed int }

func (f *retirementFrozenFixture) release()      { f.released++ }
func (f *retirementFrozenFixture) commit() error { f.committed++; return nil }
func retirementAgentLeaseFixture(t *testing.T) (*ownerAgentRetirement, net.Conn, *retirementFrozenFixture) {
	t.Helper()
	a, b := net.Pipe()
	t.Cleanup(func() { a.Close(); b.Close() })
	frozen := &retirementFrozenFixture{}
	input := &sync.Mutex{}
	input.Lock()
	r := &ownerAgentRetirement{paneID: "owner", inputMu: input, connections: func() int { return 1 }, connectionLive: func(conn net.Conn) bool { return conn == a }, lease: &ownerAgentRetirementLease{conn: a, token: "epoch", frozen: frozen, deadline: time.Now().Add(time.Second)}}
	t.Cleanup(func() { r.abort(a, "") })
	return r, a, frozen
}
func TestOwnerRetirementAgentInputCancelsRatherThanDrops(t *testing.T) {
	r, conn, frozen := retirementAgentLeaseFixture(t)
	r.input([]byte("draft"))
	r.inputDone()
	if frozen.released != 1 || frozen.committed != 0 || r.lease != nil || !r.inputFence.Seen || r.inputFence.Known {
		t.Fatalf("input did not revoke lease: %+v", r)
	}
	if !r.inputMu.TryLock() {
		t.Fatal("input remains blocked after cancellation")
	}
	r.inputMu.Unlock()
	if r.commit(conn, "epoch").Committed {
		t.Fatal("cancelled lease committed")
	}
}
func TestOwnerRetirementAgentCommitBindsConnectionAndToken(t *testing.T) {
	r, conn, frozen := retirementAgentLeaseFixture(t)
	if r.commit(conn, "stale").Committed || r.lease == nil {
		t.Fatal("stale token changed lease")
	}
	if reply := r.commit(conn, "epoch"); !reply.Committed {
		t.Fatal(reply.Error)
	}
	if frozen.committed != 1 || r.lease != nil {
		t.Fatal("commit was not exact-once")
	}
	if r.commit(conn, "epoch").Committed {
		t.Fatal("duplicate commit succeeded")
	}
}
func TestOwnerRetirementAgentNewConnectionAndDeadlineRevoke(t *testing.T) {
	for _, cause := range []string{"connection", "deadline"} {
		t.Run(cause, func(t *testing.T) {
			r, conn, frozen := retirementAgentLeaseFixture(t)
			if cause == "connection" {
				r.attached()
			} else {
				r.lease.deadline = time.Now().Add(-time.Second)
			}
			if r.commit(conn, "epoch").Committed || frozen.committed != 0 || r.lease != nil {
				t.Fatalf("%s committed", cause)
			}
			if !r.inputMu.TryLock() {
				t.Fatal("input lock leaked")
			}
			r.inputMu.Unlock()
		})
	}
}
func TestOwnerRetirementInputNeedsNewTurnStartNotPriorCompletion(t *testing.T) {
	first := providerCodexLine("task_started", "old", turnProviderStart) + "\n"
	priorEnd := providerCodexLine("task_complete", "old", turnProviderStart+1000) + "\n"
	nextStart := providerCodexLine("task_started", "new", turnProviderStart+2000) + "\n"
	nextEnd := providerCodexLine("task_complete", "new", turnProviderStart+3000) + "\n"
	for _, tc := range []struct {
		name, content string
		wantAfter     bool
	}{{"old turn ends after input", first + priorEnd, false}, {"new input completed", first + priorEnd + nextStart + nextEnd, true}} {
		t.Run(tc.name, func(t *testing.T) {
			path := filepath.Join(t.TempDir(), "source.jsonl")
			if err := os.WriteFile(path, []byte(tc.content), 0600); err != nil {
				t.Fatal(err)
			}
			f, err := os.Open(path)
			if err != nil {
				t.Fatal(err)
			}
			reader := turnProviderReader{}
			observation := reader.read(&turnProviderSource{file: f, key: "generation", agent: agentCodex, session: turnProviderSession, valid: func() bool { return true }}, turnProviderStart+10000)
			last := observation.completed[len(observation.completed)-1]
			after := observation.startedOffsets[providerTurnKey(last)] > int64(len(first))
			if after != tc.wantAfter {
				t.Fatalf("input boundary misclassified: %+v", observation)
			}
		})
	}
}

func TestOwnerRetirementPrepareRejectsActivityBeforeGoroutineRuns(t *testing.T) {
	r, conn, _ := retirementAgentLeaseFixture(t)
	r.abort(conn, "")
	sequence := r.sequence()
	r.input([]byte("draft"))
	r.inputDone()
	reply := r.prepare(conn, ownerRetirementRequest{Token: "next", PaneID: "owner"}, sequence)
	if reply.Ready || reply.Error != "terminal activity changed after retirement request" {
		t.Fatalf("stale queued prepare accepted: %+v", reply)
	}
}

func TestOwnerRetirementBindsEffectiveChildPaneEnvironment(t *testing.T) {
	if got := ownerRetirementPaneFromEnv([]string{"SOLU_PANE_ID=inherited", "OTHER=secret-free", "SOLU_PANE_ID=child"}); got != "child" {
		t.Fatalf("bound inherited rather than effective child: %s", got)
	}
}

func TestOwnerRetirementExitCannotHidePriorCommitAcknowledgement(t *testing.T) {
	p := &Pane{done: make(chan struct{})}
	close(p.done)
	replies := make(chan ownerRetirementReply, 2)
	replies <- ownerRetirementReply{Token: "old"}
	replies <- ownerRetirementReply{Token: "exact", Committed: true}
	reply, err := waitOwnerRetirementReply(context.Background(), p, replies, "exact")
	if err != nil || !reply.Committed {
		t.Fatalf("ordered commit was lost behind exit: %+v %v", reply, err)
	}
}

func TestOwnerRetirementRepliesDoNotInventQueuedSubmission(t *testing.T) {
	r, _, _ := retirementAgentLeaseFixture(t)
	original := ownerRetirementInputFence{Seen: true, Known: true, SourceKey: "generation", Offset: 50}
	r.inputFence = original
	r.submissionFence = original
	for _, reply := range []string{"\x1b[I", "\x1b[?1;2c", "\x1b[12;30R", "\x1b]11;rgb:0000/0000/0000\x07"} {
		r.input([]byte(reply))
		r.inputDone()
		if r.inputFence != original || r.submissionFence != original {
			t.Fatalf("query reply became a request: %q", reply)
		}
	}
	for _, input := range []string{"\x1b[A", "\x1b[<0;10;10M", "\x1b]52;c;ZGlzcGF0Y2g=\x07", "\x1b[200~"} {
		if ownerRetirementProtocolReplyOnly([]byte(input)) {
			t.Fatalf("real interaction ignored: %q", input)
		}
	}
}
func TestOwnerRetirementUnknownSubmissionCannotBeClearedByLaterEnter(t *testing.T) {
	r, _, _ := retirementAgentLeaseFixture(t)
	r.submissionFence = ownerRetirementInputFence{Seen: true, UnresolvedSubmission: true}
	r.input([]byte("draft"))
	r.inputDone()
	r.input([]byte("\r"))
	r.inputDone()
	if r.inputFence.Known || !r.inputFence.UnresolvedSubmission || !r.submissionFence.UnresolvedSubmission {
		t.Fatalf("unknown queued input was silently discharged: %+v", r.inputFence)
	}
}
