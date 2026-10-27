package main

import (
	"bufio"
	"bytes"

	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"
	"unicode/utf8"
)

// origin/main inspected at 46142ad4 uses reader 35. Never reuse reader 30:
// that number already belongs to build-capability persistence upstream.
// Above all inspected shipped reader floors (upstream 35). Peer schema
// compatibility is checked independently at store open.
const retirementStoreReaderVersion = 37

const retirementOwnerStoreReaderVersion = 39

const retirementOwnerRecords = "owner_records"

const retirementManifestLimit = 32 << 20

type retirementPin struct {
	Path     string              `json:"path"`
	Kind     string              `json:"kind"`
	Identity generatedFSIdentity `json:"identity"`
	Digest   string              `json:"digest,omitempty"`
	Missing  bool                `json:"missing,omitempty"`
}

type retirementEntry struct {
	RepoMissing      bool                `json:"repoMissing,omitempty"`
	BranchMissing    bool                `json:"branchMissing,omitempty"`
	ActiveWorkers    []string            `json:"activeWorkers,omitempty"`
	DriverActive     bool                `json:"driverActive,omitempty"`
	WorkerGeneration uint64              `json:"workerGeneration,omitempty"`
	RepoPath         string              `json:"repoPath,omitempty"`
	CommonIdentity   generatedFSIdentity `json:"commonIdentity,omitempty"`
	BranchHead       string              `json:"branchHead,omitempty"`
	WorktreeHead     string              `json:"worktreeHead,omitempty"`
	Dirty            bool                `json:"dirty"`
	Untracked        bool                `json:"untracked"`
	Effect           string              `json:"effect,omitempty"`
	TaskID           string              `json:"taskId"`
	RepoKey          string              `json:"repoKey"`
	Revision         int64               `json:"revision"`
	// Snapshot is the complete frozen task projection, not a status-only
	// approximation. Live coordinator admission must also capture claims and
	// invocation generations before it can use these persistence primitives.
	Snapshot json.RawMessage `json:"snapshot"`
	Pins     []retirementPin `json:"pins"`
}

type retirementManifest struct {
	TaskFamily    string                `json:"taskFamily,omitempty"`
	OperationID   string                `json:"operationId"`
	PaneID        string                `json:"paneId"`
	PaneCreatedAt time.Time             `json:"paneCreatedAt"`
	BootID        string                `json:"bootId"`
	SnapshotAt    time.Time             `json:"snapshotAt"`
	ExpiresAt     time.Time             `json:"expiresAt"`
	Fingerprint   string                `json:"fingerprint"`
	Entries       []retirementEntry     `json:"entries"`
	Excluded      []retirementExclusion `json:"excluded,omitempty"`
}

func (m retirementManifest) readerVersion() int {
	if m.TaskFamily == retirementOwnerRecords {
		return retirementOwnerStoreReaderVersion
	}
	return retirementStoreReaderVersion
}

type retirementResult struct {
	TaskID   string `json:"taskId"`
	Phase    string `json:"phase"`
	Sequence int64  `json:"sequence"`
	Status   string `json:"status"`
	Archived bool   `json:"archived"`
	Error    string `json:"error,omitempty"`
}

type retirementRecord struct {
	Kind        string               `json:"kind"`
	OperationID string               `json:"operationId"`
	Manifest    *retirementManifest  `json:"manifest,omitempty"`
	Evidence    string               `json:"authorizationEvidence,omitempty"`
	Result      *retirementResult    `json:"result,omitempty"`
	Promotion   *retirementPromotion `json:"promotion,omitempty"`
	Batch       []string             `json:"batch,omitempty"`
}

type retirementJournalEntry struct {
	Sequence uint64           `json:"sequence"`
	Previous string           `json:"previous"`
	Record   retirementRecord `json:"record"`
	Digest   string           `json:"digest"`
}

type retirementWatermark struct {
	SchemaVersion int    `json:"schemaVersion"`
	StoreUUID     string `json:"storeUUID"`
	Sequence      uint64 `json:"sequence"`
	Digest        string `json:"digest"`
}

