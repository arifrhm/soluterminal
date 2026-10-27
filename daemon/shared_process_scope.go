package main

import (
	"context"

	"os/exec"
)

func startWorkflowCommand(_ context.Context, cmd *exec.Cmd) (func(), error) {
	return func() {}, startWithoutCoreDumps(cmd)
}
