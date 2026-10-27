//go:build !linux

package main

// Outside Linux there is no rlimit manipulation or command rewriting.
func startWithoutCoreDumps(cmd interface{ Start() error }) error {
	target, _, _, err := commandCoreDumpSpec(cmd)
	if err != nil {
		return &coreDumpGuardError{Executable: target, Op: "command adaptation", Err: err}
	}
	return cmd.Start()
}
