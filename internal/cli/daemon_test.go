package cli

import (
	"bytes"
	"context"
	"encoding/json/v2"
	"errors"
	"fmt"
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

type restartedRecoveryOperations struct {
	*applyOperationsFixture

	durable *application.ApplyFacade
}

func pollRegisteredRepository(
	ctx context.Context,
	interval time.Duration,
	output io.Writer,
	reconcile func() error,
) error {
	return pollRegisteredRepositoryUntilStop(ctx, interval, output, reconcile, nil, nil)
}

func waitDaemonInterval(ctx context.Context, interval time.Duration) error {
	return waitDaemonIntervalOrStop(ctx, interval, nil)
}

func (operations *restartedRecoveryOperations) RepositoryInventory(
	ctx context.Context,
	scope compose.RepositoryScope,
) ([]application.RepositoryTransaction, error) {
	inventory, err := operations.durable.RepositoryInventory(ctx, scope)
	if err != nil {
		return nil, fmt.Errorf("read restarted recovery inventory: %w", err)
	}

	return inventory, nil
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
		commandTUI,
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
	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	registrationPath := gitOpsRegistrationPath(statePath)
	registration, err := readGitOpsRegistration(registrationPath)
	if err != nil {
		t.Fatalf("readGitOpsRegistration() error = %v", err)
	}
	registration.RemoteURL = ""
	if err = os.Remove(registrationPath); err != nil {
		t.Fatalf("Remove(registration) error = %v", err)
	}
	if err = writeGitOpsRegistration(registrationPath, registration); err != nil {
		t.Fatalf("write unbound registration error = %v", err)
	}

	timed, cancel := context.WithTimeout(t.Context(), 5*time.Second)
	defer cancel()
	err = executeDaemon(
		timed, daemonInvocation{operation: commandDaemonStart, interval: time.Hour}, io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("executeDaemon(poll wait) error = %v", err)
	}
	registration, err = readGitOpsRegistration(registrationPath)
	if err != nil || registration.RemoteURL != root {
		t.Fatalf("bound registration = %#v, %v", registration, err)
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
	output := new(bytes.Buffer)
	if err := reconcileRegisteredRepository(
		t.Context(), output, environment, io.Discard,
		func() (string, error) { return root, nil },
		nil,
		runtimes,
	); err != nil {
		t.Fatalf("reconcileRegisteredRepository(invalid source) error = %v", err)
	}
	summary := decodeGitOpsCycleSummary(t, output)
	if summary.Status != gitOpsCyclePartial || summary.Skipped != 2 ||
		len(summary.SkippedSources) != 2 {
		t.Fatalf("invalid-source cycle summary = %#v", summary)
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

//nolint:cyclop,funlen,gocognit // One table keeps the no-fetch matrix and its shared spy assertions together.
func TestReconcileRegisteredGitOpsCheckoutDoesNotFetchBeforeRecoverySettles(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		entry         string
		inventoryErr  error
		invalidSource bool
		sourceDrift   bool
		newPlan       bool
		checkoutDrift bool
		applyErr      error
		wantErr       error
		wantStatus    string
		wantLoads     bool
		wantMutations int
	}{
		{
			name: "inventory read failure", inventoryErr: errApplyTest,
			wantErr: errApplyTest, wantStatus: gitOpsCycleFailed,
		},
		{
			name: "inventory overflow", inventoryErr: application.ErrRepositoryInventoryOverflow,
			wantErr: application.ErrRepositoryInventoryOverflow, wantStatus: gitOpsCycleFailed,
		},
		{
			name: "missing associated source", entry: "services/missing.yaml",
			wantErr: errGitOpsRecoverySourceBlocked, wantStatus: errGitOpsRecoverySourceBlocked.Error(),
			wantLoads: true,
		},
		{
			name: "invalid associated source", invalidSource: true,
			wantErr: errGitOpsRecoverySourceBlocked, wantStatus: errGitOpsRecoverySourceBlocked.Error(),
			wantLoads: true,
		},
		{
			name: "associated source digest drift", sourceDrift: true,
			wantErr: errGitOpsRecoverySourceBlocked, wantStatus: errGitOpsRecoverySourceBlocked.Error(),
			wantLoads: true,
		},
		{
			name: "transaction is not a recovery", newPlan: true,
			wantErr: errGitOpsRecoverySourceBlocked, wantStatus: errGitOpsRecoverySourceBlocked.Error(),
			wantLoads: true,
		},
		{
			name: "checkout drifts before recovery mutation", checkoutDrift: true,
			wantErr: errGitOpsRepositoryInvalid, wantStatus: gitOpsCycleFailed, wantLoads: true,
		},
		{
			name: "recovery mutation failure", applyErr: errApplyTest,
			wantErr: errApplyTest, wantStatus: gitOpsCycleFailed, wantLoads: true, wantMutations: 1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
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
			entry := test.entry
			if entry == "" {
				entry = tuiTestServicePath
			}
			path := filepath.Join(root, filepath.FromSlash(entry))
			source := repositoryRecoverySource(t, root, path)
			location, err := scope.Location(source.Repository.Entry)
			if err != nil {
				t.Fatalf("RepositoryScope.Location() error = %v", err)
			}
			digest := source.Repository.Digest
			if test.sourceDrift {
				digest = domain.Hash([]byte("superseded source"))
			}

			events := make([]string, 0, 8)
			operations := &applyOperationsFixture{
				events:       &events,
				dryRunPlan:   application.Plan{Kind: application.PlanResume},
				applyPlan:    application.Plan{Kind: application.PlanResume},
				applyErr:     test.applyErr,
				inventoryErr: test.inventoryErr,
				inventory: []application.RepositoryTransaction{{
					Source: digest, Location: location,
				}},
			}
			if test.newPlan {
				operations.dryRunPlan.Kind = application.PlanBootstrap
			}
			dependencies := operationApplyDependencies(t, &events, operations)
			loads := 0
			drifted := false
			dependencies.loadSource = func(_ context.Context, requested string) (compose.Source, error) {
				loads++
				if test.checkoutDrift && !drifted {
					drifted = true
					if writeErr := os.WriteFile(
						filepath.Join(root, "concurrent-change"), []byte("changed\n"), 0o600,
					); writeErr != nil {
						return compose.Source{}, fmt.Errorf("write concurrent checkout drift: %w", writeErr)
					}
				}
				if test.invalidSource && requested == path {
					return compose.Source{}, compose.ErrInvalidSource
				}

				return repositoryRecoverySource(t, root, requested), nil
			}
			registration := testGitOpsRegistration(t, root, state.head)
			fetched := false
			output := new(bytes.Buffer)
			err = reconcileRegisteredGitOpsCheckout(
				t.Context(), output, registration, dependencies,
				func(context.Context, gitOpsRegistration) (gitOpsCheckoutSelection, error) {
					fetched = true

					return gitOpsCheckoutSelection{}, nil
				},
				compose.NewRepositoryScope,
			)
			mutations := countEvent(events, string(commandApply))
			if !errors.Is(err, test.wantErr) || fetched || mutations != test.wantMutations ||
				(loads > 0) != test.wantLoads {
				t.Fatalf(
					"reconcileRegisteredGitOpsCheckout() error = %v, fetched = %t, loads = %d, mutations = %d",
					err,
					fetched,
					loads,
					mutations,
				)
			}
			summary := decodeGitOpsCycleSummary(t, output)
			if summary.Status != test.wantStatus || summary.Failed != 1 {
				t.Fatalf("recovery failure cycle summary = %#v", summary)
			}
		})
	}
}

//nolint:cyclop,funlen // The restart fixture verifies each durable resource boundary before orchestration.
func TestReconcileRegisteredGitOpsCheckoutRecoversDurableStateBeforeFetch(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	checkout, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	scope, err := compose.NewRepositoryScope(root, root, gitOpsTestBranch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(tuiTestServicePath))
	source := repositoryRecoverySource(t, root, path)
	location, err := scope.Location(source.Repository.Entry)
	if err != nil {
		t.Fatalf("RepositoryScope.Location() error = %v", err)
	}

	stateRoot, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve state directory: %v", err)
	}
	if err = os.Chmod(stateRoot, 0o700); err != nil { //nolint:gosec // SQLite state needs a private traversable directory.
		t.Fatalf("secure state directory: %v", err)
	}
	statePath := filepath.Join(stateRoot, stateDatabaseName)
	durableState, err := store.Open(t.Context(), statePath)
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}
	lock, err := durableState.TryLockService(testProjectName, applyServiceValue)
	if err != nil {
		_ = durableState.Close()
		t.Fatalf("TryLockService() error = %v", err)
	}
	_, err = lock.BeginTransaction(t.Context(), store.TransactionIntent{
		Kind:                  store.TransactionBootstrap,
		Runtime:               domain.RuntimeDocker,
		SourceDigest:          source.Repository.Digest,
		EffectiveDigest:       domain.Hash([]byte("effective source")),
		ExecutionDigest:       domain.Hash([]byte("execution context")),
		RepositoryVersion:     scope.Version,
		RepositoryScopeDigest: scope.Digest, RepositoryLocationDigest: location,
		HasRepository: true,
	})
	if err != nil {
		_ = lock.Close()
		_ = durableState.Close()
		t.Fatalf("BeginTransaction() error = %v", err)
	}
	if err = errors.Join(lock.Close(), durableState.Close()); err != nil {
		t.Fatalf("close pre-restart state: %v", err)
	}

	events := make([]string, 0, 4)
	operations := &restartedRecoveryOperations{
		applyOperationsFixture: &applyOperationsFixture{
			events:     &events,
			dryRunPlan: application.Plan{Kind: application.PlanResume},
			applyPlan:  application.Plan{Kind: application.PlanResume},
		},
		durable: application.NewApplyFacade(
			nil, nil, nil,
			func(ctx context.Context) (application.OperationReader, error) {
				return store.OpenReader(ctx, statePath)
			},
			nil, nil,
		),
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	dependencies.loadSource = func(_ context.Context, requested string) (compose.Source, error) {
		return repositoryRecoverySource(t, root, requested), nil
	}
	registration := testGitOpsRegistration(t, root, checkout.head)
	output := new(bytes.Buffer)
	err = reconcileRegisteredGitOpsCheckout(
		t.Context(), output, registration, dependencies,
		func(context.Context, gitOpsRegistration) (gitOpsCheckoutSelection, error) {
			events = append(events, "fetch")

			return gitOpsCheckoutSelection{
				root: root, commit: checkout.head, awaitingPush: true,
			}, nil
		},
		compose.NewRepositoryScope,
	)
	if err != nil || len(events) != 3 || events[0] != dryRunEvent ||
		events[1] != string(commandApply) || events[2] != "fetch" {
		t.Fatalf("restart recovery error = %v, events = %q", err, events)
	}
	lines := bytes.Split(bytes.TrimSpace(output.Bytes()), []byte{'\n'})
	var summary gitOpsCycleSummary
	if err = json.Unmarshal(lines[len(lines)-1], &summary); err != nil {
		t.Fatalf("decode post-restart cycle summary: %v; output = %q", err, output.String())
	}
	if summary.Status != gitOpsCycleAwaitingPush || summary.Applied != 1 || summary.Failed != 0 {
		t.Fatalf("post-restart cycle summary = %#v", summary)
	}
}

func TestReconcileRegisteredGitOpsCheckoutDoesNotMutateLocalAheadCommit(t *testing.T) {
	t.Parallel()

	checkout, _, registered := initFastForwardGitOpsTestRepositories(t)
	writeGitOpsTestCommit(t, checkout, tuiTestServicePath, "services: {}\n", "local api")
	events := make([]string, 0, 4)
	operations := &applyOperationsFixture{events: &events}
	dependencies := operationApplyDependencies(t, &events, operations)
	registration := testGitOpsRegistration(t, checkout, registered)

	output := new(bytes.Buffer)
	err := reconcileRegisteredGitOpsCheckout(
		t.Context(), output, registration, dependencies, fastForwardGitOpsCheckout, compose.NewRepositoryScope,
	)
	if err != nil || countEvent(events, dryRunEvent) != 0 ||
		countEvent(events, string(commandApply)) != 0 {
		t.Fatalf("reconcileRegisteredGitOpsCheckout() error = %v, events = %q", err, events)
	}
	summary := decodeGitOpsCycleSummary(t, output)
	if summary.Status != gitOpsCycleAwaitingPush || summary.Commit == "" ||
		summary.Applied != 0 || summary.Failed != 0 {
		t.Fatalf("awaiting-push cycle summary = %#v", summary)
	}
}

func TestReconcileRegisteredGitOpsCheckoutContainsRepositoryIdentityFailures(t *testing.T) {
	t.Parallel()

	checkout, _, registered := initFastForwardGitOpsTestRepositories(t)
	registration := testGitOpsRegistration(t, checkout, registered)
	registration.Remote = testMissingName
	if err := reconcileRegisteredGitOpsCheckout(
		t.Context(), io.Discard, registration, applyDependencies{}, fastForwardGitOpsCheckout,
		compose.NewRepositoryScope,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reconcileRegisteredGitOpsCheckout(missing remote) error = %v", err)
	}
	registration.Remote = gitOpsRemoteName
	if err := reconcileRegisteredGitOpsCheckout(
		t.Context(), io.Discard, registration, applyDependencies{}, fastForwardGitOpsCheckout,
		func(string, string, string) (compose.RepositoryScope, error) {
			return compose.RepositoryScope{}, compose.ErrInvalidSource
		},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reconcileRegisteredGitOpsCheckout(scope failure) error = %v", err)
	}

	events := make([]string, 0, 2)
	operations := &applyOperationsFixture{events: &events}
	output := new(bytes.Buffer)
	if err := reconcileRegisteredGitOpsCheckout(
		t.Context(), output, registration, operationApplyDependencies(t, &events, operations),
		func(context.Context, gitOpsRegistration) (gitOpsCheckoutSelection, error) {
			return gitOpsCheckoutSelection{}, errClosedOutput
		},
		compose.NewRepositoryScope,
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("reconcileRegisteredGitOpsCheckout(fetch failure) error = %v", err)
	}
	summary := decodeGitOpsCycleSummary(t, output)
	if summary.Status != gitOpsCycleFailed || summary.Failed != 1 {
		t.Fatalf("fetch-failure cycle summary = %#v", summary)
	}
}

func decodeGitOpsCycleSummary(t *testing.T, output *bytes.Buffer) gitOpsCycleSummary {
	t.Helper()

	var summary gitOpsCycleSummary
	if err := json.Unmarshal(output.Bytes(), &summary); err != nil {
		t.Fatalf("decode GitOps cycle summary: %v; output = %q", err, output.String())
	}

	return summary
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
		{err: errGitOpsRecoverySourceBlocked, code: domain.ErrorApplyFailed},
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
