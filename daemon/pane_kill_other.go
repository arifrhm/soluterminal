//go:build !linux

package main

func killLegacyPaneAgent(pid int, _ string) error { return hardKillPid(pid) }
