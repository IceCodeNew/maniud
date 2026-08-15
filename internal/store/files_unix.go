//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"golang.org/x/sys/unix"
)

const (
	privateFileMode = 0o600
	lockRetry       = 10 * time.Millisecond
	lockTimeout     = time.Second
)

type stateAnchor struct {
	directory     int
	lock          int
	locked        bool
	directoryPath string
	databaseName  string
	directoryID   fileIdentity
	lockID        fileIdentity
	databaseID    fileIdentity
	walID         fileIdentity
	sharedID      fileIdentity
}

type fileIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	owner  uint32
	links  uint64
}

func normalizedLinkCount[T ~uint16 | ~uint32 | ~uint64](value T) uint64 {
	return uint64(value)
}

func openStateAnchor(ctx context.Context, path string) (*stateAnchor, error) {
	if !validStatePath(path) {
		return nil, ErrInvalidPath
	}

	directoryPath := filepath.Dir(path)

	directory, err := openDirectory(directoryPath)
	if err != nil {
		return nil, ErrInvalidState
	}

	anchor := &stateAnchor{
		directory:     directory,
		lock:          -1,
		locked:        false,
		directoryPath: directoryPath,
		databaseName:  filepath.Base(path),
		directoryID:   fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0},
		lockID:        fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0},
		databaseID:    fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0},
		walID:         fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0},
		sharedID:      fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0},
	}

	if !anchor.captureDirectory() {
		_ = anchor.close()

		return nil, ErrInvalidState
	}

	err = anchor.openLock(ctx)
	if err != nil {
		_ = anchor.close()

		return nil, err
	}

	if !anchor.openDatabase() || !anchor.valid() {
		_ = anchor.close()

		return nil, ErrInvalidState
	}

	return anchor, nil
}

func validStatePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && filepath.Base(path) != "." &&
		filepath.Base(path) != string(filepath.Separator)
}

func openDirectory(path string) (int, error) {
	descriptor, _ := unix.Open(
		string(filepath.Separator),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY,
		0,
	)

	for _, component := range splitAbsolutePath(path) {
		next, openErr := unix.Openat(
			descriptor,
			component,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
			0,
		)
		_ = unix.Close(descriptor)

		if openErr != nil {
			return -1, ErrInvalidState
		}

		descriptor = next
	}

	return descriptor, nil
}

func splitAbsolutePath(path string) []string {
	cleaned := filepath.Clean(path)
	if cleaned == string(filepath.Separator) {
		return nil
	}

	return strings.Split(cleaned[1:], string(filepath.Separator))
}

func (anchor *stateAnchor) captureDirectory() bool {
	identity, valid := descriptorIdentity(anchor.directory)
	if !valid || identity.mode&unix.S_IFMT != unix.S_IFDIR || int64(identity.owner) != int64(unix.Geteuid()) ||
		identity.mode&0o077 != 0 {
		return false
	}

	anchor.directoryID = identity

	return true
}

func (anchor *stateAnchor) openLock(ctx context.Context) error {
	descriptor, err := unix.Openat(
		anchor.directory,
		anchor.databaseName+".lock",
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err != nil {
		return ErrInvalidState
	}

	anchor.lock = descriptor

	identity, valid := descriptorIdentity(descriptor)
	if !valid || !privateRegular(identity) {
		return ErrInvalidState
	}

	anchor.lockID = identity

	err = waitForLock(ctx, descriptor)
	if err == nil {
		anchor.locked = true
	}

	return err
}

func waitForLock(ctx context.Context, descriptor int) error {
	deadline := time.Now().Add(lockTimeout)

	for {
		if ctx.Err() != nil {
			return errors.Join(ErrUnavailable, ctx.Err())
		}

		acquired, err := tryLock(descriptor)
		if err != nil {
			return err
		}

		if acquired {
			return nil
		}

		if time.Now().After(deadline) {
			return ErrUnavailable
		}

		timer := time.NewTimer(lockRetry)
		select {
		case <-ctx.Done():
			timer.Stop()

			return errors.Join(ErrUnavailable, ctx.Err())
		case <-timer.C:
		}
	}
}

func tryLock(descriptor int) (bool, error) {
	err := unix.Flock(descriptor, unix.LOCK_EX|unix.LOCK_NB)
	if err == nil {
		return true, nil
	}

	if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
		return false, nil
	}

	return false, ErrUnavailable
}

