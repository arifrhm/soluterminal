//go:build !linux

package main

import "os"

// An unavailable provider source is uncertainty, never a successful turn.
// Native panes keep their existing protocol adapters on every platform.
func openTurnProviderSource(*Pane) *turnProviderSource { return nil }

func turnFileIdentity(os.FileInfo) string { return "" }
