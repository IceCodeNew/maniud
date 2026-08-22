//go:build darwin

package backup

import "golang.org/x/sys/unix"

func readFilesystemCapacity(descriptor int) (filesystemCapacity, error) {
	var empty filesystemCapacity
	var value unix.Statfs_t
	if unix.Fstatfs(descriptor, &value) != nil {
		return empty, ErrInvalidBackupRoot
	}

	return darwinFilesystemCapacity(value)
}

func darwinFilesystemCapacity(value unix.Statfs_t) (filesystemCapacity, error) {
	return filesystemCapacityFromValues(
		value.Fsid.Val,
		uint64(value.Bsize),
		value.Blocks,
		value.Bavail,
		value.Ffree,
	)
}
