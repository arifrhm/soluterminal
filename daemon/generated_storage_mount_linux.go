//go:build linux

package main

import "golang.org/x/sys/unix"

func generatedProbeMount(path string) (generatedMountIdentity, error) {
	return generatedProbeMountWithStatx(path, unix.Statx)
}

func generatedProbeMountWithStatx(path string, statx func(int, string, int, int, *unix.Statx_t) error) (generatedMountIdentity, error) {
	var stat unix.Statx_t
	const requested = unix.STATX_BASIC_STATS | unix.STATX_MNT_ID
	if err := statx(unix.AT_FDCWD, path, unix.AT_SYMLINK_NOFOLLOW|unix.AT_NO_AUTOMOUNT, requested, &stat); err != nil {
		return generatedMountIdentity{}, err
	}
	// Successful statx on an older kernel may omit fields it cannot supply.
	// A replaced symlink/non-directory is not an existing cache prefix either.
	if stat.Mask&(unix.STATX_MNT_ID|unix.STATX_TYPE) != unix.STATX_MNT_ID|unix.STATX_TYPE || stat.Mnt_id == 0 || stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return generatedMountIdentity{}, errWFGeneratedUnsupported
	}
	return generatedMountIdentity{Device: unix.Mkdev(stat.Dev_major, stat.Dev_minor), MountID: stat.Mnt_id}, nil
}
