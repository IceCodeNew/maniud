//go:build linux

package store

import (
	"path/filepath"
	"strconv"

	"golang.org/x/sys/unix"
)

const descriptorRoot = "/proc/self/fd/"

func platformDatabasePath(anchor *stateAnchor) string {
	return descriptorRoot + strconv.Itoa(anchor.directory) + string(filepath.Separator) + anchor.databaseName
}

func statIdentity(metadata unix.Stat_t) fileIdentity {
	return fileIdentity{
		device: metadata.Dev,
		inode:  metadata.Ino,
		mode:   metadata.Mode,
		owner:  metadata.Uid,
	}
}
