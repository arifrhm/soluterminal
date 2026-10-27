package main

import (
	"encoding/json"
)

type retirementExclusion struct {
	TaskID   string          `json:"taskId"`
	RepoKey  string          `json:"repoKey"`
	Snapshot json.RawMessage `json:"snapshot"`
	Reason   string          `json:"reason"`
}

func retirementNextBatch(op retirementOperation) ([]retirementEntry, int) {
	batch := make([]retirementEntry, 0, 50)
	next := op.NextTask
	for scanned := 0; scanned < len(op.Manifest.Entries) && len(batch) < 50; scanned++ {
		entry := op.Manifest.Entries[next]
		next = (next + 1) % len(op.Manifest.Entries)
		if old, ok := op.Results[entry.TaskID]; !ok || !retirementPhaseFinal(old.Phase) {
			batch = append(batch, entry)
		}
	}
	return batch, next
}
