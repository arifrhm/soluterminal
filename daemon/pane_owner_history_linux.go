//go:build linux

package main

import (
	"os"
	"syscall"
)

func ownerRetirementTranscriptIdentity(info os.FileInfo, proof ownerRetirementProof) bool {
	stat, ok := info.Sys().(*syscall.Stat_t)
	return ok && uint64(stat.Dev) == proof.TranscriptDevice && stat.Ino == proof.TranscriptInode
}
