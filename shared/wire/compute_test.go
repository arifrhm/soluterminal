package wire

import (
	"bytes"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"sort"
	"strings"
	"testing"
)

type computeGoldenPair struct {
	Full json.RawMessage `json:"full"`
	Zero json.RawMessage `json:"zero"`
}

type computeContractFixture struct {
	name string
	full any
	zero any
}

func TestComputeContractsGolden(t *testing.T) {
	golden := loadComputeContractGolden(t)
	fixtures := computeContractFixtures()

	wantNames := make(map[string]struct{}, len(fixtures))
	for _, fixture := range fixtures {
		fixture := fixture
		t.Run(fixture.name, func(t *testing.T) {
			wantNames[fixture.name] = struct{}{}
			pair, ok := golden[fixture.name]
			if !ok {
				t.Fatalf("golden has no entry for %s", fixture.name)
			}

			assertGoldenEncoding(t, fixture.full, pair.Full, "full")
			assertGoldenEncoding(t, fixture.zero, pair.Zero, "zero")
			assertGoldenRoundTrip(t, fixture.full, pair.Full)
			assertStructGoldenKeys(t, fixture.full, pair.Full, true)
			assertStructGoldenKeys(t, fixture.zero, pair.Zero, false)
		})
	}

	if len(golden) != len(wantNames) {
		t.Fatalf("golden has %d entries, want %d", len(golden), len(wantNames))
	}
	for name := range golden {
		if _, ok := wantNames[name]; !ok {
			t.Errorf("golden has unexpected entry %q", name)
		}
	}
}

func TestComputeContractsForbiddenSubmissionFields(t *testing.T) {
	assertNoJSONFieldContaining(t, reflect.TypeOf(NodeEnrollmentRequest{}), "token", "secret")
	assertNoJSONFieldContaining(t, reflect.TypeOf(ComputeJobSubmitRequest{}), "authorization", "authz")
}

func TestNegotiateProtocolGolden(t *testing.T) {
	cases := loadComputeNegotiationGolden(t)
	wantCases := []string{
		"highestCommonV1Minor",
		"knownFeatureIntersection",
		"noCommonMajor",
		"disjointV1Minors",
		"malformedRange",
		"unsupportedKnownRequiredFeature",
		"unknownRequiredFeature",
		"unknownOptionalAdvertisedBitIgnored",
	}
	if len(cases) != len(wantCases) {
		t.Fatalf("negotiation golden has %d cases, want %d", len(cases), len(wantCases))
	}
	for _, name := range wantCases {
		t.Run(name, func(t *testing.T) {
			testCase, ok := cases[name]
			if !ok {
				t.Fatalf("negotiation golden has no case %q", name)
			}
			got, err := NegotiateProtocol(testCase.Local, testCase.Remote, testCase.RequiredFeatures)
			if testCase.Error == "" {
				if err != nil {
					t.Fatalf("NegotiateProtocol() error = %v", err)
				}
				if got != testCase.Selection {
					t.Fatalf("NegotiateProtocol() = %#v, want %#v", got, testCase.Selection)
				}
				return
			}

			if testCase.Error != ErrIncompatibleVersion.Error() {
				t.Fatalf("golden error = %q, want %q", testCase.Error, ErrIncompatibleVersion)
			}
			if !errors.Is(err, ErrIncompatibleVersion) {
				t.Fatalf("NegotiateProtocol() error = %v, want errors.Is(_, ErrIncompatibleVersion)", err)
			}
			if got != (NegotiatedProtocol{}) {
				t.Fatalf("NegotiateProtocol() failure selection = %#v, want zero", got)
			}
			if got != testCase.Selection {
				t.Fatalf("NegotiateProtocol() failure selection = %#v, golden = %#v", got, testCase.Selection)
			}
		})
	}
}

