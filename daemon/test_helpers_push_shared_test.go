package main

import (
	"testing"
	"time"
)

func awaitWorkflowPush(t *testing.T, bodies <-chan map[string]string) map[string]string {
	t.Helper()
	select {
	case body := <-bodies:
		return body
	case <-time.After(2 * time.Second):
		t.Fatal("timed out waiting for workflow push")
		return nil
	}
}
