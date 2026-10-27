//go:build linux

package main

import (
	"errors"
	"go/ast"
	"go/parser"
	"go/token"
	"io"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func generatedMountInfoFixture(mountpoint string) string {
	return "10 9 8:1 / " + mountpoint + " rw shared:1 - ext4 /dev/fixture rw\n"
}

func TestGeneratedStorageMountProtectionDescendants(t *testing.T) {
	root, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	owned, protected := filepath.Join(root, "owned"), filepath.Join(root, "user")
	for _, path := range []string{owned, protected} {
		if err := os.Mkdir(path, 0700); err != nil {
			t.Fatal(err)
		}
	}
	namespaceFixture := func(mountpoint string) string {
		return "10 9 0:4 mnt:[4026532232] " + mountpoint + " rw - nsfs nsfs rw\n"
	}
	for _, test := range []struct {
		name, snapshot  string
		protectedDevice uint64
		want            bool
	}{
		{"equal_root_ids_nested_alias", generatedMountInfoFixture(filepath.Join(protected, "00")), 7, true},
		{"different_devices_nested_alias", generatedMountInfoFixture(filepath.Join(protected, "00")), 8, true},
		{"owned_descendant", generatedMountInfoFixture(filepath.Join(owned, "00")), 7, true},
		{"disjoint_mountpoint", generatedMountInfoFixture(protected + "-other/00"), 7, false},
		{"disjoint_different_devices", generatedMountInfoFixture(protected + "-other/00"), 8, false},
		{"nsfs_protected_descendant", namespaceFixture(filepath.Join(protected, "namespace")), 7, true},
		{"nsfs_owned_descendant", namespaceFixture(filepath.Join(owned, "namespace")), 8, true},
		{"nsfs_disjoint", namespaceFixture(protected + "-other/namespace"), 7, false},
		{"nsfs_malformed_tail", namespaceFixture(protected+"-other/namespace") + "bad\n", 7, true},
		{"unknown", "", 7, true},
		{"malformed", "not mountinfo\n", 8, true},
		{"oversize", strings.Repeat("x", generatedMountInfoLimit+1), 8, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			probes, snapshots := 0, 0
			probe := func(path string) (generatedMountIdentity, error) {
				probes++
				if path == owned {
					return generatedMountIdentity{7, 11}, nil
				}
				mountID := uint64(11)
				if test.protectedDevice != 7 {
					mountID = 12
				}
				return generatedMountIdentity{test.protectedDevice, mountID}, nil
			}
			descendants := func(a, b string) (bool, error) {
				snapshots++
				if a != owned || b != protected {
					t.Fatalf("noncanonical roots: %q %q", a, b)
				}
				return generatedDescendantMountsFromReader(a, b, strings.NewReader(test.snapshot))
			}
			if got := generatedProtectedOverlapWithProbe(owned, protected, probe, descendants); got != test.want {
				t.Errorf("overlap=%v want %v", got, test.want)
			}
			if snapshots != 1 || (test.want && probes != 0) || (!test.want && probes != 2) {
				t.Fatalf("snapshot must precede disjointness: snapshots=%d probes=%d", snapshots, probes)
			}
		})
	}
}

