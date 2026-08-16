package application

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestBindMutationStartsOnlyRequiredTransactions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name            string
		observe         func(domain.DesiredWorkload) WorkloadObservation
		want            PlanKind
		wantTransaction bool
	}{
		{name: "bootstrap", observe: fixedObservation(missingObservation()), want: PlanBootstrap, wantTransaction: true},
		{
			name:            "adopt",
			observe:         fixedObservation(presentObservation(true, true, domain.OwnershipUnmanaged)),
			want:            PlanAdopt,
			wantTransaction: true,
		},
		{name: "unchanged", observe: matchingManagedObservation, want: PlanUnchanged, wantTransaction: false},
		{name: "upgrade", observe: changedManagedObservation, want: PlanUpgrade, wantTransaction: true},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operation := newTestOperation(t)
			observationCalls := 0
			operation.runtime.observe = func(
				_ context.Context,
				workload domain.DesiredWorkload,
			) (WorkloadObservation, error) {
				observationCalls++

				return test.observe(workload), nil
			}

			state := openMutationTestStore(t)
			defer closeMutationTestStore(t, state)

			mutation, err := operation.service.bindMutation(context.Background(), operation.request, state)
			if err != nil {
				t.Fatalf("bindMutation() error = %v", err)
			}
			defer closeBoundMutation(t, mutation)

			if observationCalls != 2 || mutation.preparation.Plan.Kind != test.want ||
				mutation.preparation.HasTransaction != test.wantTransaction {
				t.Fatalf("bound mutation = %#v, observations %d", mutation.preparation, observationCalls)
			}

			assertBoundTransaction(t, state, mutation.preparation, test.wantTransaction)
			assertMutationLockHeld(t, state, mutation.preparation)
		})
	}
}

func TestBindMutationUsesLockedRepreparation(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	observationCalls := 0
	operation.runtime.observe = func(
		_ context.Context,
		_ domain.DesiredWorkload,
	) (WorkloadObservation, error) {
		observationCalls++
		if observationCalls == 1 {
			return missingObservation(), nil
		}

		return presentObservation(true, true, domain.OwnershipUnmanaged), nil
	}

	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	mutation, err := operation.service.bindMutation(context.Background(), operation.request, state)
	if err != nil {
		t.Fatalf("bindMutation() error = %v", err)
	}
	defer closeBoundMutation(t, mutation)

	if mutation.preparation.Plan.Kind != PlanAdopt || !mutation.preparation.HasTransaction {
		t.Fatalf("locked preparation = %#v", mutation.preparation)
	}
}

func TestBindMutationResumesExactTransaction(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	baseline, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	lock, err := state.TryLockService(baseline.Plan.Project, baseline.Plan.Service)
	if err != nil {
		t.Fatalf("TryLockService() error = %v", err)
	}

	transaction, err := lock.BeginTransaction(context.Background(), transactionIntent(baseline))
	if err != nil {
		t.Fatalf("BeginTransaction() error = %v", err)
	}

	err = lock.Close()
	if err != nil {
		t.Fatalf("ServiceLock.Close() error = %v", err)
	}

	mutation, err := operation.service.bindMutation(context.Background(), operation.request, state)
	if err != nil {
		t.Fatalf("bindMutation() error = %v", err)
	}
	defer closeBoundMutation(t, mutation)

	if mutation.preparation.Plan.Kind != PlanResume ||
		!mutation.preparation.HasTransaction || mutation.preparation.Transaction != transaction {
		t.Fatalf("resumed mutation = %#v", mutation.preparation)
	}
}

func TestBindMutationRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	invalidServices := []*Service{
		nil,
		{},
		NewService(nil, operation.runtime, operation.transactions),
		NewService(operation.service.images, nil, operation.transactions),
	}
	for index, service := range invalidServices {
		mutation, err := service.bindMutation(context.Background(), operation.request, state)
		if !errors.Is(err, ErrInvalidRequest) || mutation != nil {
			t.Fatalf("bindMutation(invalid service %d) = %#v, %v", index, mutation, err)
		}
	}

	mutation, err := operation.service.bindMutation(context.Background(), operation.request, nil)
	if !errors.Is(err, ErrInvalidRequest) || mutation != nil {
		t.Fatalf("bindMutation(nil state) = %#v, %v", mutation, err)
	}
}

