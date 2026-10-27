//go:build !linux

package main

func generatedProbeMount(string) (generatedMountIdentity, error) {
	return generatedMountIdentity{}, errWFGeneratedUnsupported
}

func generatedDescendantMounts(string, string) (bool, error) {
	return false, errWFGeneratedUnsupported
}
