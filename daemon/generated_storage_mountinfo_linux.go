//go:build linux

package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
)

const generatedMountInfoLimit = 1 << 20

func generatedDescendantMounts(path, protected string) (bool, error) {
	return generatedDescendantMountsWithOpen(path, protected, func(path string) (io.ReadCloser, error) {
		return os.Open(path)
	})
}

func generatedDescendantMountsWithOpen(path, protected string, open func(string) (io.ReadCloser, error)) (bool, error) {
	file, err := open("/proc/self/mountinfo")
	if err != nil {
		return false, err
	}
	defer file.Close()
	return generatedDescendantMountsFromReader(path, protected, file)
}

// One bounded namespace snapshot, never a directory walk or mount operation.
// Parse the whole snapshot even after finding a descendant: malformed or
// truncated evidence must never authorize the disjoint-root fast path.
func generatedDescendantMountsFromReader(path, protected string, reader io.Reader) (bool, error) {
	unknown := errors.New("mount descendant evidence unavailable")
	for _, root := range []string{path, protected} {
		if !filepath.IsAbs(root) || filepath.Clean(root) != root {
			return false, unknown
		}
	}
	raw, err := io.ReadAll(io.LimitReader(reader, generatedMountInfoLimit+1))
	if err != nil {
		return false, err
	}
	if len(raw) == 0 || len(raw) > generatedMountInfoLimit || raw[len(raw)-1] != '\n' || bytes.IndexByte(raw, 0) >= 0 {
		return false, unknown
	}
	seen := make(map[uint64]bool)
	found := false
	for _, line := range strings.Split(string(raw[:len(raw)-1]), "\n") {
		prefix, suffix, separated := strings.Cut(line, " - ")
		fields := strings.Split(prefix, " ")
		if !separated || len(fields) < 6 {
			return false, unknown
		}
		for _, field := range fields {
			if field == "" || strings.ContainsAny(field, "\t\r") {
				return false, unknown
			}
		}
		// Filesystem-specific super-options are opaque: WSL's 9p drvfs
		// emitter includes unescaped spaces in Windows paths here. Only the
		// fixed filesystem/source fields precede that remainder; mountpoint
		// fields above still require the kernel's escaped-path format.
		tail := strings.SplitN(suffix, " ", 3)
		if len(tail) != 3 || tail[0] == "" || tail[1] == "" || strings.TrimSpace(tail[2]) == "" || strings.ContainsAny(tail[0]+tail[1], "\t\r") {
			return false, unknown
		}
		id, idErr := strconv.ParseUint(fields[0], 10, 64)
		parent, parentErr := strconv.ParseUint(fields[1], 10, 64)
		if idErr != nil || parentErr != nil || id == 0 || parent == 0 || seen[id] {
			return false, unknown
		}
		seen[id] = true
		device := strings.Split(fields[2], ":")
		if len(device) != 2 {
			return false, unknown
		}
		for _, number := range device {
			if _, err := strconv.ParseUint(number, 10, 32); err != nil {
				return false, unknown
			}
		}
		if err := generatedMountInfoRoot(fields[3], tail[0]); err != nil {
			return false, err
		}
		mountpoint, err := generatedMountInfoPath(fields[4])
		if err != nil {
			return false, err
		}
		if pathUnderRoot(mountpoint, path) || pathUnderRoot(mountpoint, protected) {
			found = true
		}
	}
	return found, nil
}

// nsfs exposes namespace objects, not directory roots: Linux's
// fs/nsfs.c:nsfs_show_path emits "namespace:[decimal inode]". Permit only
// that known root format on nsfs; mountpoints still require canonical paths
// and participate in the same descendant checks as every other filesystem.
func generatedMountInfoRoot(encoded, filesystem string) error {
	_, err := generatedMountInfoPath(encoded)
	if err == nil || filesystem != "nsfs" {
		return err
	}
	namespace, inode, ok := strings.Cut(encoded, ":[")
	if !ok || !strings.HasSuffix(inode, "]") {
		return err
	}
	switch namespace {
	case "cgroup", "ipc", "mnt", "net", "pid", "time", "user", "uts":
	default:
		return err
	}
	inode = strings.TrimSuffix(inode, "]")
	if len(inode) == 0 || inode[0] == '0' {
		return err
	}
	for i := range inode {
		if inode[i] < '0' || inode[i] > '9' {
			return err
		}
	}
	if _, parseErr := strconv.ParseUint(inode, 10, 64); parseErr != nil {
		return err
	}
	return nil
}

// mountinfo encodes only space, tab, newline and backslash in path fields.
// Decode once (a literal "\040" filename is encoded as "\134040").
func generatedMountInfoPath(encoded string) (string, error) {
	unknown := errors.New("invalid mountinfo path")
	var path strings.Builder
	for i := 0; i < len(encoded); i++ {
		ch := encoded[i]
		if ch == '\\' {
			if i+3 >= len(encoded) {
				return "", unknown
			}
			switch encoded[i+1 : i+4] {
			case "040":
				ch = ' '
			case "011":
				ch = '\t'
			case "012":
				ch = '\n'
			case "134":
				ch = '\\'
			default:
				return "", unknown
			}
			i += 3
		} else if ch < ' ' || ch == 127 {
			return "", unknown
		}
		path.WriteByte(ch)
	}
	decoded := path.String()
	if !filepath.IsAbs(decoded) || filepath.Clean(decoded) != decoded {
		return "", unknown
	}
	return decoded, nil
}
