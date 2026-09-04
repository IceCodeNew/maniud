//nolint:cyclop,funlen,goconst,lll,paralleltest,prealloc,tparallel // Boundary matrices intentionally keep all failure cases visible together.
package application

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type healthRollbackRuntimeFixture struct {
	*upgradeRuntimeFixture
}

type healthStopRuntimeFixture struct {
	*testWorkloadStartRuntime

	transition    WorkloadTransition
	transitionErr error
}

type healthResolutionOperationRuntime struct {
	*testRuntime
	*healthRollbackRuntimeFixture

	closes int
}

func (runtime *healthResolutionOperationRuntime) CloseIdleConnections() {
	runtime.closes++
}

func (runtime *healthStopRuntimeFixture) ApplyWorkloadTransition(
	_ context.Context,
	transition WorkloadTransition,
) error {
	runtime.transition = transition

	return runtime.transitionErr
}

func (*healthStopRuntimeFixture) ProbeWorkloadTransition(
	context.Context,
	WorkloadTransition,
) (WorkloadTransitionProbe, error) {
	return WorkloadTransitionProbe{}, nil
}

func (runtime *healthRollbackRuntimeFixture) StartWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	if err := runtime.upgradeRuntimeFixture.StartWorkload(ctx, workload, transaction); err != nil {
		return err
	}
	runtime.workload.Workload.Health = WorkloadHealth{
		Status: WorkloadHealthUnhealthy, FailingStreak: 3,
	}

	return nil
}

func (runtime *healthRollbackRuntimeFixture) ApplyWorkloadTransition(
	ctx context.Context,
	transition WorkloadTransition,
) error {
	if err := runtime.transitionApply[transition.Kind]; err != nil {
		return err
	}
	if runtime.workload.State == WorkloadEffectProbeObserved &&
		runtime.workload.Workload.ID == transition.Before.ID {
		if runtime.workload.Workload.Lifecycle != transition.Before.Lifecycle {
			return ErrConflictingState
		}
		runtime.workload.Workload.Lifecycle = transition.After.Lifecycle

		return nil
	}

	return runtime.upgradeRuntimeFixture.ApplyWorkloadTransition(ctx, transition)
}

func TestRollbackBootstrapHealthStopsDiscardsAndFailsTransaction(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	coreActions := append([]store.Action(nil), mutation.preparation.Actions...)
	if _, journeyErr := bootstrapHealthRollbackJourney(mutation.preparation); journeyErr != nil {
		t.Fatalf("bootstrapHealthRollbackJourney(core) error = %v", journeyErr)
	}
	invalidCore := mutation.preparation
	invalidCore.Actions = append([]store.Action(nil), coreActions...)
	invalidCore.Actions[0].State = store.ActionStateIntent
	if _, journeyErr := bootstrapHealthRollbackJourney(invalidCore); !errors.Is(journeyErr, ErrConflictingState) {
		t.Fatalf("bootstrapHealthRollbackJourney(invalid core) error = %v", journeyErr)
	}

	stopIntent := workloadHealthStopIntent(
		int64(len(coreActions)+1),
		mutation.preparation.Workload,
		mutation.preparation.Transaction.ID.String(),
	)
	stop := completedEffectAction(
		mutation.preparation.Transaction.ID,
		stopIntent,
		domain.Hash([]byte("stopped health candidate")),
	)
	discardIntent := workloadDiscardIntent(
		int64(len(coreActions)+2),
		mutation.preparation.Workload,
		mutation.preparation.Transaction.ID.String(),
	)
	discard := completedEffectAction(
		mutation.preparation.Transaction.ID,
		discardIntent,
		domain.Hash([]byte("discarded health candidate")),
	)
	withSuffix := mutation.preparation
	withSuffix.Actions = append(append([]store.Action(nil), coreActions...), stop, discard)
	journey, err := bootstrapHealthRollbackJourney(withSuffix)
	if err != nil || journey.stop != stop || journey.discard != discard {
		t.Fatalf("bootstrapHealthRollbackJourney(suffix) = %#v, %v", journey, err)
	}
	if cursor, prefixErr := completedHealthStopPrefix(withSuffix, len(coreActions)); prefixErr != nil || cursor != len(coreActions)+1 {
		t.Fatalf("completedHealthStopPrefix() = %d, %v", cursor, prefixErr)
	}
	for _, mutate := range []func(*Preparation){
		func(value *Preparation) { value.Actions[len(coreActions)].IntentDigest = domain.Digest{} },
		func(value *Preparation) { value.Actions[len(coreActions)+1].IntentDigest = domain.Digest{} },
		func(value *Preparation) { value.Actions = append(value.Actions, store.Action{Kind: testOtherValue}) },
	} {
		invalid := withSuffix
		invalid.Actions = append([]store.Action(nil), withSuffix.Actions...)
		mutate(&invalid)
		if _, journeyErr := bootstrapHealthRollbackJourney(invalid); !errors.Is(journeyErr, ErrConflictingState) {
			t.Fatalf("bootstrapHealthRollbackJourney(invalid suffix) error = %v", journeyErr)
		}
	}
	invalidPrefix := withSuffix
	invalidPrefix.Actions = append([]store.Action(nil), withSuffix.Actions...)
	invalidPrefix.Actions[len(coreActions)].State = store.ActionStateIntent
	if _, prefixErr := completedHealthStopPrefix(invalidPrefix, len(coreActions)); !errors.Is(prefixErr, ErrConflictingState) {
		t.Fatalf("completedHealthStopPrefix(invalid) error = %v", prefixErr)
	}
	invalidRollback := *mutation
	invalidRollback.preparation = invalidCore
	if rollbackErr := rollbackBootstrapHealth(t.Context(), &invalidRollback, state, runtime); !errors.Is(rollbackErr, ErrConflictingState) {
		t.Fatalf("rollbackBootstrapHealth(invalid journey) error = %v", rollbackErr)
	}
	completedRollback := *mutation
	completedRollback.preparation = withSuffix
	if rollbackErr := rollbackBootstrapHealth(t.Context(), &completedRollback, state, runtime); !errors.Is(rollbackErr, ErrConflictingState) {
		t.Fatalf("rollbackBootstrapHealth(completed mismatch) error = %v", rollbackErr)
	}
	if validBootstrapHealthCore(nil) {
		t.Fatal("validBootstrapHealthCore(nil) = true")
	}

	err = rollbackHealthCandidate(t.Context(), mutation, state, runtime)
	if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed ||
		runtime.workload.State != WorkloadEffectProbeMissing || runtime.discards != 1 {
		t.Fatalf("rollbackHealthCandidate() = %v, transaction %#v, workload %#v, discards %d",
			err,
			mutation.preparation.Transaction,
			runtime.workload,
			runtime.discards,
		)
	}
	assertBootstrapActions(t, state, mutation.preparation.Transaction.ID, []string{
		workloadCreateActionKind,
		workloadStartActionKind,
		workloadHealthStopActionKind,
		workloadDiscardActionKind,
	})
}

