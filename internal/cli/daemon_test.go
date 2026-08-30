package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type cliEventSinkFunc func(application.Event) bool

func (publish cliEventSinkFunc) TryPublish(event application.Event) bool {
	return publish(event)
}

func TestExecuteDaemonStopsPollingWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	runtimes := testRuntimePlugins(t)

	err := executeDaemon(
		ctx,
		daemonInvocation{operation: commandDaemonStart, interval: time.Minute},
		new(bytes.Buffer),
		map[string]string{homeKey: t.TempDir()},
		new(bytes.Buffer),
		func() (string, error) { return t.TempDir(), nil },
		nil,
		runtimes,
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeDaemon(cancelled poll) = %v", err)
	}
}

func TestExecuteDaemonRequiresRegistration(t *testing.T) {
	t.Parallel()
	runtimes := testRuntimePlugins(t)

	err := executeDaemon(
		t.Context(),
		daemonInvocation{operation: commandDaemonStart, interval: time.Minute},
		io.Discard,
		map[string]string{homeKey: t.TempDir()},
		io.Discard,
		func() (string, error) { return t.TempDir(), nil },
		nil,
		runtimes,
	)
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("executeDaemon(unregistered) = %v", err)
	}
}

func TestExecuteDaemonRejectsInitializedRepositoryWithoutRemote(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	root := filepath.Join(t.TempDir(), "desired-state")
	environment := map[string]string{homeKey: home}
	if err := executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); err != nil {
		t.Fatalf("executeGitOpsInit() error = %v", err)
	}
	runtimes := testRuntimePlugins(t)
	err := executeDaemon(
		t.Context(),
		daemonInvocation{operation: commandDaemonStart, interval: time.Minute},
		io.Discard,
		environment,
		io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	)
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("executeDaemon(without remote) = %v", err)
	}
}

//nolint:funlen,paralleltest // This lifecycle test owns its process-wide daemon receiver.
func TestDaemonStartAndStopLifecycle(t *testing.T) {
	root := initGitOpsTestRepository(t)
	environment := registerGitOpsTestRepository(t, root)
	runtimes := testRuntimePlugins(t)
	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	directory := filepath.Dir(statePath)
	startResult := make(chan error, 1)
	go func() {
		startResult <- executeDaemon(
			t.Context(),
			daemonInvocation{operation: commandDaemonStart, interval: time.Hour},
			io.Discard,
			environment,
			io.Discard,
			func() (string, error) { return root, nil },
			nil,
			runtimes,
		)
	}()
	waitForDaemonLease(t, directory)

	duplicate := executeDaemon(
		t.Context(),
		daemonInvocation{operation: commandDaemonStart, interval: time.Hour},
		io.Discard,
		environment,
		io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	)
	if !errors.Is(duplicate, errDaemonAlreadyRunning) {
		t.Fatalf("executeDaemon(duplicate start) = %v", duplicate)
	}

	output := new(bytes.Buffer)
	if err = executeDaemon(
		t.Context(),
		daemonInvocation{operation: commandDaemonStop},
		output,
		environment,
		io.Discard,
		os.Getwd,
		nil,
		runtimes,
	); err != nil {
		t.Fatalf("executeDaemon(stop) error = %v", err)
	}
	if output.String() != "Daemon stopped.\n" {
		t.Fatalf("executeDaemon(stop) output = %q", output.String())
	}
	if err = <-startResult; err != nil {
		t.Fatalf("executeDaemon(start) error = %v", err)
	}

	output.Reset()
	if err = executeDaemon(
		t.Context(), daemonInvocation{operation: commandDaemonStop}, output, environment, io.Discard, os.Getwd, nil,
		runtimes,
	); err != nil {
		t.Fatalf("executeDaemon(idempotent stop) error = %v", err)
	}
	if output.String() != "Daemon is not running.\n" {
		t.Fatalf("executeDaemon(idempotent stop) output = %q", output.String())
	}
}

