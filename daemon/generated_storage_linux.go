//go:build linux

package main

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func generatedIdentity(path string) (generatedFSIdentity, error) {
	fd, err := unix.Open(path, unix.O_RDONLY|unix.O_DIRECTORY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return generatedFSIdentity{}, err
	}
	defer unix.Close(fd)
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return generatedFSIdentity{}, err
	}
	return generatedFSIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Valid: true}, nil
}

// Per-operation seam: no global syscall overrides in concurrent tests.
type generatedFS struct {
	open           func(int, string, *unix.OpenHow) (int, error)
	afterPreflight func()
	validateRoot   func(int) error
	readDirNames   func(*os.File, int) ([]string, error)
}

func generatedNativeFS() generatedFS { return generatedFS{open: unix.Openat2} }

func generatedOpenError(err error) error {
	if errors.Is(err, unix.EXDEV) {
		return errors.New("mount_crossing")
	}
	if errors.Is(err, unix.ELOOP) {
		return errors.New("symlink_detected")
	}
	if errors.Is(err, unix.ENOSYS) || errors.Is(err, unix.EINVAL) || errors.Is(err, unix.EOPNOTSUPP) {
		return errWFGeneratedUnsupported
	}
	return err
}

func (fs generatedFS) openAt(parent int, name string, flags int) (int, error) {
	fd, err := fs.open(parent, name, &unix.OpenHow{Flags: uint64(flags | unix.O_CLOEXEC | unix.O_NOFOLLOW),
		Resolve: unix.RESOLVE_BENEATH | unix.RESOLVE_NO_SYMLINKS | unix.RESOLVE_NO_XDEV})
	return fd, generatedOpenError(err)
}

func generatedCheckFD(fd int, expected generatedFSIdentity) (unix.Stat_t, error) {
	var stat unix.Stat_t
	if err := unix.Fstat(fd, &stat); err != nil {
		return stat, err
	}
	if stat.Mode&unix.S_IFMT == unix.S_IFLNK {
		return stat, errors.New("symlink_detected")
	}
	if !expected.Valid || uint64(stat.Dev) != expected.Device {
		return stat, errors.New("mount_crossing")
	}
	if expected.Inode != 0 && stat.Ino != expected.Inode {
		return stat, errors.New("changed_identity")
	}
	return stat, nil
}

// Parent/root remain pinned across preflight and mutation. NO_XDEV covers the
// root edge and same-filesystem bind mounts, which st_dev does not distinguish.
func (fs generatedFS) root(path string, expected generatedFSIdentity) (parent, root int, name string, err error) {
	clean := filepath.Clean(path)
	name = filepath.Base(clean)
	if !filepath.IsAbs(clean) || clean == "/" || name == "." || !expected.Valid || expected.Inode == 0 {
		return -1, -1, "", errors.New("unsafe_root")
	}
	parent, err = fs.open(unix.AT_FDCWD, filepath.Dir(clean), &unix.OpenHow{
		Flags: unix.O_PATH | unix.O_DIRECTORY | unix.O_CLOEXEC | unix.O_NOFOLLOW, Resolve: unix.RESOLVE_NO_SYMLINKS})
	if err != nil {
		return -1, -1, "", generatedOpenError(err)
	}
	root, err = fs.openAt(parent, name, unix.O_RDONLY|unix.O_DIRECTORY)
	if err == nil {
		_, err = generatedCheckFD(root, expected)
		if err != nil {
			unix.Close(root)
		}
	}
	if err != nil {
		unix.Close(parent)
		return -1, -1, "", err
	}
	return parent, root, name, nil
}

func (fs generatedFS) readNames(ctx context.Context, fd int) ([]string, error) {
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	// Dup shares the directory offset: independent opens preserve the second
	// traversal after a measurement pass has consumed every entry.
	reader, err := fs.openAt(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(reader), "generated-storage")
	defer file.Close()
	read := fs.readDirNames
	if read == nil {
		read = func(file *os.File, n int) ([]string, error) { return file.Readdirnames(n) }
	}
	var names []string
	for {
		if err := ctx.Err(); err != nil {
			return nil, err
		}
		batch, err := read(file, 256)
		names = append(names, batch...)
		if errors.Is(err, io.EOF) {
			return names, nil
		}
		if err != nil {
			return nil, err
		}
	}
}

