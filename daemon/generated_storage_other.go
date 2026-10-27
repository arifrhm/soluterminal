//go:build !linux

package main

import (
	"context"
)

func generatedIdentity(string) (generatedFSIdentity, error) {
	return generatedFSIdentity{}, errWFGeneratedUnsupported
}

func generatedReadMarker(string, string) ([]byte, error) {
	return nil, errWFGeneratedUnsupported
}

func generatedMeasure(context.Context, string, generatedFSIdentity) (int64, int64, bool, string) {
	return 0, 0, false, "filesystem_identity_unsupported"
}

func generatedGuestFreeBytes(string) (int64, bool) { return 0, false }

func generatedRemove(context.Context, string, generatedFSIdentity, []string) error {
	return errWFGeneratedUnsupported
}

func generatedClearExcept(context.Context, string, generatedFSIdentity, string) error {
	return errWFGeneratedUnsupported
}

func generatedRemoveTree(context.Context, string, generatedFSIdentity) error {
	return errWFGeneratedUnsupported
}

func generatedRemoveMarkedTree(context.Context, string, generatedFSIdentity, string, func([]byte) error) error {
	return errWFGeneratedUnsupported
}

func generatedClearMarkedExcept(context.Context, string, generatedFSIdentity, string, func([]byte) error) error {
	return errWFGeneratedUnsupported
}

func generatedRemoveMarked(context.Context, string, generatedFSIdentity, []string, string, func([]byte) error) error {
	return errWFGeneratedUnsupported
}