func waitForDaemonLease(t *testing.T, directory string) {
	t.Helper()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, running, err := daemonLeaseOwner(directory)
		if err == nil && running {
			return
		}
		time.Sleep(time.Millisecond)
	}
	t.Fatal("daemon did not acquire its lease")
}

func TestDaemonControlRejectsInvalidProcessID(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	if err := os.WriteFile(filepath.Join(directory, daemonLockName), []byte("invalid\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(lock) error = %v", err)
	}
	lease, held, err := acquireDaemonLease(directory)
	if err != nil || held || lease == nil {
		t.Fatalf("acquireDaemonLease() = %#v, %t, %v", lease, held, err)
	}
	if err = lease.lock.Truncate(0); err != nil {
		t.Fatalf("Truncate(lock) error = %v", err)
	}
	if _, err = lease.lock.WriteAt([]byte("invalid\n"), 0); err != nil {
		t.Fatalf("WriteAt(lock) error = %v", err)
	}
	if _, running, ownerErr := daemonLeaseOwner(directory); !running ||
		!errors.Is(ownerErr, errDaemonControlUnavailable) {
		t.Fatalf("daemonLeaseOwner(invalid PID) = %t, %v", running, ownerErr)
	}
	if err = lease.Close(); err != nil {
		t.Fatalf("daemonLease.Close() error = %v", err)
	}
}

func TestDaemonControlRejectsInvalidLock(t *testing.T) {
	t.Parallel()

	invalidLock := t.TempDir()
	if err := os.Mkdir(filepath.Join(invalidLock, daemonLockName), 0o700); err != nil {
		t.Fatalf("Mkdir(lock) error = %v", err)
	}
	if _, _, err := acquireDaemonLease(invalidLock); !errors.Is(err, errDaemonControlUnavailable) {
		t.Fatalf("acquireDaemonLease(directory lock) = %v", err)
	}
}

//nolint:paralleltest // This test installs and removes a process-wide daemon signal receiver.
func TestExecuteDaemonContainsControlFailures(t *testing.T) {
	root := initGitOpsTestRepository(t)
	environment := registerGitOpsTestRepository(t, root)
	runtimes := testRuntimePlugins(t)
	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	lockPath := filepath.Join(filepath.Dir(statePath), daemonLockName)
	if err = os.Mkdir(lockPath, 0o700); err != nil {
		t.Fatalf("Mkdir(lock) error = %v", err)
	}
	if err = executeDaemon(
		t.Context(),
		daemonInvocation{operation: commandDaemonStart, interval: time.Hour},
		io.Discard,
		environment,
		io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	); !errors.Is(err, errDaemonControlUnavailable) {
		t.Fatalf("executeDaemon(start invalid control) = %v", err)
	}
	if err = executeDaemon(
		t.Context(), daemonInvocation{operation: commandDaemonStop}, io.Discard, environment, io.Discard, os.Getwd, nil,
		runtimes,
	); !errors.Is(err, errDaemonControlUnavailable) {
		t.Fatalf("executeDaemon(stop invalid control) = %v", err)
	}
}

func TestExecuteDaemonRejectsUnknownOperation(t *testing.T) {
	t.Parallel()
	runtimes := testRuntimePlugins(t)

	for _, operation := range []command{
		"",
		commandGen,
		commandApply,
		commandGitOpsInit,
		commandDoctor,
	} {
		err := executeDaemon(
			t.Context(), daemonInvocation{operation: operation}, io.Discard, nil, io.Discard, os.Getwd, nil,
			runtimes,
		)
		if !errors.Is(err, errGitOpsRepositoryInvalid) {
			t.Fatalf("executeDaemon(%q) = %v", operation, err)
		}
	}
}

func TestWriteDaemonStatusContainsOutputFailure(t *testing.T) {
	t.Parallel()

	if err := writeDaemonStatus(failingWriterWithError{err: errClosedOutput}, "status"); !errors.Is(err, errClosedOutput) {
		t.Fatalf("writeDaemonStatus() error = %v", err)
	}
}

