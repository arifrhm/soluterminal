package main

import (
	"time"
)

// Retirement is process housekeeping, never task resolution or approval.
// The exact link and pane identities bind retries; phase changes cannot reset
// task budgets or transfer the old conversation's authority to its successor.
type ownerRetirement struct {
	CurrentSuccessorID string     `json:"currentSuccessorId,omitempty"`
	HistoryArtifact    string     `json:"historyArtifact,omitempty"`
	PaneID             string     `json:"paneId"`
	SuccessorID        string     `json:"successorId"`
	LinkAt             time.Time  `json:"linkAt"`
	Phase              string     `json:"phase"` // deferred, closing, retired, reactivated
	Reason             string     `json:"reason,omitempty"`
	Attempts           int        `json:"attempts"`
	UpdatedAt          time.Time  `json:"updatedAt"`
	NextRetryAt        *time.Time `json:"nextRetryAt,omitempty"`
	ConversationID     string     `json:"conversationId,omitempty"`
	HistoryAvailable   bool       `json:"historyAvailable"`
}