type retirementOperation struct {
	Manifest   retirementManifest
	Evidence   string
	Accepted   bool
	Durable    bool
	Results    map[string]retirementResult
	Promotions map[string]retirementPromotion
	// Derived exclusively from durable batch_started records. A failed row
	// cannot monopolize subsequent calls, including after daemon restart.
	NextTask int
}

type retirementAuthorityStore struct {
	root     string
	material *retirementMaterial
	// One admission token, acquired without waiting. Durability operations
	// remain on the owning goroutine; a context deadline cannot abandon an
	// fsync to continue mutating behind the caller's back.
	admission  chan struct{}
	mu         sync.Mutex
	marker     retirementWatermark
	entries    []retirementJournalEntry
	operations map[string]*retirementOperation
	loadErr    error
	uncertain  bool
	// Fault seams fail operations; they do not waive the real sync calls.
	fault func(string) error
}

func newRetirementAuthority(dataDir string) *retirementAuthorityStore {
	return &retirementAuthorityStore{root: filepath.Join(dataDir, "workflow", "retirement"), material: newWfRetirementMaterial(), admission: make(chan struct{}, 1), operations: map[string]*retirementOperation{}}
}

// Read-only and independently callable before workflow init, recovery,
// preview load or GC. Absence is valid only if neither infrastructure nor a
// retirement reader floor exists. Errors return a poisoned authority too.
func loadRetirementAuthority(dataDir string) (*retirementAuthorityStore, error) {
	a := newRetirementAuthority(dataDir)
	err := a.load(dataDir)
	if err != nil {
		a.loadErr = err
		a.material.index.Store(&retirementIndex{Error: err.Error()})
	}
	return a, err
}

