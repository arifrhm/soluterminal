package main

const (
	storeReaderVersion                = 45
	processScopeReaderVersion         = 45
	taskModelProfileReaderVersion     = 33
	buildCapabilityStoreReaderVersion = 30
	// Every reader <= 13 (including the template-registry reader) shipped
	// without owner evidence constraints; they would ignore AutonomyProof
	// and treat an owner decider as ordinary policy approval.
	// Reader 14 can carry an owner approval without refreshing repo commands.
	// Pin the strengthened owner contract before recording any such evidence.
	ownerEvidenceStoreReaderVersion               = 15
	collaborationBudgetRecoveryStoreReaderVersion = 11
	templateRegistryStoreReaderVersion            = 13 // above every pre-template reader: they would ignore the frozen plan
	escalationRecoveryStoreReaderVersion          = 10
	adaptiveRoutingStoreReaderVersion             = 9
	unattendedStoreReaderVersion                  = 8
	proposalRecoveryStoreReaderVersion            = 8
	goCacheRecoveryStoreReaderVersion             = 8
	collaborationAccountingStoreReaderVersion     = 7
	routingSnapshotStoreReaderVersion             = 7
	supervisedAutomationStoreReaderVersion        = 12
	coordinationStoreReaderVersion                = 4
	// Feature requirements stay pinned when the binary gains newer readers.
	controlStoreReaderVersion       = 2
	collaborationStoreReaderVersion = 3
)

type taskDiskUsage struct {
	LogsBytes       int64 `json:"logsBytes"`
	ArtifactsBytes  int64 `json:"artifactsBytes"`
	EventsBytes     int64 `json:"eventsBytes"`
	OwnerFilesBytes int64 `json:"ownerFilesBytes"`
}

func (u taskDiskUsage) TotalBytes() int64 {
	return u.LogsBytes + u.ArtifactsBytes + u.EventsBytes + u.OwnerFilesBytes
}
