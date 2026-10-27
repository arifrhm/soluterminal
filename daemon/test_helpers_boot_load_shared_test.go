package main

import (
	"context"
	"testing"
	"time"
)

// Deadlines are failure bounds only; every successful wait is channel-driven.
func receiveWorkflowBoot[T any](t *testing.T, ch <-chan T, what string) T {
	t.Helper()
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	select {
	case value := <-ch:
		return value
	case <-ctx.Done():
		t.Fatalf("waiting for %s: %v", what, ctx.Err())
	}
	var zero T
	return zero
}
