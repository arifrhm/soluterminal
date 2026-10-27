package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"soluterminal-shared/wire"
)

var computeTestBaseTime = time.Date(2026, 9, 1, 1, 2, 3, 0, time.UTC)

func computeTestJob(id, principal, key string) wire.ComputeJobRecord {
	return wire.ComputeJobRecord{
		SchemaVersion:  1,
		JobID:          id,
		IdempotencyKey: key,
		Spec: wire.ComputeJobSpec{
			SchemaVersion:  1,
			JobKind:        "repo.verify.v1",
			Handler:        wire.HandlerVersion{HandlerID: "soluterminal-shared-tests", Version: 1},
			HandlerProfile: "soluterminal-shared-tests.v1",
			WorkspaceID:    "soluterminal",
			Revision:       "50ced849",
			SourceDigest:   "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			DataClass:      "internal",
			Requirements: wire.JobRequirements{
				OS: []string{"linux"}, Architectures: []string{"amd64"}, MinimumCPU: 2,
			},
			Inputs:       []wire.ArtifactRef{},
			Priority:     "normal",
			TimeoutSec:   60,
			CancelPolicy: "lease",
			MaxAttempts:  2,
		},
		Authz: wire.ComputeAuthz{
			TenantID: "tenant-a", Principal: principal, SubmittedAt: computeTestBaseTime.Format(time.RFC3339),
		},
		State: "queued",
	}
}

func computeTestOffer(job wire.ComputeJobRecord, assignment, execution string, epoch uint64) wire.AssignmentOffer {
	return wire.AssignmentOffer{
		AssignmentID: assignment,
		JobID:        job.JobID,
		ExecutionID:  execution,
		Epoch:        epoch,
		SpecHash:     job.SpecHash,
		Spec:         job.Spec,
		Authz: wire.AssignmentAuthorization{
			TenantID: job.Authz.TenantID, Principal: job.Authz.Principal,
			AssignmentID: assignment, SpecHash: job.SpecHash,
			IssuedAt:  computeTestBaseTime.Add(time.Minute).Format(time.RFC3339),
			ExpiresAt: computeTestBaseTime.Add(2 * time.Minute).Format(time.RFC3339),
		},
		ExpiresAt: computeTestBaseTime.Add(2 * time.Minute).Format(time.RFC3339),
	}
}

func computeTestClaim(offer wire.AssignmentOffer, receipt string) wire.AssignmentClaim {
	return wire.AssignmentClaim{
		AssignmentID: offer.AssignmentID, JobID: offer.JobID, ExecutionID: offer.ExecutionID,
		Epoch: offer.Epoch, SpecHash: offer.SpecHash, ReceiptID: receipt,
	}
}

func computeTestGrant(offer wire.AssignmentOffer) wire.AssignmentGrant {
	return wire.AssignmentGrant{
		AssignmentID: offer.AssignmentID, JobID: offer.JobID, ExecutionID: offer.ExecutionID,
		Epoch: offer.Epoch, LeaseUntil: computeTestBaseTime.Add(3 * time.Minute).Format(time.RFC3339),
	}
}

func computeTestChunk(offer wire.AssignmentOffer, stream string, sequence uint64, offset int64, data []byte, eof bool) wire.LogChunk {
	sum := sha256.Sum256(data)
	return wire.LogChunk{
		JobID: offer.JobID, ExecutionID: offer.ExecutionID, Epoch: offer.Epoch,
		Stream: stream, Sequence: sequence, ByteOffset: offset,
		Data: base64.StdEncoding.EncodeToString(data), SHA256: "sha256:" + hex.EncodeToString(sum[:]), EOF: eof,
	}
}

func computeTestTerminal(job wire.ComputeJobRecord, offer wire.AssignmentOffer) wire.TerminalResult {
	exit := 0
	return wire.TerminalResult{
		SchemaVersion: 1, JobID: job.JobID, NodeID: "node-a", ExecutionID: offer.ExecutionID, Epoch: offer.Epoch,
		Handler: job.Spec.Handler, SourceDigest: job.Spec.SourceDigest,
		StartedAt:  computeTestBaseTime.Add(time.Minute).Format(time.RFC3339),
		EndedAt:    computeTestBaseTime.Add(2 * time.Minute).Format(time.RFC3339),
		DurationMs: 60000, ExitCode: &exit,
	}
}

func computeMustLoad(t *testing.T, store *computeStore, id string) *computeJobState {
	t.Helper()
	state, err := store.Load(id)
	if err != nil {
		t.Fatal(err)
	}
	return state
}

func computeMustCreate(t *testing.T, store *computeStore, job wire.ComputeJobRecord) wire.ComputeJobRecord {
	t.Helper()
	created, err := store.CreateJob(job)
	if err != nil {
		t.Fatal(err)
	}
	return created
}

