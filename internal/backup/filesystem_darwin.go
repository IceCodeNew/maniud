//go:build darwin

package backup

import (
	"fmt"
	"path/filepath"
	"strings"

	"golang.org/x/sys/unix"
)

func canonicalBackupParent(path string) string {
	canonical, err := filepath.EvalSymlinks(path)
	if err != nil {
		return path
	}
	if canonical == path {
		return path
	}
	for _, alias := range []string{"/tmp", "/var"} {
		if (path == alias || strings.HasPrefix(path, alias+"/")) &&
			(canonical == "/private"+path || strings.HasPrefix(canonical, "/private"+path+"/")) {
			return canonical
		}
	}

	return path
}

func statIdentity(metadata unix.Stat_t) fileIdentity {
	links := uint64(metadata.Nlink)
	if metadata.Mode&unix.S_IFMT == unix.S_IFDIR {
		// APFS changes a directory's link count as entries are added and removed.
		links = 0
	}

	return fileIdentity{
		//nolint:gosec // Darwin stat.Dev is int32; device identity is stored as uint64.
		device: uint64(metadata.Dev),
		inode:  metadata.Ino,
		mode:   uint32(metadata.Mode),
		owner:  metadata.Uid,
		links:  links,
	}
}

func renameNoReplace(directory int, source, destination string) error {
	err := unix.RenameatxNp(directory, source, directory, destination, unix.RENAME_EXCL)
	if err != nil {
		return fmt.Errorf("publish backup directory: %w", err)
	}

	return nil
}