//nolint:paralleltest // Parallel Git-heavy tests can exhaust the polling deadline on macOS race runs.
func TestDaemonReconcilesRegisteredRepositoryImmediatelyAndThenPolls(t *testing.T) {
	home := t.TempDir()
	root := initGitOpsTestRepository(t)
	environment := map[string]string{homeKey: home}
	runtimes := testRuntimePlugins(t)
	if err := executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); err != nil {
		t.Fatalf("executeGitOpsInit() error = %v", err)
	}

	timed, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err := executeDaemon(
		timed, daemonInvocation{operation: commandDaemonStart, interval: time.Hour}, io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("executeDaemon(poll wait) error = %v", err)
	}

	rapid, stopRapid := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer stopRapid()
	if err = executeDaemon(
		rapid,
		daemonInvocation{operation: commandDaemonStart, interval: time.Nanosecond},
		io.Discard,
		environment,
		io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	); err == nil {
		t.Fatal("executeDaemon(rapid poll) returned without cancellation")
	}
}

func TestReconcileRegisteredRepositoryRejectsStateAndCheckoutFailures(t *testing.T) {
	t.Parallel()
	runtimes := testRuntimePlugins(t)

	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, nil, io.Discard, os.Getwd, nil,
		runtimes,
	); !errors.Is(err, errStateHomeUnavailable) {
		t.Fatalf("reconcileRegisteredRepository(missing state home) = %v", err)
	}
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, map[string]string{homeKey: t.TempDir()}, io.Discard, os.Getwd, nil,
		runtimes,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reconcileRegisteredRepository(missing registration) = %v", err)
	}

	root := initGitOpsTestRepository(t)
	environment := registerGitOpsTestRepository(t, root)
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return "", errClosedOutput },
		nil,
		runtimes,
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("reconcileRegisteredRepository(dependencies) = %v", err)
	}

	root = initGitOpsTestRepository(t)
	environment = registerGitOpsTestRepository(t, root)
	if err := os.WriteFile(filepath.Join(root, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(dirty) error = %v", err)
	}
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reconcileRegisteredRepository(dirty) = %v", err)
	}
}

func TestReconcileRegisteredRepositoryContinuesPastInvalidSourceAndRejectsFetchFailure(t *testing.T) {
	t.Parallel()
	runtimes := testRuntimePlugins(t)

	root := initGitOpsSnapshotTestRepository(t)
	environment := registerGitOpsTestRepository(t, root)
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	); err != nil {
		t.Fatalf("reconcileRegisteredRepository(invalid source) error = %v", err)
	}

	root = initGitOpsTestRepository(t)
	environment = registerGitOpsTestRepository(t, root)
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	if _, err := runGit(t.Context(), root, "remote", "set-url", gitOpsRemoteName, missingRemote); err != nil {
		t.Fatalf("git remote set-url error = %v", err)
	}
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reconcileRegisteredRepository(fetch failure) = %v", err)
	}
}

