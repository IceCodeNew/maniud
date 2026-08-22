//go:build linux || darwin

package store

import (
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestRenameNoReplacePublishesWithinAnchoredDirectory(t *testing.T) {
	t.Parallel()

	directoryPath := privateTempDir(t)
	directory, err := openDirectory(directoryPath)
	requireNoError(t, err)
	t.Cleanup(func() {
		requireNoError(t, unix.Close(directory))
	})

	writeAnchoredFile(t, directory, "snapshot.partial", "snapshot")
	requireNoError(t, renameNoReplace(directory, "snapshot.partial", "snapshot.sqlite"))

	content := readAnchoredFile(t, directory, "snapshot.sqlite")
	if content != "snapshot" {
		t.Fatalf("published content = %q", content)
	}

	_, err = os.Lstat(filepath.Join(directoryPath, "snapshot.partial"))
	if !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("temporary entry error = %v", err)
	}
}

func TestRenameNoReplacePreservesConflictAndMissingSource(t *testing.T) {
	t.Parallel()

	directoryPath := privateTempDir(t)
	directory, err := openDirectory(directoryPath)
	requireNoError(t, err)
	t.Cleanup(func() {
		requireNoError(t, unix.Close(directory))
	})

	writeAnchoredFile(t, directory, "manifest.partial", "new")
	writeAnchoredFile(t, directory, "manifest.json", "existing")

	err = renameNoReplace(directory, "manifest.partial", "manifest.json")
	if !errors.Is(err, unix.EEXIST) {
		t.Fatalf("rename conflict error = %v", err)
	}

	for name, want := range map[string]string{
		"manifest.partial": "new",
		"manifest.json":    "existing",
	} {
		content := readAnchoredFile(t, directory, name)
		if content != want {
			t.Fatalf("%s content = %q", name, content)
		}
	}

	err = renameNoReplace(directory, "missing.partial", "missing.json")
	if !errors.Is(err, unix.ENOENT) {
		t.Fatalf("missing source error = %v", err)
	}
}

func writeAnchoredFile(t *testing.T, directory int, name, content string) {
	t.Helper()

	descriptor, err := unix.Openat(
		directory,
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	requireNoError(t, err)

	_, err = unix.Write(descriptor, []byte(content))
	requireNoError(t, err)
	requireNoError(t, unix.Fsync(descriptor))
	requireNoError(t, unix.Close(descriptor))
}

func readAnchoredFile(t *testing.T, directory int, name string) string {
	t.Helper()

	descriptor, err := unix.Openat(directory, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	requireNoError(t, err)

	file := os.NewFile(uintptr(descriptor), name)
	content, err := io.ReadAll(file)
	requireNoError(t, err)
	requireNoError(t, file.Close())

	return string(content)
}