func retirementStrictJSON(raw []byte, target any) error {
	// encoding/json otherwise silently accepts repeated object keys. Exact
	// scope and immutable authority records must never use last-key-wins.
	keys := json.NewDecoder(bytes.NewReader(raw))
	keys.UseNumber()
	var value func(int) error
	value = func(depth int) error {
		if depth > 128 {
			return errors.New("retirement JSON nesting exceeds bounds")
		}
		token, err := keys.Token()
		if err != nil {
			return err
		}
		delimiter, ok := token.(json.Delim)
		if !ok {
			return nil
		}
		switch delimiter {
		case '{':
			seen := map[string]bool{}
			for keys.More() {
				key, err := keys.Token()
				if err != nil {
					return err
				}
				name, ok := key.(string)
				if !ok || seen[name] {
					return errors.New("retirement JSON has duplicate or invalid key")
				}
				seen[name] = true
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		case '[':
			for keys.More() {
				if err := value(depth + 1); err != nil {
					return err
				}
			}
		default:
			return errors.New("retirement JSON unexpected delimiter")
		}
		_, err = keys.Token()
		return err
	}
	if err := value(0); err != nil {
		return err
	}
	if _, err := keys.Token(); err != io.EOF {
		return errors.New("retirement JSON has trailing input")
	}
	d := json.NewDecoder(bytes.NewReader(raw))
	d.DisallowUnknownFields()
	if err := d.Decode(target); err != nil {
		return err
	}
	var extra any
	if err := d.Decode(&extra); err != io.EOF {
		return errors.New("retirement JSON has trailing input")
	}
	return nil
}

func (a *retirementAuthorityStore) load(dataDir string) error {
	markerRaw, err := os.ReadFile(filepath.Join(a.root, "committed.json"))
	if os.IsNotExist(err) {
		if _, rootErr := os.Lstat(a.root); !os.IsNotExist(rootErr) {
			return fmt.Errorf("%w: incomplete infrastructure", errRetirementAuthority)
		}
		var version struct {
			MinReaderVersion int `json:"minReaderVersion"`
		}
		if raw, versionErr := os.ReadFile(filepath.Join(dataDir, "workflow", "version.json")); versionErr == nil {
			if json.Unmarshal(raw, &version) != nil || version.MinReaderVersion >= retirementStoreReaderVersion {
				return fmt.Errorf("%w: retirement authority missing", errRetirementAuthority)
			}
		} else if !os.IsNotExist(versionErr) {
			return versionErr
		}
		return nil
	}
	if err != nil {
		return err
	}
	if err := retirementStrictJSON(markerRaw, &a.marker); err != nil {
		return err
	}
	if a.marker.SchemaVersion != 1 || len(a.marker.StoreUUID) != 32 || !retirementHex(a.marker.StoreUUID, 32) {
		return errors.New("retirement authority marker invalid")
	}
	f, err := os.Open(filepath.Join(a.root, "authority.jsonl"))
	if err != nil {
		return fmt.Errorf("retirement journal: %w", err)
	}
	defer f.Close()
	scan := bufio.NewScanner(f)
	scan.Buffer(make([]byte, 4096), retirementManifestLimit+1<<20)
	previous := ""
	for scan.Scan() {
		var entry retirementJournalEntry
		if err := retirementStrictJSON(scan.Bytes(), &entry); err != nil {
			return fmt.Errorf("retirement journal: %w", err)
		}
		if entry.Sequence != uint64(len(a.entries))+1 || entry.Previous != previous || entry.Digest != retirementEntryDigest(entry) {
			return errors.New("retirement journal sequence/digest mismatch")
		}
		if err := a.reduce(entry.Record); err != nil {
			return err
		}
		a.entries = append(a.entries, entry)
		previous = entry.Digest
	}
	if err := scan.Err(); err != nil {
		return err
	}
	if a.marker.Sequence > uint64(len(a.entries)) {
		return errors.New("retirement committed journal tail missing")
	}
	if a.marker.Sequence == 0 {
		if a.marker.Digest != "" {
			return errors.New("retirement zero watermark has digest")
		}
	} else if a.entries[a.marker.Sequence-1].Digest != a.marker.Digest {
		return errors.New("retirement committed watermark mismatch")
	}
	// Conservatively pin and hold any visible uncommitted tail, but do not
	// confer durable authority. A successful reconciliation sync is required.
	// A warm process restart cannot prove that a previously visible watermark
	// rename's directory sync succeeded. Revalidate and resync on an explicit
	// attempt before conferring mutation authority; read stays observational.
	a.uncertain = len(a.entries) != 0
	a.publish()
	// A planned rename is retention preparation, never cancellation consent.
	// Reconcile its alias before allowing any independently started cleaners.
	for _, op := range a.operations {
		for _, promotion := range op.Promotions {
			if err := a.reconcilePromotion(promotion); err != nil {
				return err
			}
		}
	}
	return nil
}

func retirementHex(s string, size int) bool {
	if len(s) != size {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil && strings.ToLower(s) == s
}

func retirementEntryDigest(entry retirementJournalEntry) string {
	entry.Digest = ""
	raw, _ := json.Marshal(entry)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func retirementManifestFingerprint(m retirementManifest) string {
	m.Fingerprint = ""
	raw, _ := json.Marshal(m)
	hash := sha256.Sum256(raw)
	return hex.EncodeToString(hash[:])
}

func retirementValidateManifest(m retirementManifest) error {
	if m.TaskFamily != "" && m.TaskFamily != retirementOwnerRecords {
		return errors.New("unknown retirement task family")
	}
	if !configurationCallerValid(m.PaneID) || m.PaneCreatedAt.IsZero() || m.BootID == "" || m.SnapshotAt.IsZero() || !m.ExpiresAt.After(m.SnapshotAt) || m.ExpiresAt.Sub(m.SnapshotAt) > 10*time.Minute || !retirementHex(m.OperationID, 32) {
		return errors.New("retirement manifest provenance invalid")
	}
	if len(m.Entries)+len(m.Excluded) < 1 || len(m.Entries)+len(m.Excluded) > 1000 {
		return errors.New("retirement manifest must contain 1–1000 exact IDs")
	}
	previous := ""
	repos := map[string]bool{}
	for _, e := range m.Entries {
		if !taskIDRe.MatchString(e.TaskID) || taskIDRe.FindString(e.TaskID) != e.TaskID || e.TaskID <= previous || e.TaskID == "wft_da274062" || !filepath.IsAbs(e.RepoKey) {
			return errors.New("retirement manifest IDs/repositories invalid or unsorted")
		}
		previous = e.TaskID
		repos[e.RepoKey] = true
		var task retainedTaskIdentity
		if len(e.Snapshot) == 0 || json.Unmarshal(e.Snapshot, &task) != nil || task.ID != e.TaskID || task.LastAppliedEventSeq != e.Revision {
			return errors.New("retirement snapshot ineligible or mismatched")
		}
		for _, pin := range e.Pins {
			if !filepath.IsAbs(pin.Path) || filepath.Clean(pin.Path) != pin.Path || pin.Path == filepath.VolumeName(pin.Path)+string(filepath.Separator) || pin.Kind == "" || (!pin.Missing && !pin.Identity.Valid) {
				return errors.New("retirement material identity unavailable")
			}
		}
	}
	if len(m.Entries)+len(m.Excluded) > 1000 {
		return errors.New("retirement requested scope exceeds 1000 IDs")
	}
	eligible := map[string]bool{}
	for _, row := range m.Entries {
		eligible[row.TaskID] = true
	}
	previous = ""
	for _, row := range m.Excluded {
		if row.TaskID <= previous || eligible[row.TaskID] || taskIDRe.FindString(row.TaskID) != row.TaskID || row.Reason == "" || !filepath.IsAbs(row.RepoKey) {
			return errors.New("retirement exclusion scope invalid")
		}
		var t retainedTaskIdentity
		if json.Unmarshal(row.Snapshot, &t) != nil || t.ID != row.TaskID {
			return errors.New("retirement exclusion snapshot invalid")
		}
		repos[row.RepoKey] = true
		previous = row.TaskID
	}
	if len(repos) > 16 {
		return errors.New("retirement manifest exceeds 16 repositories")
	}
	raw, err := json.Marshal(m)
	if err != nil || len(raw) > retirementManifestLimit {
		return errors.New("retirement manifest exceeds 32 MiB")
	}
	if m.Fingerprint != retirementManifestFingerprint(m) {
		return errors.New("retirement manifest fingerprint mismatch")
	}
	return nil
}

func (a *retirementAuthorityStore) reduce(r retirementRecord) error {
	op := a.operations[r.OperationID]
	if len(r.Batch) != 0 && r.Kind != "batch_started" {
		return errors.New("retirement batch outside continuation record")
	}
	switch r.Kind {
	case "prepared":
		if r.Manifest == nil || r.Evidence != "" || r.Result != nil || r.Promotion != nil || op != nil || r.Manifest.OperationID != r.OperationID {
			return errors.New("retirement preparation invalid")
		}
		if err := retirementValidateManifest(*r.Manifest); err != nil {
			return err
		}
		a.operations[r.OperationID] = &retirementOperation{Manifest: *r.Manifest, Results: map[string]retirementResult{}, Promotions: map[string]retirementPromotion{}}
	case "accepted":
		if op == nil || op.Accepted || r.Manifest != nil || r.Result != nil || r.Promotion != nil || !utf8.ValidString(r.Evidence) || len(r.Evidence) < 1 || len(r.Evidence) > 4000 || strings.TrimSpace(r.Evidence) == "" {
			return errors.New("retirement consent invalid")
		}
		op.Accepted, op.Evidence = true, r.Evidence
	case "batch_started":
		if op == nil || !op.Accepted || r.Manifest != nil || r.Result != nil || r.Promotion != nil || r.Evidence != "" || len(r.Batch) == 0 {
			return errors.New("retirement batch without accepted authority")
		}
		batch, next := retirementNextBatch(*op)
		if len(batch) != len(r.Batch) {
			return errors.New("retirement batch selection changed")
		}
		for i, entry := range batch {
			if entry.TaskID != r.Batch[i] {
				return errors.New("retirement batch selection changed")
			}
		}
		op.NextTask = next
	case "result":
		if op == nil || !op.Accepted || r.Result == nil || r.Manifest != nil || r.Promotion != nil || r.Evidence != "" {
			return errors.New("retirement result without accepted authority")
		}
		result := *r.Result
		found := false
		for _, entry := range op.Manifest.Entries {
			if entry.TaskID == result.TaskID {
				found = true
				break
			}
		}
		if !found || !retirementPhaseValid(result.Phase) || result.Sequence < 0 {
			return errors.New("retirement result scope/phase invalid")
		}
		if old, exists := op.Results[result.TaskID]; exists {
			if old.Sequence > result.Sequence || (retirementPhaseFinal(old.Phase) && old != result) || (old.Phase == "cancelled" && result.Phase != "cancelled" && !retirementPhaseFinal(result.Phase)) {
				return errors.New("retirement result regression")
			}
		}
		op.Results[result.TaskID] = result
	case "promotion_planned", "promotion_completed":
		if op == nil || r.Promotion == nil || r.Manifest != nil || r.Result != nil || r.Evidence != "" {
			return errors.New("retirement promotion without preparation")
		}
		p := *r.Promotion
		if err := a.validatePromotion(op.Manifest, p); err != nil {
			return err
		}
		old, exists := op.Promotions[p.Source]
		if r.Kind == "promotion_planned" {
			if exists || p.Completed {
				return errors.New("retirement promotion plan immutable")
			}
		} else {
			if !exists || old.Completed || !p.Completed {
				return errors.New("retirement promotion completion invalid")
			}
			x := p
			x.Completed = false
			if x != old {
				return errors.New("retirement promotion identity changed")
			}
		}
		op.Promotions[p.Source] = p
	default:
		return errors.New("retirement unknown authority record")
	}
	return nil
}

func retirementPhaseValid(phase string) bool {
	switch phase {
	case "shutdown_requested", "shutdown_pending", "shutdown_uncertain", "cancelled", "archived", "already_archived", "failed", "deferred":
		return true
	}
	return false
}

func retirementPhaseFinal(phase string) bool {
	return phase == "archived" || phase == "already_archived"
}

// Called only while holding material writer + a.mu, or on an unpublished
// authority during boot. Every published index is permanently immutable.
func (a *retirementAuthorityStore) publish() {
	i := &retirementIndex{Tasks: map[string]bool{}, Held: map[string]bool{}, Bindings: map[string]retirementMaterialBinding{}, Promotions: map[string]retirementPromotion{}}
	paths := map[string]bool{}
	for _, op := range a.operations {
		op.Durable = op.Accepted && !a.uncertain
		for _, entry := range op.Manifest.Entries {
			i.Tasks[entry.TaskID] = true
			if op.Accepted {
				i.Held[entry.TaskID] = true
				i.Bindings[entry.TaskID] = retirementMaterialBinding{StoreUUID: a.marker.StoreUUID, AuthorityPath: a.root, OperationID: op.Manifest.OperationID}
			}
			for _, pin := range entry.Pins {
				paths[pin.Path] = true
			}
		}
		for _, promotion := range op.Promotions {
			paths[promotion.Source] = true
			paths[promotion.Destination] = true
			if promotion.Completed {
				if old, exists := i.Promotions[promotion.Source]; exists && old != promotion {
					i.Error = "conflicting retained preview promotions"
				}
				i.Promotions[promotion.Source] = promotion
			}
		}
	}
	for path := range paths {
		i.Paths = append(i.Paths, path)
	}
	sort.Strings(i.Paths)
	a.material.index.Store(i)
}

type retainedTaskIdentity struct {
	ID                  string `json:"id"`
	LastAppliedEventSeq int64  `json:"lastAppliedEventSeq"`
}