func TestHealthRollbackSkipsStopForMissingCandidate(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	mutation.preparation.Plan.Observation = missingObservation()
	actions := len(mutation.preparation.Actions)
	if err := settleHealthRollbackStop(t.Context(), mutation, state, runtime); err != nil {
		t.Fatalf("settleHealthRollbackStop(missing) = %v", err)
	}
	if len(mutation.preparation.Actions) != actions ||
		runtime.transitionApplies[WorkloadTransitionStop] != 0 {
		t.Fatalf("missing candidate stop = actions %d -> %d, applies %#v",
			actions, len(mutation.preparation.Actions), runtime.transitionApplies)
	}
}

func TestSettleHealthRollbackStopPropagatesEveryFailureBoundary(t *testing.T) {
	t.Parallel()

	t.Run("prepare", func(t *testing.T) {
		state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		mutation.preparation.Transaction.Kind = store.TransactionAdopt
		if err := settleHealthRollbackStop(t.Context(), mutation, state, runtime); !errors.Is(err, ErrInvalidRequest) {
			t.Fatalf("settleHealthRollbackStop(prepare) error = %v", err)
		}
		mutation.preparation.Transaction.Kind = store.TransactionKind("new")
		if _, _, err := prepareHealthRollbackStop(t.Context(), mutation); !errors.Is(err, ErrConflictingState) {
			t.Fatalf("prepareHealthRollbackStop(unknown) error = %v", err)
		}
	})

	t.Run("observation", func(t *testing.T) {
		state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		mutation.preparation.Plan.Observation.Lifecycle = WorkloadLifecycleRestarting
		if err := rollbackHealthCandidate(t.Context(), mutation, state, runtime); !errors.Is(err, ErrHealthPending) {
			t.Fatalf("rollbackHealthCandidate(restarting) error = %v", err)
		}
	})

	t.Run("effect", func(t *testing.T) {
		state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		runtime.transitionApply[WorkloadTransitionStop] = errTestBoundary
		if err := settleHealthRollbackStop(t.Context(), mutation, state, runtime); !errors.Is(err, errTestBoundary) {
			t.Fatalf("settleHealthRollbackStop(effect) error = %v", err)
		}

		sequence := int64(len(mutation.preparation.Actions) + 1)
		intent := workloadHealthStopIntent(
			sequence,
			mutation.preparation.Workload,
			mutation.preparation.Transaction.ID.String(),
		)
		wrong := completedEffectAction(
			mutation.preparation.Transaction.ID,
			intent,
			domain.Hash([]byte("wrong health stop")),
		)
		wrong.IntentDigest = domain.Digest{}
		if err := settleWorkloadHealthStop(
			t.Context(), mutation, runtime, wrong, sequence,
		); !errors.Is(err, ErrConflictingState) {
			t.Fatalf("settleWorkloadHealthStop(mismatch) error = %v", err)
		}
		wrong.IntentDigest = intent.IntentDigest
		if err := settleWorkloadHealthStop(
			t.Context(), mutation, runtime, wrong, sequence,
		); !errors.Is(err, ErrConflictingState) {
			t.Fatalf("settleWorkloadHealthStop(completed mismatch) error = %v", err)
		}
	})

	t.Run("refresh", func(t *testing.T) {
		state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		closedState := openMutationTestStore(t)
		closeMutationTestStore(t, closedState)
		if err := settleHealthRollbackStop(t.Context(), mutation, closedState, runtime); err == nil {
			t.Fatal("settleHealthRollbackStop(closed refresh state) error = nil")
		}
	})

	t.Run("rollback refresh and journal", func(t *testing.T) {
		state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
		t.Cleanup(func() {
			closeBoundMutation(t, mutation)
			closeMutationTestStore(t, state)
		})
		closedState := openMutationTestStore(t)
		closeMutationTestStore(t, closedState)
		if err := rollbackBootstrapHealth(t.Context(), mutation, closedState, runtime); err == nil {
			t.Fatal("rollbackBootstrapHealth(closed refresh state) error = nil")
		}
		if err := mutation.lock.Close(); err != nil {
			t.Fatalf("ServiceLock.Close() error = %v", err)
		}
		if err := failHealthResolution(
			t.Context(), mutation, EventTransactionFailed,
		); !errors.Is(err, store.ErrInvalidState) {
			t.Fatalf("failHealthResolution(closed lock) error = %v", err)
		}
		mutation.lock = nil
	})

	t.Run("already degraded convergence", func(t *testing.T) {
		state, mutation, runtime := newDegradedBootstrapHealthMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		if err := settleMutationConvergence(
			t.Context(), mutation, runtime.workload.Workload,
		); !errors.Is(err, ErrHealthDegraded) {
			t.Fatalf("settleMutationConvergence(already degraded) error = %v", err)
		}
	})

	t.Run("convergence journal", func(t *testing.T) {
		state, mutation := newBootstrapMutation(t)
		mutation.preparation.Workload.Healthcheck = &domain.Healthcheck{
			Test: []string{testHealthCommand, testTrueCommand},
		}
		evidence := startedWorkloadEffectEvidence(
			mutation.preparation.Workload,
			mutation.preparation.Transaction.ID.String(),
		)
		evidence.Health = WorkloadHealth{Status: WorkloadHealthUnhealthy}
		if err := mutation.lock.Close(); err != nil {
			t.Fatalf("ServiceLock.Close() error = %v", err)
		}
		if err := settleMutationConvergence(
			t.Context(), mutation, evidence,
		); !errors.Is(err, ErrHealthDegraded) || !errors.Is(err, store.ErrInvalidState) {
			t.Fatalf("settleMutationConvergence(closed lock) error = %v", err)
		}
		mutation.lock = nil
		closeMutationTestStore(t, state)
	})
}

