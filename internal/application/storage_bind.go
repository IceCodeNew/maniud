package application

import (
	"fmt"
	"os"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	replacementBindDirectory     = "replacements"
	replacementBindDirectoryMode = os.FileMode(0o700)
)

func prepareUpgradeReplacementBinds(execution *upgradeExecution) error {
	if execution == nil || execution.mutation == nil {
		return ErrInvalidRequest
	}

	replacements := replacementBindIndexes(execution.sources, execution.mutation.preparation.Workload)
	if len(replacements) == 0 {
		return nil
	}

	root := execution.mutation.backupRoot
	if root == "" {
		return ErrInvalidRequest
	}

	transaction := execution.mutation.preparation.Transaction.ID.String()
	for _, index := range replacements {
		desired := execution.mutation.preparation.Workload.Mounts[index]
		path, err := replacementBindPath(root, transaction, desired)
		if err != nil {
			return err
		}
		if err = ensureEmptyReplacementBind(path); err != nil {
			return err
		}

		execution.mutation.preparation.Workload.Mounts[index].Source = path
	}

	return nil
}

func replacementBindPath(root, transaction string, desired domain.Mount) (string, error) {
	if root == "" || transaction == "" || desired.Target == "" {
		return "", ErrInvalidRequest
	}

	name := filepath.Base(filepath.Clean(desired.Target))
	if name == "" || name == "." || name == string(filepath.Separator) {
		return "", ErrInvalidRequest
	}

	return filepath.Join(root, replacementBindDirectory, transaction, name), nil
}

func ensureEmptyReplacementBind(path string) error {
	parent := filepath.Dir(path)
	if err := os.MkdirAll(parent, replacementBindDirectoryMode); err != nil {
		return fmt.Errorf("create replacement bind parent: %w", err)
	}

	info, err := os.Lstat(path)
	switch {
	case err == nil:
		if !info.IsDir() {
			return ErrConflictingState
		}
		entries, readErr := os.ReadDir(path)
		if readErr != nil {
			return fmt.Errorf("read replacement bind: %w", readErr)
		}
		if len(entries) != 0 {
			return ErrConflictingState
		}

		return nil
	case os.IsNotExist(err):
		if err = os.Mkdir(path, replacementBindDirectoryMode); err != nil {
			return fmt.Errorf("create replacement bind: %w", err)
		}

		return nil
	default:
		return fmt.Errorf("stat replacement bind: %w", err)
	}
}
