//go:build linux || darwin

package backup

import (
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

//nolint:cyclop // Each assertion covers one independent filesystem identity boundary.
func TestBackupRootOpenBoundaries(t *testing.T) {
	t.Parallel()

	if root, found := openExistingBackupRoot("relative"); root != nil || found {
		t.Fatalf("openExistingBackupRoot(relative) = %#v, %t", root, found)
	}
	broad := t.TempDir()
	if root, found := openExistingBackupRoot(broad); root != nil || found {
		t.Fatalf("openExistingBackupRoot(broad) = %#v, %t", root, found)
	}
	parent := t.TempDir()
	root, err := openBackupRoot(filepath.Join(parent, "missing", "backups"))
	if root != nil || err == nil {
		t.Fatalf("openBackupRoot(missing parent) = %#v, %v", root, err)
	}
	if err := os.Chmod(parent, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	target := filepath.Join(parent, "target")
	if err := os.Mkdir(target, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	symlink := filepath.Join(parent, "symlink")
	if err := os.Symlink(target, symlink); err != nil {
		t.Fatal(err)
	}
	if root, err := openBackupRoot(symlink); root != nil || err == nil {
		t.Fatalf("openBackupRoot(symlink) = %#v, %v", root, err)
	}
	descriptor, err := openDirectory(parent)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = unix.Close(descriptor) })
	root, err = openRootAnchor(descriptor, "missing", filepath.Join(parent, "missing"))
	if root != nil || err == nil {
		t.Fatalf("openRootAnchor(missing) = %#v, %v", root, err)
	}
	if err := os.Chmod(target, 0o755); err != nil { //nolint:gosec // Broad mode is the rejected fixture.
		t.Fatal(err)
	}
	root, err = openRootAnchor(descriptor, "target", target)
	if root != nil || err == nil {
		t.Fatalf("openRootAnchor(broad) = %#v, %v", root, err)
	}
}
