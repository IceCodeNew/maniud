//go:build linux

package backup

import "golang.org/x/sys/unix"

func readFilesystemCapacity(descriptor int) (filesystemCapacity, error) {
	var empty filesystemCapacity
	var value unix.Statfs_t
	if unix.Fstatfs(descriptor, &value) != nil {
		return empty, ErrInvalidBackupRoot
	}

	return linuxFilesystemCapacity(value)
}

func linuxFilesystemCapacity(value unix.Statfs_t) (filesystemCapacity, error) {
	if value.Frsize <= 0 {
		return filesystemCapacity{}, ErrInvalidBackupRoot
	}

	return filesystemCapacityFromValues(
		value.Fsid.Val,
		uint64(value.Frsize),
		value.Blocks,
		value.Bavail,
		value.Ffree,
	)
}