func TestGeneratedStorageMountProtectionMountInfoParsing(t *testing.T) {
	valid := generatedMountInfoFixture("/elsewhere")
	for _, test := range []struct {
		name, snapshot, protected string
		want, bad                 bool
	}{
		{"protected_child", generatedMountInfoFixture("/user/cache/00"), "/user/cache", true, false},
		{"owned_child", generatedMountInfoFixture("/owned/cache/00"), "/user/cache", true, false},
		{"sibling", generatedMountInfoFixture("/user/cache-other/00"), "/user/cache", false, false},
		{"root_itself", generatedMountInfoFixture("/user/cache"), "/user/cache", false, false},
		{"space", generatedMountInfoFixture(`/user/my\040cache/00`), "/user/my cache", true, false},
		{"tab", generatedMountInfoFixture(`/user/my\011cache/00`), "/user/my\tcache", true, false},
		{"newline", generatedMountInfoFixture(`/user/my\012cache/00`), "/user/my\ncache", true, false},
		{"backslash", generatedMountInfoFixture(`/user/my\134cache/00`), `/user/my\cache`, true, false},
		{"decode_once", generatedMountInfoFixture(`/user/\134040cache/00`), `/user/\040cache`, true, false},
		{"relative_mountpoint", generatedMountInfoFixture("user/cache/00"), "/user/cache", false, true},
		{"dotdot", generatedMountInfoFixture("/elsewhere/../user/cache/00"), "/user/cache", false, true},
		{"unknown_escape", generatedMountInfoFixture(`/user/\041cache`), "/user/cache", false, true},
		{"incomplete_escape", generatedMountInfoFixture(`/user/cache\04`), "/user/cache", false, true},
		{"nul", generatedMountInfoFixture("/user/\x00cache"), "/user/cache", false, true},
		{"relative_root", strings.Replace(valid, "8:1 / ", "8:1 relative ", 1), "/user/cache", false, true},
		{"bad_id", strings.Replace(valid, "10 9", "bad 9", 1), "/user/cache", false, true},
		{"bad_parent", strings.Replace(valid, "10 9", "10 bad", 1), "/user/cache", false, true},
		{"bad_device", strings.Replace(valid, "8:1", "8:no", 1), "/user/cache", false, true},
		{"no_separator", strings.Replace(valid, " - ", " no ", 1), "/user/cache", false, true},
		{"missing_field", strings.Replace(valid, "ext4 /dev/fixture", "ext4", 1), "/user/cache", false, true},
		{"opaque_super_options", strings.TrimSuffix(valid, "\n") + " opaque  options\n", "/user/cache", false, false},
		{"wsl_9p_spaces", "10 9 0:123 / /elsewhere ro - 9p C:\\134Program\\040Files rw,aname=drvfs;path=C:\\Program Files\\Docker;cache=0x5\n", "/user/cache", false, false},
		{"empty", "", "/user/cache", false, true},
		{"truncated", strings.TrimSuffix(valid, "\n"), "/user/cache", false, true},
		{"blank_line", valid + "\n", "/user/cache", false, true},
		{"duplicate_id", valid + valid, "/user/cache", false, true},
		{"malformed_tail", valid + "bad\n", "/user/cache", false, true},
		{"oversize", strings.Repeat("x", generatedMountInfoLimit+1), "/user/cache", false, true},
		{"relative_argument", valid, "relative/cache", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := generatedDescendantMountsFromReader("/owned/cache", test.protected, strings.NewReader(test.snapshot))
			if (err != nil) != test.bad || got != test.want {
				t.Fatalf("descendant=%v err=%v want=%v bad=%v", got, err, test.want, test.bad)
			}
		})
	}
}

func TestGeneratedStorageMountProtectionNamespaceRoots(t *testing.T) {
	fixture := func(root, mountpoint, filesystem string) string {
		return "61 9 0:4 " + root + " " + mountpoint + " rw - " + filesystem + " nsfs rw\n"
	}
	for _, namespace := range []string{"cgroup", "ipc", "mnt", "net", "pid", "time", "user", "uts"} {
		t.Run("valid_"+namespace, func(t *testing.T) {
			snapshot := fixture(namespace+":[4026532232]", "/run/snapd/ns/firefox.mnt", "nsfs")
			got, err := generatedDescendantMountsFromReader("/owned/cache", "/user/cache", strings.NewReader(snapshot))
			if err != nil || got {
				t.Fatalf("disjoint namespace mount: descendant=%v err=%v", got, err)
			}
		})
	}
	for _, test := range []struct {
		name, root, mountpoint, filesystem string
		want, bad                          bool
	}{
		{"ordinary_nsfs_root", "/", "/elsewhere", "nsfs", false, false},
		{"escaped_nsfs_path", `/namespace\040root`, "/elsewhere", "nsfs", false, false},
		{"largest_inode", "mnt:[18446744073709551615]", "/elsewhere", "nsfs", false, false},
		{"protected_descendant", "mnt:[4026532232]", "/user/cache/namespace", "nsfs", true, false},
		{"owned_descendant", "mnt:[4026532232]", "/owned/cache/namespace", "nsfs", true, false},
		{"root_mountpoint", "mnt:[4026532232]", "/user/cache", "nsfs", false, false},
		{"lexical_sibling", "mnt:[4026532232]", "/user/cache-other/namespace", "nsfs", false, false},
		{"non_nsfs_namespace_root", "mnt:[4026532232]", "/elsewhere", "ext4", false, true},
		{"relative_nsfs_root", "relative", "/elsewhere", "nsfs", false, true},
		{"unknown_namespace", "unknown:[4026532232]", "/elsewhere", "nsfs", false, true},
		{"zero_inode", "mnt:[0]", "/elsewhere", "nsfs", false, true},
		{"leading_zero", "mnt:[01]", "/elsewhere", "nsfs", false, true},
		{"empty_inode", "mnt:[]", "/elsewhere", "nsfs", false, true},
		{"signed_inode", "mnt:[+1]", "/elsewhere", "nsfs", false, true},
		{"negative_inode", "mnt:[-1]", "/elsewhere", "nsfs", false, true},
		{"overflow_inode", "mnt:[18446744073709551616]", "/elsewhere", "nsfs", false, true},
		{"nondecimal_inode", "mnt:[0x123]", "/elsewhere", "nsfs", false, true},
		{"missing_close", "mnt:[4026532232", "/elsewhere", "nsfs", false, true},
		{"extra_suffix", "mnt:[4026532232]extra", "/elsewhere", "nsfs", false, true},
		{"extra_bracket", "mnt:[4026532232]]", "/elsewhere", "nsfs", false, true},
		{"escaped_inode", `mnt:[\0401]`, "/elsewhere", "nsfs", false, true},
		{"namespace_mountpoint", "mnt:[4026532232]", "mnt:[4026532233]", "nsfs", false, true},
		{"noncanonical_mountpoint", "mnt:[4026532232]", "/elsewhere/../user/cache/namespace", "nsfs", false, true},
	} {
		t.Run(test.name, func(t *testing.T) {
			got, err := generatedDescendantMountsFromReader("/owned/cache", "/user/cache", strings.NewReader(fixture(test.root, test.mountpoint, test.filesystem)))
			if (err != nil) != test.bad || got != test.want {
				t.Fatalf("descendant=%v err=%v want=%v bad=%v", got, err, test.want, test.bad)
			}
		})
	}
	valid := fixture("mnt:[4026532232]", "/elsewhere", "nsfs")
	descendant := fixture("mnt:[4026532232]", "/owned/cache/namespace", "nsfs")
	for _, snapshot := range []string{valid + "bad\n", descendant + "bad\n", valid + valid, strings.TrimSuffix(valid, "\n")} {
		if got, err := generatedDescendantMountsFromReader("/owned/cache", "/user/cache", strings.NewReader(snapshot)); err == nil || got {
			t.Fatalf("namespace entry authorized incomplete evidence: descendant=%v err=%v", got, err)
		}
	}
}

