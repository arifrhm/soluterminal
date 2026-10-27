//go:build windows

package main

import "os"

func lowerProcessPriority(*os.Process) {}
