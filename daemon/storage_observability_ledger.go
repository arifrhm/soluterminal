package main

import (
	"sort"
	"time"
)

const (
	hostStorageProducerCategoryCap = 32
	hostStorageOperationReasonCap  = 16
	hostStorageReadEventCeiling    = 4096
	hostStorageProducerHeartbeat   = 5 * time.Hour
)

func knownStorageCategory(id string) bool {
	for _, def := range storageCategoryDefinitions {
		if def.ID == id {
			return true
		}
	}
	_, ok := storageCapacityCategoryLabels[id]
	return ok
}

// Ledger snapshots contain category IDs and numbers only. A caller cannot use
// a filesystem path or arbitrary label as a persisted category.
func boundProducerSample(sample storageProducerSample) storageProducerSample {
	keys := make([]string, 0, len(sample.Categories))
	for key := range sample.Categories {
		if knownStorageCategory(key) {
			keys = append(keys, key)
		}
	}
	sort.Strings(keys)
	if len(keys) > hostStorageProducerCategoryCap {
		keys = keys[:hostStorageProducerCategoryCap]
		sample.Complete = false
	}
	values := make(map[string]int64, len(keys))
	allocated := make(map[string]int64, len(keys))
	for _, key := range keys {
		if value := sample.Categories[key]; value >= 0 {
			values[key] = value
		}
		if value, ok := sample.Allocated[key]; ok && value >= 0 {
			allocated[key] = value
		}
	}
	sample.Categories = values
	sample.Allocated = allocated
	return sample
}

func producerSamplesEqual(a, b map[string]int64) bool {
	if len(a) != len(b) {
		return false
	}
	for key, value := range a {
		if b[key] != value {
			return false
		}
	}
	return true
}

func boundStorageOperation(op storageCleanupOperation) storageCleanupOperation {
	if len(op.Skipped) > hostStorageOperationReasonCap {
		op.Skipped = op.Skipped[:hostStorageOperationReasonCap]
	}
	op.Detail = redactSensitive(boundDetail(op.Detail))
	for i := range op.Skipped {
		op.Skipped[i].Reason = redactSensitive(boundDetail(op.Skipped[i].Reason))
	}
	if op.DryRun || !op.Executed {
		op.Executed = false
		op.Verified = false
		op.ReclaimedBytes = nil
		op.GuestFreeDeltaBytes = nil
		op.HostFreeDeltaBytes = nil
	}
	if op.Layer != "host" || !op.Verified {
		op.HostFreeDeltaBytes = nil
		if op.Layer == "host" {
			op.ReclaimedBytes = nil
		}
	}
	return op
}

// AppendProducerSample persists a bounded snapshot only after a successful
// ledger append. Duplicate snapshots are suppressed between inventory ticks,
// while periodic heartbeats preserve a continuous growth baseline.
func (l *hostStorageLedger) AppendProducerSample(bootID string, sample storageProducerSample) {
	if l == nil {
		return
	}
	if sample.At.IsZero() {
		sample.At = l.now().UTC()
	}
	sample.At = sample.At.UTC()
	sample.BootID = bootID
	sample = boundProducerSample(sample)
	l.mu.Lock()
	defer l.mu.Unlock()
	if prev := l.lastProducer; prev != nil && sample.At.Sub(prev.At) < hostStorageProducerHeartbeat && prev.Complete == sample.Complete && producerSamplesEqual(prev.Categories, sample.Categories) && producerSamplesEqual(prev.Allocated, sample.Allocated) {
		return
	}
	if err := l.appendLocked(hostStorageEvent{At: sample.At, BootID: bootID, Kind: "producer_sample", Producer: &sample}); err == nil {
		l.lastProducer = &sample
	}
}

func (l *hostStorageLedger) AppendCleanupOperation(op storageCleanupOperation) {
	if l == nil {
		return
	}
	if op.At.IsZero() {
		op.At = l.now().UTC()
	}
	op.At = op.At.UTC()
	op = boundStorageOperation(op)
	_ = l.append(hostStorageEvent{At: op.At, Kind: "cleanup_operation", CleanupOperation: &op})
}

