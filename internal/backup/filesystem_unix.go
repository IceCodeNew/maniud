//go:build linux || darwin

package backup

import (
	"errors"
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

const (
	privateDirectoryMode = 0o700
	privateFileMode      = 0o600
	unixPermissionMode   = 0o7777
)

type fileIdentity struct {
	device uint64
	inode  uint64
	mode   uint32
	owner  uint32
	links  uint64
}

type directoryAnchor struct {
	descriptor int
	path       string
	identity   fileIdentity
}

func openBackupRoot(root string) (*directoryAnchor, error) {
	return openBackupRootWithSync(root, unix.Fsync)
}

func openExistingBackupRoot(root string) (*directoryAnchor, bool) {
	if !validRootPath(root) {
		return nil, false
	}

	descriptor, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, false
	}

	identity, valid := descriptorIdentity(descriptor)
	anchor := &directoryAnchor{descriptor: descriptor, path: root, identity: identity}
	if !valid || !privateDirectory(identity) || !anchor.valid() {
		_ = anchor.close()

		return nil, false
	}

	return anchor, true
}

func openBackupRootWithSync(root string, syncDirectory func(int) error) (*directoryAnchor, error) {
	if !validRootPath(root) {
		return nil, ErrInvalidBackupRoot
	}

	parentPath := canonicalBackupParent(filepath.Dir(root))
	parent, err := openDirectory(parentPath)
	if err != nil {
		return nil, ErrInvalidBackupRoot
	}
	defer func() {
		_ = unix.Close(parent)
	}()

	name := filepath.Base(root)
	created, err := ensureBackupRootEntry(parent, name)
	if err != nil {
		return nil, err
	}
	anchor, err := openRootAnchor(parent, name, root)
	if err != nil {
		return nil, err
	}
	if created && (syncDirectory(parent) != nil || !anchor.valid()) {
		_ = anchor.close()

		return nil, ErrInvalidBackupRoot
	}

	return anchor, nil
}

func ensureBackupRootEntry(parent int, name string) (bool, error) {
	err := unix.Mkdirat(parent, name, privateDirectoryMode)
	created := err == nil
	if err != nil && !errors.Is(err, unix.EEXIST) {
		return false, ErrInvalidBackupRoot
	}

	return created, nil
}

func openRootAnchor(parent int, name, root string) (*directoryAnchor, error) {
	descriptor, err := unix.Openat(
		parent,
		name,
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err != nil {
		return nil, ErrInvalidBackupRoot
	}

	identity, valid := descriptorIdentity(descriptor)
	anchor := &directoryAnchor{descriptor: descriptor, path: root, identity: identity}
	if !valid || !privateDirectory(identity) || !anchor.valid() {
		_ = anchor.close()

		return nil, ErrInvalidBackupRoot
	}

	return anchor, nil
}

func validRootPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value &&
		value != string(filepath.Separator) && filepath.Base(value) != "."
}

func openDirectory(value string) (int, error) {
	return openDirectoryWith(value, unix.Open)
}

func openDirectoryWith(value string, openRoot func(string, int, uint32) (int, error)) (int, error) {
	descriptor, err := openRoot(
		string(filepath.Separator),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY,
		0,
	)
	if err != nil {
		return -1, fmt.Errorf("open filesystem root: %w", err)
	}

	for _, component := range splitAbsolutePath(value) {
		next, openErr := unix.Openat(
			descriptor,
			component,
			unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
			0,
		)
		_ = unix.Close(descriptor)
		if openErr != nil {
			return -1, fmt.Errorf("open backup parent: %w", openErr)
		}
		descriptor = next
	}

	return descriptor, nil
}

func splitAbsolutePath(value string) []string {
	cleaned := filepath.Clean(value)
	if cleaned == string(filepath.Separator) {
		return nil
	}

	return strings.Split(strings.TrimPrefix(cleaned, string(filepath.Separator)), string(filepath.Separator))
}

func (anchor *directoryAnchor) valid() bool {
	if anchor == nil || anchor.descriptor < 0 {
		return false
	}

	descriptor, descriptorValid := descriptorIdentity(anchor.descriptor)
	pathValue, pathValid := pathIdentity(anchor.path)

	return descriptorValid && pathValid && sameNode(descriptor, anchor.identity) &&
		sameNode(pathValue, anchor.identity) && privateDirectory(descriptor)
}

func (anchor *directoryAnchor) close() error {
	if anchor == nil || anchor.descriptor < 0 {
		return nil
	}

	err := unix.Close(anchor.descriptor)
	anchor.descriptor = -1
	if err != nil {
		return fmt.Errorf("close backup directory: %w", err)
	}

	return nil
}

func descriptorIdentity(descriptor int) (fileIdentity, bool) {
	var metadata unix.Stat_t
	if unix.Fstat(descriptor, &metadata) != nil {
		return fileIdentity{}, false
	}

	return statIdentity(metadata), true
}

func pathIdentity(value string) (fileIdentity, bool) {
	var metadata unix.Stat_t
	if unix.Lstat(value, &metadata) != nil {
		return fileIdentity{}, false
	}

	return statIdentity(metadata), true
}

func entryIdentity(directory int, name string) (fileIdentity, bool) {
	var metadata unix.Stat_t
	if unix.Fstatat(directory, name, &metadata, unix.AT_SYMLINK_NOFOLLOW) != nil {
		return fileIdentity{}, false
	}

	return statIdentity(metadata), true
}

func privateDirectory(identity fileIdentity) bool {
	return identity.mode&(unix.S_IFMT|unixPermissionMode) == unix.S_IFDIR|privateDirectoryMode &&
		int64(identity.owner) == int64(unix.Geteuid())
}

func privateRegular(identity fileIdentity) bool {
	return identity.mode&(unix.S_IFMT|unixPermissionMode) == unix.S_IFREG|privateFileMode &&
		int64(identity.owner) == int64(unix.Geteuid()) && identity.links == 1
}

func sameNode(left, right fileIdentity) bool {
	return left.device == right.device && left.inode == right.inode && left.mode == right.mode &&
		left.owner == right.owner
}

func sameEntry(left, right fileIdentity) bool {
	return left == right
}