func TestComputeStoreAppendReplayRoundTrip(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	job := computeMustCreate(t, store, computeTestJob("job-roundtrip", "operator-a", "submit-1"))
	offer := computeTestOffer(job, "assignment-1", "execution-1", 1)
	if err := store.RecordOffer(1, "node-a", offer); err != nil {
		t.Fatal(err)
	}
	claim := computeTestClaim(offer, "receipt-1")
	if err := store.RecordClaim("node-a", claim); err != nil {
		t.Fatal(err)
	}
	grant := computeTestGrant(offer)
	if err := store.RecordGrant(3, "node-a", grant); err != nil {
		t.Fatal(err)
	}
	renewal := wire.LeaseRenewal{JobID: job.JobID, ExecutionID: offer.ExecutionID, Epoch: offer.Epoch, LeaseUntil: computeTestBaseTime.Add(4 * time.Minute).Format(time.RFC3339)}
	if err := store.RecordLeaseRenewal(4, "node-a", renewal); err != nil {
		t.Fatal(err)
	}
	journalAfterRenewal, _ := os.ReadFile(store.eventsPath(job.JobID))
	if err := store.RecordLeaseRenewal(4, "node-a", renewal); err != nil {
		t.Fatalf("duplicate renewal = %v", err)
	}
	journalAfterRenewalRetry, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(journalAfterRenewal, journalAfterRenewalRetry) {
		t.Fatal("duplicate renewal appended")
	}
	chunk := computeTestChunk(offer, "stdout", 1, 0, []byte("ok\n"), true)
	ack, err := store.RecordLogChunk("node-a", chunk)
	if err != nil {
		t.Fatal(err)
	}
	if ack.ContiguousSequence != 1 {
		t.Fatalf("ack = %+v", ack)
	}
	terminal := computeTestTerminal(job, offer)
	if err := store.RecordTerminal("node-a", terminal); err != nil {
		t.Fatal(err)
	}
	journalAfterTerminal, _ := os.ReadFile(store.eventsPath(job.JobID))
	if err := store.RecordTerminal("node-a", terminal); err != nil {
		t.Fatalf("duplicate terminal = %v", err)
	}
	journalAfterTerminalRetry, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(journalAfterTerminal, journalAfterTerminalRetry) {
		t.Fatal("duplicate terminal appended")
	}
	wantSnapshot, err := os.ReadFile(store.snapPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.snapPath(job.JobID)); err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(store.controllerPath()); err != nil {
		t.Fatal(err)
	}
	reopened, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	gotSnapshot, err := os.ReadFile(reopened.snapPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotSnapshot, wantSnapshot) {
		t.Fatalf("full replay did not reconstruct byte-identical state\nwant=%s\ngot=%s", wantSnapshot, gotSnapshot)
	}
	state := computeMustLoad(t, reopened, job.JobID)
	if state.Job.State != "succeeded" || state.Fence.AuthenticatedNodeID != "node-a" || state.Terminal == nil || state.LastAppliedEventSequence != 7 {
		t.Fatalf("replayed state = %+v", state)
	}
	ack, err = reopened.RecordLogChunk("node-a", chunk)
	if err != nil || ack.ContiguousSequence != 1 {
		t.Fatalf("durable duplicate ack = %+v, %v", ack, err)
	}
	controllerRaw, err := os.ReadFile(reopened.controllerPath())
	if err != nil || !bytes.Contains(controllerRaw, []byte(job.JobID)) {
		t.Fatalf("rebuilt controller index = %s, %v", controllerRaw, err)
	}
	for _, path := range []string{
		filepath.Join(dataDir, "compute"), reopened.jobsRoot, reopened.jobDir(job.JobID),
		filepath.Join(reopened.jobDir(job.JobID), "logs"), filepath.Join(reopened.jobDir(job.JobID), "artifacts"),
	} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o700 {
			t.Fatalf("directory %s mode = %v, %v", path, info, err)
		}
	}
	for _, path := range []string{reopened.eventsPath(job.JobID), reopened.snapPath(job.JobID), reopened.controllerPath(), filepath.Join(dataDir, "compute", "version.json")} {
		if info, err := os.Stat(path); err != nil || info.Mode().Perm() != 0o600 {
			t.Fatalf("file %s mode = %v, %v", path, info, err)
		}
	}
}

