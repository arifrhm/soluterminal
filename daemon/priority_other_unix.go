//go:build !linux && !windows

package main

func lowerProcessIOPriority(int) {}
