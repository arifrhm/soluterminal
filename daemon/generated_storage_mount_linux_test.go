//go:build linux

package main

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestGeneratedStorageMountProtectionStatx(t *testing.T) {
	for _, name := range []string{"known", "device_zero", "missing_mount_field", "missing_type_field", "zero_mount", "symlink", "file", "unsupported", "permission", "missing"} {
		t.Run(name, func(t *testing.T) {
			var wantErr error
			identity, err := generatedProbeMountWithStatx("/fixture/cache", func(fd int, path string, flags, mask int, stat *unix.Statx_t) error {
				if fd != unix.AT_FDCWD || path != "/fixture/cache" || flags&unix.AT_SYMLINK_NOFOLLOW == 0 || flags&unix.AT_NO_AUTOMOUNT == 0 || mask&unix.STATX_MNT_ID == 0 || mask&unix.STATX_TYPE == 0 {
					t.Fatalf("unsafe/incomplete statx request: fd=%d path=%q flags=%x mask=%x", fd, path, flags, mask)
				}
				*stat = unix.Statx_t{Mask: unix.STATX_BASIC_STATS | unix.STATX_MNT_ID, Mode: unix.S_IFDIR | 0700, Dev_major: 8, Dev_minor: 17, Mnt_id: 42}
				switch name {
				case "device_zero":
					stat.Dev_major, stat.Dev_minor = 0, 0
				case "missing_mount_field":
					stat.Mask &^= unix.STATX_MNT_ID
				case "missing_type_field":
					stat.Mask &^= unix.STATX_TYPE
				case "zero_mount":
					stat.Mnt_id = 0
				case "symlink":
					stat.Mode = unix.S_IFLNK | 0777
				case "file":
					stat.Mode = unix.S_IFREG | 0600
				case "unsupported":
					wantErr = unix.ENOSYS
				case "permission":
					wantErr = unix.EACCES
				case "missing":
					wantErr = unix.ENOENT
				}
				return wantErr
			})
			if name == "known" || name == "device_zero" {
				device := unix.Mkdev(8, 17)
				if name == "device_zero" {
					device = 0
				}
				if err != nil || identity != (generatedMountIdentity{Device: device, MountID: 42}) {
					t.Fatalf("identity=%+v err=%v", identity, err)
				}
				return
			}
			if wantErr == nil {
				wantErr = errWFGeneratedUnsupported
			}
			if !errors.Is(err, wantErr) || identity != (generatedMountIdentity{}) {
				t.Fatalf("uncertain statx returned proof: identity=%+v err=%v want=%v", identity, err, wantErr)
			}
		})
	}
}

func TestGeneratedStorageMountProtectionProductionSameMount(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	left, right := filepath.Join(root, "left"), filepath.Join(root, "right")
	for _, path := range []string{left, right} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	a, aErr := generatedProbeMount(left)
	b, bErr := generatedProbeMount(right)
	if aErr != nil || bErr != nil {
		if !generatedProtectedOverlap(left, right) {
			t.Fatal("native probe failure did not fail closed")
		}
		t.Skipf("native mount identity unavailable: %v / %v", aErr, bErr)
	}
	if a != b || a.MountID == 0 {
		t.Fatalf("adjacent fixture directories have inconsistent identity: %+v / %+v", a, b)
	}
	for _, protected := range []string{right, filepath.Join(right, "missing", "cache")} {
		if generatedProtectedOverlap(left, protected) {
			t.Fatalf("production rejected disjoint same-mount paths: %q", protected)
		}
	}
}