func TestComputeStoreSnapshotHealingAndTornTail(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	job := computeMustCreate(t, store, computeTestJob("job-heal", "operator", "heal"))
	oldSnapshot, err := os.ReadFile(store.snapPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	offer := computeTestOffer(job, "assignment-heal", "execution-heal", 1)
	if err := store.RecordOffer(1, "node-a", offer); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(store.snapPath(job.JobID), oldSnapshot, 0o600); err != nil {
		t.Fatal(err)
	}
	state := computeMustLoad(t, store, job.JobID)
	if state.LastAppliedEventSequence != 2 || state.Offer == nil {
		t.Fatalf("lagging snapshot did not heal: %+v", state)
	}
	journalBefore, err := os.ReadFile(store.eventsPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schemaVersion":1,"sequence":3`); err != nil {
		t.Fatal(err)
	}
	if err := f.Sync(); err != nil {
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	reopened, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	journalAfter, err := os.ReadFile(reopened.eventsPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(journalAfter, journalBefore) {
		t.Fatalf("torn tail truncation = %q, want %q", journalAfter, journalBefore)
	}
	claim := computeTestClaim(offer, "receipt-heal")
	if err := reopened.RecordClaim("node-a", claim); err != nil {
		t.Fatal(err)
	}
	second, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if got := computeMustLoad(t, second, job.JobID); got.LastAppliedEventSequence != 3 || got.Claim == nil {
		t.Fatalf("append after truncation did not replay: %+v", got)
	}
}

func TestComputeStoreUnterminatedCompleteInvalidEventFailsClosed(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	job := computeMustCreate(t, store, computeTestJob("job-invalid-tail", "operator", "invalid-tail"))
	f, err := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.WriteString(`{"schemaVersion":"invalid"}`); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}
	wantJournal, err := os.ReadFile(store.eventsPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := newComputeStore(dataDir); !errors.Is(err, ErrComputeCorruptStore) {
		t.Fatalf("complete invalid unterminated event error = %v", err)
	}
	gotJournal, err := os.ReadFile(store.eventsPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(gotJournal, wantJournal) {
		t.Fatalf("complete invalid unterminated event changed journal\nwant=%q\ngot=%q", wantJournal, gotJournal)
	}
}

func TestComputeStoreUnterminatedValidEventIsNormalized(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	job := computeMustCreate(t, store, computeTestJob("job-valid-tail", "operator", "valid-tail"))
	event := wire.ComputeJobEvent{
		SchemaVersion: 1,
		Sequence:      2,
		At:            computeTestBaseTime.Format(time.RFC3339),
		Type:          "future.projection",
		JobID:         job.JobID,
	}
	raw, err := json.Marshal(event)
	if err != nil {
		t.Fatal(err)
	}
	f, err := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := f.Write(raw); err != nil {
		_ = f.Close()
		t.Fatal(err)
	}
	if err := f.Close(); err != nil {
		t.Fatal(err)
	}

	reopened, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	journal, err := os.ReadFile(reopened.eventsPath(job.JobID))
	if err != nil {
		t.Fatal(err)
	}
	if len(journal) == 0 || journal[len(journal)-1] != '\n' {
		t.Fatalf("recovered journal was not newline-normalized: %q", journal)
	}
	offer := computeTestOffer(job, "assignment-valid-tail", "execution-valid-tail", 1)
	if err := reopened.RecordOffer(2, "node-a", offer); err != nil {
		t.Fatal(err)
	}
	second, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	state := computeMustLoad(t, second, job.JobID)
	if state.LastAppliedEventSequence != 3 || state.Offer == nil {
		t.Fatalf("append after complete tail recovery did not replay: %+v", state)
	}
}

func TestComputeStoreCorruptionFailsClosedAndSnapshotQuarantines(t *testing.T) {
	t.Parallel()
	t.Run("complete malformed event", func(t *testing.T) {
		dataDir := t.TempDir()
		store, _ := newComputeStore(dataDir)
		job := computeMustCreate(t, store, computeTestJob("job-corrupt-line", "operator", "corrupt-line"))
		f, err := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		_, _ = f.WriteString("{bad json}\n")
		_ = f.Close()
		if _, err := newComputeStore(dataDir); !errors.Is(err, ErrComputeCorruptStore) {
			t.Fatalf("malformed complete event error = %v", err)
		}
	})
	t.Run("malformed event between valid records", func(t *testing.T) {
		dataDir := t.TempDir()
		store, _ := newComputeStore(dataDir)
		job := computeMustCreate(t, store, computeTestJob("job-corrupt-middle", "operator", "corrupt-middle"))
		valid := wire.ComputeJobEvent{SchemaVersion: 1, Sequence: 2, At: computeTestBaseTime.Format(time.RFC3339), Type: "future.projection", JobID: job.JobID}
		validRaw, _ := json.Marshal(valid)
		f, err := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := f.WriteString("{bad json}\n"); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if _, err := f.Write(append(validRaw, '\n')); err != nil {
			_ = f.Close()
			t.Fatal(err)
		}
		if err := f.Close(); err != nil {
			t.Fatal(err)
		}
		wantJournal, err := os.ReadFile(store.eventsPath(job.JobID))
		if err != nil {
			t.Fatal(err)
		}
		if _, err := newComputeStore(dataDir); !errors.Is(err, ErrComputeCorruptStore) {
			t.Fatalf("malformed mid-file event error = %v", err)
		}
		gotJournal, err := os.ReadFile(store.eventsPath(job.JobID))
		if err != nil {
			t.Fatal(err)
		}
		if !bytes.Equal(gotJournal, wantJournal) {
			t.Fatalf("malformed mid-file event changed journal\nwant=%q\ngot=%q", wantJournal, gotJournal)
		}
	})
	t.Run("sequence gap", func(t *testing.T) {
		dataDir := t.TempDir()
		store, _ := newComputeStore(dataDir)
		job := computeMustCreate(t, store, computeTestJob("job-gap", "operator", "gap"))
		event := wire.ComputeJobEvent{SchemaVersion: 1, Sequence: 3, At: computeTestBaseTime.Format(time.RFC3339), Type: "future.projection", JobID: job.JobID}
		raw, _ := json.Marshal(event)
		f, _ := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
		_, _ = f.Write(append(raw, '\n'))
		_ = f.Close()
		if _, err := newComputeStore(dataDir); !errors.Is(err, ErrComputeCorruptStore) {
			t.Fatalf("gap error = %v", err)
		}
	})
	t.Run("conflicting later duplicate", func(t *testing.T) {
		dataDir := t.TempDir()
		store, _ := newComputeStore(dataDir)
		job := computeMustCreate(t, store, computeTestJob("job-conflicting-event", "operator", "conflicting-event"))
		offer := computeTestOffer(job, "assignment-conflict", "execution-conflict", 1)
		if err := store.RecordOffer(1, "node-a", offer); err != nil {
			t.Fatal(err)
		}
		conflict := offer
		conflict.ExpiresAt = computeTestBaseTime.Add(20 * time.Minute).Format(time.RFC3339)
		stored, err := makeComputeStoredRecord(computeRecordID(computeEventAssignmentOffered, conflict), "node-a", computeDispositionAccepted, conflict)
		if err != nil {
			t.Fatal(err)
		}
		payload, _ := json.Marshal(stored)
		event := wire.ComputeJobEvent{SchemaVersion: 1, Sequence: 3, At: computeTestBaseTime.Format(time.RFC3339), Type: computeEventAssignmentOffered, JobID: job.JobID, ExecutionID: offer.ExecutionID, Epoch: offer.Epoch, Payload: payload}
		raw, _ := json.Marshal(event)
		f, _ := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
		_, _ = f.Write(append(raw, '\n'))
		_ = f.Close()
		if _, err := newComputeStore(dataDir); !errors.Is(err, ErrComputeCorruptStore) || !errors.Is(err, ErrComputeConflictingDuplicate) {
			t.Fatalf("conflicting duplicate replay error = %v", err)
		}
	})
	t.Run("snapshot ahead", func(t *testing.T) {
		dataDir := t.TempDir()
		store, _ := newComputeStore(dataDir)
		job := computeMustCreate(t, store, computeTestJob("job-ahead", "operator", "ahead"))
		state := computeMustLoad(t, store, job.JobID)
		state.LastAppliedEventSequence++
		raw, _ := json.Marshal(state)
		if err := os.WriteFile(store.snapPath(job.JobID), raw, 0o600); err != nil {
			t.Fatal(err)
		}
		if _, err := newComputeStore(dataDir); !errors.Is(err, ErrComputeCorruptStore) {
			t.Fatalf("snapshot-ahead error = %v", err)
		}
	})
	t.Run("corrupt snapshot", func(t *testing.T) {
		dataDir := t.TempDir()
		store, _ := newComputeStore(dataDir)
		job := computeMustCreate(t, store, computeTestJob("job-corrupt-snap", "operator", "corrupt-snap"))
		if err := os.WriteFile(store.snapPath(job.JobID), []byte("{"), 0o600); err != nil {
			t.Fatal(err)
		}
		reopened, err := newComputeStore(dataDir)
		if err != nil {
			t.Fatal(err)
		}
		if _, err := os.Stat(store.snapPath(job.JobID) + ".corrupt"); err != nil {
			t.Fatalf("corrupt snapshot not quarantined: %v", err)
		}
		if got := computeMustLoad(t, reopened, job.JobID); got.Job.JobID != job.JobID {
			t.Fatalf("rebuilt snapshot = %+v", got)
		}
	})
}

func TestComputeStoreReaderMarkerFencing(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	if err := store.RaiseMinReaderVersion(computeStoreReaderVersion + 1); err == nil {
		t.Fatal("raising above compiled reader unexpectedly succeeded")
	}
	marker, _ := json.Marshal(struct {
		MinReaderVersion int `json:"minReaderVersion"`
	}{computeStoreReaderVersion + 1})
	if err := os.WriteFile(filepath.Join(dataDir, "compute", "version.json"), marker, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := newComputeStore(dataDir); err == nil || !strings.Contains(err.Error(), "requires reader version") {
		t.Fatalf("too-new marker error = %v", err)
	}
}

func TestComputeStoreUnknownAdditiveEventAdvancesSequence(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, _ := newComputeStore(dataDir)
	job := computeMustCreate(t, store, computeTestJob("job-additive", "operator", "additive"))
	event := wire.ComputeJobEvent{
		SchemaVersion: 1, Sequence: 2, At: computeTestBaseTime.Format(time.RFC3339),
		Type: "future.additive_projection", JobID: job.JobID, Payload: json.RawMessage(`{"optional":true}`),
	}
	raw, _ := json.Marshal(event)
	f, _ := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write(append(raw, '\n'))
	_ = f.Close()
	if err := os.Remove(store.snapPath(job.JobID)); err != nil {
		t.Fatal(err)
	}
	state := computeMustLoad(t, store, job.JobID)
	if state.LastAppliedEventSequence != 2 || state.Job.State != "queued" {
		t.Fatalf("additive replay = %+v", state)
	}
	offer := computeTestOffer(job, "assignment-additive", "execution-additive", 1)
	if err := store.RecordOffer(2, "node-a", offer); err != nil {
		t.Fatalf("append after additive event = %v", err)
	}
}

func TestComputeStoreCanonicalIdempotencyScopes(t *testing.T) {
	t.Parallel()
	var specA, specB wire.ComputeJobSpec
	if err := json.Unmarshal([]byte(`{"schemaVersion":1,"jobKind":"repo.verify.v1","handler":{"handlerId":"h","version":1},"handlerProfile":"p","workspaceId":"w","revision":"r","sourceDigest":"sha256:a","dataClass":"public","requirements":{"minimumCpu":2,"os":["linux"],"requiredFeatures":0},"inputs":[],"priority":"normal","timeoutSec":1,"cancelPolicy":"lease","maxAttempts":1,"requiredFeatures":0}`), &specA); err != nil {
		t.Fatal(err)
	}
	if err := json.Unmarshal([]byte(`{"requiredFeatures":0,"maxAttempts":1,"cancelPolicy":"lease","timeoutSec":1,"priority":"normal","inputs":[],"requirements":{"requiredFeatures":0,"os":["linux"],"minimumCpu":2},"dataClass":"public","sourceDigest":"sha256:a","revision":"r","workspaceId":"w","handlerProfile":"p","handler":{"version":1,"handlerId":"h"},"jobKind":"repo.verify.v1","schemaVersion":1}`), &specB); err != nil {
		t.Fatal(err)
	}
	hashA, _ := computeSpecHash(specA)
	hashB, _ := computeSpecHash(specB)
	if hashA != hashB {
		t.Fatalf("canonical hashes differ: %s != %s", hashA, hashB)
	}
	dataDir := t.TempDir()
	store, _ := newComputeStore(dataDir)
	first := computeTestJob("job-idem-a", "operator-a", "same-key")
	first.Spec = specA
	created := computeMustCreate(t, store, first)
	journalBefore, _ := os.ReadFile(store.eventsPath(created.JobID))
	retry := first
	retry.JobID = "job-idem-retry"
	retry.Spec = specB
	got, err := store.CreateJob(retry)
	if err != nil || got.JobID != created.JobID {
		t.Fatalf("same scope/spec retry = %+v, %v", got, err)
	}
	journalAfter, _ := os.ReadFile(store.eventsPath(created.JobID))
	if !bytes.Equal(journalAfter, journalBefore) {
		t.Fatal("idempotent retry appended another event")
	}
	reopened, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got, err = reopened.CreateJob(retry)
	if err != nil || got.JobID != created.JobID {
		t.Fatalf("restart idempotency = %+v, %v", got, err)
	}
	changed := retry
	changed.Spec.TimeoutSec++
	if _, err := reopened.CreateJob(changed); !errors.Is(err, ErrComputeIdempotencyConflict) {
		t.Fatalf("changed spec error = %v", err)
	}
	isolated := retry
	isolated.JobID = "job-idem-b"
	isolated.Authz.Principal = "operator-b"
	if got, err := reopened.CreateJob(isolated); err != nil || got.JobID != isolated.JobID {
		t.Fatalf("principal-isolated scope = %+v, %v", got, err)
	}
}

func TestComputeStoreDuplicatesControllerFencingAndWorkerFencing(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, _ := newComputeStore(dataDir)
	job := computeMustCreate(t, store, computeTestJob("job-fence", "operator", "fence"))
	offer := computeTestOffer(job, "assignment-one", "execution-one", 1)
	if err := store.RecordOffer(1, "node-a", offer); err != nil {
		t.Fatal(err)
	}
	eventsAfterOffer, _ := os.ReadFile(store.eventsPath(job.JobID))
	if err := store.RecordOffer(1, "node-a", offer); err != nil {
		t.Fatalf("identical controller retry = %v", err)
	}
	eventsAfterRetry, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(eventsAfterOffer, eventsAfterRetry) {
		t.Fatal("duplicate offer appended")
	}
	staleStore, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	secondOffer := computeTestOffer(job, "assignment-two", "execution-two", 2)
	if err := staleStore.RecordOffer(1, "node-a", secondOffer); !errors.Is(err, ErrComputeStaleControllerSequence) {
		t.Fatalf("stale controller error = %v", err)
	}
	eventsAfterStaleController, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(eventsAfterRetry, eventsAfterStaleController) {
		t.Fatal("stale controller appended")
	}

	staleClaim := computeTestClaim(offer, "stale-receipt")
	staleClaim.AssignmentID = "not-current"
	beforeStaleWorker := len(eventsAfterStaleController)
	if err := store.RecordClaim("node-wrong", staleClaim); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("stale worker error = %v", err)
	}
	afterFirstStale, _ := os.ReadFile(store.eventsPath(job.JobID))
	if len(afterFirstStale) <= beforeStaleWorker {
		t.Fatal("stale worker was not audited")
	}
	if err := store.RecordClaim("node-wrong", staleClaim); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("stale retry error = %v", err)
	}
	afterSecondStale, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(afterFirstStale, afterSecondStale) {
		t.Fatal("repeated stale worker appended")
	}
	if state := computeMustLoad(t, store, job.JobID); state.Claim != nil || state.Job.State != "offered" {
		t.Fatalf("stale audit mutated projection: %+v", state)
	}

	claim := computeTestClaim(offer, "receipt-one")
	if err := store.RecordClaim("node-a", claim); err != nil {
		t.Fatal(err)
	}
	claimBytes, _ := os.ReadFile(store.eventsPath(job.JobID))
	if err := store.RecordClaim("node-a", claim); err != nil {
		t.Fatalf("duplicate claim = %v", err)
	}
	claimBytes2, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(claimBytes, claimBytes2) {
		t.Fatal("duplicate claim appended")
	}
	conflict := claim
	conflict.ReceiptID = "different-receipt"
	if err := store.RecordClaim("node-a", conflict); !errors.Is(err, ErrComputeConflictingDuplicate) {
		t.Fatalf("conflicting natural record reuse = %v", err)
	}

	// Store the same accepted claim again at a later journal sequence. Replay
	// must only advance sequence, never apply the claim transition twice.
	lines := bytes.Split(bytes.TrimSpace(claimBytes), []byte("\n"))
	var duplicate wire.ComputeJobEvent
	if err := json.Unmarshal(lines[len(lines)-1], &duplicate); err != nil {
		t.Fatal(err)
	}
	duplicate.Sequence++
	duplicate.At = computeTestBaseTime.Add(10 * time.Minute).Format(time.RFC3339)
	duplicateRaw, _ := json.Marshal(duplicate)
	f, _ := os.OpenFile(store.eventsPath(job.JobID), os.O_WRONLY|os.O_APPEND, 0o600)
	_, _ = f.Write(append(duplicateRaw, '\n'))
	_ = f.Sync()
	_ = f.Close()
	if err := os.Remove(store.snapPath(job.JobID)); err != nil {
		t.Fatal(err)
	}
	reopened, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	state := computeMustLoad(t, reopened, job.JobID)
	if state.LastAppliedEventSequence != 5 || state.Claim == nil || state.Job.State != "claimed" {
		t.Fatalf("later-sequence duplicate replay = %+v", state)
	}
	grant := computeTestGrant(offer)
	if err := reopened.RecordGrant(5, "node-a", grant); err != nil {
		t.Fatal(err)
	}
	wrongNodeRenewal := wire.LeaseRenewal{JobID: job.JobID, ExecutionID: offer.ExecutionID, Epoch: offer.Epoch, LeaseUntil: computeTestBaseTime.Add(5 * time.Minute).Format(time.RFC3339)}
	beforeWrongRenewal, _ := os.ReadFile(reopened.eventsPath(job.JobID))
	if err := reopened.RecordLeaseRenewal(6, "node-wrong", wrongNodeRenewal); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("wrong-node renewal = %v", err)
	}
	afterWrongRenewal, _ := os.ReadFile(reopened.eventsPath(job.JobID))
	if !bytes.Equal(beforeWrongRenewal, afterWrongRenewal) {
		t.Fatal("wrong-node renewal appended")
	}
	wrongFenceChunk := computeTestChunk(offer, "system", 1, 0, []byte("late"), false)
	wrongFenceChunk.Epoch = 99
	if _, err := reopened.RecordLogChunk("node-a", wrongFenceChunk); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("wrong epoch chunk = %v", err)
	}
	seqAfterAudit := computeMustLoad(t, reopened, job.JobID).LastAppliedEventSequence
	lapsed := computeExecutionLapsed{
		AssignmentID: offer.AssignmentID, JobID: job.JobID, ExecutionID: offer.ExecutionID, Epoch: offer.Epoch,
		ControllerAt:      computeTestBaseTime.Add(3 * time.Minute).Format(time.RFC3339),
		EffectiveDeadline: computeTestGrant(offer).LeaseUntil, Reason: "lease_lost", Retry: true,
	}
	if err := reopened.RecordExecutionLapsed(seqAfterAudit, "node-a", lapsed); err != nil {
		t.Fatal(err)
	}
	seqAfterAudit = computeMustLoad(t, reopened, job.JobID).LastAppliedEventSequence
	secondOffer = computeTestOffer(job, "assignment-two", "execution-two", 2)
	if err := reopened.RecordOffer(seqAfterAudit, "node-b", secondOffer); err != nil {
		t.Fatal(err)
	}
	late := computeTestChunk(offer, "stderr", 1, 0, []byte("old"), false)
	if _, err := reopened.RecordLogChunk("node-a", late); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("late epoch-N chunk = %v", err)
	}
	current := computeMustLoad(t, reopened, job.JobID)
	if current.Fence.Epoch != 2 || current.Fence.AuthenticatedNodeID != "node-b" || current.Job.State != "offered" || len(current.LogStreams) != 0 {
		t.Fatalf("late epoch mutated current projection: %+v", current)
	}
	if err := reopened.RecordClaim("node-a", claim); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("previously accepted epoch-N claim was not fenced after epoch N+1: %v", err)
	}
}