func newDegradedBootstrapHealthMutation(
	t *testing.T,
) (*store.Store, *boundMutation, *healthRollbackRuntimeFixture) {
	t.Helper()

	state, mutation := newBootstrapMutation(t)
	mutation.preparation.Workload.Healthcheck = &domain.Healthcheck{
		Test: []string{testHealthCommand, testTrueCommand},
	}
	runtime := newHealthRollbackRuntime(mutation.preparation.Workload)
	runtime.image = observedImageProbe(mutation.preparation.Workload.Image)
	err := runBootstrap(t.Context(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, ErrHealthDegraded) ||
		mutation.preparation.Transaction.State != store.TransactionHealthDegraded {
		closeBootstrapMutation(t, state, mutation)
		t.Fatalf("runBootstrap(unhealthy) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}
	if err = refreshMutationActions(t.Context(), mutation, state); err != nil {
		closeBootstrapMutation(t, state, mutation)
		t.Fatalf("refreshMutationActions() error = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanHealthDegraded
	mutation.preparation.Plan.Observation = healthRollbackObservation(runtime.workload.Workload)

	return state, mutation, runtime
}

func TestResolveHealthOwnsRuntimeAndMutableStateBoundary(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	runtime := &healthResolutionOperationRuntime{
		testRuntime:                  operation.runtime,
		healthRollbackRuntimeFixture: newHealthRollbackRuntime(testWorkloadEffect(t)),
	}
	reader := &operationReaderFixture{testTransactions: operation.transactions, events: new([]string)}
	facade := newOperationTestFacade(operation, runtime, reader)
	resolution := HealthResolution{
		Transaction: store.TransactionID{1}.String(),
		Action:      HealthResolutionRollback,
		Observation: HealthResolutionObservation{State: WorkloadObservationMissing},
	}

	if _, err := (*ApplyFacade)(nil).ResolveHealth(t.Context(), operation.request, resolution); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ResolveHealth(nil facade) error = %v", err)
	}
	if _, err := facade.ResolveHealth(t.Context(), operation.request, HealthResolution{}); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ResolveHealth(invalid resolution) error = %v", err)
	}
	invalidRequest := operation.request
	invalidRequest.Source.Content = nil
	if _, err := facade.ResolveHealth(t.Context(), invalidRequest, resolution); err == nil {
		t.Fatal("ResolveHealth(invalid source) error = nil")
	}

	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return nil, errTestBoundary
	}
	if _, err := facade.ResolveHealth(t.Context(), operation.request, resolution); !errors.Is(err, errTestBoundary) {
		t.Fatalf("ResolveHealth(runtime selection) error = %v", err)
	}
	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) { return nil, errTestBoundary }, nil
	}
	if _, err := facade.ResolveHealth(t.Context(), operation.request, resolution); !errors.Is(err, errTestBoundary) {
		t.Fatalf("ResolveHealth(runtime open) error = %v", err)
	}
	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) {
			return nil, nil //nolint:nilnil // Exercise the facade's nil runtime rejection.
		}, nil
	}
	if _, err := facade.ResolveHealth(t.Context(), operation.request, resolution); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ResolveHealth(nil runtime) error = %v", err)
	}
	facade.selectRuntime = func(domain.RuntimeKind) (OperationRuntimeFactory, error) {
		return func(context.Context) (OperationRuntime, error) { return runtime, nil }, nil
	}
	facade.openState = func(context.Context) (*store.Store, error) { return nil, errTestBoundary }
	if _, err := facade.ResolveHealth(t.Context(), operation.request, resolution); !errors.Is(err, errTestBoundary) {
		t.Fatalf("ResolveHealth(state open) error = %v", err)
	}
	facade.openState = func(context.Context) (*store.Store, error) {
		return nil, nil //nolint:nilnil // Exercise the facade's nil state rejection.
	}
	if _, err := facade.ResolveHealth(t.Context(), operation.request, resolution); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("ResolveHealth(nil state) error = %v", err)
	}
	directState := openMutationTestStore(t)
	if _, err := facade.resolveHealth(
		t.Context(),
		operation.request,
		resolution,
		directState,
		&operationRuntimeFixture{testRuntime: operation.runtime, events: new([]string)},
	); !errors.Is(err, ErrInvalidRequest) {
		closeMutationTestStore(t, directState)
		t.Fatalf("resolveHealth(incomplete runtime) error = %v", err)
	}
	closeMutationTestStore(t, directState)
	closedState := openMutationTestStore(t)
	closeMutationTestStore(t, closedState)
	if _, err := facade.resolveHealth(
		t.Context(), operation.request, resolution, closedState, runtime,
	); err == nil {
		t.Fatal("resolveHealth(closed state) error = nil")
	}

	state := openMutationTestStore(t)
	facade.openState = func(context.Context) (*store.Store, error) { return state, nil }
	if _, err := facade.ResolveHealth(t.Context(), operation.request, resolution); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("ResolveHealth(stale confirmation) error = %v", err)
	}
	if runtime.closes != 3 {
		t.Fatalf("ResolveHealth() runtime closes = %d", runtime.closes)
	}
}

