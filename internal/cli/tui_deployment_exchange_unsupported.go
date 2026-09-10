//go:build !linux && !darwin

package cli

import (
	"fmt"
	"os"
)

func exchangeDeploymentEntries(*os.File, string, string) error {
	return fmt.Errorf("exchange deployment entries: %w", errDeploymentEditInvalid)
}