func TestComputeStoreLogValidationAndDurableAck(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, _ := newComputeStore(dataDir)
	job := computeMustCreate(t, store, computeTestJob("job-logs", "operator", "logs"))
	offer := computeTestOffer(job, "assignment-logs", "execution-logs", 1)
	if err := store.RecordOffer(1, "node-a", offer); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordClaim("node-a", computeTestClaim(offer, "receipt-logs")); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordGrant(3, "node-a", computeTestGrant(offer)); err != nil {
		t.Fatal(err)
	}
	base := computeTestChunk(offer, "stdout", 1, 0, []byte("hello"), false)
	badBase64 := base
	badBase64.Data = "not+standard===bad"
	base64Whitespace := base
	base64Whitespace.Data = "aGVs\nbG8="
	badDigest := base
	badDigest.SHA256 = "sha256:" + strings.Repeat("0", 64)
	badOffset := base
	badOffset.ByteOffset = 1
	gap := base
	gap.Sequence = 2
	oversize := computeTestChunk(offer, "stdout", 1, 0, bytes.Repeat([]byte("x"), 64*1024+1), false)
	badStream := base
	badStream.Stream = "debug"
	before, _ := os.ReadFile(store.eventsPath(job.JobID))
	for name, chunk := range map[string]wire.LogChunk{
		"base64": badBase64, "base64-whitespace": base64Whitespace, "digest": badDigest, "offset": badOffset,
		"gap": gap, "oversize": oversize, "stream": badStream,
	} {
		if ack, err := store.RecordLogChunk("node-a", chunk); err == nil || ack.ContiguousSequence != 0 {
			t.Errorf("%s invalid chunk ack=%+v err=%v", name, ack, err)
		}
	}
	afterInvalid, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(before, afterInvalid) {
		t.Fatal("invalid log chunk appended")
	}
	first := computeTestChunk(offer, "stdout", 1, 0, []byte("hello"), false)
	ack, err := store.RecordLogChunk("node-a", first)
	if err != nil || ack.ContiguousSequence != 1 {
		t.Fatalf("first ack=%+v err=%v", ack, err)
	}
	stderr := computeTestChunk(offer, "stderr", 1, 0, []byte("warning"), false)
	stderrAck, err := store.RecordLogChunk("node-a", stderr)
	if err != nil || stderrAck.ContiguousSequence != 1 {
		t.Fatalf("independent stderr ack=%+v err=%v", stderrAck, err)
	}
	second := computeTestChunk(offer, "stdout", 2, 5, []byte(" world"), true)
	ack, err = store.RecordLogChunk("node-a", second)
	if err != nil || ack.ContiguousSequence != 2 {
		t.Fatalf("second ack=%+v err=%v", ack, err)
	}
	journalAfterAck, _ := os.ReadFile(store.eventsPath(job.JobID))
	ack, err = store.RecordLogChunk("node-a", second)
	if err != nil || ack.ContiguousSequence != 2 {
		t.Fatalf("duplicate ack=%+v err=%v", ack, err)
	}
	journalAfterDuplicate, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(journalAfterAck, journalAfterDuplicate) {
		t.Fatal("duplicate chunk appended")
	}
	afterEOF := computeTestChunk(offer, "stdout", 3, 11, []byte("!"), false)
	if _, err := store.RecordLogChunk("node-a", afterEOF); err == nil {
		t.Fatal("chunk after EOF accepted")
	}
	if err := os.Remove(store.snapPath(job.JobID)); err != nil {
		t.Fatal(err)
	}
	reopened, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	ack, err = reopened.RecordLogChunk("node-a", second)
	if err != nil || ack.ContiguousSequence != 2 {
		t.Fatalf("replayed durable ACK = %+v, %v", ack, err)
	}
}