func TestResolveHealthCancelsOnlyPendingAdoptionIntent(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	var observation WorkloadObservation
	operation.runtime.observe = func(_ context.Context, workload domain.DesiredWorkload) (WorkloadObservation, error) {
		observation = presentObservation(true, true, domain.OwnershipUnmanaged)
		observation.RuntimeMounts = testRuntimeMounts(workload)
		observation.StorageDigest = storageDigestForTest(workload)

		return observation, nil
	}
	preparation, err := operation.service.Prepare(t.Context(), operation.request)
	if err != nil || preparation.Plan.Kind != PlanAdopt {
		t.Fatalf("Prepare(adoption) = %#v, %v", preparation, err)
	}
	state := openMutationTestStore(t)
	lock, err := state.TryLockService(testProjectName, testServiceName)
	if err != nil {
		closeMutationTestStore(t, state)
		t.Fatalf("TryLockService() error = %v", err)
	}
	transaction, err := lock.BeginTransaction(t.Context(), transactionIntent(preparation))
	if err != nil {
		closeMutationTestLock(t, lock)
		closeMutationTestStore(t, state)
		t.Fatalf("BeginTransaction() error = %v", err)
	}
	if err = lock.Close(); err != nil {
		closeMutationTestStore(t, state)
		t.Fatalf("ServiceLock.Close() error = %v", err)
	}

	runtime := &healthResolutionOperationRuntime{
		testRuntime:                  operation.runtime,
		healthRollbackRuntimeFixture: newHealthRollbackRuntime(testWorkloadEffect(t)),
	}
	facade := newOperationTestFacade(
		operation,
		runtime,
		&operationReaderFixture{testTransactions: operation.transactions, events: new([]string)},
	)
	facade.openState = func(context.Context) (*store.Store, error) { return state, nil }
	resolution := HealthResolution{
		Transaction: transaction.ID.String(),
		Action:      HealthResolutionCancelAdoption,
		Observation: healthResolutionObservation(observation),
	}
	for _, action := range []HealthResolutionAction{
		HealthResolutionRollback,
		HealthResolutionRetryRestoreStart,
		"new",
	} {
		candidate := resolution
		candidate.Action = action
		if _, resolutionErr := facade.resolveHealth(
			t.Context(), operation.request, candidate, state, runtime,
		); !errors.Is(resolutionErr, ErrInvalidRequest) {
			closeMutationTestStore(t, state)
			t.Fatalf("resolveHealth(%q) error = %v", action, resolutionErr)
		}
	}

	plan, err := facade.ResolveHealth(t.Context(), operation.request, resolution)
	if err != nil || plan.Kind != PlanResume || runtime.closes != 1 {
		t.Fatalf("ResolveHealth(cancel adoption) = %#v, %v, closes %d", plan, err, runtime.closes)
	}
}

func TestWorkloadHealthStopApplyRejectsUnprovenRuntimeState(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := store.TransactionID{1}.String()
	if _, err := runWorkloadHealthStop(
		t.Context(), nil, store.TransactionID{1}, 1, workload, nil,
	); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("runWorkloadHealthStop(nil runtime) error = %v", err)
	}
	matching := startedWorkloadEffectEvidence(workload, transaction)
	mismatched := matching
	mismatched.ConfigurationMatches = false
	tests := []struct {
		name          string
		probe         WorkloadEffectProbe
		probeErr      error
		transitionErr error
		wantErr       error
		wantStop      bool
	}{
		{name: "probe failure", probeErr: errTestBoundary, wantErr: errTestBoundary},
		{name: "missing", probe: WorkloadEffectProbe{State: WorkloadEffectProbeMissing}},
		{name: "missing with evidence", probe: WorkloadEffectProbe{State: WorkloadEffectProbeMissing, Workload: matching}, wantErr: ErrConflictingState},
		{name: "unknown", probe: WorkloadEffectProbe{State: WorkloadEffectProbeUnknown}, wantErr: ErrConflictingState},
		{name: "invalid state", probe: WorkloadEffectProbe{State: WorkloadEffectProbeState(99)}, wantErr: ErrConflictingState},
		{name: "mismatched", probe: WorkloadEffectProbe{State: WorkloadEffectProbeObserved, Workload: mismatched}, wantErr: ErrConflictingState},
		{name: "created", probe: healthStopProbe(matching, WorkloadLifecycleCreated)},
		{name: "exited", probe: healthStopProbe(matching, WorkloadLifecycleExited)},
		{name: "running", probe: healthStopProbe(matching, WorkloadLifecycleRunning), wantStop: true},
		{name: "stop failure", probe: healthStopProbe(matching, WorkloadLifecycleRunning), transitionErr: errTestBoundary, wantErr: errTestBoundary, wantStop: true},
		{name: "restarting", probe: healthStopProbe(matching, WorkloadLifecycleRestarting), wantErr: ErrHealthPending},
		{name: "unknown lifecycle", probe: healthStopProbe(matching, WorkloadLifecycleUnknown), wantErr: ErrConflictingState},
		{name: "paused", probe: healthStopProbe(matching, WorkloadLifecyclePaused), wantErr: ErrConflictingState},
		{name: "removing", probe: healthStopProbe(matching, WorkloadLifecycleRemoving), wantErr: ErrConflictingState},
		{name: "dead", probe: healthStopProbe(matching, WorkloadLifecycleDead), wantErr: ErrConflictingState},
		{name: "invalid lifecycle", probe: healthStopProbe(matching, WorkloadLifecycle(99)), wantErr: ErrConflictingState},
	}
	for _, test := range tests {
		events := make([]string, 0, 1)
		runtime := &healthStopRuntimeFixture{
			testWorkloadStartRuntime: &testWorkloadStartRuntime{
				events: &events, probe: test.probe, probeErr: test.probeErr,
			},
			transitionErr: test.transitionErr,
		}
		effect := &workloadHealthStopEffect{runtime: runtime, workload: workload, transaction: transaction}

		err := effect.Apply(t.Context())
		if !errors.Is(err, test.wantErr) || test.wantStop != (runtime.transition.Kind == WorkloadTransitionStop) {
			t.Fatalf("workloadHealthStopEffect.Apply(%s) = %v, transition %#v", test.name, err, runtime.transition)
		}
	}
}

