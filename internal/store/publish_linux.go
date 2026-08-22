//go:build linux

package store

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func renameNoReplace(directory int, source, destination string) error {
	err := unix.Renameat2(directory, source, directory, destination, unix.RENAME_NOREPLACE)
	if err != nil {
		return fmt.Errorf("rename without replacement: %w", err)
	}

	return nil
}
