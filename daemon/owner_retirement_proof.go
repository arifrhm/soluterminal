package main

// Transport-local proof, never an API grant. Only the exact owning agent can
// produce it for a stopped provider tree under its bounded input fence.
type ownerRetirementProof struct {
	ProcessIDs           []int  `json:"processIds"`
	AgentPID             int    `json:"agentPid"`
	ProviderPID          int    `json:"providerPid"`
	SourceKey            string `json:"sourceKey"`
	Agent                string `json:"agent"`
	ConversationID       string `json:"conversationId"`
	TranscriptSourcePath string `json:"transcriptSourcePath"`
	TranscriptPath       string `json:"transcriptPath"`
	TranscriptSize       int64  `json:"transcriptSize"`
	TranscriptDevice     uint64 `json:"transcriptDevice"`
	TranscriptInode      uint64 `json:"transcriptInode"`
	TranscriptModifiedNs int64  `json:"transcriptModifiedNs"`
	CompletedMs          int64  `json:"completedMs"`
}

type ownerRetirementRequest struct {
	Token                string `json:"token"`
	PaneID               string `json:"paneId"`
	ProcessIDs           []int  `json:"processIds"`
	AgentPID             int    `json:"agentPid"`
	ConversationID       string `json:"conversationId"`
	ClaudeTranscriptPath string `json:"claudeTranscriptPath,omitempty"`
}

// Last input is tied to transcript ordering, not wall-clock timestamps. A prior
// turn completing after new input arrived does not acknowledge that new input.
type ownerRetirementInputFence struct {
	UnresolvedSubmission bool
	Seen                 bool
	Known                bool
	SourceKey            string
	Offset               int64
	Device               uint64
	Inode                uint64
}