func TestWorkloadHealthStopProbeReportsExactPostcondition(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := store.TransactionID{1}.String()
	matching := startedWorkloadEffectEvidence(workload, transaction)
	mismatched := matching
	mismatched.Name = "other"
	tests := []struct {
		name      string
		probe     WorkloadEffectProbe
		probeErr  error
		wantErr   error
		satisfied bool
	}{
		{name: "probe failure", probeErr: errTestBoundary, wantErr: errTestBoundary},
		{name: "missing", probe: WorkloadEffectProbe{State: WorkloadEffectProbeMissing}, satisfied: true},
		{name: "missing with evidence", probe: WorkloadEffectProbe{State: WorkloadEffectProbeMissing, Workload: matching}, wantErr: ErrConflictingState},
		{name: "unknown", probe: WorkloadEffectProbe{State: WorkloadEffectProbeUnknown}, wantErr: ErrConflictingState},
		{name: "invalid state", probe: WorkloadEffectProbe{State: WorkloadEffectProbeState(99)}, wantErr: ErrConflictingState},
		{name: "mismatched", probe: WorkloadEffectProbe{State: WorkloadEffectProbeObserved, Workload: mismatched}, wantErr: ErrConflictingState},
		{name: "created", probe: healthStopProbe(matching, WorkloadLifecycleCreated), satisfied: true},
		{name: "exited", probe: healthStopProbe(matching, WorkloadLifecycleExited), satisfied: true},
		{name: "running", probe: healthStopProbe(matching, WorkloadLifecycleRunning)},
		{name: "restarting", probe: healthStopProbe(matching, WorkloadLifecycleRestarting), wantErr: ErrHealthPending},
		{name: "unknown lifecycle", probe: healthStopProbe(matching, WorkloadLifecycleUnknown), wantErr: ErrConflictingState},
		{name: "paused", probe: healthStopProbe(matching, WorkloadLifecyclePaused), wantErr: ErrConflictingState},
		{name: "removing", probe: healthStopProbe(matching, WorkloadLifecycleRemoving), wantErr: ErrConflictingState},
		{name: "dead", probe: healthStopProbe(matching, WorkloadLifecycleDead), wantErr: ErrConflictingState},
		{name: "invalid lifecycle", probe: healthStopProbe(matching, WorkloadLifecycle(99)), wantErr: ErrConflictingState},
	}
	for _, test := range tests {
		events := make([]string, 0, 1)
		runtime := &healthStopRuntimeFixture{testWorkloadStartRuntime: &testWorkloadStartRuntime{
			events: &events, probe: test.probe, probeErr: test.probeErr,
		}}
		effect := &workloadHealthStopEffect{runtime: runtime, workload: workload, transaction: transaction}

		postcondition, err := effect.Probe(t.Context())
		if !errors.Is(err, test.wantErr) || err == nil && (postcondition.Digest == (domain.Digest{}) || postcondition.Satisfied != test.satisfied) {
			t.Fatalf("workloadHealthStopEffect.Probe(%s) = %#v, %v", test.name, postcondition, err)
		}
	}
}

func healthStopProbe(evidence WorkloadEffectEvidence, lifecycle WorkloadLifecycle) WorkloadEffectProbe {
	evidence.Lifecycle = lifecycle

	return WorkloadEffectProbe{State: WorkloadEffectProbeObserved, Workload: evidence}
}