func TestNegotiateProtocolSchemaVersionsAreIndependent(t *testing.T) {
	local := ProtocolRange{MinMajor: 1, MaxMajor: 1, MinMinor: 0, MaxMinor: 4, Features: ComputeFeatureMaskV1}
	remote := ProtocolRange{MinMajor: 1, MaxMajor: 1, MinMinor: 2, MaxMinor: 3, Features: FeatureSequencedLogs | FeatureLeaseFencing}

	job := ComputeJobSpec{SchemaVersion: 1}
	artifact := ArtifactRef{SchemaVersion: 1}
	want, err := NegotiateProtocol(local, remote, FeatureLeaseFencing)
	if err != nil {
		t.Fatalf("initial negotiation: %v", err)
	}
	job.SchemaVersion = 90210
	artifact.SchemaVersion = 404
	got, err := NegotiateProtocol(local, remote, FeatureLeaseFencing)
	if err != nil {
		t.Fatalf("negotiation after schema changes: %v", err)
	}
	if got != want {
		t.Fatalf("schema versions changed negotiation from %#v to %#v", want, got)
	}
	if job.SchemaVersion == artifact.SchemaVersion {
		t.Fatal("test setup did not preserve independent job and artifact schemas")
	}
}

func TestCheckRequiredFeatures(t *testing.T) {
	tests := []struct {
		name      string
		available FeatureBits
		required  FeatureBits
		wantErr   bool
	}{
		{name: "known available", available: ComputeFeatureMaskV1, required: FeatureArtifactStreaming},
		{name: "unknown optional ignored", available: FeatureSequencedLogs | 1<<63, required: FeatureSequencedLogs},
		{name: "known unavailable", available: FeatureSequencedLogs, required: FeatureLeaseFencing, wantErr: true},
		{name: "unknown required", available: 1 << 63, required: 1 << 63, wantErr: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			err := CheckRequiredFeatures(test.available, test.required)
			if test.wantErr && !errors.Is(err, ErrIncompatibleVersion) {
				t.Fatalf("CheckRequiredFeatures() error = %v, want incompatible version", err)
			}
			if !test.wantErr && err != nil {
				t.Fatalf("CheckRequiredFeatures() error = %v", err)
			}
		})
	}
}

func TestNegotiateProtocolRejectsMalformedRanges(t *testing.T) {
	valid := ProtocolRange{MinMajor: 1, MaxMajor: 1, MinMinor: 0, MaxMinor: 3, Features: ComputeFeatureMaskV1}
	tests := []struct {
		name   string
		local  ProtocolRange
		remote ProtocolRange
	}{
		{name: "reversed local majors", local: ProtocolRange{MinMajor: 2, MaxMajor: 1, MinMinor: 0, MaxMinor: 3}, remote: valid},
		{name: "reversed remote minors", local: valid, remote: ProtocolRange{MinMajor: 1, MaxMajor: 1, MinMinor: 3, MaxMinor: 2}},
		{name: "negative local major", local: ProtocolRange{MinMajor: -1, MaxMajor: 1, MinMinor: 0, MaxMinor: 3}, remote: valid},
		{name: "negative remote minor", local: valid, remote: ProtocolRange{MinMajor: 1, MaxMajor: 1, MinMinor: -1, MaxMinor: 3}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := NegotiateProtocol(test.local, test.remote, 0)
			if !errors.Is(err, ErrIncompatibleVersion) {
				t.Fatalf("NegotiateProtocol() error = %v, want incompatible version", err)
			}
			if got != (NegotiatedProtocol{}) {
				t.Fatalf("NegotiateProtocol() = %#v, want zero selection", got)
			}
		})
	}
}

type computeNegotiationGoldenCase struct {
	Local            ProtocolRange      `json:"local"`
	Remote           ProtocolRange      `json:"remote"`
	RequiredFeatures FeatureBits        `json:"requiredFeatures"`
	Selection        NegotiatedProtocol `json:"selection"`
	Error            string             `json:"error,omitempty"`
}

func loadComputeContractGolden(t *testing.T) map[string]computeGoldenPair {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "compute_v1.golden.json"))
	if err != nil {
		t.Fatalf("read compute contract golden: %v", err)
	}
	var golden map[string]computeGoldenPair
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode compute contract golden: %v", err)
	}
	return golden
}