func (fs generatedFS) measureFD(ctx context.Context, fd int, device uint64) (apparent, allocated int64, err error) {
	if err = ctx.Err(); err != nil {
		return
	}
	stat, err := generatedCheckFD(fd, generatedFSIdentity{Device: device, Valid: true})
	if err != nil {
		return 0, 0, err
	}
	apparent, allocated = stat.Size, stat.Blocks*512
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return
	}
	names, err := fs.readNames(ctx, fd)
	if err != nil {
		return apparent, allocated, err
	}
	for _, name := range names {
		child, openErr := fs.openAt(fd, name, unix.O_PATH)
		if openErr != nil {
			return apparent, allocated, openErr
		}
		a, b, childErr := fs.measureFD(ctx, child, device)
		unix.Close(child)
		apparent += a
		allocated += b
		if childErr != nil {
			return apparent, allocated, childErr
		}
	}
	return
}

func generatedMeasure(ctx context.Context, path string, expected generatedFSIdentity) (apparent, allocated int64, complete bool, reason string) {
	fs := generatedNativeFS()
	parent, root, _, err := fs.root(path, expected)
	if err != nil {
		return 0, 0, false, generatedReason(err)
	}
	defer unix.Close(parent)
	defer unix.Close(root)
	apparent, allocated, err = fs.measureFD(ctx, root, expected.Device)
	if err != nil {
		if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
			return apparent, allocated, false, "measurement_incomplete"
		}
		return apparent, allocated, false, generatedReason(err)
	}
	return apparent, allocated, true, ""
}

func generatedGuestFreeBytes(path string) (int64, bool) {
	var stat unix.Statfs_t
	if err := unix.Statfs(path, &stat); err != nil {
		return 0, false
	}
	return int64(stat.Bavail) * int64(stat.Bsize), true
}

func generatedRemove(ctx context.Context, path string, expected generatedFSIdentity, rels []string) error {
	return generatedNativeFS().remove(ctx, path, expected, rels, generatedMarkerName, false)
}
func generatedClearExcept(ctx context.Context, path string, expected generatedFSIdentity, markerName string) error {
	return generatedNativeFS().remove(ctx, path, expected, nil, markerName, false)
}
func generatedRemoveTree(ctx context.Context, path string, expected generatedFSIdentity) error {
	return generatedNativeFS().remove(ctx, path, expected, nil, "", true)
}

func generatedRemoveMarkedTree(ctx context.Context, path string, expected generatedFSIdentity, marker string, validate func([]byte) error) error {
	return generatedMarkedFS(marker, validate).remove(ctx, path, expected, nil, "", true)
}

func generatedClearMarkedExcept(ctx context.Context, path string, expected generatedFSIdentity, marker string, validate func([]byte) error) error {
	return generatedMarkedFS(marker, validate).remove(ctx, path, expected, nil, marker, false)
}

func generatedRemoveMarked(ctx context.Context, path string, expected generatedFSIdentity, rels []string, marker string, validate func([]byte) error) error {
	return generatedMarkedFS(marker, validate).remove(ctx, path, expected, rels, marker, false)
}

func generatedMarkedFS(marker string, validate func([]byte) error) generatedFS {
	fs := generatedNativeFS()
	fs.validateRoot = func(root int) error {
		raw, err := fs.readMarker(root, marker)
		if err != nil {
			return err
		}
		return validate(raw)
	}
	return fs
}

// Inventory uses the same bounded, nonblocking, descriptor-relative read as
// mutation. A FIFO, symlink or oversized marker is never proof of ownership.
func generatedReadMarker(path, marker string) ([]byte, error) {
	identity, err := generatedIdentity(path)
	if err != nil {
		return nil, err
	}
	fs := generatedNativeFS()
	parent, root, _, err := fs.root(path, identity)
	if err != nil {
		return nil, err
	}
	defer unix.Close(parent)
	defer unix.Close(root)
	return fs.readMarker(root, marker)
}

func (fs generatedFS) readMarker(root int, marker string) ([]byte, error) {
	fd, err := fs.openAt(root, marker, unix.O_RDONLY|unix.O_NONBLOCK)
	if err != nil {
		return nil, err
	}
	file := os.NewFile(uintptr(fd), "ownership-marker")
	defer file.Close()
	info, err := file.Stat()
	if err != nil {
		return nil, err
	}
	if !info.Mode().IsRegular() || info.Size() > 16384 {
		return nil, errors.New("ownership_marker_corrupt")
	}
	raw, err := io.ReadAll(io.LimitReader(file, 16385))
	if err == nil && len(raw) > 16384 {
		err = errors.New("ownership_marker_corrupt")
	}
	return raw, err
}