func TestRollbackUpgradeHealthRestoresPredecessorBeforeFailingTransaction(t *testing.T) {
	t.Parallel()

	state, mutation, baseRuntime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	mutation.preparation.Workload.Healthcheck = &domain.Healthcheck{Test: []string{testHealthCommand, testTrueCommand}}
	runtime := &healthRollbackRuntimeFixture{upgradeRuntimeFixture: baseRuntime}

	err := runUpgrade(t.Context(), mutation, runtime, bootstrapCredentials{})
	if !errors.Is(err, ErrHealthDegraded) ||
		mutation.preparation.Transaction.State != store.TransactionHealthDegraded ||
		baseRuntime.predecessor.Lifecycle != WorkloadLifecycleExited {
		t.Fatalf("runUpgrade(unhealthy) = %v, transaction %#v, predecessor %#v",
			err,
			mutation.preparation.Transaction,
			baseRuntime.predecessor,
		)
	}
	if err = refreshMutationActions(t.Context(), mutation, state); err != nil {
		t.Fatalf("refreshMutationActions() error = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanHealthDegraded
	mutation.preparation.Plan.Observation = healthRollbackObservation(baseRuntime.workload.Workload)
	coreActions := append([]store.Action(nil), mutation.preparation.Actions...)
	if action, sequence, stopErr := upgradeHealthStopAction(t.Context(), mutation); stopErr != nil || action != (store.Action{}) || sequence != int64(len(coreActions)+1) {
		t.Fatalf("upgradeHealthStopAction(core) = %#v, %d, %v", action, sequence, stopErr)
	}
	for _, mutate := range []func(*Preparation){
		func(value *Preparation) { value.Actions = nil },
		func(value *Preparation) { value.Actions[0].State = store.ActionStateIntent },
		func(value *Preparation) { value.Actions = append(value.Actions, store.Action{Kind: testOtherValue}) },
	} {
		candidate := *mutation
		candidate.preparation = mutation.preparation
		candidate.preparation.Actions = append([]store.Action(nil), coreActions...)
		mutate(&candidate.preparation)
		if _, _, stopErr := upgradeHealthStopAction(t.Context(), &candidate); !errors.Is(stopErr, ErrConflictingState) {
			t.Fatalf("upgradeHealthStopAction(invalid) error = %v", stopErr)
		}
	}
	stopIntent := workloadHealthStopIntent(
		int64(len(coreActions)+1),
		mutation.preparation.Workload,
		mutation.preparation.Transaction.ID.String(),
	)
	stop := completedEffectAction(
		mutation.preparation.Transaction.ID,
		stopIntent,
		domain.Hash([]byte("stopped upgrade candidate")),
	)
	withStop := *mutation
	withStop.preparation = mutation.preparation
	withStop.preparation.Actions = append(append([]store.Action(nil), coreActions...), stop)
	if action, sequence, stopErr := upgradeHealthStopAction(t.Context(), &withStop); stopErr != nil || action != stop || sequence != stop.Sequence {
		t.Fatalf("upgradeHealthStopAction(stop) = %#v, %d, %v", action, sequence, stopErr)
	}

	err = rollbackHealthCandidate(t.Context(), mutation, state, runtime)
	if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed ||
		baseRuntime.predecessor.ID != mutation.preparation.Applied.WorkloadID ||
		baseRuntime.predecessor.Name != mutation.preparation.Workload.ContainerName ||
		baseRuntime.predecessor.Lifecycle != WorkloadLifecycleRunning {
		t.Fatalf("rollbackHealthCandidate(upgrade) = %v, transaction %#v, predecessor %#v",
			err,
			mutation.preparation.Transaction,
			baseRuntime.predecessor,
		)
	}
	assertBootstrapActions(t, state, mutation.preparation.Transaction.ID, []string{
		workloadStopActionKind,
		workloadRenameActionKind,
		workloadCreateActionKind,
		workloadStartActionKind,
		workloadHealthStopActionKind,
		workloadDiscardActionKind,
		workloadRenameActionKind,
		workloadRestoreStartActionKind,
	})
}

func TestRollbackUpgradeHealthPropagatesJournalAndRefreshFailures(t *testing.T) {
	t.Parallel()

	t.Run("journal", func(t *testing.T) {
		state, mutation, runtime := newUpgradeMutation(t)
		if err := mutation.lock.Close(); err != nil {
			t.Fatalf("ServiceLock.Close() error = %v", err)
		}
		if err := rollbackUpgradeHealth(t.Context(), mutation, state, runtime); !errors.Is(err, store.ErrInvalidState) {
			t.Fatalf("rollbackUpgradeHealth(closed lock) error = %v", err)
		}
		mutation.lock = nil
		closeMutationTestStore(t, state)
	})

	t.Run("refresh", func(t *testing.T) {
		state, mutation, runtime := newUpgradeMutation(t)
		defer closeBootstrapMutation(t, state, mutation)
		closedState := openMutationTestStore(t)
		closeMutationTestStore(t, closedState)
		if err := rollbackUpgradeHealth(t.Context(), mutation, closedState, runtime); err == nil {
			t.Fatal("rollbackUpgradeHealth(closed refresh state) error = nil")
		}
	})
}

func TestRetryRestoreStartJournalsEveryStoppedPredecessorRestart(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newStoppedRestoreRetryMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	baseActions := len(mutation.preparation.Actions)
	runtime.predecessorHealth = WorkloadHealth{Status: WorkloadHealthStarting}

	err := retryRestoreStart(t.Context(), mutation, runtime)
	if !errors.Is(err, ErrHealthPending) ||
		mutation.preparation.Transaction.State != store.TransactionDegraded {
		t.Fatalf("retryRestoreStart(starting) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	if len(mutation.preparation.Actions) != baseActions+1 ||
		mutation.preparation.Actions[len(mutation.preparation.Actions)-1].Kind != workloadRestoreStartActionKind {
		t.Fatalf("first retry actions = %#v", mutation.preparation.Actions)
	}

	runtime.predecessor.Lifecycle = WorkloadLifecycleExited
	runtime.predecessorHealth = WorkloadHealth{Status: WorkloadHealthHealthy}
	mutation.preparation.Plan.Observation.Lifecycle = WorkloadLifecycleExited
	mutation.preparation.Plan.Observation.Health = WorkloadHealth{Status: WorkloadHealthUnknown}
	err = retryRestoreStart(t.Context(), mutation, runtime)
	if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed {
		t.Fatalf("retryRestoreStart(healthy) = %v, transaction %#v", err, mutation.preparation.Transaction)
	}
	actions := readUpgradeActions(t, state, mutation)
	if len(actions) != baseActions+2 ||
		actions[len(actions)-1].Kind != workloadRestoreStartActionKind ||
		runtime.transitionApplies[WorkloadTransitionRestoreStart] != 3 {
		t.Fatalf("repeated retry actions = %#v, applies %d",
			actions,
			runtime.transitionApplies[WorkloadTransitionRestoreStart],
		)
	}
}

func TestRetryRestoreStartPreservesDegradedTransactionForUnhealthyPredecessor(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newStoppedRestoreRetryMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.predecessorHealth = WorkloadHealth{Status: WorkloadHealthUnhealthy, FailingStreak: 2}

	err := retryRestoreStart(t.Context(), mutation, runtime)
	if !errors.Is(err, ErrHealthDegraded) ||
		mutation.preparation.Transaction.State != store.TransactionDegraded ||
		runtime.predecessor.Lifecycle != WorkloadLifecycleRunning {
		t.Fatalf("retryRestoreStart(unhealthy) = %v, transaction %#v, predecessor %#v",
			err,
			mutation.preparation.Transaction,
			runtime.predecessor,
		)
	}
}

func TestRunRestoreRecoversUnknownRetryStartWithoutReplay(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newStoppedRestoreRetryMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.predecessorHealth = WorkloadHealth{Status: WorkloadHealthHealthy}
	runtime.transitionProbeAt[runtime.transitionProbes+1] = errTestBoundary

	err := retryRestoreStart(t.Context(), mutation, runtime)
	if !errors.Is(err, errTestBoundary) {
		t.Fatalf("retryRestoreStart(interrupted) = %v", err)
	}
	before := runtime.transitionApplies[WorkloadTransitionRestoreStart]
	delete(runtime.transitionProbeAt, runtime.transitionProbes)
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)

	err = runRestore(t.Context(), mutation, runtime)
	if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed ||
		runtime.transitionApplies[WorkloadTransitionRestoreStart] != before {
		t.Fatalf("runRestore(retry recovery) = %v, transaction %#v, applies %d -> %d",
			err,
			mutation.preparation.Transaction,
			before,
			runtime.transitionApplies[WorkloadTransitionRestoreStart],
		)
	}
}

func TestRetryRestoreStartResumesPendingJournalAction(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newStoppedRestoreRetryMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.predecessorHealth = WorkloadHealth{Status: WorkloadHealthHealthy}
	runtime.transitionProbeAt[runtime.transitionProbes+1] = errTestBoundary

	err := retryRestoreStart(t.Context(), mutation, runtime)
	if !errors.Is(err, errTestBoundary) {
		t.Fatalf("retryRestoreStart(interrupted) = %v", err)
	}
	before := runtime.transitionApplies[WorkloadTransitionRestoreStart]
	runtime.predecessor.Lifecycle = WorkloadLifecycleExited
	delete(runtime.transitionProbeAt, runtime.transitionProbes)
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)

	err = retryRestoreStart(t.Context(), mutation, runtime)
	if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed ||
		runtime.transitionApplies[WorkloadTransitionRestoreStart] != before+1 {
		t.Fatalf("retryRestoreStart(resumed) = %v, transaction %#v, applies %d -> %d",
			err,
			mutation.preparation.Transaction,
			before,
			runtime.transitionApplies[WorkloadTransitionRestoreStart],
		)
	}
}

func TestRetryRestoreStartRequiresExactExitedAppliedPredecessor(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*WorkloadObservation)
	}{
		{name: "missing", mutate: func(value *WorkloadObservation) { *value = missingObservation() }},
		{name: "running", mutate: func(value *WorkloadObservation) { value.Lifecycle = WorkloadLifecycleRunning }},
		{name: "created", mutate: func(value *WorkloadObservation) { value.Lifecycle = WorkloadLifecycleCreated }},
		{name: "identity", mutate: func(value *WorkloadObservation) { value.ID = testOtherValue }},
		{name: "configuration", mutate: func(value *WorkloadObservation) {
			value.ConfigurationDigest = domain.Hash([]byte("other configuration"))
		}},
		{name: "ownership", mutate: func(value *WorkloadObservation) {
			value.Ownership = domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newStoppedRestoreRetryMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			test.mutate(&mutation.preparation.Plan.Observation)
			if err := retryRestoreStart(t.Context(), mutation, runtime); !errors.Is(err, ErrConflictingState) {
				t.Fatalf("retryRestoreStart(%s) = %v", test.name, err)
			}
		})
	}
}

