//go:build !linux

package main

import "fmt"

type unsupportedFleetBinaryPlatform struct{}

func newFleetBinaryPlatform() fleetBinaryPlatform      { return unsupportedFleetBinaryPlatform{} }
func (unsupportedFleetBinaryPlatform) Supported() bool { return false }
func (unsupportedFleetBinaryPlatform) Target(string) (fleetTargetBinary, error) {
	return fleetTargetBinary{}, fmt.Errorf("fleet binary inventory requires Linux /proc")
}
func (unsupportedFleetBinaryPlatform) Process(int, string) (fleetProcessBinary, error) {
	return fleetProcessBinary{}, fmt.Errorf("fleet binary inventory requires Linux /proc")
}
