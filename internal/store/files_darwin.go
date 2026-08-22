//go:build darwin

package store

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func platformDatabasePath(anchor *stateAnchor) string {
	return platformEntryPath(anchor, anchor.databaseName)
}

func platformEntryPath(anchor *stateAnchor, name string) string {
	// Darwin cannot traverse an entry below a directory descriptor through
	// /dev/fd. The retained descriptor and identity checks reject replacements
	// that remain visible at a validation boundary, while SQLite uses the public
	// path for native WAL and shared-memory semantics. A hostile same-EUID process
	// can still swap and restore that path between checks; preventing that would
	// require a writable openat-based SQLite VFS rather than another path check.
	return filepath.Join(anchor.directoryPath, name)
}

func statIdentity(metadata unix.Stat_t) fileIdentity {
	return fileIdentity{
		device: uint64(metadata.Dev),
		inode:  metadata.Ino,
		mode:   uint32(metadata.Mode),
		owner:  metadata.Uid,
		links:  normalizedLinkCount(metadata.Nlink),
	}
}