func newStoppedRestoreRetryMutation(
	t *testing.T,
) (*store.Store, *boundMutation, *upgradeRuntimeFixture) {
	t.Helper()

	state, mutation, runtime := newUpgradeMutation(t)
	predecessor := mutation.preparation.Plan.Observation
	mutation.preparation.Workload.Healthcheck = &domain.Healthcheck{
		Test: []string{testHealthCommand, testTrueCommand},
	}
	mutation.preparation.Applied.Healthcheck = true
	healthRuntime := &healthRollbackRuntimeFixture{upgradeRuntimeFixture: runtime}
	if err := runUpgrade(t.Context(), mutation, healthRuntime, bootstrapCredentials{}); !errors.Is(err, ErrHealthDegraded) {
		closeBootstrapMutation(t, state, mutation)
		t.Fatalf("runUpgrade(unhealthy) = %v", err)
	}
	if err := refreshMutationActions(t.Context(), mutation, state); err != nil {
		closeBootstrapMutation(t, state, mutation)
		t.Fatalf("refreshMutationActions(upgrade) = %v", err)
	}
	mutation.preparation.Plan.Kind = PlanHealthDegraded
	mutation.preparation.Plan.Observation = healthRollbackObservation(runtime.workload.Workload)
	runtime.predecessorHealth = WorkloadHealth{Status: WorkloadHealthStarting}
	if err := rollbackHealthCandidate(t.Context(), mutation, state, healthRuntime); !errors.Is(err, ErrHealthPending) {
		closeBootstrapMutation(t, state, mutation)
		t.Fatalf("rollbackHealthCandidate(starting predecessor) = %v", err)
	}
	if err := refreshMutationActions(t.Context(), mutation, state); err != nil {
		closeBootstrapMutation(t, state, mutation)
		t.Fatalf("refreshMutationActions(restore) = %v", err)
	}

	runtime.predecessor.Lifecycle = WorkloadLifecycleExited
	predecessor.Lifecycle = WorkloadLifecycleExited
	predecessor.Health = WorkloadHealth{Status: WorkloadHealthUnknown}
	mutation.preparation.Plan.Kind = PlanRestore
	mutation.preparation.Plan.Observation = predecessor

	return state, mutation, runtime
}

func TestCancelPendingAdoptionOnlyFailsLocalTransaction(t *testing.T) {
	t.Parallel()

	state, mutation := newAdoptMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	mutation.preparation.Plan.Kind = PlanResume
	if err := cancelPendingAdoption(t.Context(), nil); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("cancelPendingAdoption(nil) error = %v", err)
	}
	invalid := *mutation
	invalid.preparation = mutation.preparation
	invalid.preparation.Actions = []store.Action{{Kind: testOtherValue}}
	if err := cancelPendingAdoption(t.Context(), &invalid); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("cancelPendingAdoption(actions) error = %v", err)
	}
	invalid.preparation = mutation.preparation
	invalid.preparation.Plan.Observation = emptyObservation()
	if err := cancelPendingAdoption(t.Context(), &invalid); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("cancelPendingAdoption(observation) error = %v", err)
	}

	if err := cancelPendingAdoption(t.Context(), mutation); err != nil {
		t.Fatalf("cancelPendingAdoption() error = %v", err)
	}
	if mutation.preparation.Transaction.State != store.TransactionFailed {
		t.Fatalf("cancelled adoption transaction = %#v", mutation.preparation.Transaction)
	}
	if _, found, err := state.AppliedService(t.Context(), testProjectName, testServiceName); err != nil || found {
		t.Fatalf("AppliedService(cancelled adoption) = found %t, error %v", found, err)
	}
}