func TestReconcileRegisteredGitOpsCheckoutDoesNotFetchBlockedRecoverySource(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	scope, err := compose.NewRepositoryScope(root, root, gitOpsTestBranch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	path := filepath.Join(root, gitOpsServicesDirectory, "api.yaml")
	source := repositoryRecoverySource(t, root, path)
	location, err := scope.Location(source.Repository.Entry)
	if err != nil {
		t.Fatalf("RepositoryScope.Location() error = %v", err)
	}
	events := make([]string, 0, 4)
	operations := &applyOperationsFixture{
		events: &events,
		inventory: []application.RepositoryTransaction{{
			Source: source.Repository.Digest, Location: location,
		}},
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	dependencies.loadSource = func(_ context.Context, requested string) (compose.Source, error) {
		if requested == path {
			return compose.Source{}, compose.ErrInvalidSource
		}

		return repositoryRecoverySource(t, root, requested), nil
	}
	registration := gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: root, Branch: gitOpsTestBranch,
		Remote: gitOpsRemoteName, BaselineCommit: state.head,
	}
	fetched := false
	err = reconcileRegisteredGitOpsCheckout(
		t.Context(),
		io.Discard,
		registration,
		dependencies,
		func(context.Context, string, string, string) (gitOpsCheckoutSelection, error) {
			fetched = true

			return gitOpsCheckoutSelection{}, nil
		},
	)
	if !errors.Is(err, errGitOpsRecoverySourceBlocked) || fetched {
		t.Fatalf("reconcileRegisteredGitOpsCheckout() error = %v, fetched = %t", err, fetched)
	}
}

func TestReconcileRegisteredGitOpsCheckoutDoesNotMutateLocalAheadCommit(t *testing.T) {
	t.Parallel()

	checkout, _, registered := initFastForwardGitOpsTestRepositories(t)
	writeGitOpsTestCommit(t, checkout, tuiTestServicePath, "services: {}\n", "local api")
	events := make([]string, 0, 4)
	operations := &applyOperationsFixture{events: &events}
	dependencies := operationApplyDependencies(t, &events, operations)
	registration := gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: checkout, Branch: gitOpsTestBranch,
		Remote: gitOpsRemoteName, BaselineCommit: registered,
	}

	err := reconcileRegisteredGitOpsCheckout(
		t.Context(), io.Discard, registration, dependencies, fastForwardGitOpsCheckout,
	)
	if err != nil || countEvent(events, dryRunEvent) != 0 ||
		countEvent(events, string(commandApply)) != 0 {
		t.Fatalf("reconcileRegisteredGitOpsCheckout() error = %v, events = %q", err, events)
	}
}

func registerGitOpsTestRepository(t *testing.T, root string) map[string]string {
	t.Helper()

	environment := map[string]string{homeKey: t.TempDir()}
	if err := executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); err != nil {
		t.Fatalf("executeGitOpsInit() error = %v", err)
	}

	return environment
}

func TestDaemonPollingBoundaries(t *testing.T) {
	t.Parallel()

	if err := pollRegisteredRepository(
		t.Context(), 0, io.Discard, func() error { return nil },
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("pollRegisteredRepository(zero interval) = %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := pollRegisteredRepository(
		cancelled, time.Second, io.Discard, func() error { return nil },
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("pollRegisteredRepository(cancelled) = %v", err)
	}
	if err := pollRegisteredRepository(
		t.Context(), time.Second, io.Discard, func() error { return errGitOpsRepositoryInvalid },
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("pollRegisteredRepository(unregistered) = %v", err)
	}

	retryContext, stopRetry := context.WithCancel(t.Context())
	output := new(bytes.Buffer)
	attempts := 0
	err := pollRegisteredRepository(retryContext, time.Nanosecond, output, func() error {
		attempts++
		if attempts == 1 {
			return store.ErrUnavailable
		}
		stopRetry()

		return nil
	})
	if !errors.Is(err, context.Canceled) || attempts != 2 || output.String() != retryableApplyFailureJSON {
		t.Fatalf("pollRegisteredRepository(retry) = %v, attempts = %d, output = %q", err, attempts, output)
	}

	if err := waitDaemonInterval(t.Context(), time.Nanosecond); err != nil {
		t.Fatalf("waitDaemonInterval(elapsed) = %v", err)
	}
	if err := waitDaemonInterval(cancelled, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitDaemonInterval(cancelled) = %v", err)
	}
}

func TestDaemonNotificationObserversDoNotChangeRetry(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		panic bool
	}{
		{name: "drop"},
		{name: "panic", panic: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			ctx, cancel := context.WithCancel(t.Context())
			var observed []application.Event
			sink := cliEventSinkFunc(func(event application.Event) bool {
				observed = append(observed, event)
				if test.panic {
					panic("notification observer failure")
				}

				return false
			})
			attempts := 0
			var output bytes.Buffer
			err := pollRegisteredRepositoryUntilStop(ctx, time.Nanosecond, &output, func() error {
				attempts++
				if attempts == 1 {
					return store.ErrUnavailable
				}
				cancel()

				return nil
			}, sink, nil)
			if !errors.Is(err, context.Canceled) || attempts != 2 ||
				output.String() != retryableApplyFailureJSON ||
				len(observed) != 1 || observed[0] != (application.Event{Kind: application.EventDaemonUnavailable}) {
				t.Fatalf(
					"poll with %s observer = %v, attempts %d, output %q, events %#v",
					test.name, err, attempts, output.String(), observed,
				)
			}
		})
	}
}

