//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"golang.org/x/sys/unix"
)

func TestMigrationSnapshotDetectsReplacement(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)

	snapshot, err := createMigrationSnapshot(
		context.Background(),
		database,
		anchor,
		currentSchemaVersion,
		currentSchemaVersion+1,
	)
	if err != nil {
		t.Fatal(err)
	}

	entry := filepath.Join(anchor.directoryPath, snapshot.name)
	requireNoError(t, os.Rename(entry, entry+".moved"))
	requireNoError(t, os.WriteFile(entry, []byte("replacement"), privateFileMode))

	if snapshot.Valid() {
		t.Fatal("migration snapshot remained valid after replacement")
	}

	if !errors.Is(snapshot.Close(), ErrInvalidState) {
		t.Fatal("migration snapshot removed an unowned replacement")
	}

	content := readAnchoredFile(t, anchor.directory, snapshot.name)
	if content != "replacement" {
		t.Fatalf("replacement = %q", content)
	}
}

func TestOpenMigrationSnapshotRejectsRandomFailureAndCollision(t *testing.T) {
	t.Parallel()

	anchor, _ := testMigrationDatabase(t)

	snapshot, err := openMigrationSnapshot(anchor, strings.NewReader("short"), 1, 2)
	if snapshot != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("openMigrationSnapshot(short random) = %#v, %v", snapshot, err)
	}

	nonce := make([]byte, migrationSnapshotNonceBytes)
	name := migrationSnapshotPrefix + hex.EncodeToString(nonce) + migrationSnapshotSuffix
	requireNoError(t, os.WriteFile(filepath.Join(anchor.directoryPath, name), nil, privateFileMode))

	snapshot, err = openMigrationSnapshot(anchor, bytes.NewReader(nonce), 1, 2)
	if snapshot != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("openMigrationSnapshot(collision) = %#v, %v", snapshot, err)
	}
}

func TestAdoptMigrationSnapshotRejectsUnsafeDescriptor(t *testing.T) {
	t.Parallel()

	anchor, _ := testMigrationDatabase(t)
	name := migrationSnapshotPrefix + "unsafe" + migrationSnapshotSuffix
	descriptor, err := unix.Openat(
		anchor.directory,
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	requireNoError(t, err)
	requireNoError(t, unix.Fchmod(descriptor, 0o644))

	snapshot, err := adoptMigrationSnapshot(anchor, name, descriptor, 1, 2)
	if snapshot != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("adoptMigrationSnapshot(unsafe) = %#v, %v", snapshot, err)
	}

	requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, name)))
}

func TestMigrationSnapshotMeasureContainsReadFailure(t *testing.T) {
	t.Parallel()

	anchor, _ := testMigrationDatabase(t)
	nonce := bytes.Repeat([]byte{1}, migrationSnapshotNonceBytes)

	snapshot, err := openMigrationSnapshot(anchor, bytes.NewReader(nonce), 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	_, err = snapshot.file.Write([]byte("content"))
	requireNoError(t, err)
	requireNoError(t, snapshot.file.Close())

	descriptor, err := unix.Openat(anchor.directory, snapshot.name, unix.O_WRONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	requireNoError(t, err)

	snapshot.file = os.NewFile(uintptr(descriptor), snapshot.name)

	if _, _, valid := snapshot.measure(); valid {
		t.Fatal("write-only snapshot produced a digest")
	}

	requireNoError(t, snapshot.Close())
}

func TestMigrationSnapshotCloseContainsFilesystemFailure(t *testing.T) {
	t.Parallel()

	anchor, _ := testMigrationDatabase(t)
	nonce := bytes.Repeat([]byte{2}, migrationSnapshotNonceBytes)

	snapshot, err := openMigrationSnapshot(anchor, bytes.NewReader(nonce), 1, 2)
	if err != nil {
		t.Fatal(err)
	}

	entry := filepath.Join(anchor.directoryPath, snapshot.name)

	err = snapshot.closeWith(
		func(int, string) error {
			requireNoError(t, snapshot.file.Close())

			return ErrUnavailable
		},
		func(int) error { return nil },
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("migrationSnapshot.closeWith() = %v", err)
	}

	requireNoError(t, os.Remove(entry))
}

func TestMigrationSnapshotFilesystemHelpersContainErrors(t *testing.T) {
	t.Parallel()

	file, err := os.CreateTemp(t.TempDir(), "snapshot")
	requireNoError(t, err)
	requireNoError(t, file.Close())

	if syncMigrationSnapshot(file) == nil {
		t.Fatal("syncMigrationSnapshot(closed) succeeded")
	}

	if unlinkMigrationSnapshot(-1, "missing") == nil {
		t.Fatal("unlinkMigrationSnapshot(invalid descriptor) succeeded")
	}

	if syncMigrationSnapshotDirectory(-1) == nil {
		t.Fatal("syncMigrationSnapshotDirectory(invalid descriptor) succeeded")
	}
}
