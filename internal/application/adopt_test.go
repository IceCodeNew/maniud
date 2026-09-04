//nolint:goconst // Scenario labels remain beside the mutations they identify.
package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type adoptRuntimeFixture struct {
	observation WorkloadObservation
	err         error
	calls       int
}

type adoptObservationDriftTest struct {
	name   string
	mutate func(*WorkloadObservation)
	err    error
}

func (runtime *adoptRuntimeFixture) Inspect(context.Context) (RuntimeEvidence, error) {
	return testExecutionEvidence(), nil
}

func (runtime *adoptRuntimeFixture) CheckWorkload(domain.DesiredWorkload) error {
	return nil
}

func (runtime *adoptRuntimeFixture) ObserveWorkload(
	context.Context,
	domain.DesiredWorkload,
) (WorkloadObservation, error) {
	runtime.calls++

	return runtime.observation, runtime.err
}

func TestRunAdoptPublishesExactUnmanagedBaseline(t *testing.T) {
	t.Parallel()

	state, mutation := newAdoptMutation(t)
	t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

	runtime := &adoptRuntimeFixture{observation: mutation.preparation.Plan.Observation}
	err := runAdopt(context.Background(), mutation, runtime)
	if err != nil {
		t.Fatalf("runAdopt() error = %v", err)
	}

	if runtime.calls != 1 || mutation.preparation.Transaction.State != store.TransactionSucceeded ||
		!mutation.preparation.HasApplied || mutation.preparation.Applied.Kind != store.TransactionAdopt ||
		mutation.preparation.Applied.WorkloadID != runtime.observation.ID {
		t.Fatalf("adoption = %#v, calls %d", mutation.preparation, runtime.calls)
	}

	actions, actionsErr := state.Actions(context.Background(), mutation.preparation.Transaction.ID)
	if actionsErr != nil || len(actions) != 0 {
		t.Fatalf("Actions() = %#v, %v", actions, actionsErr)
	}
}

func TestRunAdoptResumesActionlessTransaction(t *testing.T) {
	t.Parallel()

	state, mutation := newAdoptMutation(t)
	t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

	mutation.preparation.Plan.Kind = PlanResume
	runtime := &adoptRuntimeFixture{observation: mutation.preparation.Plan.Observation}

	err := runAdopt(context.Background(), mutation, runtime)
	if err != nil || mutation.preparation.Transaction.State != store.TransactionSucceeded {
		t.Fatalf("runAdopt(resume) = %v, %#v", err, mutation.preparation.Transaction)
	}
}

func TestRunAdoptPreservesPendingAndDegradedHealthEvidence(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name   string
		health WorkloadHealth
		want   error
		state  store.TransactionState
	}{
		{
			name: "pending", health: WorkloadHealth{Status: WorkloadHealthStarting},
			want: ErrHealthPending, state: store.TransactionActive,
		},
		{
			name: "degraded", health: WorkloadHealth{Status: WorkloadHealthUnhealthy, FailingStreak: 3},
			want: ErrHealthDegraded, state: store.TransactionHealthDegraded,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation := newAdoptMutation(t)
			t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })
			mutation.preparation.Workload.Healthcheck = &domain.Healthcheck{
				Test: []string{testHealthCommand, testTrueCommand},
			}
			observation := mutation.preparation.Plan.Observation
			observation.Health = test.health

			err := runAdopt(t.Context(), mutation, &adoptRuntimeFixture{observation: observation})
			if !errors.Is(err, test.want) || mutation.preparation.Transaction.State != test.state {
				t.Fatalf("runAdopt(%s) = %v, transaction %#v", test.name, err, mutation.preparation.Transaction)
			}
		})
	}
}

func TestSettleAdoptionHealthDegradedPreservesCauseOnJournalFailure(t *testing.T) {
	t.Parallel()

	state, mutation := newAdoptMutation(t)
	if err := mutation.lock.Close(); err != nil {
		t.Fatalf("ServiceLock.Close() error = %v", err)
	}
	defer closeMutationTestStore(t, state)

	err := settleAdoptionHealthDegraded(t.Context(), mutation, ErrHealthDegraded)
	if !errors.Is(err, ErrHealthDegraded) || !errors.Is(err, store.ErrInvalidState) {
		t.Fatalf("settleAdoptionHealthDegraded(closed lock) error = %v", err)
	}
}

func TestSettleAdoptionHealthDegradedSkipsSettledTransaction(t *testing.T) {
	t.Parallel()

	state, mutation := newAdoptMutation(t)
	transaction, err := mutation.lock.SetTransactionState(
		t.Context(),
		mutation.preparation.Transaction.ID,
		store.TransactionHealthDegraded,
	)
	if err != nil {
		t.Fatalf("SetTransactionState(health degraded) error = %v", err)
	}
	mutation.preparation.Transaction = transaction
	if err = mutation.lock.Close(); err != nil {
		t.Fatalf("ServiceLock.Close() error = %v", err)
	}
	defer closeMutationTestStore(t, state)

	err = settleAdoptionHealthDegraded(t.Context(), mutation, ErrHealthDegraded)
	if !errors.Is(err, ErrHealthDegraded) || errors.Is(err, store.ErrInvalidState) {
		t.Fatalf("settleAdoptionHealthDegraded(settled transaction) error = %v", err)
	}
}

