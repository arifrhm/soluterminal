package main

import (
	"errors"
)

const roleEvidence = "scout:review_evidence"

var errHostDiskLow = errors.New("windows host volume backing the WSL disk image is below the configured floor")

var errResourceUnavailable = errors.New("workflow resource admission remained unavailable")