func (fs generatedFS) remove(ctx context.Context, path string, expected generatedFSIdentity, rels []string, preserveMarker string, removeRoot bool) error {
	ctx, release, guardErr := retirementCleanupContext(ctx, nil, "", path)
	if guardErr != nil {
		return guardErr
	}
	defer release()
	for _, rel := range rels {
		if rel == "" || filepath.IsAbs(rel) || rel == "." || rel == ".." || strings.Contains(rel, string(filepath.Separator)) {
			return fmt.Errorf("unsafe relative deletion target %q", rel)
		}
		if rel == generatedMarkerName {
			return errors.New("refusing to remove ownership marker")
		}
	}
	parent, root, name, err := fs.root(path, expected)
	if err != nil {
		return err
	}
	defer unix.Close(parent)
	defer unix.Close(root)
	// No chmod/unlink before full preflight. Late hazards stop deletion, but
	// do not promise transactional rollback of already removed owned siblings.
	if fs.validateRoot != nil {
		if err := fs.validateRoot(root); err != nil {
			return err
		}
	}
	if _, _, err := fs.measureFD(ctx, root, expected.Device); err != nil {
		return err
	}
	if fs.afterPreflight != nil {
		fs.afterPreflight()
	}
	if fs.validateRoot != nil {
		if err := fs.validateRoot(root); err != nil {
			return err
		}
	}
	if err := fs.clearFD(ctx, root, expected.Device, rels, preserveMarker); err != nil {
		return err
	}
	if !removeRoot {
		return nil
	}
	return fs.unlinkChecked(ctx, parent, name, expected, unix.AT_REMOVEDIR)
}

func (fs generatedFS) clearFD(ctx context.Context, fd int, device uint64, rels []string, preserve string) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	stat, err := generatedCheckFD(fd, generatedFSIdentity{Device: device, Valid: true})
	if err != nil {
		return err
	}
	// Go modules have read-only directories. Change only a pinned owned fd.
	if stat.Mode&0300 != 0300 {
		if err := unix.Fchmod(fd, stat.Mode&0777|0300); err != nil {
			return err
		}
	}
	if len(rels) == 0 {
		rels, err = fs.readNames(ctx, fd)
		if err != nil {
			return err
		}
	}
	for _, name := range rels {
		if preserve != "" && name == preserve {
			continue
		}
		if err := fs.removeAt(ctx, fd, name, device); err != nil {
			return err
		}
	}
	return nil
}

func (fs generatedFS) unlinkChecked(ctx context.Context, parent int, name string, expected generatedFSIdentity, flags int) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := fs.openAt(parent, name, unix.O_PATH)
	if err != nil {
		return err
	}
	_, err = generatedCheckFD(fd, expected)
	unix.Close(fd)
	if err != nil {
		return err
	}
	return unix.Unlinkat(parent, name, flags)
}

func (fs generatedFS) removeAt(ctx context.Context, parent int, name string, device uint64) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	fd, err := fs.openAt(parent, name, unix.O_PATH)
	if errors.Is(err, unix.ENOENT) {
		return nil
	}
	if err != nil {
		return err
	}
	defer unix.Close(fd)
	stat, err := generatedCheckFD(fd, generatedFSIdentity{Device: device, Valid: true})
	if err != nil {
		return err
	}
	expected := generatedFSIdentity{Device: uint64(stat.Dev), Inode: stat.Ino, Valid: true}
	if stat.Mode&unix.S_IFMT != unix.S_IFDIR {
		return fs.unlinkChecked(ctx, parent, name, expected, 0)
	}
	reader, err := fs.openAt(fd, ".", unix.O_RDONLY|unix.O_DIRECTORY)
	if err != nil {
		return err
	}
	err = fs.clearFD(ctx, reader, device, nil, "")
	unix.Close(reader)
	if err != nil {
		return err
	}
	return fs.unlinkChecked(ctx, parent, name, expected, unix.AT_REMOVEDIR)
}

func generatedRemoveAt(ctx context.Context, parent int, name string, device uint64) error {
	return generatedNativeFS().removeAt(ctx, parent, name, device)
}
