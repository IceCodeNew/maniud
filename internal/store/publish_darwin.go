//go:build darwin

package store

import (
	"fmt"

	"golang.org/x/sys/unix"
)

func renameNoReplace(directory int, source, destination string) error {
	err := unix.RenameatxNp(directory, source, directory, destination, unix.RENAME_EXCL)
	if err != nil {
		return fmt.Errorf("rename without replacement: %w", err)
	}

	return nil
}