// Read the bounded retained generations in time order. The newest generation
// is never skipped when an older generation reaches the event ceiling.
func readAllHostStorageEvents(path string, maxEvents int) []hostStorageEvent {
	if path == "" {
		return nil
	}
	var out []hostStorageEvent
	for _, candidate := range []string{path + ".2", path + ".1", path} {
		out = append(out, readHostStorageEvents(candidate, 256<<10, "")...)
	}
	if maxEvents > 0 && len(out) > maxEvents {
		out = out[len(out)-maxEvents:]
	}
	return out
}

func (l *hostStorageLedger) TailProducerSamples(limit int) []storageProducerSample {
	if l == nil || limit <= 0 {
		return nil
	}
	events := readAllHostStorageEvents(l.path, hostStorageReadEventCeiling)
	out := make([]storageProducerSample, 0)
	for _, ev := range events {
		if ev.Kind == "producer_sample" && ev.Producer != nil && !ev.Producer.At.IsZero() {
			out = append(out, *ev.Producer)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}

func normalizeLegacyMaintenanceEvent(ev hostStorageEvent) (storageCleanupOperation, bool) {
	if ev.Kind != "maintenance" && ev.Kind != "maintenance_operation" {
		return storageCleanupOperation{}, false
	}
	op := storageCleanupOperation{At: ev.At, Kind: "windows_maintenance", Source: "legacy", Layer: "host", Complete: ev.Before != nil && ev.After != nil}
	if ev.Operation == "prepare-trim" {
		op.Layer = "guest"
	}
	if ev.Kind == "maintenance" {
		// Older free-text rows cannot prove execution, even when their mode or
		// reclaimed-byte fields suggest that maintenance was attempted.
		op.DryRun = true
		op.Detail = ev.Detail
		return boundStorageOperation(op), true
	}
	if ev.Status != "succeeded" {
		return boundStorageOperation(op), true
	}
	op.Executed = true
	op.Verified = ev.Before != nil && ev.After != nil
	if !op.Verified {
		return boundStorageOperation(op), true
	}
	if ev.Before.GuestUsedBytes != nil && ev.After.GuestUsedBytes != nil {
		delta := *ev.Before.GuestUsedBytes - *ev.After.GuestUsedBytes
		if delta > 0 {
			op.GuestFreeDeltaBytes = &delta
		}
	}
	if ev.Before.HostFreeBytes != nil && ev.After.HostFreeBytes != nil {
		op.HostFreeBeforeBytes = ev.Before.HostFreeBytes
		op.HostFreeAfterBytes = ev.After.HostFreeBytes
		if delta := *ev.After.HostFreeBytes - *ev.Before.HostFreeBytes; delta > 0 {
			op.HostFreeDeltaBytes = &delta
		}
	}
	if ev.Before.PhysicalAllocationBytes != nil && ev.After.PhysicalAllocationBytes != nil {
		op.VHDXBeforeBytes = ev.Before.PhysicalAllocationBytes
		op.VHDXAfterBytes = ev.After.PhysicalAllocationBytes
		if op.Layer == "host" {
			if delta := *ev.Before.PhysicalAllocationBytes - *ev.After.PhysicalAllocationBytes; delta > 0 {
				op.ReclaimedBytes = &delta
			}
		}
	}
	return boundStorageOperation(op), true
}

func (l *hostStorageLedger) TailOperations(limit int) []storageCleanupOperation {
	if l == nil || limit <= 0 {
		return nil
	}
	events := readAllHostStorageEvents(l.path, hostStorageReadEventCeiling)
	out := make([]storageCleanupOperation, 0)
	for _, ev := range events {
		if ev.Kind == "cleanup_operation" && ev.CleanupOperation != nil {
			out = append(out, *ev.CleanupOperation)
		} else if op, ok := normalizeLegacyMaintenanceEvent(ev); ok {
			out = append(out, op)
		}
	}
	sort.SliceStable(out, func(i, j int) bool { return out[i].At.Before(out[j].At) })
	if len(out) > limit {
		out = out[len(out)-limit:]
	}
	return out
}