func TestHealthRollbackRequiresExactDegradedCandidate(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	workload.Healthcheck = &domain.Healthcheck{Test: []string{testHealthCommand, testTrueCommand}}
	transaction := store.Transaction{ID: store.TransactionID{1}}
	evidence := startedWorkloadEffectEvidence(workload, transaction.ID.String())
	matching := healthRollbackObservation(evidence)
	mismatched := matching
	mismatched.ConfigurationMatches = false
	tests := []struct {
		name        string
		observation WorkloadObservation
		wantStop    bool
		wantErr     error
	}{
		{name: "missing", observation: missingObservation()},
		{name: "unknown", observation: emptyObservation(), wantErr: ErrConflictingState},
		{name: "mismatched", observation: mismatched, wantErr: ErrConflictingState},
		{name: "healthy", observation: matching, wantErr: ErrSnapshotStale},
		{name: "starting", observation: healthRollbackObservationWith(matching, WorkloadLifecycleRunning, WorkloadHealth{Status: WorkloadHealthStarting}), wantErr: ErrHealthPending},
		{name: "unhealthy", observation: healthRollbackObservationWith(matching, WorkloadLifecycleRunning, WorkloadHealth{Status: WorkloadHealthUnhealthy}), wantStop: true},
		{name: "restarting", observation: healthRollbackObservationWith(matching, WorkloadLifecycleRestarting, matching.Health), wantErr: ErrHealthPending},
		{name: "created", observation: healthRollbackObservationWith(matching, WorkloadLifecycleCreated, matching.Health)},
		{name: "exited", observation: healthRollbackObservationWith(matching, WorkloadLifecycleExited, matching.Health)},
		{name: "invalid lifecycle", observation: healthRollbackObservationWith(matching, WorkloadLifecycle(99), matching.Health), wantErr: ErrConflictingState},
	}
	for _, lifecycle := range []WorkloadLifecycle{
		WorkloadLifecycleUnknown, WorkloadLifecyclePaused, WorkloadLifecycleRemoving, WorkloadLifecycleDead,
	} {
		tests = append(tests, struct {
			name        string
			observation WorkloadObservation
			wantStop    bool
			wantErr     error
		}{
			name:        "rejected lifecycle",
			observation: healthRollbackObservationWith(matching, lifecycle, matching.Health),
			wantErr:     ErrConflictingState,
		})
	}
	for _, test := range tests {
		preparation := Preparation{
			Plan:        Plan{Observation: test.observation},
			Workload:    workload,
			Transaction: transaction,
		}
		got, err := healthRollbackNeedsStop(preparation)
		if got != test.wantStop || !errors.Is(err, test.wantErr) {
			t.Fatalf("healthRollbackNeedsStop(%s) = %t, %v", test.name, got, err)
		}
	}
}

func healthRollbackObservationWith(
	observation WorkloadObservation,
	lifecycle WorkloadLifecycle,
	health WorkloadHealth,
) WorkloadObservation {
	observation.Lifecycle = lifecycle
	observation.Health = health

	return observation
}

func TestHealthResolutionObservationBindsDisplayedRuntimeState(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := store.Transaction{ID: store.TransactionID{1}}
	evidence := startedWorkloadEffectEvidence(workload, transaction.ID.String())
	observation := healthRollbackObservation(evidence)
	projected := healthResolutionObservation(observation)
	resolution := HealthResolution{
		Transaction: transaction.ID.String(), Action: HealthResolutionRollback,
		Observation: projected,
	}
	if !validHealthResolution(resolution) {
		t.Fatalf("validHealthResolution() rejected %#v", resolution)
	}

	changed := observation
	changed.Health = WorkloadHealth{Status: WorkloadHealthUnhealthy, FailingStreak: 1}
	if healthResolutionObservation(changed) == projected {
		t.Fatal("health state drift preserved confirmation evidence")
	}
	changed = observation
	changed.StartedAt = time.Unix(1, 0).UTC()
	if healthResolutionObservation(changed) == projected {
		t.Fatal("workload start drift preserved confirmation evidence")
	}
	missing := HealthResolutionObservation{State: WorkloadObservationMissing}
	resolution.Observation = missing
	if !validHealthResolution(resolution) {
		t.Fatal("validHealthResolution() rejected missing evidence")
	}
	resolution.Observation.WorkloadID = testServiceName
	if validHealthResolution(resolution) {
		t.Fatal("validHealthResolution() accepted contradictory missing evidence")
	}
	for _, state := range []WorkloadObservationState{WorkloadObservationUnknown, WorkloadObservationState(99)} {
		resolution.Observation = HealthResolutionObservation{State: state}
		if validHealthResolution(resolution) {
			t.Fatalf("validHealthResolution() accepted observation state %d", state)
		}
	}
	resolution.Observation = projected
	resolution.Observation.Ownership.Status = domain.OwnershipStatus(99)
	if validHealthResolution(resolution) {
		t.Fatal("validHealthResolution() accepted invalid ownership")
	}
}

func newHealthRollbackRuntime(_ domain.DesiredWorkload) *healthRollbackRuntimeFixture {
	base := missingBootstrapRuntime()
	base.workload = WorkloadEffectProbe{
		State:    WorkloadEffectProbeMissing,
		Workload: emptyWorkloadEffectEvidence(),
	}

	return &healthRollbackRuntimeFixture{upgradeRuntimeFixture: &upgradeRuntimeFixture{
		bootstrapRuntimeFixture: *base,
		transitionApplies:       make(map[WorkloadTransitionKind]int),
		transitionProbeAt:       make(map[int]error),
		transitionApply:         make(map[WorkloadTransitionKind]error),
		transitionSkip:          make(map[WorkloadTransitionKind]bool),
		startProbeErrAt:         make(map[int]error),
		archives:                make(map[string][]byte),
		putRewrite:              make(map[string][]byte),
		getErrAt:                make(map[int]error),
	}}
}

func healthRollbackObservation(evidence WorkloadEffectEvidence) WorkloadObservation {
	return WorkloadObservation{
		ID: evidence.ID, State: WorkloadObservationPresent,
		ConfigurationDigest:  evidence.ConfigurationDigest,
		StorageDigest:        evidence.StorageDigest,
		RuntimeMounts:        evidence.RuntimeMounts,
		ConfigurationMatches: evidence.ConfigurationMatches,
		Lifecycle:            evidence.Lifecycle,
		Health:               evidence.Health,
		Ownership:            evidence.Ownership,
	}
}