func TestBindMutationContainsPreparationAndLockFailures(t *testing.T) {
	t.Parallel()

	t.Run("initial preparation", func(t *testing.T) {
		t.Parallel()

		operation := newTestOperation(t)
		operation.runtime.inspect = func(context.Context) (RuntimeEvidence, error) {
			return RuntimeEvidence{}, errTestBoundary
		}

		state := openMutationTestStore(t)
		defer closeMutationTestStore(t, state)

		mutation, err := operation.service.bindMutation(context.Background(), operation.request, state)
		if !errors.Is(err, errTestBoundary) || mutation != nil {
			t.Fatalf("bindMutation(initial failure) = %#v, %v", mutation, err)
		}
	})

	t.Run("cancelled after preflight", func(t *testing.T) {
		t.Parallel()

		operation := newTestOperation(t)
		ctx, cancel := context.WithCancel(context.Background())
		operation.runtime.observe = func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error) {
			cancel()

			return missingObservation(), nil
		}

		state := openMutationTestStore(t)
		defer closeMutationTestStore(t, state)

		mutation, err := operation.service.bindMutation(ctx, operation.request, state)
		if !errors.Is(err, context.Canceled) || mutation != nil {
			t.Fatalf("bindMutation(cancelled) = %#v, %v", mutation, err)
		}
	})

	t.Run("contended service", func(t *testing.T) {
		t.Parallel()

		operation := newTestOperation(t)

		state := openMutationTestStore(t)
		defer closeMutationTestStore(t, state)

		owner, err := state.TryLockService(testProjectName, testServiceName)
		if err != nil {
			t.Fatalf("TryLockService() error = %v", err)
		}
		defer closeMutationTestLock(t, owner)

		mutation, err := operation.service.bindMutation(context.Background(), operation.request, state)
		if !errors.Is(err, store.ErrUnavailable) || mutation != nil {
			t.Fatalf("bindMutation(contended) = %#v, %v", mutation, err)
		}
	})
}

func TestBindMutationReleasesLockAfterFinalEvidenceFailure(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	observationCalls := 0
	operation.runtime.observe = func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error) {
		observationCalls++
		if observationCalls == 2 {
			return emptyObservation(), errTestBoundary
		}

		return missingObservation(), nil
	}

	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	mutation, err := operation.service.bindMutation(context.Background(), operation.request, state)
	if !errors.Is(err, errTestBoundary) || mutation != nil {
		t.Fatalf("bindMutation(final failure) = %#v, %v", mutation, err)
	}

	assertMutationLockReleased(t, state, testProjectName, testServiceName)
}

func TestBindMutationReleasesLockAfterFenceFailure(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	ctx, cancel := context.WithCancel(context.Background())
	observationCalls := 0
	operation.runtime.observe = func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error) {
		observationCalls++
		if observationCalls == 2 {
			cancel()
		}

		return missingObservation(), nil
	}

	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	mutation, err := operation.service.bindMutation(ctx, operation.request, state)
	if !errors.Is(err, context.Canceled) || mutation != nil {
		t.Fatalf("bindMutation(fence failure) = %#v, %v", mutation, err)
	}

	assertMutationLockReleased(t, state, testProjectName, testServiceName)
}

func TestBindMutationRejectsScopeDriftAndReleasesLock(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	originalInspect := operation.runtime.inspect
	inspectCalls := 0
	operation.runtime.inspect = func(ctx context.Context) (RuntimeEvidence, error) {
		inspectCalls++
		evidence, err := originalInspect(ctx)

		if inspectCalls == 1 {
			oldProject := []byte("name: example")
			newProject := []byte("name: changed")

			index := bytes.Index(operation.request.Source.Content, oldProject)
			if index < 0 {
				return RuntimeEvidence{}, errTestBoundary
			}

			copy(operation.request.Source.Content[index:], newProject)
		}

		return evidence, err
	}

	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	mutation, err := operation.service.bindMutation(context.Background(), operation.request, state)
	if !errors.Is(err, ErrConflictingState) || mutation != nil {
		t.Fatalf("bindMutation(scope drift) = %#v, %v", mutation, err)
	}

	assertMutationLockReleased(t, state, testProjectName, testServiceName)
}

func TestBindPreparedTransactionRejectsInvalidEvidence(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	preparation, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	got, err := bindPreparedTransaction(context.Background(), nil, preparation)
	assertRejectedPreparedTransaction(t, "nil lock", got, err, ErrInvalidRequest)

	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	lock, err := state.TryLockService(preparation.Plan.Project, preparation.Plan.Service)
	if err != nil {
		t.Fatalf("TryLockService() error = %v", err)
	}
	defer closeMutationTestLock(t, lock)

	invalidRuntime := preparation
	invalidRuntime.Execution.Kind = domain.RuntimeContainerd
	got, err = bindPreparedTransaction(context.Background(), lock, invalidRuntime)
	assertRejectedPreparedTransaction(t, "invalid runtime", got, err, store.ErrInvalidState)

	invalidRecovery := preparation
	invalidRecovery.HasTransaction = true
	invalidRecovery.Plan.Kind = PlanBootstrap
	invalidRecovery.Transaction = exactTransaction(preparation, store.TransactionActive)

	got, err = bindPreparedTransaction(context.Background(), lock, invalidRecovery)
	assertRejectedPreparedTransaction(t, "invalid recovery", got, err, ErrConflictingState)

	missingRecovery := preparation
	missingRecovery.Plan.Kind = PlanResume
	got, err = bindPreparedTransaction(context.Background(), lock, missingRecovery)
	assertRejectedPreparedTransaction(t, "missing recovery", got, err, ErrConflictingState)

	unknown := preparation
	unknown.Plan.Kind = PlanKind("unknown")
	got, err = bindPreparedTransaction(context.Background(), lock, unknown)
	assertRejectedPreparedTransaction(t, "unknown", got, err, ErrConflictingState)
}