type generatedMountInfoReadFixture struct {
	io.Reader
	read   int
	closed bool
}

func (r *generatedMountInfoReadFixture) Read(p []byte) (int, error) {
	n, err := r.Reader.Read(p)
	r.read += n
	return n, err
}
func (r *generatedMountInfoReadFixture) Close() error { r.closed = true; return nil }

func TestGeneratedStorageMountProtectionMountInfoReader(t *testing.T) {
	for _, oversized := range []bool{false, true} {
		prefix := "10 9 8:1 / /elsewhere rw - ext4 /dev/fixture "
		snapshot := prefix + strings.Repeat("x", generatedMountInfoLimit-len(prefix)-1) + "\n"
		if oversized {
			snapshot += "more than the bounded read"
		}
		reader := &generatedMountInfoReadFixture{Reader: strings.NewReader(snapshot)}
		got, err := generatedDescendantMountsWithOpen("/owned/cache", "/user/cache", func(path string) (io.ReadCloser, error) {
			if path != "/proc/self/mountinfo" {
				t.Fatalf("wrong namespace source: %q", path)
			}
			return reader, nil
		})
		wantRead := generatedMountInfoLimit
		if oversized {
			wantRead++
		}
		if got || (err != nil) != oversized || reader.read != wantRead || !reader.closed {
			t.Fatalf("bounded read: descendant=%v err=%v read=%d closed=%v", got, err, reader.read, reader.closed)
		}
	}
	_, err := generatedDescendantMountsWithOpen("/owned/cache", "/user/cache", func(string) (io.ReadCloser, error) {
		return nil, os.ErrPermission
	})
	if !errors.Is(err, os.ErrPermission) {
		t.Fatalf("open failure lost: %v", err)
	}
	_, err = generatedDescendantMountsFromReader("/owned/cache", "/user/cache", generatedMountInfoFailedReader{})
	if !errors.Is(err, io.ErrUnexpectedEOF) {
		t.Fatalf("read failure lost: %v", err)
	}
}

type generatedMountInfoFailedReader struct{}

func (generatedMountInfoFailedReader) Read([]byte) (int, error) { return 0, io.ErrUnexpectedEOF }

func TestGeneratedStorageMountProtectionDescendantProductionWiring(t *testing.T) {
	file, err := parser.ParseFile(token.NewFileSet(), "generated_storage.go", nil, 0)
	if err != nil {
		t.Fatal(err)
	}
	// The common per-call fixtures must exercise the same descendant callback
	// as automatic cleanup, not an unwired parser or a test-only global hook.
	wired := false
	for _, decl := range file.Decls {
		fn, ok := decl.(*ast.FuncDecl)
		if !ok || fn.Name.Name != "generatedProtectedOverlap" {
			continue
		}
		ast.Inspect(fn.Body, func(node ast.Node) bool {
			call, ok := node.(*ast.CallExpr)
			if !ok || len(call.Args) != 4 {
				return true
			}
			callee, ok := call.Fun.(*ast.Ident)
			if !ok || callee.Name != "generatedProtectedOverlapWithProbe" {
				return true
			}
			callback, ok := call.Args[3].(*ast.Ident)
			wired = ok && callback.Name == "generatedDescendantMounts"
			return true
		})
	}
	if !wired {
		t.Fatal("production overlap is not wired to native descendant evidence")
	}
}
