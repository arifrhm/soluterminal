package main

// D2/D8 compute persistence. Per-job events.jsonl is the sole authority:
// append+fsync precedes an atomic job.json projection, while controller.json
// is rebuilt from job.created records. Worker writes are authenticated by the
// caller-supplied node identity and fenced by (job, execution, node, epoch).

import (
	"bytes"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode"

	"soluterminal-shared/wire"
)

const computeEventSchemaVersion = 1

type computeStore struct {
	root     string // <dataDir>/compute
	jobsRoot string
	mu       sync.Mutex
	index    map[string]computeIdempotencyEntry
}

func newComputeStore(dataDir string) (*computeStore, error) {
	root := filepath.Join(dataDir, "compute")
	if err := ensureComputePrivateDir(root); err != nil {
		return nil, err
	}
	if err := loadComputeReaderMarker(root); err != nil {
		return nil, err
	}
	if _, err := os.Stat(filepath.Join(root, "version.json")); err == nil {
		if err := os.Chmod(filepath.Join(root, "version.json"), 0o600); err != nil {
			return nil, err
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	jobsRoot := filepath.Join(root, "jobs")
	if err := ensureComputePrivateDir(jobsRoot); err != nil {
		return nil, err
	}
	s := &computeStore{root: root, jobsRoot: jobsRoot, index: make(map[string]computeIdempotencyEntry)}
	if _, err := os.Stat(filepath.Join(root, "version.json")); os.IsNotExist(err) {
		if err := s.raiseMinReaderVersionLocked(computeInitialStoreReaderVersion); err != nil {
			return nil, err
		}
	} else if err != nil {
		return nil, err
	}
	if raw, err := os.ReadFile(s.controllerPath()); err == nil {
		var projection computeControllerState
		if json.Unmarshal(raw, &projection) != nil {
			quarantineCorruptState(s.controllerPath(), ErrComputeCorruptStore)
		}
	} else if !os.IsNotExist(err) {
		return nil, err
	}
	if err := s.rebuildControllerIndexLocked(); err != nil {
		return nil, err
	}
	return s, nil
}

func ensureComputePrivateDir(path string) error {
	if err := os.MkdirAll(path, 0o700); err != nil {
		return err
	}
	return os.Chmod(path, 0o700)
}

func loadComputeReaderMarker(root string) error {
	raw, err := os.ReadFile(filepath.Join(root, "version.json"))
	if os.IsNotExist(err) {
		return nil
	}
	if err != nil {
		return err
	}
	var marker struct {
		MinReaderVersion int `json:"minReaderVersion"`
	}
	if err := json.Unmarshal(raw, &marker); err != nil {
		return fmt.Errorf("compute version marker: %w", err)
	}
	if marker.MinReaderVersion < 0 {
		return fmt.Errorf("compute version marker: %w: negative reader version", ErrComputeCorruptStore)
	}
	if marker.MinReaderVersion > computeStoreReaderVersion {
		return fmt.Errorf("compute store requires reader version %d; binary supports %d", marker.MinReaderVersion, computeStoreReaderVersion)
	}
	return nil
}

func (s *computeStore) RaiseMinReaderVersion(n int) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.raiseMinReaderVersionLocked(n)
}

func (s *computeStore) raiseMinReaderVersionLocked(n int) error {
	if n < 0 || n > computeStoreReaderVersion {
		return fmt.Errorf("cannot raise compute reader version to %d; binary supports %d", n, computeStoreReaderVersion)
	}
	path := filepath.Join(s.root, "version.json")
	current := 0
	if raw, err := os.ReadFile(path); err == nil {
		var marker struct {
			MinReaderVersion int `json:"minReaderVersion"`
		}
		if err := json.Unmarshal(raw, &marker); err != nil {
			return fmt.Errorf("compute version marker: %w", err)
		}
		if marker.MinReaderVersion > computeStoreReaderVersion {
			return fmt.Errorf("compute store requires reader version %d; binary supports %d", marker.MinReaderVersion, computeStoreReaderVersion)
		}
		current = marker.MinReaderVersion
	} else if !os.IsNotExist(err) {
		return err
	}
	if current >= n {
		return nil
	}
	raw, _ := json.MarshalIndent(struct {
		MinReaderVersion int `json:"minReaderVersion"`
	}{n}, "", "  ")
	return writeFileAtomicSynced(path, raw, 0o600)
}

func (s *computeStore) controllerPath() string    { return filepath.Join(s.root, "controller.json") }
func (s *computeStore) jobDir(id string) string   { return filepath.Join(s.jobsRoot, id) }
func (s *computeStore) snapPath(id string) string { return filepath.Join(s.jobDir(id), "job.json") }
func (s *computeStore) eventsPath(id string) string {
	return filepath.Join(s.jobDir(id), "events.jsonl")
}

func validComputeID(id string) bool {
	if id == "" || id == "." || id == ".." || filepath.Base(id) != id || strings.ContainsAny(id, `/\\`) {
		return false
	}
	for _, r := range id {
		if !(unicode.IsLetter(r) || unicode.IsDigit(r) || r == '_' || r == '-') {
			return false
		}
	}
	return true
}

func computeSpecHash(spec wire.ComputeJobSpec) (string, error) {
	raw, err := json.Marshal(spec)
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(raw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func computeScopeKey(tenant, principal, key string) string {
	raw, _ := json.Marshal([3]string{tenant, principal, key})
	return string(raw)
}

func validateComputeJob(job wire.ComputeJobRecord) error {
	if !validComputeID(job.JobID) {
		return fmt.Errorf("%w: unsafe job ID %q", ErrComputeMalformedRecord, job.JobID)
	}
	if job.SchemaVersion != 1 || job.Spec.SchemaVersion != 1 {
		return fmt.Errorf("%w: unsupported job/spec schema", ErrComputeMalformedRecord)
	}
	if job.IdempotencyKey == "" || job.Authz.TenantID == "" || job.Authz.Principal == "" || job.Authz.SubmittedAt == "" {
		return fmt.Errorf("%w: missing controller-owned submission fields", ErrComputeMalformedRecord)
	}
	if _, err := time.Parse(time.RFC3339, job.Authz.SubmittedAt); err != nil {
		return fmt.Errorf("%w: submittedAt: %v", ErrComputeMalformedRecord, err)
	}
	if job.Spec.JobKind == "" || job.Spec.Handler.HandlerID == "" || job.Spec.Handler.Version <= 0 || job.Spec.HandlerProfile == "" || job.Spec.WorkspaceID == "" {
		return fmt.Errorf("%w: incomplete immutable spec", ErrComputeMalformedRecord)
	}
	if job.State != "" && job.State != "queued" {
		return fmt.Errorf("%w: initial state must be queued", ErrComputeMalformedRecord)
	}
	if job.Attempt < 0 || job.Spec.MaxAttempts < 0 {
		return fmt.Errorf("%w: negative attempt", ErrComputeMalformedRecord)
	}
	return nil
}

func (s *computeStore) CreateJob(job wire.ComputeJobRecord) (wire.ComputeJobRecord, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if err := validateComputeJob(job); err != nil {
		return wire.ComputeJobRecord{}, err
	}
	hash, err := computeSpecHash(job.Spec)
	if err != nil {
		return wire.ComputeJobRecord{}, err
	}
	job.SpecHash = hash
	job.State = "queued"
	if err := s.rebuildControllerIndexLocked(); err != nil {
		return wire.ComputeJobRecord{}, err
	}
	scope := computeScopeKey(job.Authz.TenantID, job.Authz.Principal, job.IdempotencyKey)
	if prior, ok := s.index[scope]; ok {
		if prior.SpecHash != hash {
			return wire.ComputeJobRecord{}, &ComputeIdempotencyConflictError{ExistingJobID: prior.JobID, ExistingHash: prior.SpecHash, RequestedHash: hash}
		}
		state, err := s.loadLocked(prior.JobID)
		if err != nil {
			return wire.ComputeJobRecord{}, err
		}
		return state.Job, nil
	}
	if _, err := os.Stat(s.jobDir(job.JobID)); err == nil {
		return wire.ComputeJobRecord{}, fmt.Errorf("%w: job ID %s already exists", ErrComputeConflictingDuplicate, job.JobID)
	} else if !os.IsNotExist(err) {
		return wire.ComputeJobRecord{}, err
	}
	for _, dir := range []string{s.jobDir(job.JobID), filepath.Join(s.jobDir(job.JobID), "logs"), filepath.Join(s.jobDir(job.JobID), "artifacts")} {
		if err := ensureComputePrivateDir(dir); err != nil {
			return wire.ComputeJobRecord{}, err
		}
	}
	recordID := computeRecordID(computeEventJobCreated, job)
	envelope, err := makeComputeStoredRecord(recordID, "", computeDispositionAccepted, job)
	if err != nil {
		return wire.ComputeJobRecord{}, err
	}
	state := newComputeJobState()
	if _, err := s.appendPreparedLocked(job.JobID, state, computeEventJobCreated, "", 0, envelope); err != nil {
		return wire.ComputeJobRecord{}, err
	}
	if err := s.rebuildControllerIndexLocked(); err != nil {
		return wire.ComputeJobRecord{}, err
	}
	return job, nil
}

func newComputeJobState() *computeJobState {
	return &computeJobState{
		LogStreams:     make(map[string]*computeLogStreamState),
		AppliedRecords: make(map[string]computeAppliedRecord),
	}
}

func (s *computeStore) List() ([]string, error) {
	entries, err := os.ReadDir(s.jobsRoot)
	if err != nil {
		return nil, err
	}
	ids := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() && validComputeID(entry.Name()) {
			ids = append(ids, entry.Name())
		}
	}
	sort.Strings(ids)
	return ids, nil
}

func (s *computeStore) Load(jobID string) (*computeJobState, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.loadLocked(jobID)
}

func (s *computeStore) loadLocked(jobID string) (*computeJobState, error) {
	if !validComputeID(jobID) {
		return nil, fmt.Errorf("%w: unsafe job ID %q", ErrComputeMalformedRecord, jobID)
	}
	state, err := s.replayJournalLocked(jobID)
	if err != nil {
		return nil, err
	}
	snapshotRaw, snapErr := os.ReadFile(s.snapPath(jobID))
	rewrite := false
	switch {
	case snapErr == nil:
		var snapshot computeJobState
		if err := json.Unmarshal(snapshotRaw, &snapshot); err != nil {
			quarantineCorruptState(s.snapPath(jobID), err)
			rewrite = true
		} else if snapshot.LastAppliedEventSequence > state.LastAppliedEventSequence {
			return nil, fmt.Errorf("%w: snapshot for %s is ahead of journal (%d > %d)", ErrComputeCorruptStore, jobID, snapshot.LastAppliedEventSequence, state.LastAppliedEventSequence)
		} else if err := validateComputeSnapshotShape(jobID, &snapshot); err != nil {
			quarantineCorruptState(s.snapPath(jobID), err)
			rewrite = true
		} else if snapshot.LastAppliedEventSequence < state.LastAppliedEventSequence {
			rewrite = true
		} else {
			want, _ := json.Marshal(state)
			got, _ := json.Marshal(&snapshot)
			if !bytes.Equal(got, want) {
				quarantineCorruptState(s.snapPath(jobID), ErrComputeCorruptStore)
				rewrite = true
			}
		}
	case os.IsNotExist(snapErr):
		rewrite = true
	default:
		return nil, snapErr
	}
	if rewrite {
		if err := s.writeSnapshotLocked(jobID, state); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func validateComputeSnapshotShape(jobID string, state *computeJobState) error {
	if state.LastAppliedEventSequence == 0 || state.Job.JobID != jobID || state.LogStreams == nil || state.AppliedRecords == nil {
		return fmt.Errorf("%w: incomplete compute snapshot", ErrComputeCorruptStore)
	}
	hash, err := computeSpecHash(state.Job.Spec)
	if err != nil || state.Job.SpecHash != hash {
		return fmt.Errorf("%w: snapshot immutable spec hash mismatch", ErrComputeCorruptStore)
	}
	createdID := computeRecordID(computeEventJobCreated, state.Job)
	if created, ok := state.AppliedRecords[createdID]; !ok || created.Disposition != computeDispositionAccepted {
		return fmt.Errorf("%w: snapshot lacks job.created receipt", ErrComputeCorruptStore)
	}
	if state.Fence.Epoch == 0 {
		if state.Fence.JobID != "" || state.Fence.ExecutionID != "" || state.Fence.AuthenticatedNodeID != "" {
			return fmt.Errorf("%w: empty snapshot fence has identity", ErrComputeCorruptStore)
		}
	} else if state.Fence.JobID != jobID || state.Fence.ExecutionID == "" || state.Fence.AuthenticatedNodeID == "" {
		return fmt.Errorf("%w: incomplete snapshot fence", ErrComputeCorruptStore)
	}
	return nil
}

func (s *computeStore) replayJournalLocked(jobID string) (*computeJobState, error) {
	path := s.eventsPath(jobID)
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, fmt.Errorf("%w: job %s has no event journal", ErrComputeCorruptStore, jobID)
		}
		return nil, err
	}
	state := newComputeJobState()
	offset := 0
	expected := uint64(1)
	needsTerminator := false
	for offset < len(raw) {
		start := offset
		relEnd := bytes.IndexByte(raw[start:], '\n')
		hasNewline := relEnd >= 0
		end := len(raw)
		if hasNewline {
			end = start + relEnd
			offset = end + 1
		} else {
			offset = len(raw)
		}
		line := raw[start:end]
		if len(bytes.TrimSpace(line)) == 0 {
			return nil, fmt.Errorf("%w: empty event line at byte %d", ErrComputeCorruptStore, start)
		}
		var event wire.ComputeJobEvent
		if err := json.Unmarshal(line, &event); err != nil {
			if !hasNewline && end == len(raw) && incompleteComputeJSONTail(line, err) {
				if err := truncateComputeJournal(path, int64(start)); err != nil {
					return nil, err
				}
				break
			}
			return nil, fmt.Errorf("%w: malformed event at byte %d: %v", ErrComputeCorruptStore, start, err)
		}
		if event.SchemaVersion != computeEventSchemaVersion || event.JobID != jobID || event.Sequence != expected || event.Type == "" {
			return nil, fmt.Errorf("%w: invalid event at byte %d (schema=%d job=%q sequence=%d want=%d type=%q)", ErrComputeCorruptStore, start, event.SchemaVersion, event.JobID, event.Sequence, expected, event.Type)
		}
		if _, err := time.Parse(time.RFC3339, event.At); err != nil {
			return nil, fmt.Errorf("%w: event %d timestamp: %v", ErrComputeCorruptStore, event.Sequence, err)
		}
		if err := reduceComputeEvent(state, event); err != nil {
			return nil, fmt.Errorf("%w: event %d %s: %w", ErrComputeCorruptStore, event.Sequence, event.Type, err)
		}
		if !hasNewline {
			needsTerminator = true
		}
		expected++
	}
	if state.LastAppliedEventSequence == 0 || state.Job.JobID == "" {
		return nil, fmt.Errorf("%w: job %s has no job.created authority", ErrComputeCorruptStore, jobID)
	}
	if needsTerminator {
		if err := appendComputeJournalTerminator(path); err != nil {
			return nil, err
		}
	}
	return state, nil
}

func incompleteComputeJSONTail(line []byte, decodeErr error) bool {
	if errors.Is(decodeErr, io.ErrUnexpectedEOF) {
		return true
	}
	var syntaxErr *json.SyntaxError
	return errors.As(decodeErr, &syntaxErr) &&
		syntaxErr.Offset == int64(len(line)) &&
		syntaxErr.Error() == "unexpected end of JSON input"
}

func truncateComputeJournal(path string, size int64) error {
	f, err := os.OpenFile(path, os.O_RDWR, 0)
	if err != nil {
		return err
	}
	if err := f.Truncate(size); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func appendComputeJournalTerminator(path string) error {
	f, err := os.OpenFile(path, os.O_WRONLY|os.O_APPEND, 0)
	if err != nil {
		return err
	}
	if _, err := f.Write([]byte{'\n'}); err != nil {
		_ = f.Close()
		return err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return err
	}
	return f.Close()
}

func (s *computeStore) writeSnapshotLocked(jobID string, state *computeJobState) error {
	raw, err := json.MarshalIndent(state, "", "  ")
	if err != nil {
		return err
	}
	return writeFileAtomicSynced(s.snapPath(jobID), raw, 0o600)
}

func (s *computeStore) appendPreparedLocked(jobID string, current *computeJobState, kind, executionID string, epoch uint64, record computeStoredRecord) (*computeJobState, error) {
	prepared, err := cloneComputeState(current)
	if err != nil {
		return nil, err
	}
	payload, err := json.Marshal(record)
	if err != nil {
		return nil, err
	}
	event := wire.ComputeJobEvent{
		SchemaVersion: computeEventSchemaVersion,
		Sequence:      current.LastAppliedEventSequence + 1,
		At:            time.Now().UTC().Format(time.RFC3339Nano),
		Type:          kind,
		JobID:         jobID,
		ExecutionID:   executionID,
		Epoch:         epoch,
		Payload:       payload,
	}
	if err := reduceComputeEvent(prepared, event); err != nil {
		return nil, err
	}
	line, err := json.Marshal(event)
	if err != nil {
		return nil, err
	}
	f, err := os.OpenFile(s.eventsPath(jobID), os.O_CREATE|os.O_WRONLY|os.O_APPEND, 0o600)
	if err != nil {
		return nil, err
	}
	if err := f.Chmod(0o600); err != nil {
		_ = f.Close()
		return nil, err
	}
	if _, err := f.Write(append(line, '\n')); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Sync(); err != nil {
		_ = f.Close()
		return nil, err
	}
	if err := f.Close(); err != nil {
		return nil, err
	}
	if err := s.writeSnapshotLocked(jobID, prepared); err != nil {
		return nil, err
	}
	return prepared, nil
}

func cloneComputeState(state *computeJobState) (*computeJobState, error) {
	raw, err := json.Marshal(state)
	if err != nil {
		return nil, err
	}
	var clone computeJobState
	if err := json.Unmarshal(raw, &clone); err != nil {
		return nil, err
	}
	if clone.LogStreams == nil {
		clone.LogStreams = make(map[string]*computeLogStreamState)
	}
	if clone.AppliedRecords == nil {
		clone.AppliedRecords = make(map[string]computeAppliedRecord)
	}
	return &clone, nil
}

func makeComputeStoredRecord(recordID, nodeID, disposition string, value any) (computeStoredRecord, error) {
	raw, err := json.Marshal(value)
	if err != nil {
		return computeStoredRecord{}, err
	}
	digestRaw, err := json.Marshal(struct {
		AuthenticatedNodeID string          `json:"authenticatedNodeId"`
		Record              json.RawMessage `json:"record"`
	}{nodeID, raw})
	if err != nil {
		return computeStoredRecord{}, err
	}
	sum := sha256.Sum256(digestRaw)
	return computeStoredRecord{
		RecordID: recordID, Digest: "sha256:" + hex.EncodeToString(sum[:]),
		AuthenticatedNodeID: nodeID, Disposition: disposition, Record: raw,
	}, nil
}

func computeRecordID(kind string, value any) string {
	switch record := value.(type) {
	case wire.ComputeJobRecord:
		return kind + ":" + record.JobID
	case wire.AssignmentOffer:
		return fmt.Sprintf("%s:%s:%d", kind, record.AssignmentID, record.Epoch)
	case wire.AssignmentClaim:
		return fmt.Sprintf("%s:%s:%d", kind, record.AssignmentID, record.Epoch)
	case wire.AssignmentGrant:
		return fmt.Sprintf("%s:%s:%d", kind, record.AssignmentID, record.Epoch)
	case computeAssignmentExpired:
		return fmt.Sprintf("%s:%s:%d", kind, record.AssignmentID, record.Epoch)
	case wire.LeaseRenewal:
		return fmt.Sprintf("%s:%s:%s:%d:%s", kind, record.JobID, record.ExecutionID, record.Epoch, record.LeaseUntil)
	case wire.LogChunk:
		return fmt.Sprintf("%s:%s:%s:%d:%s:%d", kind, record.JobID, record.ExecutionID, record.Epoch, record.Stream, record.Sequence)
	case wire.TerminalResult:
		return fmt.Sprintf("%s:%s:%s:%d", kind, record.JobID, record.ExecutionID, record.Epoch)
	case computeExecutionLapsed:
		return fmt.Sprintf("%s:%s:%s:%d", kind, record.JobID, record.ExecutionID, record.Epoch)
	default:
		return kind
	}
}

func knownComputeEvent(kind string) bool {
	switch kind {
	case computeEventJobCreated, computeEventAssignmentOffered, computeEventAssignmentClaimed,
		computeEventAssignmentGranted, computeEventAssignmentExpired, computeEventLeaseRenewed,
		computeEventLogChunkAccepted, computeEventExecutionLapsed, computeEventExecutionTerminal:
		return true
	default:
		return false
	}
}

func reduceComputeEvent(state *computeJobState, event wire.ComputeJobEvent) error {
	if event.Sequence != state.LastAppliedEventSequence+1 {
		return fmt.Errorf("%w: event sequence %d after %d", ErrComputeMalformedRecord, event.Sequence, state.LastAppliedEventSequence)
	}
	if !knownComputeEvent(event.Type) {
		state.LastAppliedEventSequence = event.Sequence
		return nil // D9 additive event: retain and advance without control effects.
	}
	var stored computeStoredRecord
	if err := json.Unmarshal(event.Payload, &stored); err != nil {
		return fmt.Errorf("%w: storage envelope: %v", ErrComputeMalformedRecord, err)
	}
	if stored.RecordID == "" || stored.Digest == "" || len(stored.Record) == 0 ||
		(stored.Disposition != computeDispositionAccepted && stored.Disposition != computeDispositionStaleFence) {
		return fmt.Errorf("%w: incomplete storage envelope", ErrComputeMalformedRecord)
	}
	if stored.Disposition == computeDispositionStaleFence && event.Type != computeEventAssignmentClaimed &&
		event.Type != computeEventLogChunkAccepted && event.Type != computeEventExecutionTerminal {
		return fmt.Errorf("%w: stale disposition on controller record", ErrComputeMalformedRecord)
	}
	wantDigest, err := digestStoredRecord(stored.AuthenticatedNodeID, stored.Record)
	if err != nil || wantDigest != stored.Digest {
		return fmt.Errorf("%w: record digest mismatch", ErrComputeMalformedRecord)
	}
	if err := validateStoredComputeRecord(event, stored); err != nil {
		return err
	}
	if prior, ok := state.AppliedRecords[stored.RecordID]; ok {
		if prior.Digest != stored.Digest || prior.Disposition != stored.Disposition {
			return fmt.Errorf("%w: record ID %s", ErrComputeConflictingDuplicate, stored.RecordID)
		}
		state.LastAppliedEventSequence = event.Sequence
		return nil
	}
	if stored.Disposition == computeDispositionAccepted {
		if err := applyAcceptedComputeRecord(state, event, stored); err != nil {
			return err
		}
	} else if event.Type == computeEventJobCreated {
		return fmt.Errorf("%w: job.created cannot be stale", ErrComputeMalformedRecord)
	}
	state.AppliedRecords[stored.RecordID] = computeAppliedRecord{Digest: stored.Digest, Disposition: stored.Disposition}
	state.LastAppliedEventSequence = event.Sequence
	return nil
}

func validateStoredComputeRecord(event wire.ComputeJobEvent, stored computeStoredRecord) error {
	checkIdentity := func(value any, jobID, executionID string, epoch uint64) error {
		if stored.RecordID != computeRecordID(event.Type, value) {
			return fmt.Errorf("%w: unstable record ID for %s", ErrComputeMalformedRecord, event.Type)
		}
		if event.JobID != jobID || event.ExecutionID != executionID || event.Epoch != epoch {
			return fmt.Errorf("%w: event tuple does not match %s record", ErrComputeMalformedRecord, event.Type)
		}
		return nil
	}
	switch event.Type {
	case computeEventJobCreated:
		var record wire.ComputeJobRecord
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: job.created payload: %v", ErrComputeMalformedRecord, err)
		}
		return checkIdentity(record, record.JobID, "", 0)
	case computeEventAssignmentOffered:
		var record wire.AssignmentOffer
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: assignment.offered payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.AssignmentID == "" || record.ExecutionID == "" || record.Epoch == 0 {
			return fmt.Errorf("%w: incomplete offer identity", ErrComputeMalformedRecord)
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	case computeEventAssignmentClaimed:
		var record wire.AssignmentClaim
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: assignment.claimed payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.AssignmentID == "" || record.ExecutionID == "" || record.Epoch == 0 || record.SpecHash == "" || record.ReceiptID == "" {
			return fmt.Errorf("%w: incomplete claim", ErrComputeMalformedRecord)
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	case computeEventAssignmentGranted:
		var record wire.AssignmentGrant
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: assignment.granted payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.AssignmentID == "" || record.ExecutionID == "" || record.Epoch == 0 {
			return fmt.Errorf("%w: incomplete grant identity", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.LeaseUntil); err != nil {
			return fmt.Errorf("%w: grant lease: %v", ErrComputeMalformedRecord, err)
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	case computeEventAssignmentExpired:
		var record computeAssignmentExpired
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: assignment.expired payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.AssignmentID == "" || record.ExecutionID == "" || record.Epoch == 0 {
			return fmt.Errorf("%w: incomplete assignment expiry identity", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.ControllerAt); err != nil {
			return fmt.Errorf("%w: assignment expiry controller time", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.OfferDeadline); err != nil {
			return fmt.Errorf("%w: assignment expiry deadline", ErrComputeMalformedRecord)
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	case computeEventLeaseRenewed:
		var record wire.LeaseRenewal
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: lease.renewed payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.ExecutionID == "" || record.Epoch == 0 {
			return fmt.Errorf("%w: incomplete renewal identity", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.LeaseUntil); err != nil {
			return fmt.Errorf("%w: renewal lease: %v", ErrComputeMalformedRecord, err)
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	case computeEventLogChunkAccepted:
		var record wire.LogChunk
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: logs.chunk_accepted payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.ExecutionID == "" || record.Epoch == 0 {
			return fmt.Errorf("%w: incomplete log identity", ErrComputeMalformedRecord)
		}
		if _, err := validateLogChunkValue(record); err != nil {
			return err
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	case computeEventExecutionTerminal:
		var record wire.TerminalResult
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: execution.terminal payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.SchemaVersion != 1 || record.ExecutionID == "" || record.Epoch == 0 || record.NodeID != stored.AuthenticatedNodeID || record.DurationMs < 0 {
			return fmt.Errorf("%w: incomplete terminal identity", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.StartedAt); err != nil {
			return fmt.Errorf("%w: terminal startedAt", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.EndedAt); err != nil {
			return fmt.Errorf("%w: terminal endedAt", ErrComputeMalformedRecord)
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	case computeEventExecutionLapsed:
		var record computeExecutionLapsed
		if err := json.Unmarshal(stored.Record, &record); err != nil {
			return fmt.Errorf("%w: execution.lapsed payload: %v", ErrComputeMalformedRecord, err)
		}
		if record.AssignmentID == "" || record.ExecutionID == "" || record.Epoch == 0 || record.Reason != "lease_lost" {
			return fmt.Errorf("%w: incomplete execution lapse identity", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.ControllerAt); err != nil {
			return fmt.Errorf("%w: execution lapse controller time", ErrComputeMalformedRecord)
		}
		if _, err := time.Parse(time.RFC3339, record.EffectiveDeadline); err != nil {
			return fmt.Errorf("%w: execution lapse deadline", ErrComputeMalformedRecord)
		}
		return checkIdentity(record, record.JobID, record.ExecutionID, record.Epoch)
	default:
		return nil
	}
}

func digestStoredRecord(nodeID string, raw json.RawMessage) (string, error) {
	digestRaw, err := json.Marshal(struct {
		AuthenticatedNodeID string          `json:"authenticatedNodeId"`
		Record              json.RawMessage `json:"record"`
	}{nodeID, raw})
	if err != nil {
		return "", err
	}
	sum := sha256.Sum256(digestRaw)
	return "sha256:" + hex.EncodeToString(sum[:]), nil
}

func applyAcceptedComputeRecord(state *computeJobState, event wire.ComputeJobEvent, stored computeStoredRecord) error {
	switch event.Type {
	case computeEventJobCreated:
		if event.Sequence != 1 || state.Job.JobID != "" || event.ExecutionID != "" || event.Epoch != 0 {
			return fmt.Errorf("%w: invalid job.created position", ErrComputeMalformedRecord)
		}
		var job wire.ComputeJobRecord
		if err := json.Unmarshal(stored.Record, &job); err != nil {
			return err
		}
		if err := validateComputeJob(job); err != nil {
			return err
		}
		hash, err := computeSpecHash(job.Spec)
		if err != nil || job.SpecHash != hash || job.State != "queued" {
			return fmt.Errorf("%w: invalid job.created hash/state", ErrComputeMalformedRecord)
		}
		state.Job = job
	case computeEventAssignmentOffered:
		var offer wire.AssignmentOffer
		if err := json.Unmarshal(stored.Record, &offer); err != nil {
			return err
		}
		if err := validateOfferTransition(state, stored.AuthenticatedNodeID, offer); err != nil {
			return err
		}
		if event.ExecutionID != offer.ExecutionID || event.Epoch != offer.Epoch {
			return fmt.Errorf("%w: offer event tuple mismatch", ErrComputeMalformedRecord)
		}
		state.Offer = &offer
		state.Claim, state.Grant, state.Lease, state.Terminal = nil, nil, nil, nil
		state.Fence = computeExecutionFence{JobID: offer.JobID, ExecutionID: offer.ExecutionID, AuthenticatedNodeID: stored.AuthenticatedNodeID, Epoch: offer.Epoch}
		state.Job.State = "offered"
		state.Job.QueueReason = ""
	case computeEventAssignmentClaimed:
		var claim wire.AssignmentClaim
		if err := json.Unmarshal(stored.Record, &claim); err != nil {
			return err
		}
		if err := validateClaimTransition(state, stored.AuthenticatedNodeID, claim); err != nil {
			return err
		}
		if event.ExecutionID != claim.ExecutionID || event.Epoch != claim.Epoch {
			return fmt.Errorf("%w: claim event tuple mismatch", ErrComputeMalformedRecord)
		}
		state.Claim = &claim
		state.Job.State = "claimed"
	case computeEventAssignmentGranted:
		var grant wire.AssignmentGrant
		if err := json.Unmarshal(stored.Record, &grant); err != nil {
			return err
		}
		if err := validateGrantTransition(state, stored.AuthenticatedNodeID, grant); err != nil {
			return err
		}
		if event.ExecutionID != grant.ExecutionID || event.Epoch != grant.Epoch {
			return fmt.Errorf("%w: grant event tuple mismatch", ErrComputeMalformedRecord)
		}
		state.Grant = &grant
		state.Job.State = "running"
		state.Job.QueueReason = ""
		state.Job.Attempt++
	case computeEventAssignmentExpired:
		var expired computeAssignmentExpired
		if err := json.Unmarshal(stored.Record, &expired); err != nil {
			return err
		}
		if err := validateAssignmentExpiryTransition(state, stored.AuthenticatedNodeID, expired); err != nil {
			return err
		}
		state.LatestAssignmentExpiry = &expired
		clearComputeActiveAssignment(state)
		state.Job.State = "queued"
		state.Job.QueueReason = "offer_expired"
	case computeEventLeaseRenewed:
		var renewal wire.LeaseRenewal
		if err := json.Unmarshal(stored.Record, &renewal); err != nil {
			return err
		}
		if err := validateLeaseTransition(state, stored.AuthenticatedNodeID, renewal); err != nil {
			return err
		}
		if event.ExecutionID != renewal.ExecutionID || event.Epoch != renewal.Epoch {
			return fmt.Errorf("%w: renewal event tuple mismatch", ErrComputeMalformedRecord)
		}
		state.Lease = &renewal
	case computeEventLogChunkAccepted:
		var chunk wire.LogChunk
		if err := json.Unmarshal(stored.Record, &chunk); err != nil {
			return err
		}
		decoded, err := validateLogTransition(state, stored.AuthenticatedNodeID, chunk)
		if err != nil {
			return err
		}
		if event.ExecutionID != chunk.ExecutionID || event.Epoch != chunk.Epoch {
			return fmt.Errorf("%w: log event tuple mismatch", ErrComputeMalformedRecord)
		}
		streamKey := computeLogStreamKey(chunk.ExecutionID, chunk.Epoch, chunk.Stream)
		stream := state.LogStreams[streamKey]
		if stream == nil {
			stream = &computeLogStreamState{JobID: chunk.JobID, ExecutionID: chunk.ExecutionID, Epoch: chunk.Epoch, Stream: chunk.Stream, Receipts: make(map[uint64]computeLogChunkReceipt)}
			state.LogStreams[streamKey] = stream
		}
		stream.Receipts[chunk.Sequence] = computeLogChunkReceipt{Sequence: chunk.Sequence, ByteOffset: chunk.ByteOffset, SizeBytes: len(decoded), Digest: chunk.SHA256, EOF: chunk.EOF}
		stream.ContiguousSequence = chunk.Sequence
		stream.NextByteOffset = chunk.ByteOffset + int64(len(decoded))
		stream.EOF = chunk.EOF
	case computeEventExecutionLapsed:
		var lapsed computeExecutionLapsed
		if err := json.Unmarshal(stored.Record, &lapsed); err != nil {
			return err
		}
		if err := validateExecutionLapseTransition(state, stored.AuthenticatedNodeID, lapsed); err != nil {
			return err
		}
		state.LatestExecutionLapse = &lapsed
		clearComputeActiveAssignment(state)
		state.Job.QueueReason = "lease_lost"
		if lapsed.Retry {
			state.Job.State = "retry_wait"
		} else {
			state.Job.State = "failed"
		}
	case computeEventExecutionTerminal:
		var terminal wire.TerminalResult
		if err := json.Unmarshal(stored.Record, &terminal); err != nil {
			return err
		}
		if err := validateTerminalTransition(state, stored.AuthenticatedNodeID, terminal); err != nil {
			return err
		}
		if event.ExecutionID != terminal.ExecutionID || event.Epoch != terminal.Epoch {
			return fmt.Errorf("%w: terminal event tuple mismatch", ErrComputeMalformedRecord)
		}
		state.Terminal = &terminal
		state.Job.State = terminalComputeState(terminal)
		state.Job.QueueReason = ""
	}
	return nil
}

func clearComputeActiveAssignment(state *computeJobState) {
	state.Offer = nil
	state.Claim = nil
	state.Grant = nil
	state.Lease = nil
	state.Terminal = nil
}

func validateOfferTransition(state *computeJobState, nodeID string, offer wire.AssignmentOffer) error {
	if state.Job.State != "queued" && state.Job.State != "retry_wait" {
		return fmt.Errorf("%w: cannot offer job in %s", ErrComputeInvalidLifecycleState, state.Job.State)
	}
	if nodeID == "" || offer.AssignmentID == "" || offer.ExecutionID == "" || offer.JobID != state.Job.JobID || offer.Epoch == 0 || offer.Epoch <= state.Fence.Epoch {
		return fmt.Errorf("%w: invalid or non-monotonic offer", ErrComputeMalformedRecord)
	}
	if offer.SpecHash != state.Job.SpecHash || !reflect.DeepEqual(offer.Spec, state.Job.Spec) ||
		offer.Authz.TenantID != state.Job.Authz.TenantID || offer.Authz.Principal != state.Job.Authz.Principal ||
		offer.Authz.AssignmentID != offer.AssignmentID || offer.Authz.SpecHash != offer.SpecHash {
		return fmt.Errorf("%w: offer is not bound to immutable job authority", ErrComputeMalformedRecord)
	}
	if _, err := time.Parse(time.RFC3339, offer.ExpiresAt); err != nil {
		return fmt.Errorf("%w: offer expiry: %v", ErrComputeMalformedRecord, err)
	}
	if offer.Authz.IssuedAt == "" || offer.Authz.ExpiresAt == "" {
		return fmt.Errorf("%w: incomplete offer authorization", ErrComputeMalformedRecord)
	}
	if _, err := time.Parse(time.RFC3339, offer.Authz.IssuedAt); err != nil {
		return fmt.Errorf("%w: authorization issue time", ErrComputeMalformedRecord)
	}
	if _, err := time.Parse(time.RFC3339, offer.Authz.ExpiresAt); err != nil {
		return fmt.Errorf("%w: authorization expiry", ErrComputeMalformedRecord)
	}
	if offer.Authz.ExpiresAt != offer.ExpiresAt {
		return fmt.Errorf("%w: authorization and offer deadlines differ", ErrComputeMalformedRecord)
	}
	return nil
}

func fenceMatches(state *computeJobState, nodeID, jobID, executionID string, epoch uint64) bool {
	return nodeID != "" && state.Fence == (computeExecutionFence{JobID: jobID, ExecutionID: executionID, AuthenticatedNodeID: nodeID, Epoch: epoch})
}

func validateClaimTransition(state *computeJobState, nodeID string, claim wire.AssignmentClaim) error {
	if state.Job.State != "offered" {
		return fmt.Errorf("%w: cannot claim job in %s", ErrComputeInvalidLifecycleState, state.Job.State)
	}
	if state.Offer == nil || claim.ReceiptID == "" || claim.AssignmentID != state.Offer.AssignmentID || claim.SpecHash != state.Job.SpecHash ||
		!fenceMatches(state, nodeID, claim.JobID, claim.ExecutionID, claim.Epoch) {
		return fmt.Errorf("%w: claim does not match current offer/fence", ErrComputeMalformedRecord)
	}
	if state.Claim != nil {
		return fmt.Errorf("%w: current claim already accepted", ErrComputeMalformedRecord)
	}
	return nil
}

func validateGrantTransition(state *computeJobState, nodeID string, grant wire.AssignmentGrant) error {
	if state.Job.State != "claimed" {
		return fmt.Errorf("%w: cannot grant job in %s", ErrComputeInvalidLifecycleState, state.Job.State)
	}
	if state.Claim == nil || state.Grant != nil || grant.AssignmentID != state.Claim.AssignmentID ||
		!fenceMatches(state, nodeID, grant.JobID, grant.ExecutionID, grant.Epoch) {
		return fmt.Errorf("%w: grant does not match accepted claim/fence", ErrComputeMalformedRecord)
	}
	if _, err := time.Parse(time.RFC3339, grant.LeaseUntil); err != nil {
		return fmt.Errorf("%w: grant lease: %v", ErrComputeMalformedRecord, err)
	}
	return nil
}

func validateLeaseTransition(state *computeJobState, nodeID string, renewal wire.LeaseRenewal) error {
	if state.Job.State != "running" {
		return fmt.Errorf("%w: cannot renew job in %s", ErrComputeInvalidLifecycleState, state.Job.State)
	}
	if state.Grant == nil || !fenceMatches(state, nodeID, renewal.JobID, renewal.ExecutionID, renewal.Epoch) {
		return fmt.Errorf("%w: renewal does not match current grant/fence", ErrComputeMalformedRecord)
	}
	deadline, err := time.Parse(time.RFC3339, renewal.LeaseUntil)
	if err != nil {
		return fmt.Errorf("%w: renewal lease: %v", ErrComputeMalformedRecord, err)
	}
	previous := state.Grant.LeaseUntil
	if state.Lease != nil {
		previous = state.Lease.LeaseUntil
	}
	priorDeadline, err := time.Parse(time.RFC3339, previous)
	if err != nil || !deadline.After(priorDeadline) {
		return fmt.Errorf("%w: renewal deadline is not strictly later", ErrComputeMalformedRecord)
	}
	return nil
}

func validateAssignmentExpiryTransition(state *computeJobState, nodeID string, expired computeAssignmentExpired) error {
	if state.Job.State != "offered" || state.Offer == nil || state.Claim != nil {
		return fmt.Errorf("%w: assignment expiry requires an unclaimed offer", ErrComputeInvalidLifecycleState)
	}
	if expired.AssignmentID != state.Offer.AssignmentID || expired.OfferDeadline != state.Offer.ExpiresAt ||
		!fenceMatches(state, nodeID, expired.JobID, expired.ExecutionID, expired.Epoch) {
		return fmt.Errorf("%w: assignment expiry does not match current offer", ErrComputeInvalidLifecycleState)
	}
	now, err := time.Parse(time.RFC3339, expired.ControllerAt)
	if err != nil {
		return fmt.Errorf("%w: assignment expiry controller time", ErrComputeMalformedRecord)
	}
	deadline, err := time.Parse(time.RFC3339, expired.OfferDeadline)
	if err != nil {
		return fmt.Errorf("%w: assignment expiry deadline", ErrComputeMalformedRecord)
	}
	if now.Before(deadline) {
		return fmt.Errorf("%w: offer deadline is %s", ErrComputeExpiredOffer, expired.OfferDeadline)
	}
	return nil
}

func effectiveComputeLeaseDeadline(state *computeJobState) string {
	if state.Lease != nil {
		return state.Lease.LeaseUntil
	}
	if state.Grant != nil {
		return state.Grant.LeaseUntil
	}
	return ""
}

func validateExecutionLapseTransition(state *computeJobState, nodeID string, lapsed computeExecutionLapsed) error {
	if state.Job.State != "running" || state.Offer == nil || state.Grant == nil || state.Terminal != nil {
		return fmt.Errorf("%w: execution lapse requires a running grant", ErrComputeInvalidLifecycleState)
	}
	if lapsed.AssignmentID != state.Offer.AssignmentID || lapsed.EffectiveDeadline != effectiveComputeLeaseDeadline(state) ||
		lapsed.Reason != "lease_lost" || !fenceMatches(state, nodeID, lapsed.JobID, lapsed.ExecutionID, lapsed.Epoch) {
		return fmt.Errorf("%w: execution lapse does not match current lease", ErrComputeInvalidLifecycleState)
	}
	now, err := time.Parse(time.RFC3339, lapsed.ControllerAt)
	if err != nil {
		return fmt.Errorf("%w: execution lapse controller time", ErrComputeMalformedRecord)
	}
	deadline, err := time.Parse(time.RFC3339, lapsed.EffectiveDeadline)
	if err != nil {
		return fmt.Errorf("%w: execution lapse deadline", ErrComputeMalformedRecord)
	}
	if now.Before(deadline) {
		return fmt.Errorf("%w: lease deadline is %s", ErrComputeExpiredLease, lapsed.EffectiveDeadline)
	}
	wantRetry := state.Job.Attempt < computeEffectiveMaxAttempts(state.Job.Spec)
	if lapsed.Retry != wantRetry {
		return fmt.Errorf("%w: recorded retry=%t, want %t", ErrComputeInvalidLifecycleState, lapsed.Retry, wantRetry)
	}
	return nil
}

func validateLogChunkValue(chunk wire.LogChunk) ([]byte, error) {
	if chunk.Stream != "stdout" && chunk.Stream != "stderr" && chunk.Stream != "system" {
		return nil, fmt.Errorf("%w: invalid log stream", ErrComputeMalformedRecord)
	}
	if chunk.Sequence == 0 || chunk.ByteOffset < 0 {
		return nil, fmt.Errorf("%w: invalid log sequence/offset", ErrComputeMalformedRecord)
	}
	decoded, err := base64.StdEncoding.Strict().DecodeString(chunk.Data)
	if err != nil {
		return nil, fmt.Errorf("%w: invalid standard base64", ErrComputeMalformedRecord)
	}
	if base64.StdEncoding.EncodeToString(decoded) != chunk.Data {
		return nil, fmt.Errorf("%w: non-canonical standard base64", ErrComputeMalformedRecord)
	}
	if len(decoded) > 64*1024 {
		return nil, fmt.Errorf("%w: log chunk exceeds 64 KiB", ErrComputeMalformedRecord)
	}
	const maxInt64 = int64(^uint64(0) >> 1)
	if chunk.ByteOffset > maxInt64-int64(len(decoded)) {
		return nil, fmt.Errorf("%w: log byte offset overflows", ErrComputeMalformedRecord)
	}
	sum := sha256.Sum256(decoded)
	want := "sha256:" + hex.EncodeToString(sum[:])
	if chunk.SHA256 != want {
		return nil, fmt.Errorf("%w: log digest mismatch", ErrComputeMalformedRecord)
	}
	return decoded, nil
}

func validateLogTransition(state *computeJobState, nodeID string, chunk wire.LogChunk) ([]byte, error) {
	decoded, err := validateLogChunkValue(chunk)
	if err != nil {
		return nil, err
	}
	if state.Job.State != "running" || state.Grant == nil || state.Terminal != nil || !fenceMatches(state, nodeID, chunk.JobID, chunk.ExecutionID, chunk.Epoch) {
		return nil, fmt.Errorf("%w: log does not match current running fence", ErrComputeMalformedRecord)
	}
	stream := state.LogStreams[computeLogStreamKey(chunk.ExecutionID, chunk.Epoch, chunk.Stream)]
	if stream == nil {
		if chunk.Sequence != 1 || chunk.ByteOffset != 0 {
			return nil, fmt.Errorf("%w: first log chunk is not contiguous", ErrComputeMalformedRecord)
		}
		return decoded, nil
	}
	if stream.EOF || chunk.Sequence != stream.ContiguousSequence+1 || chunk.ByteOffset != stream.NextByteOffset {
		return nil, fmt.Errorf("%w: log gap, offset mismatch, or data after EOF", ErrComputeMalformedRecord)
	}
	return decoded, nil
}

func computeLogStreamKey(executionID string, epoch uint64, stream string) string {
	return fmt.Sprintf("%s:%d:%s", base64.RawURLEncoding.EncodeToString([]byte(executionID)), epoch, stream)
}

func validateTerminalTransition(state *computeJobState, nodeID string, terminal wire.TerminalResult) error {
	if terminal.SchemaVersion != 1 || terminal.NodeID != nodeID || state.Job.State != "running" || state.Grant == nil || state.Terminal != nil ||
		!fenceMatches(state, nodeID, terminal.JobID, terminal.ExecutionID, terminal.Epoch) {
		return fmt.Errorf("%w: terminal does not match current running fence", ErrComputeMalformedRecord)
	}
	if terminal.Handler != state.Job.Spec.Handler || terminal.SourceDigest != state.Job.Spec.SourceDigest {
		return fmt.Errorf("%w: terminal handler/source mismatch", ErrComputeMalformedRecord)
	}
	start, err := time.Parse(time.RFC3339, terminal.StartedAt)
	if err != nil {
		return fmt.Errorf("%w: terminal startedAt", ErrComputeMalformedRecord)
	}
	end, err := time.Parse(time.RFC3339, terminal.EndedAt)
	if err != nil || end.Before(start) || terminal.DurationMs < 0 {
		return fmt.Errorf("%w: terminal timing", ErrComputeMalformedRecord)
	}
	return nil
}

func terminalComputeState(terminal wire.TerminalResult) string {
	if terminal.ErrorKind == "" && terminal.ExitCode != nil && *terminal.ExitCode == 0 {
		return "succeeded"
	}
	if terminal.ErrorKind == "cancelled" {
		return "cancelled"
	}
	if terminal.ErrorKind == "timeout" {
		return "timed_out"
	}
	return "failed"
}

func lookupStoredRecord(state *computeJobState, record computeStoredRecord) (computeAppliedRecord, bool, error) {
	prior, ok := state.AppliedRecords[record.RecordID]
	if !ok {
		return computeAppliedRecord{}, false, nil
	}
	if prior.Digest != record.Digest {
		return computeAppliedRecord{}, false, fmt.Errorf("%w: record ID %s", ErrComputeConflictingDuplicate, record.RecordID)
	}
	return prior, true, nil
}

func (s *computeStore) RecordOffer(expectedSequence uint64, nodeID string, offer wire.AssignmentOffer) error {
	return s.recordControllerMutation(expectedSequence, nodeID, computeEventAssignmentOffered, offer.JobID, offer.ExecutionID, offer.Epoch, offer)
}

func (s *computeStore) RecordGrant(expectedSequence uint64, nodeID string, grant wire.AssignmentGrant) error {
	return s.recordControllerMutation(expectedSequence, nodeID, computeEventAssignmentGranted, grant.JobID, grant.ExecutionID, grant.Epoch, grant)
}

func (s *computeStore) RecordAssignmentExpired(expectedSequence uint64, nodeID string, expired computeAssignmentExpired) error {
	return s.recordControllerMutation(expectedSequence, nodeID, computeEventAssignmentExpired, expired.JobID, expired.ExecutionID, expired.Epoch, expired)
}

func (s *computeStore) RecordLeaseRenewal(expectedSequence uint64, nodeID string, renewal wire.LeaseRenewal) error {
	return s.recordControllerMutation(expectedSequence, nodeID, computeEventLeaseRenewed, renewal.JobID, renewal.ExecutionID, renewal.Epoch, renewal)
}

func (s *computeStore) RecordExecutionLapsed(expectedSequence uint64, nodeID string, lapsed computeExecutionLapsed) error {
	return s.recordControllerMutation(expectedSequence, nodeID, computeEventExecutionLapsed, lapsed.JobID, lapsed.ExecutionID, lapsed.Epoch, lapsed)
}

func (s *computeStore) recordControllerMutation(expectedSequence uint64, nodeID, kind, jobID, executionID string, epoch uint64, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked(jobID)
	if err != nil {
		return err
	}
	record, err := makeComputeStoredRecord(computeRecordID(kind, value), nodeID, computeDispositionAccepted, value)
	if err != nil {
		return err
	}
	if prior, duplicate, err := lookupStoredRecord(state, record); err != nil {
		return err
	} else if duplicate {
		if prior.Disposition == computeDispositionStaleFence {
			return ErrComputeStaleExecutionFence
		}
		if !fenceMatches(state, nodeID, jobID, executionID, epoch) {
			return ErrComputeStaleExecutionFence
		}
		return nil
	}
	if expectedSequence != state.LastAppliedEventSequence {
		return fmt.Errorf("%w: expected %d, durable sequence is %d", ErrComputeStaleControllerSequence, expectedSequence, state.LastAppliedEventSequence)
	}
	if kind != computeEventAssignmentOffered && !fenceMatches(state, nodeID, jobID, executionID, epoch) {
		return ErrComputeStaleExecutionFence
	}
	if kind == computeEventAssignmentGranted || kind == computeEventAssignmentExpired || kind == computeEventExecutionLapsed {
		if err := s.raiseMinReaderVersionLocked(computeStoreReaderVersion); err != nil {
			return err
		}
	}
	_, err = s.appendPreparedLocked(jobID, state, kind, executionID, epoch, record)
	return err
}

func (s *computeStore) RecordClaim(authenticatedNodeID string, claim wire.AssignmentClaim) error {
	if claim.AssignmentID == "" || claim.ExecutionID == "" || claim.JobID == "" || claim.Epoch == 0 || claim.SpecHash == "" || claim.ReceiptID == "" {
		return fmt.Errorf("%w: incomplete claim", ErrComputeMalformedRecord)
	}
	return s.recordWorkerMutation(authenticatedNodeID, computeEventAssignmentClaimed, claim.JobID, claim.ExecutionID, claim.Epoch, claim)
}

func (s *computeStore) RecordTerminal(authenticatedNodeID string, terminal wire.TerminalResult) error {
	if terminal.NodeID != authenticatedNodeID {
		return fmt.Errorf("%w: terminal node ID is not the authenticated node", ErrComputeMalformedRecord)
	}
	return s.recordWorkerMutation(authenticatedNodeID, computeEventExecutionTerminal, terminal.JobID, terminal.ExecutionID, terminal.Epoch, terminal)
}

func (s *computeStore) recordWorkerMutation(authenticatedNodeID, kind, jobID, executionID string, epoch uint64, value any) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked(jobID)
	if err != nil {
		return err
	}
	record, err := makeComputeStoredRecord(computeRecordID(kind, value), authenticatedNodeID, computeDispositionAccepted, value)
	if err != nil {
		return err
	}
	if prior, duplicate, err := lookupStoredRecord(state, record); err != nil {
		return err
	} else if duplicate {
		if prior.Disposition == computeDispositionStaleFence {
			return ErrComputeStaleExecutionFence
		}
		if !fenceMatches(state, authenticatedNodeID, jobID, executionID, epoch) {
			return ErrComputeStaleExecutionFence
		}
		return nil
	}
	if !fenceMatches(state, authenticatedNodeID, jobID, executionID, epoch) || !computeWorkerMutationIsCurrent(state, kind) {
		record.Disposition = computeDispositionStaleFence
		if _, err := s.appendPreparedLocked(jobID, state, kind, executionID, epoch, record); err != nil {
			return err
		}
		return ErrComputeStaleExecutionFence
	}
	if _, err := s.appendPreparedLocked(jobID, state, kind, executionID, epoch, record); err != nil {
		return err
	}
	return nil
}

func computeWorkerMutationIsCurrent(state *computeJobState, kind string) bool {
	switch kind {
	case computeEventAssignmentClaimed:
		return state.Job.State == "offered" && state.Offer != nil && state.Claim == nil
	case computeEventLogChunkAccepted, computeEventExecutionTerminal:
		return state.Job.State == "running" && state.Grant != nil && state.Terminal == nil
	default:
		return false
	}
}

func (s *computeStore) RecordLogChunk(authenticatedNodeID string, chunk wire.LogChunk) (wire.LogAck, error) {
	if _, err := validateLogChunkValue(chunk); err != nil {
		return wire.LogAck{}, err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	state, err := s.loadLocked(chunk.JobID)
	if err != nil {
		return wire.LogAck{}, err
	}
	record, err := makeComputeStoredRecord(computeRecordID(computeEventLogChunkAccepted, chunk), authenticatedNodeID, computeDispositionAccepted, chunk)
	if err != nil {
		return wire.LogAck{}, err
	}
	if prior, duplicate, err := lookupStoredRecord(state, record); err != nil {
		return wire.LogAck{}, err
	} else if duplicate {
		if prior.Disposition == computeDispositionStaleFence {
			return wire.LogAck{}, ErrComputeStaleExecutionFence
		}
		if !fenceMatches(state, authenticatedNodeID, chunk.JobID, chunk.ExecutionID, chunk.Epoch) {
			return wire.LogAck{}, ErrComputeStaleExecutionFence
		}
		return computeLogAck(state, chunk), nil
	}
	if !fenceMatches(state, authenticatedNodeID, chunk.JobID, chunk.ExecutionID, chunk.Epoch) || !computeWorkerMutationIsCurrent(state, computeEventLogChunkAccepted) {
		record.Disposition = computeDispositionStaleFence
		if _, err := s.appendPreparedLocked(chunk.JobID, state, computeEventLogChunkAccepted, chunk.ExecutionID, chunk.Epoch, record); err != nil {
			return wire.LogAck{}, err
		}
		return wire.LogAck{}, ErrComputeStaleExecutionFence
	}
	prepared, err := s.appendPreparedLocked(chunk.JobID, state, computeEventLogChunkAccepted, chunk.ExecutionID, chunk.Epoch, record)
	if err != nil {
		return wire.LogAck{}, err
	}
	return computeLogAck(prepared, chunk), nil
}

func computeLogAck(state *computeJobState, chunk wire.LogChunk) wire.LogAck {
	ack := wire.LogAck{JobID: chunk.JobID, ExecutionID: chunk.ExecutionID, Stream: chunk.Stream}
	if stream := state.LogStreams[computeLogStreamKey(chunk.ExecutionID, chunk.Epoch, chunk.Stream)]; stream != nil {
		ack.ContiguousSequence = stream.ContiguousSequence
	}
	return ack
}

func (s *computeStore) rebuildControllerIndexLocked() error {
	ids, err := s.List()
	if err != nil {
		return err
	}
	index := make(map[string]computeIdempotencyEntry, len(ids))
	entries := make([]computeIdempotencyEntry, 0, len(ids))
	for _, id := range ids {
		state, err := s.loadLocked(id)
		if err != nil {
			return err
		}
		entry := computeIdempotencyEntry{
			TenantID: state.Job.Authz.TenantID, Principal: state.Job.Authz.Principal,
			IdempotencyKey: state.Job.IdempotencyKey, SpecHash: state.Job.SpecHash, JobID: state.Job.JobID,
		}
		key := computeScopeKey(entry.TenantID, entry.Principal, entry.IdempotencyKey)
		if prior, exists := index[key]; exists && prior.JobID != entry.JobID {
			return fmt.Errorf("%w: jobs %s and %s claim one idempotency scope", ErrComputeCorruptStore, prior.JobID, entry.JobID)
		}
		index[key] = entry
		entries = append(entries, entry)
	}
	sort.Slice(entries, func(i, j int) bool {
		if entries[i].TenantID != entries[j].TenantID {
			return entries[i].TenantID < entries[j].TenantID
		}
		if entries[i].Principal != entries[j].Principal {
			return entries[i].Principal < entries[j].Principal
		}
		if entries[i].IdempotencyKey != entries[j].IdempotencyKey {
			return entries[i].IdempotencyKey < entries[j].IdempotencyKey
		}
		return entries[i].JobID < entries[j].JobID
	})
	raw, err := json.MarshalIndent(computeControllerState{Entries: entries}, "", "  ")
	if err != nil {
		return err
	}
	if err := writeFileAtomicSynced(s.controllerPath(), raw, 0o600); err != nil {
		return err
	}
	s.index = index
	return nil
}
