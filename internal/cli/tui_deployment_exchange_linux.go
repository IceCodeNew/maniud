//go:build linux

package cli

import (
	"fmt"
	"os"

	"golang.org/x/sys/unix"
)

func exchangeDeploymentEntries(directory *os.File, first, second string) error {
	descriptor := int(directory.Fd())
	if err := unix.Renameat2(descriptor, first, descriptor, second, unix.RENAME_EXCHANGE); err != nil {
		return fmt.Errorf("exchange deployment entries: %w", err)
	}

	return nil
}