func assertRejectedPreparedTransaction(
	t *testing.T,
	name string,
	got Preparation,
	err error,
	want error,
) {
	t.Helper()

	var empty Preparation
	if !errors.Is(err, want) || !reflect.DeepEqual(got, empty) {
		t.Fatalf("bindPreparedTransaction(%s) = %#v, %v", name, got, err)
	}
}

func TestBoundMutationRejectsInvalidClose(t *testing.T) {
	t.Parallel()

	if !errors.Is((*boundMutation)(nil).close(), ErrInvalidRequest) {
		t.Fatal("nil boundMutation.close() succeeded")
	}

	mutation := new(boundMutation)
	if !errors.Is(mutation.close(), ErrInvalidRequest) {
		t.Fatal("empty boundMutation.close() succeeded")
	}
}

func TestMutationCloseReportsLockReleaseFailure(t *testing.T) {
	t.Parallel()

	t.Run("failed binding", func(t *testing.T) {
		t.Parallel()

		lock := mutationLockOverClosedStore(t)

		mutation, err := closeMutationLock(lock, errTestBoundary)
		if mutation != nil || !errors.Is(err, errTestBoundary) {
			t.Fatalf("closeMutationLock() = %#v, %v", mutation, err)
		}
	})

	t.Run("bound mutation", func(t *testing.T) {
		t.Parallel()

		mutation := new(boundMutation)
		mutation.lock = mutationLockOverClosedStore(t)

		err := mutation.close()
		if err == nil || mutation.lock != nil {
			t.Fatalf("boundMutation.close() = %v, lock %#v", err, mutation.lock)
		}
	})
}

func mutationLockOverClosedStore(t *testing.T) *store.ServiceLock {
	t.Helper()

	state := openMutationTestStore(t)

	lock, err := state.TryLockService(testProjectName, testServiceName)
	if err != nil {
		closeMutationTestStore(t, state)
		t.Fatalf("TryLockService() error = %v", err)
	}

	err = state.Close()
	if err != nil {
		closeMutationTestLock(t, lock)
		t.Fatalf("Store.Close() error = %v", err)
	}

	return lock
}

func openMutationTestStore(t *testing.T) *store.Store {
	t.Helper()

	directory := t.TempDir()

	err := os.Chmod(directory, 0o700) //nolint:gosec // The private directory must not be accessible to other users.
	if err != nil {
		t.Fatalf("chmod mutation test directory: %v", err)
	}

	physicalDirectory, err := filepath.EvalSymlinks(directory)
	if err != nil {
		t.Fatalf("resolve mutation test directory: %v", err)
	}

	state, err := store.Open(context.Background(), filepath.Join(physicalDirectory, "state.db"))
	if err != nil {
		t.Fatalf("store.Open() error = %v", err)
	}

	return state
}

func closeMutationTestStore(t *testing.T, state *store.Store) {
	t.Helper()

	err := state.Close()
	if err != nil {
		t.Errorf("Store.Close() error = %v", err)
	}
}

func closeMutationTestLock(t *testing.T, lock *store.ServiceLock) {
	t.Helper()

	if lock != nil {
		err := lock.Close()
		if err != nil {
			t.Errorf("ServiceLock.Close() error = %v", err)
		}
	}
}

func closeBoundMutation(t *testing.T, mutation *boundMutation) {
	t.Helper()

	if mutation != nil && mutation.lock != nil {
		err := mutation.close()
		if err != nil {
			t.Errorf("boundMutation.close() error = %v", err)
		}
	}
}

func assertBoundTransaction(
	t *testing.T,
	state *store.Store,
	preparation Preparation,
	wantTransaction bool,
) {
	t.Helper()

	transaction, found, err := state.UnresolvedTransaction(
		context.Background(),
		preparation.Plan.Project,
		preparation.Plan.Service,
	)
	if err != nil || found != wantTransaction {
		t.Fatalf("UnresolvedTransaction() = %#v, %t, %v", transaction, found, err)
	}

	if wantTransaction && (transaction != preparation.Transaction ||
		transaction.ID == (store.TransactionID{}) || transaction.State != store.TransactionActive ||
		!transactionMatches(transaction, preparation.Workload, preparation.Execution)) {
		t.Fatalf("bound transaction = %#v, preparation %#v", transaction, preparation.Transaction)
	}
}

func assertMutationLockHeld(t *testing.T, state *store.Store, preparation Preparation) {
	t.Helper()

	contender, err := state.TryLockService(preparation.Plan.Project, preparation.Plan.Service)
	if !errors.Is(err, store.ErrUnavailable) || contender != nil {
		t.Fatalf("TryLockService(contender) = %#v, %v", contender, err)
	}
}

func assertMutationLockReleased(t *testing.T, state *store.Store, project, service string) {
	t.Helper()

	lock, err := state.TryLockService(project, service)
	if err != nil {
		t.Fatalf("TryLockService(released) error = %v", err)
	}

	closeMutationTestLock(t, lock)
}
