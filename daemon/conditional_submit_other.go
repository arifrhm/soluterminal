//go:build !linux

package main

import "context"

func conditionalSubmitSupported() bool { return false }

func probeConditionalSubmitProcess(context.Context, int) (conditionalSubmitProcess, error) {
	return conditionalSubmitProcess{}, errConditionalProcessUnavailable
}