func TestWaitDaemonIntervalStopsOnRequest(t *testing.T) {
	t.Parallel()

	stop := make(chan struct{})
	close(stop)
	if err := waitDaemonIntervalOrStop(t.Context(), time.Second, stop); err != nil {
		t.Fatalf("waitDaemonIntervalOrStop(stopped) = %v", err)
	}
}

func TestListGitOpsServiceFilesIgnoresDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, gitOpsServicesDirectory, "nested"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, gitOpsServicesDirectory, "api.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, gitOpsServicesDirectory, "ignore.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := listGitOpsServiceFiles(root)
	if err != nil || len(got) != 1 || filepath.Base(got[0]) != "api.yaml" {
		t.Fatalf("listGitOpsServiceFiles() = %#v, %v", got, err)
	}
}

func TestListGitOpsServiceFilesHandlesMissingAndInvalidDirectories(t *testing.T) {
	t.Parallel()

	missing, err := listGitOpsServiceFiles(t.TempDir())
	if err != nil || missing != nil {
		t.Fatalf("listGitOpsServiceFiles(missing) = %#v, %v", missing, err)
	}

	blocked := t.TempDir()
	if err = os.WriteFile(filepath.Join(blocked, gitOpsServicesDirectory), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(services) error = %v", err)
	}
	if _, err = listGitOpsServiceFiles(blocked); err == nil {
		t.Fatal("listGitOpsServiceFiles(file) succeeded")
	}
}

func TestClassifyDaemonCommandFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		code domain.ErrorCode
	}{
		{err: context.Canceled, code: domain.ErrorOperationCancelled},
		{err: errGitOpsRegistrationExists, code: domain.ErrorApplyFailed},
		{err: errGitOpsRepositoryInvalid, code: domain.ErrorInvalidInput},
		{err: compose.ErrInvalidSource, code: domain.ErrorInvalidInput},
		{err: errStateHomeUnavailable, code: domain.ErrorInvalidInput},
		{err: errStateHomeInvalid, code: domain.ErrorInvalidInput},
		{err: errApplyTest, code: domain.ErrorApplyFailed},
	}
	if classifyDaemonCommandFailure(nil) != nil {
		t.Fatal("classifyDaemonCommandFailure(nil) returned a failure")
	}
	for _, test := range tests {
		got := classifyDaemonCommandFailure(test.err)
		if got == nil || got.Code() != test.code {
			t.Fatalf("classifyDaemonCommandFailure(%v) = %#v", test.err, got)
		}
	}
}

func TestRunDaemonRequiresExecutor(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	status := runDaemon(
		invocation{arguments: daemonInvocation{operation: commandDaemonStart}, debug: false}, output, nil, nil,
	)
	if status != 1 || output.String() != internalErrorJSON {
		t.Fatalf("runDaemon() = %d, %q", status, output.String())
	}

	output.Reset()
	status = runDaemon(invocation{
		arguments: daemonInvocation{operation: commandDaemonStop},
	}, output, nil, func(daemonInvocation) error {
		return nil
	})
	if status != 0 || output.Len() != 0 {
		t.Fatalf("runDaemon(success) = %d, %q", status, output.String())
	}
}
