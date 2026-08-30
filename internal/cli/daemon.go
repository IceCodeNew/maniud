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

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const gitOpsServicesDirectory = "services"

func executeDaemon(
	ctx context.Context,
	arguments daemonInvocation,
	output io.Writer,
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
	events application.EventSink,
	runtimes runtimeplugin.Set,
) error {
	switch arguments.operation {
	case commandDaemonStart:
		return executeDaemonStart(ctx, arguments, output, environment, stderr, getWorkingDirectory, events, runtimes)
	case commandDaemonStop:
		return executeDaemonStop(ctx, output, environment)
	case commandGen, commandApply, commandTUI, commandGitOpsInit, commandDoctor:
		return errGitOpsRepositoryInvalid
	default:
		return errGitOpsRepositoryInvalid
	}
}

func executeDaemonStart(
	ctx context.Context,
	arguments daemonInvocation,
	output io.Writer,
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
	events application.EventSink,
	runtimes runtimeplugin.Set,
) error {
	if err := ctx.Err(); err != nil {
		return fmt.Errorf("start daemon: %w", err)
	}
	statePath, err := defaultStatePath(environment)
	if err != nil {
		return err
	}
	if _, err = readGitOpsRegistration(gitOpsRegistrationPath(statePath)); err != nil {
		return errGitOpsRepositoryInvalid
	}

	requested, closeSignals := daemonStopRequests(ctx)
	defer closeSignals()
	lease, held, err := acquireDaemonLease(filepath.Dir(statePath))
	if err != nil {
		return err
	}
	if held {
		return errDaemonAlreadyRunning
	}

	reconcile := func() error {
		return reconcileRegisteredRepository(
			ctx, output, environment, stderr, getWorkingDirectory, events, runtimes,
		)
	}
	runErr := pollRegisteredRepositoryUntilStop(ctx, arguments.interval, output, reconcile, events, requested)

	return errors.Join(runErr, lease.Close())
}

func executeDaemonStop(ctx context.Context, output io.Writer, environment map[string]string) error {
	statePath, err := defaultStatePath(environment)
	if err != nil {
		return err
	}
	running, err := requestDaemonStop(ctx, filepath.Dir(statePath))
	if err != nil {
		return err
	}
	if !running {
		return writeDaemonStatus(output, "Daemon is not running.\n")
	}

	return writeDaemonStatus(output, "Daemon stopped.\n")
}

func writeDaemonStatus(output io.Writer, message string) error {
	if _, err := io.WriteString(output, message); err != nil {
		return fmt.Errorf("write daemon status: %w", err)
	}

	return nil
}

func channelClosed(channel <-chan struct{}) bool {
	select {
	case <-channel:
		return true
	default:
		return false
	}
}

func pollRegisteredRepository(
	ctx context.Context,
	interval time.Duration,
	output io.Writer,
	reconcile func() error,
) error {
	return pollRegisteredRepositoryUntilStop(ctx, interval, output, reconcile, nil, nil)
}

func pollRegisteredRepositoryUntilStop(
	ctx context.Context,
	interval time.Duration,
	output io.Writer,
	reconcile func() error,
	events application.EventSink,
	stop <-chan struct{},
) error {
	if interval <= 0 || reconcile == nil {
		return errGitOpsRepositoryInvalid
	}

	for {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("poll registered repository: %w", err)
		}
		if err := reconcile(); err != nil {
			failure := classifyDaemonCommandFailure(err)
			if !failure.Retryable() {
				return err
			}
			publishCLIEvent(events, application.Event{Kind: application.EventDaemonUnavailable})
			emitFailure(output, failure)
		}
		if channelClosed(stop) {
			return nil
		}
		if err := waitDaemonIntervalOrStop(ctx, interval, stop); err != nil {
			return err
		}
	}
}

func publishCLIEvent(events application.EventSink, event application.Event) {
	if events == nil {
		return
	}

	defer func() {
		_ = recover()
	}()
	events.TryPublish(event)
}

func waitDaemonInterval(ctx context.Context, interval time.Duration) error {
	return waitDaemonIntervalOrStop(ctx, interval, nil)
}

func waitDaemonIntervalOrStop(ctx context.Context, interval time.Duration, stop <-chan struct{}) error {
	timer := time.NewTimer(interval)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return fmt.Errorf("wait daemon interval: %w", ctx.Err())
	case <-stop:
		return nil
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
	events application.EventSink,
	runtimes runtimeplugin.Set,
) error {
	statePath, err := defaultStatePath(environment)
	if err != nil {
		return err
	}

	registration, err := readGitOpsRegistration(gitOpsRegistrationPath(statePath))
	if err != nil {
		return errGitOpsRepositoryInvalid
	}

	dependencies, err := defaultApplyDependencies(environment, stderr, getWorkingDirectory, events, runtimes)
	if err != nil {
		return err
	}

	return reconcileRegisteredGitOpsCheckout(
		ctx,
		output,
		registration,
		dependencies,
		fastForwardGitOpsCheckout,
	)
}

func reconcileRegisteredGitOpsCheckout(
	ctx context.Context,
	output io.Writer,
	registration gitOpsRegistration,
	dependencies applyDependencies,
	fastForward func(context.Context, string, string, string) (gitOpsCheckoutSelection, error),
) error {
	root, currentCommit, err := registeredGitOpsCheckout(
		ctx,
		registration.Repository,
		registration.Branch,
		registration.BaselineCommit,
	)
	if err != nil {
		return err
	}
	remote, err := gitRemoteURL(ctx, root, registration.Remote)
	if err != nil {
		return errGitOpsRepositoryInvalid
	}
	scope, err := compose.NewRepositoryScope(root, remote, registration.Branch)
	if err != nil {
		return errGitOpsRepositoryInvalid
	}
	dependencies.repositoryRoot = root
	dependencies.repository = scope
	if err = recoverGitOpsSnapshot(ctx, root, currentCommit, output, dependencies); err != nil {
		return err
	}

	selection, err := fastForward(
		ctx,
		registration.Repository,
		registration.Branch,
		registration.BaselineCommit,
	)
	if err != nil {
		return err
	}
	if selection.awaitingPush {
		return nil
	}

	return reconcileGitOpsSnapshot(ctx, selection.root, selection.commit, output, dependencies)
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
	case errors.Is(err, errGitOpsRecoverySourceBlocked):
		return domain.ApplyFailed(true)
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
