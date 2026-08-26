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

type mutationPlanTest struct {
	name            string
	observe         func(domain.DesiredWorkload) WorkloadObservation
	want            PlanKind
	wantTransaction bool
}

func TestBindMutationStartsOnlyRequiredTransactions(t *testing.T) {
	t.Parallel()

	tests := []mutationPlanTest{
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
			testBoundMutationStartsRequiredTransaction(t, test)
		})
	}
}

func testBoundMutationStartsRequiredTransaction(t *testing.T, test mutationPlanTest) {
	t.Helper()

	operation := newTestOperation(t)
	state := openMutationTestStore(t)
	defer closeMutationTestStore(t, state)

	applied := appliedForMutationPlan(t, operation, state, test.want)
	observationCalls := 0
	operation.runtime.observe = func(
		_ context.Context,
		workload domain.DesiredWorkload,
	) (WorkloadObservation, error) {
		observationCalls++

		if test.want == PlanUnchanged || test.want == PlanUpgrade {
			return appliedMutationObservation(workload, applied, test.want == PlanUnchanged), nil
		}

		return test.observe(workload), nil
	}

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
	assertBoundUpgradeTransaction(t, mutation.preparation, applied, test.want)
	assertMutationLockHeld(t, state, mutation.preparation)
}

func assertBoundUpgradeTransaction(
	t *testing.T,
	preparation Preparation,
	applied store.AppliedService,
	kind PlanKind,
) {
	t.Helper()

	if kind != PlanUpgrade {
		return
	}

	transaction := preparation.Transaction
	if transaction.SourceDigest != preparation.Workload.SourceDigest ||
		transaction.EffectiveDigest != preparation.Workload.EffectiveDigest ||
		transaction.BaseTransactionID != applied.TransactionID ||
		transaction.PredecessorWorkloadID != applied.WorkloadID {
		t.Fatalf("upgrade transaction = %#v, applied %#v", transaction, applied)
	}
}

func appliedForMutationPlan(
	t *testing.T,
	operation testOperation,
	state *store.Store,
	kind PlanKind,
) store.AppliedService {
	t.Helper()

	if kind != PlanUnchanged && kind != PlanUpgrade {
		return store.AppliedService{}
	}

	desired, err := operation.service.prepareDesired(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("prepareDesired() error = %v", err)
	}

	workload := desired.workload
	if kind == PlanUpgrade {
		workload.SourceDigest = domain.Hash([]byte("previous source"))
		workload.EffectiveDigest = domain.Hash([]byte("previous desired state"))
	}

	return seedAppliedMutation(t, state, workload, desired.execution)
}

func seedAppliedMutation(
	t *testing.T,
	state *store.Store,
	workload domain.DesiredWorkload,
	execution RuntimeEvidence,
) store.AppliedService {
	t.Helper()

	lock, err := state.TryLockService(testProjectName, testServiceName)
	if err != nil {
		t.Fatalf("TryLockService(seed) error = %v", err)
	}
	defer closeMutationTestLock(t, lock)

	transaction, err := lock.BeginTransaction(context.Background(), store.TransactionIntent{
		Kind:            store.TransactionBootstrap,
		Runtime:         execution.Kind,
		SourceDigest:    workload.SourceDigest,
		EffectiveDigest: workload.EffectiveDigest,
		ExecutionDigest: execution.Digest,
	})
	if err != nil {
		t.Fatalf("BeginTransaction(seed) error = %v", err)
	}

	applied, err := lock.CommitAppliedService(context.Background(), transaction.ID, store.AppliedServiceIntent{
		WorkloadID:             testWorkloadEffectID,
		ConfigurationDigest:    domain.Hash([]byte("runtime configuration")),
		StorageDigest:          storageDigestForTest(workload),
		ReferenceDigest:        workload.Image.ReferenceDigest,
		PlatformManifestDigest: workload.Image.PlatformManifest,
		ImageConfigDigest:      workload.Image.ImageConfig,
	})
	if err != nil {
		t.Fatalf("CommitAppliedService(seed) error = %v", err)
	}

	return applied
}

func storageDigestForTest(workload domain.DesiredWorkload) domain.Digest {
	digest, valid := domain.ComputeStorageDigest(workload, testRuntimeMounts(workload))
	if !valid {
		panic("invalid test storage evidence")
	}

	return digest
}

func appliedMutationObservation(
	workload domain.DesiredWorkload,
	applied store.AppliedService,
	configurationMatches bool,
) WorkloadObservation {
	runtimeMounts := testRuntimeMounts(workload)
	storageDigest, valid := domain.ComputeStorageDigest(workload, runtimeMounts)
	if !valid {
		panic("invalid test storage evidence")
	}

	return WorkloadObservation{
		ID:                   applied.WorkloadID,
		State:                WorkloadObservationPresent,
		ConfigurationDigest:  applied.ConfigurationDigest,
		StorageDigest:        storageDigest,
		RuntimeMounts:        runtimeMounts,
		ConfigurationMatches: configurationMatches,
		Running:              true,
		Ownership: domain.WorkloadOwnership{
			Status:           domain.OwnershipManaged,
			Service:          workload.ServiceName,
			Transaction:      applied.TransactionID.String(),
			DesiredState:     applied.EffectiveDigest,
			Reference:        applied.ReferenceDigest,
			ImageConfig:      applied.ImageConfigDigest,
			PlatformManifest: applied.PlatformManifestDigest,
		},
	}
}

func TestBindMutationUsesLockedRepreparation(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	observationCalls := 0
	operation.runtime.observe = func(
		_ context.Context,
		workload domain.DesiredWorkload,
	) (WorkloadObservation, error) {
		observationCalls++
		if observationCalls == 1 {
			return missingObservation(), nil
		}

		return fixedObservation(presentObservation(true, true, domain.OwnershipUnmanaged))(workload), nil
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
	invalidRuntime.Execution.Kind = domain.RuntimeKind("unknown")
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
