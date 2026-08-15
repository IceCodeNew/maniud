//go:build darwin

package store

import (
	"path/filepath"

	"golang.org/x/sys/unix"
)

func platformDatabasePath(anchor *stateAnchor) string {
	return filepath.Join(anchor.directoryPath, anchor.databaseName)
}

func statIdentity(metadata unix.Stat_t) fileIdentity {
	return fileIdentity{
		device: uint64(metadata.Dev),
		inode:  metadata.Ino,
		mode:   uint32(metadata.Mode),
		owner:  metadata.Uid,
	}
}
