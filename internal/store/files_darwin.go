//go:build darwin

package store

import (
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

func platformDatabasePath(anchor *stateAnchor) string {
	return platformEntryPath(anchor, anchor.databaseName)
}

func platformEntryPath(anchor *stateAnchor, name string) string {
	return filepath.Join(anchor.directoryPath, name)
}

func platformDescriptorPath(descriptor int) string {
	return "/dev/fd/" + strconv.Itoa(descriptor)
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
