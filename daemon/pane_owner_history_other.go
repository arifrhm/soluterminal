//go:build !linux

package main

import "os"

func ownerRetirementTranscriptIdentity(os.FileInfo, ownerRetirementProof) bool { return false }
