//go:build linux || darwin

package store

import (
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

func openMigrationSnapshot(
	anchor *stateAnchor,
	random io.Reader,
	sourceSchema int,
	targetSchema int,
) (*migrationSnapshot, error) {
	nonce := make([]byte, migrationSnapshotNonceBytes)

	_, err := io.ReadFull(random, nonce)
	if err != nil {
		return nil, ErrUnavailable
	}

	name := migrationSnapshotPrefix + hex.EncodeToString(nonce) + migrationSnapshotSuffix

	descriptor, err := unix.Openat(
		anchor.directory,
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err != nil {
		return nil, ErrInvalidState
	}

	return adoptMigrationSnapshot(anchor, name, descriptor, sourceSchema, targetSchema)
}

func adoptMigrationSnapshot(
	anchor *stateAnchor,
	name string,
	descriptor int,
	sourceSchema int,
	targetSchema int,
) (*migrationSnapshot, error) {
	identity, valid := descriptorIdentity(descriptor)
	snapshot := &migrationSnapshot{
		anchor:       anchor,
		file:         os.NewFile(uintptr(descriptor), name),
		name:         name,
		identity:     identity,
		sourceSchema: sourceSchema,
		targetSchema: targetSchema,
		size:         0,
		digest:       [sha256.Size]byte{},
	}

	if !valid || !privateRegular(identity) || !snapshot.Valid() {
		_ = snapshot.Close()

		return nil, ErrInvalidState
	}

	return snapshot, nil
}

// Valid reports whether the temporary snapshot, its directory entry, and the
// migration-lock filesystem anchor still identify the created objects.
func (snapshot *migrationSnapshot) Valid() bool {
	if snapshot == nil || snapshot.file == nil || snapshot.anchor == nil || !snapshot.anchor.locked ||
		!snapshot.anchor.valid() {
		return false
	}

	identity, valid := descriptorIdentity(int(snapshot.file.Fd()))

	return valid && privateRegular(identity) && identity == snapshot.identity &&
		snapshot.anchor.validEntry(snapshot.name, snapshot.identity)
}

// Close removes an owned temporary snapshot, syncs its directory, and closes
// the retained file descriptor. It preserves entries whose identity changed.
func (snapshot *migrationSnapshot) Close() error {
	return snapshot.closeWith(unlinkMigrationSnapshot, syncMigrationSnapshotDirectory)
}

func (snapshot *migrationSnapshot) measure() (int64, [sha256.Size]byte, bool) {
	if !snapshot.Valid() {
		return 0, [sha256.Size]byte{}, false
	}

	metadata, err := snapshot.file.Stat()
	if err != nil || metadata.Size() <= 0 {
		return 0, [sha256.Size]byte{}, false
	}

	digester := sha256.New()
	reader := io.NewSectionReader(snapshot.file, 0, metadata.Size())

	size, err := io.Copy(digester, reader)
	if err != nil || size != metadata.Size() || !snapshot.Valid() {
		return 0, [sha256.Size]byte{}, false
	}

	var digest [sha256.Size]byte
	copy(digest[:], digester.Sum(nil))

	return size, digest, true
}

func (snapshot *migrationSnapshot) closeWith(
	unlink func(int, string) error,
	syncDirectory func(int) error,
) error {
	if snapshot == nil || snapshot.file == nil {
		return ErrUnavailable
	}

	valid := snapshot.Valid()
	unlinkErr := error(nil)
	fsyncErr := error(nil)

	if valid {
		unlinkErr = unlink(snapshot.anchor.directory, snapshot.name)
		if unlinkErr == nil {
			fsyncErr = syncDirectory(snapshot.anchor.directory)
		}
	}

	closeErr := snapshot.file.Close()
	snapshot.file = nil

	if !valid {
		return ErrInvalidState
	}

	if unlinkErr != nil || fsyncErr != nil || closeErr != nil {
		return errors.Join(ErrUnavailable, unlinkErr, fsyncErr, closeErr)
	}

	return nil
}

func unlinkMigrationSnapshot(directory int, name string) error {
	err := unix.Unlinkat(directory, name, 0)
	if err != nil {
		return fmt.Errorf("unlink migration snapshot: %w", err)
	}

	return nil
}

func syncMigrationSnapshotDirectory(directory int) error {
	err := unix.Fsync(directory)
	if err != nil {
		return fmt.Errorf("sync migration snapshot directory: %w", err)
	}

	return nil
}
