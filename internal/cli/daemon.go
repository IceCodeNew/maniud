package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const gitOpsServicesDirectory = "services"

func executeDaemon(
	ctx context.Context,
	arguments daemonInvocation,
	output io.Writer,
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
) error {
	if arguments.once {
		return reconcileRegisteredRepository(ctx, output, environment, stderr, getWorkingDirectory)
	}

	return pollRegisteredRepository(ctx, arguments.interval, output, environment, stderr, getWorkingDirectory)
}

func pollRegisteredRepository(
	ctx context.Context,
	interval time.Duration,
	output io.Writer,
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
) error {
	if interval <= 0 {
		return errGitOpsRepositoryInvalid
	}

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("poll registered repository: %w", err)
		}
		if err := reconcileRegisteredRepository(ctx, output, environment, stderr, getWorkingDirectory); err != nil {
			return err
		}
		if err := waitDaemonInterval(ctx, interval); err != nil {
			return err
		}
	}
}

func waitDaemonInterval(ctx context.Context, interval time.Duration) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait daemon interval: %w", ctx.Err())
	case <-timer.C:
		return nil
	}
}

func reconcileRegisteredRepository(
	ctx context.Context,
	output io.Writer,
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
) error {
	statePath, err := defaultStatePath(environment)
	if err != nil {
		return err
	}

	registration, err := readGitOpsRegistration(gitOpsRegistrationPath(statePath))
	if err != nil {
		return errGitOpsRepositoryInvalid
	}

	dependencies, err := defaultApplyDependencies(environment, stderr, getWorkingDirectory)
	if err != nil {
		return err
	}

	root, currentCommit, err := registeredGitOpsCheckout(
		ctx,
		registration.Repository,
		registration.Branch,
		registration.Commit,
	)
	if err != nil {
		return classifyGitOpsFailure(err)
	}
	if err = recoverGitOpsSnapshot(ctx, root, currentCommit, output, dependencies); err != nil {
		return err
	}

	root, selectedCommit, err := fastForwardGitOpsCheckout(
		ctx,
		registration.Repository,
		registration.Branch,
		registration.Commit,
	)
	if err != nil {
		return classifyGitOpsFailure(err)
	}

	return reconcileGitOpsSnapshot(ctx, root, selectedCommit, output, dependencies)
}

func listGitOpsServiceFiles(root string) ([]string, error) {
	directory := filepath.Join(root, gitOpsServicesDirectory)
	entries, err := os.ReadDir(directory)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("read gitops services: %w", err)
	}

	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if !gitOpsServiceFile(entry) {
			continue
		}

		names = append(names, filepath.Join(directory, entry.Name()))
	}
	slices.Sort(names)

	return names, nil
}

func gitOpsServiceFile(entry fs.DirEntry) bool {
	if entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		return false
	}

	name := entry.Name()

	return strings.HasSuffix(name, ".yaml") || strings.HasSuffix(name, ".yml")
}

func classifyDaemonCommandFailure(err error) *domain.FailureError {
	if err == nil {
		return nil
	}

	switch {
	case errors.Is(err, context.Canceled):
		return domain.OperationCancelled()
	case errors.Is(err, errGitOpsRegistrationExists),
		errors.Is(err, errGitOpsRepositoryInvalid),
		errors.Is(err, compose.ErrInvalidSource),
		errors.Is(err, errStateHomeUnavailable),
		errors.Is(err, errStateHomeInvalid):
		return classifyGitOpsCommandFailure(err)
	default:
		return classifyApplyFailure(err)
	}
}
