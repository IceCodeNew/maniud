//go:build linux

package backup

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func canonicalBackupParent(path string) string {
	return path
}

func statIdentity(metadata unix.Stat_t) fileIdentity {
	return fileIdentity{
		device: metadata.Dev,
		inode:  metadata.Ino,
		mode:   metadata.Mode,
		owner:  metadata.Uid,
		links:  uint64(metadata.Nlink),
	}
}

func renameNoReplace(directory int, source, destination string) error {
	err := unix.Renameat2(directory, source, directory, destination, unix.RENAME_NOREPLACE)
	if err != nil {
		return fmt.Errorf("publish backup directory: %w", err)
	}

	return nil
}