func TestComputeTerminalStateProjection(t *testing.T) {
	t.Parallel()
	zero, nonzero := 0, 1
	for name, test := range map[string]struct {
		terminal wire.TerminalResult
		want     string
	}{
		"success":   {wire.TerminalResult{ExitCode: &zero}, "succeeded"},
		"nonzero":   {wire.TerminalResult{ExitCode: &nonzero}, "failed"},
		"cancelled": {wire.TerminalResult{ExitCode: &zero, ErrorKind: "cancelled"}, "cancelled"},
		"timeout":   {wire.TerminalResult{ErrorKind: "timeout"}, "timed_out"},
		"internal":  {wire.TerminalResult{ErrorKind: "internal"}, "failed"},
		"no_exit":   {wire.TerminalResult{}, "failed"},
	} {
		if got := terminalComputeState(test.terminal); got != test.want {
			t.Errorf("%s state = %q, want %q", name, got, test.want)
		}
	}
}

func TestComputeStoreVersionTwoMarkerAndOfferExpiry(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	markerPath := filepath.Join(dataDir, "compute", "version.json")
	readMarker := func() int {
		t.Helper()
		raw, err := os.ReadFile(markerPath)
		if err != nil {
			t.Fatal(err)
		}
		var marker struct {
			MinReaderVersion int `json:"minReaderVersion"`
		}
		if err := json.Unmarshal(raw, &marker); err != nil {
			t.Fatal(err)
		}
		return marker.MinReaderVersion
	}
	if got := readMarker(); got != computeInitialStoreReaderVersion {
		t.Fatalf("fresh marker = %d, want %d", got, computeInitialStoreReaderVersion)
	}
	job := computeMustCreate(t, store, computeTestJob("job-offer-expiry", "operator", "offer-expiry"))
	offer := computeTestOffer(job, "assignment-expiry", "execution-expiry", 1)
	if err := store.RecordOffer(1, "node-a", offer); err != nil {
		t.Fatal(err)
	}
	if got := readMarker(); got != computeInitialStoreReaderVersion {
		t.Fatalf("offer unexpectedly raised marker to %d", got)
	}
	early := computeAssignmentExpired{
		AssignmentID: offer.AssignmentID, JobID: job.JobID, ExecutionID: offer.ExecutionID, Epoch: offer.Epoch,
		ControllerAt: computeTestBaseTime.Add(119 * time.Second).Format(time.RFC3339), OfferDeadline: offer.ExpiresAt,
	}
	journalBefore, _ := os.ReadFile(store.eventsPath(job.JobID))
	if err := store.RecordAssignmentExpired(2, "node-a", early); !errors.Is(err, ErrComputeExpiredOffer) {
		t.Fatalf("early expiry error = %v", err)
	}
	journalAfter, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(journalBefore, journalAfter) {
		t.Fatal("early expiry mutated journal")
	}
	if got := readMarker(); got != computeStoreReaderVersion {
		t.Fatalf("semantic append preparation marker = %d, want %d", got, computeStoreReaderVersion)
	}
	early.ControllerAt = offer.ExpiresAt
	if err := store.RecordAssignmentExpired(2, "node-a", early); err != nil {
		t.Fatal(err)
	}
	state := computeMustLoad(t, store, job.JobID)
	if state.Job.State != "queued" || state.Job.QueueReason != "offer_expired" || state.Job.Attempt != 0 ||
		state.Offer != nil || state.Claim != nil || state.Grant != nil || state.Fence.Epoch != 1 || state.LatestAssignmentExpiry == nil {
		t.Fatalf("expired projection = %+v", state)
	}
	replacement := computeTestOffer(job, "assignment-replacement", "execution-replacement", 2)
	if err := store.RecordOffer(state.LastAppliedEventSequence, "node-b", replacement); err != nil {
		t.Fatal(err)
	}
	lateClaim := computeTestClaim(offer, "receipt-late")
	if err := store.RecordClaim("node-a", lateClaim); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("late claim error = %v", err)
	}
	afterLate := computeMustLoad(t, store, job.JobID)
	if afterLate.Job.State != "offered" || afterLate.Offer == nil || afterLate.Offer.AssignmentID != replacement.AssignmentID ||
		afterLate.Fence.Epoch != 2 || afterLate.Fence.AuthenticatedNodeID != "node-b" {
		t.Fatalf("late claim changed replacement = %+v", afterLate)
	}
	seqAfterLate := afterLate.LastAppliedEventSequence
	if err := store.RecordClaim("node-a", lateClaim); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("duplicate late claim error = %v", err)
	}
	if got := computeMustLoad(t, store, job.JobID).LastAppliedEventSequence; got != seqAfterLate {
		t.Fatalf("duplicate stale claim appended: %d -> %d", seqAfterLate, got)
	}

	// Reader version 2 remains able to reconstruct a P1b marker-1 store.
	legacyDir := t.TempDir()
	legacy, err := newComputeStore(legacyDir)
	if err != nil {
		t.Fatal(err)
	}
	legacyJob := computeMustCreate(t, legacy, computeTestJob("job-marker-v1", "operator", "marker-v1"))
	if err := legacy.RecordOffer(1, "node-a", computeTestOffer(legacyJob, "assignment-marker-v1", "execution-marker-v1", 1)); err != nil {
		t.Fatal(err)
	}
	if _, err := newComputeStore(legacyDir); err != nil {
		t.Fatalf("reader v2 did not open marker-v1 store: %v", err)
	}
}