func loadComputeNegotiationGolden(t *testing.T) map[string]computeNegotiationGoldenCase {
	t.Helper()
	data, err := os.ReadFile(filepath.Join("testdata", "compute_negotiation_v1.golden.json"))
	if err != nil {
		t.Fatalf("read compute negotiation golden: %v", err)
	}
	var golden map[string]computeNegotiationGoldenCase
	if err := json.Unmarshal(data, &golden); err != nil {
		t.Fatalf("decode compute negotiation golden: %v", err)
	}
	return golden
}

func assertGoldenEncoding(t *testing.T, value any, golden json.RawMessage, kind string) {
	t.Helper()
	got, err := json.Marshal(value)
	if err != nil {
		t.Fatalf("marshal %s value: %v", kind, err)
	}
	var want bytes.Buffer
	if err := json.Compact(&want, golden); err != nil {
		t.Fatalf("compact %s golden: %v", kind, err)
	}
	if !bytes.Equal(got, want.Bytes()) {
		t.Fatalf("%s encoding mismatch:\n got %s\nwant %s", kind, got, want.Bytes())
	}
}

func assertGoldenRoundTrip(t *testing.T, want any, golden json.RawMessage) {
	t.Helper()
	decoded := reflect.New(reflect.TypeOf(want))
	if err := json.Unmarshal(golden, decoded.Interface()); err != nil {
		t.Fatalf("decode full golden into %T: %v", want, err)
	}
	if got := decoded.Elem().Interface(); !reflect.DeepEqual(got, want) {
		t.Fatalf("decoded full golden mismatch:\n got %#v\nwant %#v", got, want)
	}
}

func assertStructGoldenKeys(t *testing.T, value any, golden json.RawMessage, full bool) {
	t.Helper()
	typ := reflect.TypeOf(value)
	if typ.Kind() != reflect.Struct {
		return
	}
	var object map[string]json.RawMessage
	if err := json.Unmarshal(golden, &object); err != nil {
		t.Fatalf("decode golden object: %v", err)
	}

	wantKeys := make(map[string]bool)
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		if !field.IsExported() {
			continue
		}
		parts := strings.Split(field.Tag.Get("json"), ",")
		if len(parts) == 0 || parts[0] == "" || parts[0] == "-" {
			continue
		}
		omitEmpty := false
		for _, option := range parts[1:] {
			omitEmpty = omitEmpty || option == "omitempty"
		}
		wantKeys[parts[0]] = !omitEmpty || full
	}

	for key, shouldExist := range wantKeys {
		_, exists := object[key]
		if exists != shouldExist {
			t.Errorf("key %q exists = %v, want %v", key, exists, shouldExist)
		}
	}
	if len(object) != countTrue(wantKeys) {
		keys := make([]string, 0, len(object))
		for key := range object {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		t.Errorf("golden emitted %d keys, want %d: %v", len(object), countTrue(wantKeys), keys)
	}
}

func countTrue(values map[string]bool) int {
	count := 0
	for _, value := range values {
		if value {
			count++
		}
	}
	return count
}

func assertNoJSONFieldContaining(t *testing.T, typ reflect.Type, fragments ...string) {
	t.Helper()
	for i := 0; i < typ.NumField(); i++ {
		field := typ.Field(i)
		name := strings.ToLower(field.Name + " " + field.Tag.Get("json"))
		for _, fragment := range fragments {
			if strings.Contains(name, fragment) {
				t.Errorf("%s unexpectedly exposes field %s (%s)", typ.Name(), field.Name, field.Tag.Get("json"))
			}
		}
	}
}

