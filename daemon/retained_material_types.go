package main

import (
	"regexp"
	"strings"
)

// Comparable so existing final-fd ownership comparisons remain exact. An old
// or foreign cleaner refuses schema 2 independently of its own pin index.
type retirementMaterialBinding struct {
	StoreUUID     string `json:"retirementStoreUUID,omitempty"`
	AuthorityPath string `json:"retirementAuthorityPath,omitempty"`
	OperationID   string `json:"retirementOperationId,omitempty"`
}

type retirementPromotion struct {
	Source      string              `json:"source"`
	Destination string              `json:"destination"`
	Digest      string              `json:"digest"`
	Identity    generatedFSIdentity `json:"identity"`
	Completed   bool                `json:"completed"`
}

var taskIDRe = regexp.MustCompile(`^wft_[a-zA-Z0-9]+$`)

func configurationCallerValid(pane string) bool { return strings.HasPrefix(pane, "pane_") }