func TestComputeStoreLeaseLapseAttemptsReplayAndStaleTerminal(t *testing.T) {
	t.Parallel()
	dataDir := t.TempDir()
	store, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	jobTemplate := computeTestJob("job-lapse", "operator", "lapse")
	jobTemplate.Spec.MaxAttempts = 0 // exact D11 tuple defaults to two attempts.
	job := computeMustCreate(t, store, jobTemplate)
	firstOffer := computeTestOffer(job, "assignment-lapse-1", "execution-lapse-1", 1)
	if err := store.RecordOffer(1, "node-a", firstOffer); err != nil {
		t.Fatal(err)
	}
	if err := store.RecordClaim("node-a", computeTestClaim(firstOffer, "receipt-lapse-1")); err != nil {
		t.Fatal(err)
	}
	firstGrant := computeTestGrant(firstOffer)
	if err := store.RecordGrant(3, "node-a", firstGrant); err != nil {
		t.Fatal(err)
	}
	markerRaw, err := os.ReadFile(filepath.Join(dataDir, "compute", "version.json"))
	if err != nil {
		t.Fatal(err)
	}
	var marker struct {
		MinReaderVersion int `json:"minReaderVersion"`
	}
	if err := json.Unmarshal(markerRaw, &marker); err != nil || marker.MinReaderVersion != computeStoreReaderVersion {
		t.Fatalf("grant reader marker = %+v, err=%v", marker, err)
	}
	state := computeMustLoad(t, store, job.JobID)
	if state.Job.Attempt != 1 {
		t.Fatalf("first grant attempt = %d", state.Job.Attempt)
	}
	journalAfterGrant, _ := os.ReadFile(store.eventsPath(job.JobID))
	if err := store.RecordGrant(3, "node-a", firstGrant); err != nil {
		t.Fatalf("duplicate grant = %v", err)
	}
	journalAfterDuplicate, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(journalAfterGrant, journalAfterDuplicate) || computeMustLoad(t, store, job.JobID).Job.Attempt != 1 {
		t.Fatal("duplicate grant appended or consumed an attempt")
	}
	firstLapse := computeExecutionLapsed{
		AssignmentID: firstOffer.AssignmentID, JobID: job.JobID, ExecutionID: firstOffer.ExecutionID, Epoch: 1,
		ControllerAt: firstGrant.LeaseUntil, EffectiveDeadline: firstGrant.LeaseUntil, Reason: "lease_lost", Retry: true,
	}
	earlyLapse := firstLapse
	earlyLapse.ControllerAt = computeTestBaseTime.Add(3*time.Minute - time.Second).Format(time.RFC3339)
	journalBeforeLapse, _ := os.ReadFile(store.eventsPath(job.JobID))
	if err := store.RecordExecutionLapsed(4, "node-a", earlyLapse); !errors.Is(err, ErrComputeExpiredLease) {
		t.Fatalf("early lapse error = %v", err)
	}
	journalAfterEarlyLapse, _ := os.ReadFile(store.eventsPath(job.JobID))
	if !bytes.Equal(journalBeforeLapse, journalAfterEarlyLapse) {
		t.Fatal("early lease lapse mutated journal")
	}
	if err := store.RecordExecutionLapsed(4, "node-a", firstLapse); err != nil {
		t.Fatal(err)
	}
	state = computeMustLoad(t, store, job.JobID)
	if state.Job.State != "retry_wait" || state.Job.QueueReason != "lease_lost" || state.Job.Attempt != 1 ||
		state.Offer != nil || state.Grant != nil || state.LatestExecutionLapse == nil || !state.LatestExecutionLapse.Retry {
		t.Fatalf("retry lapse projection = %+v", state)
	}
	lateChunk := computeTestChunk(firstOffer, "system", 1, 0, []byte("late after lapse"), true)
	if _, err := store.RecordLogChunk("node-a", lateChunk); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("late log error = %v", err)
	}
	state = computeMustLoad(t, store, job.JobID)
	seqAfterLateChunk := state.LastAppliedEventSequence
	if _, err := store.RecordLogChunk("node-a", lateChunk); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("duplicate late log error = %v", err)
	}
	if got := computeMustLoad(t, store, job.JobID); got.LastAppliedEventSequence != seqAfterLateChunk || len(got.LogStreams) != 0 {
		t.Fatalf("duplicate stale log changed projection = %+v", got)
	}
	state = computeMustLoad(t, store, job.JobID)

	secondOffer := computeTestOffer(job, "assignment-lapse-2", "execution-lapse-2", 2)
	if err := store.RecordOffer(state.LastAppliedEventSequence, "node-b", secondOffer); err != nil {
		t.Fatal(err)
	}
	lateTerminal := computeTestTerminal(job, firstOffer)
	if err := store.RecordTerminal("node-a", lateTerminal); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("late terminal error = %v", err)
	}
	state = computeMustLoad(t, store, job.JobID)
	if state.Job.State != "offered" || state.Fence.Epoch != 2 || state.Offer == nil || state.Offer.AssignmentID != secondOffer.AssignmentID {
		t.Fatalf("late terminal changed current assignment = %+v", state)
	}
	seqAfterStale := state.LastAppliedEventSequence
	if err := store.RecordTerminal("node-a", lateTerminal); !errors.Is(err, ErrComputeStaleExecutionFence) {
		t.Fatalf("duplicate stale terminal error = %v", err)
	}
	if got := computeMustLoad(t, store, job.JobID).LastAppliedEventSequence; got != seqAfterStale {
		t.Fatal("duplicate stale terminal appended")
	}
	if err := store.RecordClaim("node-b", computeTestClaim(secondOffer, "receipt-lapse-2")); err != nil {
		t.Fatal(err)
	}
	secondGrant := computeTestGrant(secondOffer)
	secondGrant.LeaseUntil = computeTestBaseTime.Add(6 * time.Minute).Format(time.RFC3339)
	state = computeMustLoad(t, store, job.JobID)
	if err := store.RecordGrant(state.LastAppliedEventSequence, "node-b", secondGrant); err != nil {
		t.Fatal(err)
	}
	state = computeMustLoad(t, store, job.JobID)
	if state.Job.Attempt != 2 {
		t.Fatalf("second grant attempt = %d", state.Job.Attempt)
	}
	secondLapse := computeExecutionLapsed{
		AssignmentID: secondOffer.AssignmentID, JobID: job.JobID, ExecutionID: secondOffer.ExecutionID, Epoch: 2,
		ControllerAt: secondGrant.LeaseUntil, EffectiveDeadline: secondGrant.LeaseUntil, Reason: "lease_lost", Retry: false,
	}
	if err := store.RecordExecutionLapsed(state.LastAppliedEventSequence, "node-b", secondLapse); err != nil {
		t.Fatal(err)
	}
	want := computeMustLoad(t, store, job.JobID)
	if want.Job.State != "failed" || want.Job.Attempt != 2 || want.Fence.Epoch != 2 || want.LatestExecutionLapse == nil || want.LatestExecutionLapse.Retry {
		t.Fatalf("exhausted lapse projection = %+v", want)
	}
	if err := os.Remove(store.snapPath(job.JobID)); err != nil {
		t.Fatal(err)
	}
	reopened, err := newComputeStore(dataDir)
	if err != nil {
		t.Fatal(err)
	}
	got := computeMustLoad(t, reopened, job.JobID)
	if got.Job.State != want.Job.State || got.Job.Attempt != want.Job.Attempt || got.Fence != want.Fence ||
		got.LastAppliedEventSequence != want.LastAppliedEventSequence || got.LatestExecutionLapse == nil || *got.LatestExecutionLapse != *want.LatestExecutionLapse {
		t.Fatalf("journal-only lapse replay\nwant=%+v\ngot=%+v", want, got)
	}
}

func TestComputeStoreNonD11ZeroMaxAttemptsDefaultsToOne(t *testing.T) {
	spec := wire.ComputeJobSpec{MaxAttempts: 0, JobKind: "other", Handler: wire.HandlerVersion{HandlerID: "other", Version: 1}, HandlerProfile: "other.v1"}
	if got := computeEffectiveMaxAttempts(spec); got != 1 {
		t.Fatalf("non-D11 default = %d, want 1", got)
	}
	spec.MaxAttempts = 7
	if got := computeEffectiveMaxAttempts(spec); got != 7 {
		t.Fatalf("explicit attempts = %d, want 7", got)
	}
}