func TestRunAdoptRejectsDriftAndProbeFailure(t *testing.T) {
	t.Parallel()

	for _, test := range adoptObservationDriftTests() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation := newAdoptMutation(t)
			t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

			observation := mutation.preparation.Plan.Observation
			test.mutate(&observation)
			runtime := &adoptRuntimeFixture{observation: observation}

			err := runAdopt(context.Background(), mutation, runtime)
			if !errors.Is(err, test.err) {
				t.Fatalf("runAdopt() = %v", err)
			}
		})
	}

	state, mutation := newAdoptMutation(t)
	t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

	runtime := &adoptRuntimeFixture{
		observation: mutation.preparation.Plan.Observation,
		err:         errTestBoundary,
	}
	if err := runAdopt(context.Background(), mutation, runtime); !errors.Is(err, errTestBoundary) {
		t.Fatalf("runAdopt(probe failure) = %v", err)
	}
}

func adoptObservationDriftTests() []adoptObservationDriftTest {
	return []adoptObservationDriftTest{
		{
			name:   testMissingValue,
			mutate: func(value *WorkloadObservation) { *value = missingObservation() },
			err:    ErrConflictingState,
		},
		{
			name:   "identity",
			mutate: func(value *WorkloadObservation) { value.ID = testDifferentWorkload },
			err:    ErrConflictingState,
		},
		{
			name: "configuration",
			mutate: func(value *WorkloadObservation) {
				value.ConfigurationDigest = domain.Hash([]byte("drift"))
			},
			err: ErrConflictingState,
		},
		{
			name: "storage",
			mutate: func(value *WorkloadObservation) {
				value.StorageDigest = domain.Hash([]byte("drift"))
			},
			err: ErrConflictingState,
		},
		{
			name:   "not matching",
			mutate: func(value *WorkloadObservation) { value.ConfigurationMatches = false },
			err:    ErrConflictingState,
		},
		{
			name:   "stopped",
			mutate: func(value *WorkloadObservation) { value.Lifecycle = WorkloadLifecycleExited },
			err:    ErrConflictingState,
		},
		{
			name: "managed",
			mutate: func(value *WorkloadObservation) {
				value.Ownership.Status = domain.OwnershipManaged
			},
			err: ErrConflictingState,
		},
	}
}

func TestRunAdoptRejectsInvalidMutation(t *testing.T) {
	t.Parallel()

	if !errors.Is(runAdopt(context.Background(), nil, &adoptRuntimeFixture{}), ErrInvalidRequest) ||
		!errors.Is(runAdopt(context.Background(), &boundMutation{}, nil), ErrInvalidRequest) ||
		validAdoptMutation(nil) {
		t.Fatal("runAdopt() accepted nil input")
	}

	state, mutation := newAdoptMutation(t)
	t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

	runtime := &adoptRuntimeFixture{observation: mutation.preparation.Plan.Observation}
	invalid := []func(*boundMutation){
		func(value *boundMutation) { value.preparation.Plan.Kind = PlanBootstrap },
		func(value *boundMutation) { value.preparation.Actions = []store.Action{{Kind: "unexpected"}} },
		func(value *boundMutation) { value.preparation.Transaction.Kind = store.TransactionBootstrap },
		func(value *boundMutation) { value.preparation.Transaction.State = store.TransactionFailed },
		func(value *boundMutation) { value.preparation.HasApplied = true },
	}

	for index, mutate := range invalid {
		candidate := *mutation
		candidate.preparation = mutation.preparation
		mutate(&candidate)

		if !errors.Is(runAdopt(context.Background(), &candidate, runtime), ErrInvalidRequest) {
			t.Fatalf("runAdopt(invalid %d) accepted mutation", index)
		}
	}
}

func TestRunAdoptReturnsBaselineCommitFailure(t *testing.T) {
	t.Parallel()

	state, mutation := newAdoptMutation(t)
	runtime := &adoptRuntimeFixture{observation: mutation.preparation.Plan.Observation}

	if err := mutation.lock.Close(); err != nil {
		t.Fatalf("ServiceLock.Close() error = %v", err)
	}

	err := runAdopt(context.Background(), mutation, runtime)
	if err == nil {
		t.Fatal("runAdopt() accepted a closed writer fence")
	}

	mutation.lock = nil
	closeMutationTestStore(t, state)
}

func newAdoptMutation(t *testing.T) (*store.Store, *boundMutation) {
	t.Helper()

	state := openMutationTestStore(t)
	workload := testWorkloadEffect(t)
	execution := testExecutionEvidence()
	observation := presentObservation(true, true, domain.OwnershipUnmanaged)
	observation.RuntimeMounts = testRuntimeMounts(workload)
	observation.StorageDigest = storageDigestForTest(workload)

	lock, err := state.TryLockService(testProjectName, testServiceName)
	if err != nil {
		closeMutationTestStore(t, state)
		t.Fatalf("TryLockService() error = %v", err)
	}

	transaction, err := lock.BeginTransaction(context.Background(), store.TransactionIntent{
		Kind:                  store.TransactionAdopt,
		Runtime:               execution.Kind,
		SourceDigest:          workload.SourceDigest,
		EffectiveDigest:       workload.EffectiveDigest,
		ExecutionDigest:       execution.Digest,
		PredecessorWorkloadID: observation.ID,
	})
	if err != nil {
		closeMutationTestLock(t, lock)
		closeMutationTestStore(t, state)
		t.Fatalf("BeginTransaction() error = %v", err)
	}

	return state, &boundMutation{
		preparation: Preparation{
			Plan: Plan{
				Kind:        PlanAdopt,
				Project:     testProjectName,
				Service:     testServiceName,
				Runtime:     execution.Kind,
				Platform:    execution.Platform,
				Image:       workload.Image,
				Source:      workload.SourceDigest,
				Desired:     workload.EffectiveDigest,
				Observation: observation,
			},
			Workload:       workload,
			Execution:      execution,
			Transaction:    transaction,
			HasTransaction: true,
		},
		lock: lock,
	}
}