func computeContractFixtures() []computeContractFixture {
	approvedAt := "2026-09-01T01:02:03Z"
	revokedAt := "2026-09-02T01:02:03Z"
	load := 1.25
	onACPower := true
	batteryPercent := 88
	minimumBattery := 35
	exitCode := 17

	protocol := ProtocolRange{
		MinMajor: 1,
		MaxMajor: 1,
		MinMinor: 0,
		MaxMinor: 3,
		Features: ComputeFeatureMaskV1,
	}
	handler := HandlerVersion{HandlerID: "repo.verify.v1", Version: 1}
	admin := AdminCapabilities{
		ApprovedWorkspaceIDs: []string{"workspace-1"},
		ApprovedHandlers:     []HandlerVersion{handler},
		Labels:               []string{"singapore"},
		HandlerPreference:    map[string]int{"repo.verify.v1": 7},
	}
	observed := ObservedCapabilities{
		SampledAt:        "2026-09-01T01:02:03Z",
		OS:               "linux",
		Architecture:     "amd64",
		LogicalCPU:       16,
		TotalRAMBytes:    34359738368,
		GPUs:             []GPUCapability{{Vendor: "nvidia", Model: "RTX", MemoryBytes: 8589934592}},
		DetectedHandlers: []HandlerVersion{handler},
		ToolVersions:     map[string]string{"go": "1.25.0"},
		WorkspaceDigests: map[string]string{"workspace-1": "sha256:workspace"},
		CacheDigests:     []string{"sha256:cache"},
	}
	node := NodeRecord{
		SchemaVersion:        1,
		NodeID:               "node-1",
		TenantID:             "tenant-1",
		PublicKeyFingerprint: "ed25519:node",
		State:                "enabled",
		ApprovedAt:           &approvedAt,
		RevokedAt:            &revokedAt,
		Admin:                admin,
		Observed:             observed,
		Protocol:             protocol,
	}
	requirements := JobRequirements{
		OS:               []string{"linux"},
		Architectures:    []string{"amd64"},
		MinimumCPU:       4,
		MinimumGPU:       1,
		MinimumRAMBytes:  4294967296,
		MinimumDiskBytes: 8589934592,
		RequiredFeatures: FeatureSequencedLogs | FeatureLeaseFencing,
		RequireDataOwner: true,
		DataOwnerNodeID:  "node-1",
	}
	artifact := ArtifactRef{
		SchemaVersion: 1,
		StoreID:       "store-1",
		ArtifactID:    "artifact-1",
		SHA256:        "0123456789abcdef",
		MediaType:     "application/x-tar",
		SizeBytes:     12345,
	}
	spec := ComputeJobSpec{
		SchemaVersion:    1,
		JobKind:          "repo.verify.v1",
		Handler:          handler,
		HandlerProfile:   "soluterminal-shared-tests.v1",
		WorkspaceID:      "workspace-1",
		Revision:         "8fcd0ead",
		SourceDigest:     "sha256:source",
		DataClass:        "internal",
		Requirements:     requirements,
		Inputs:           []ArtifactRef{artifact},
		Priority:         "normal",
		TimeoutSec:       900,
		CancelPolicy:     "lease_fenced",
		MaxAttempts:      2,
		RetryTimeout:     true,
		RequiredFeatures: FeatureSequencedLogs | FeatureLeaseFencing,
	}
	authz := ComputeAuthz{TenantID: "tenant-1", Principal: "principal-1", SubmittedAt: "2026-09-01T01:02:03Z"}
	assignmentAuthz := AssignmentAuthorization{
		TenantID:     "tenant-1",
		Principal:    "principal-1",
		AssignmentID: "assignment-1",
		SpecHash:     "sha256:spec",
		IssuedAt:     "2026-09-01T01:03:00Z",
		ExpiresAt:    "2026-09-01T01:03:15Z",
	}
	usage := ResourceUsage{PeakRAMBytes: 123456, CPUSeconds: 12.5, ReadBytes: 3456, WriteBytes: 7890}

	return []computeContractFixture{
		{name: "ProtocolRange", full: protocol, zero: ProtocolRange{}},
		{name: "FeatureBits", full: ComputeFeatureMaskV1, zero: FeatureBits(0)},
		{name: "NegotiatedProtocol", full: NegotiatedProtocol{Major: 1, Minor: 3, Features: ComputeFeatureMaskV1}, zero: NegotiatedProtocol{}},
		{name: "ComputeEnvelope", full: ComputeEnvelope{ProtocolMajor: 1, ProtocolMinor: 3, SessionID: "session-1", Sequence: 4, MessageID: "message-1", Timestamp: "2026-09-01T01:02:03Z", Type: "assignment.offer", RequiredFeatures: FeatureLeaseFencing, Payload: json.RawMessage(`{"assignmentId":"assignment-1"}`), Signature: "ed25519:signature"}, zero: ComputeEnvelope{}},
		{name: "NodeEnrollmentRequest", full: NodeEnrollmentRequest{SchemaVersion: 1, NodePublicKey: "ed25519:public", ControllerFingerprint: "ed25519:controller", Protocol: protocol}, zero: NodeEnrollmentRequest{}},
		{name: "NodeEnrollmentResponse", full: NodeEnrollmentResponse{SchemaVersion: 1, ControllerProof: "ed25519:proof", Node: node}, zero: NodeEnrollmentResponse{}},
		{name: "NodeSessionHello", full: NodeSessionHello{NodeID: "node-1", Protocol: protocol, ClientNonce: "nonce-1"}, zero: NodeSessionHello{}},
		{name: "NodeSessionChallenge", full: NodeSessionChallenge{ChallengeID: "challenge-1", Challenge: "base64:challenge", ControllerProof: "ed25519:proof"}, zero: NodeSessionChallenge{}},
		{name: "NodeSessionResponse", full: NodeSessionResponse{ChallengeID: "challenge-1", NodeProof: "ed25519:proof"}, zero: NodeSessionResponse{}},
		{name: "NodeSessionEstablished", full: NodeSessionEstablished{SessionID: "session-1", Protocol: NegotiatedProtocol{Major: 1, Minor: 3, Features: ComputeFeatureMaskV1}, NodeToControllerInitialSequence: InitialEnvelopeSequenceV1, ControllerToNodeInitialSequence: InitialEnvelopeSequenceV1}, zero: NodeSessionEstablished{}},
		{name: "NodeRecord", full: node, zero: NodeRecord{}},
		{name: "HandlerVersion", full: handler, zero: HandlerVersion{}},
		{name: "AdminCapabilities", full: admin, zero: AdminCapabilities{}},
		{name: "ObservedCapabilities", full: observed, zero: ObservedCapabilities{}},
		{name: "GPUCapability", full: GPUCapability{Vendor: "nvidia", Model: "RTX", MemoryBytes: 8589934592}, zero: GPUCapability{}},
		{name: "NodeTelemetry", full: NodeTelemetry{SchemaVersion: 1, SampledAt: "2026-09-01T01:02:03Z", SessionSequence: 9, AvailableRAMBytes: 17179869184, AvailableDiskBytes: 68719476736, CPUPercent: 37.5, Load: &load, InteractiveIdleSec: 600, OnACPower: &onACPower, BatteryPercent: &batteryPercent, ActiveJobs: 2, ActiveByKind: map[string]int{"repo.verify.v1": 2}, Health: "healthy"}, zero: NodeTelemetry{}},
		{name: "ComputePolicy", full: ComputePolicy{SchemaVersion: 1, TenantID: "tenant-1", AllowedPrincipals: []string{"principal-1"}, WorkspaceIDs: []string{"workspace-1"}, AllowedArtifacts: []string{"store-1"}, AllowedHandlers: []HandlerVersion{handler}, AllowedJobKinds: []string{"repo.verify.v1"}, MaximumDataClass: "internal", AllowRemoteTransfer: true, MaxCPU: 8, MaxGPU: 1, MaxRAMBytes: 17179869184, MaxDiskBytes: 68719476736, MaxConcurrency: 2, MaxConcurrencyByKind: map[string]int{"repo.verify.v1": 1}, MinimumIdleSec: 300, RequireAC: true, MinimumBatteryPct: &minimumBattery, CancelOnActivityHandlers: []HandlerVersion{handler}}, zero: ComputePolicy{}},
		{name: "ArtifactRef", full: artifact, zero: ArtifactRef{}},
		{name: "JobRequirements", full: requirements, zero: JobRequirements{}},
		{name: "ComputeJobSpec", full: spec, zero: ComputeJobSpec{}},
		{name: "ComputeJobSubmitRequest", full: ComputeJobSubmitRequest{IdempotencyKey: "idempotency-1", Spec: spec}, zero: ComputeJobSubmitRequest{}},
		{name: "ComputeAuthz", full: authz, zero: ComputeAuthz{}},
		{name: "AssignmentAuthorization", full: assignmentAuthz, zero: AssignmentAuthorization{}},
		{name: "ComputeJobRecord", full: ComputeJobRecord{SchemaVersion: 1, JobID: "job-1", IdempotencyKey: "idempotency-1", SpecHash: "sha256:spec", Spec: spec, Authz: authz, State: "queued", QueueReason: "no_eligible_node", Attempt: 1}, zero: ComputeJobRecord{}},
		{name: "AssignmentOffer", full: AssignmentOffer{AssignmentID: "assignment-1", JobID: "job-1", ExecutionID: "execution-1", Epoch: 2, SpecHash: "sha256:spec", Spec: spec, Authz: assignmentAuthz, ExpiresAt: "2026-09-01T01:03:15Z"}, zero: AssignmentOffer{}},
		{name: "AssignmentClaim", full: AssignmentClaim{AssignmentID: "assignment-1", JobID: "job-1", ExecutionID: "execution-1", Epoch: 2, SpecHash: "sha256:spec", ReceiptID: "receipt-1"}, zero: AssignmentClaim{}},
		{name: "AssignmentGrant", full: AssignmentGrant{AssignmentID: "assignment-1", JobID: "job-1", ExecutionID: "execution-1", Epoch: 2, LeaseUntil: "2026-09-01T01:04:00Z"}, zero: AssignmentGrant{}},
		{name: "LeaseRenewal", full: LeaseRenewal{JobID: "job-1", ExecutionID: "execution-1", Epoch: 2, LeaseUntil: "2026-09-01T01:04:20Z"}, zero: LeaseRenewal{}},
		{name: "CancelRequest", full: CancelRequest{JobID: "job-1", ExecutionID: "execution-1", Epoch: 2, ReasonCode: "user_cancelled"}, zero: CancelRequest{}},
		{name: "LogChunk", full: LogChunk{JobID: "job-1", ExecutionID: "execution-1", Epoch: 2, Stream: "stdout", Sequence: 3, ByteOffset: 128, Data: "aGVsbG8=", SHA256: "sha256:log", EOF: true}, zero: LogChunk{}},
		{name: "LogAck", full: LogAck{JobID: "job-1", ExecutionID: "execution-1", Stream: "stdout", ContiguousSequence: 3}, zero: LogAck{}},
		{name: "ComputeJobEvent", full: ComputeJobEvent{SchemaVersion: 1, Sequence: 5, At: "2026-09-01T01:03:00Z", Type: "assignment.granted", JobID: "job-1", ExecutionID: "execution-1", Epoch: 2, Payload: json.RawMessage(`{"leaseUntil":"2026-09-01T01:04:00Z"}`)}, zero: ComputeJobEvent{}},
		{name: "ResourceUsage", full: usage, zero: ResourceUsage{}},
		{name: "TerminalResult", full: TerminalResult{SchemaVersion: 1, JobID: "job-1", NodeID: "node-1", ExecutionID: "execution-1", Epoch: 2, Handler: handler, SourceDigest: "sha256:source", StartedAt: "2026-09-01T01:03:00Z", EndedAt: "2026-09-01T01:03:12Z", DurationMs: 12500, ExitCode: &exitCode, ErrorKind: "process_exit", Retryable: true, Usage: usage, Artifacts: []ArtifactRef{artifact}}, zero: TerminalResult{}},
	}
}