func (anchor *stateAnchor) openDatabase() bool {
	databaseID, databaseValid := anchor.openRegular(anchor.databaseName)
	walID, walValid := anchor.openRegular(anchor.databaseName + "-wal")

	sharedID, sharedValid := anchor.openRegular(anchor.databaseName + "-shm")
	if !databaseValid || !walValid || !sharedValid {
		return false
	}

	anchor.databaseID = databaseID
	anchor.walID = walID
	anchor.sharedID = sharedID

	return true
}

func (anchor *stateAnchor) openRegular(name string) (fileIdentity, bool) {
	descriptor, err := unix.Openat(
		anchor.directory,
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err != nil {
		return fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	identity, valid := descriptorIdentity(descriptor)
	closeErr := unix.Close(descriptor)

	if !valid || closeErr != nil || !privateRegular(identity) {
		return fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	return identity, true
}

func privateRegular(identity fileIdentity) bool {
	return identity.mode&unix.S_IFMT == unix.S_IFREG && int64(identity.owner) == int64(unix.Geteuid()) &&
		identity.mode&0o077 == 0 && identity.links == 1
}

func descriptorIdentity(descriptor int) (fileIdentity, bool) {
	var metadata unix.Stat_t
	if unix.Fstat(descriptor, &metadata) != nil {
		return fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	return statIdentity(metadata), true
}

func (anchor *stateAnchor) valid() bool {
	return anchor.validDirectory() && anchor.validEntry(anchor.databaseName+".lock", anchor.lockID) &&
		anchor.validEntry(anchor.databaseName, anchor.databaseID) &&
		anchor.validEntry(anchor.databaseName+"-wal", anchor.walID) &&
		anchor.validEntry(anchor.databaseName+"-shm", anchor.sharedID)
}

func (anchor *stateAnchor) validDirectory() bool {
	directory, directoryValid := descriptorIdentity(anchor.directory)
	path, pathValid := pathIdentity(anchor.directoryPath)

	return directoryValid && pathValid && sameNode(directory, anchor.directoryID) && sameNode(path, anchor.directoryID)
}

func sameNode(left, right fileIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.mode == right.mode &&
		left.owner == right.owner
}

func (anchor *stateAnchor) validEntry(name string, expected fileIdentity) bool {
	identity, valid := entryIdentity(anchor.directory, name)

	return valid && identity == expected
}

func pathIdentity(path string) (fileIdentity, bool) {
	var metadata unix.Stat_t
	if unix.Lstat(path, &metadata) != nil {
		return fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	return statIdentity(metadata), true
}

func entryIdentity(directory int, name string) (fileIdentity, bool) {
	var metadata unix.Stat_t
	if unix.Fstatat(directory, name, &metadata, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	return statIdentity(metadata), true
}

func (anchor *stateAnchor) databasePath() string {
	return platformDatabasePath(anchor)
}

func (anchor *stateAnchor) unlock() error {
	anchor.locked = false

	if unix.Flock(anchor.lock, unix.LOCK_UN) != nil {
		return ErrUnavailable
	}

	return nil
}

func (anchor *stateAnchor) close() error {
	anchor.locked = false

	lockErr := error(nil)
	if anchor.lock >= 0 {
		lockErr = unix.Close(anchor.lock)
		anchor.lock = -1
	}

	directoryErr := unix.Close(anchor.directory)
	anchor.directory = -1

	return errors.Join(lockErr, directoryErr)
}
