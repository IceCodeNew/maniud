package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestCompletedWorkloadEffectsRequireExactEvidence(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	preparation := mutation.preparation
	transaction := preparation.Transaction.ID.String()

	missing := workloadEffectDigest(
		workloadEffectMissing,
		workloadCreateActionKind,
		preparation.Workload,
		transaction,
		"",
	)
	result, err := completedWorkloadCreateResult(
		context.Background(),
		store.Action{PostconditionDigest: &missing},
		runtime,
		preparation,
	)
	if err != nil || result.Satisfied {
		t.Fatalf("completed missing create = %#v, %v", result, err)
	}

	result, err = completedWorkloadCreateResult(
		context.Background(),
		store.Action{},
		runtime,
		preparation,
	)
	if !errors.Is(err, ErrConflictingState) || result.Digest != (domain.Digest{}) {
		t.Fatalf("completed unproven create = %#v, %v", result, err)
	}

	runtime.workload = startedWorkloadEffectEvidenceProbe(preparation.Workload, transaction)
	runtime.startProbeErrAt[1] = errTestBoundary
	if _, err = completedWorkloadStartResult(
		context.Background(),
		store.Action{},
		runtime,
		preparation,
	); !errors.Is(err, errTestBoundary) {
		t.Fatalf("completed start probe error = %v", err)
	}

	delete(runtime.startProbeErrAt, 1)
	if _, err = completedWorkloadStartResult(
		context.Background(),
		store.Action{},
		runtime,
		preparation,
	); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completed start without digest = %v", err)
	}
}

func TestWorkloadStartRejectsObservedIdentityDrift(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := store.TransactionID{1}.String()
	evidence := startedWorkloadEffectEvidence(workload, transaction)
	evidence.Ownership.Reference = domain.Hash([]byte("drift"))
	effect := workloadStartEffect{
		runtime: &bootstrapRuntimeFixture{
			workload: WorkloadEffectProbe{State: WorkloadEffectProbeObserved, Workload: evidence},
		},
		workload:    workload,
		transaction: transaction,
	}

	if _, err := effect.Probe(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("ProbeStartedWorkload(drift) = %v", err)
	}
}

func TestUpgradeImageAndTransitionResumeHelpers(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	journey := newUpgradeJourney(mutation.preparation)
	stopIntent := workloadTransitionIntent(1, journey.stop)
	mutation.preparation.Actions = []store.Action{{
		TransactionID: mutation.preparation.Transaction.ID,
		Sequence:      stopIntent.Sequence,
		Kind:          stopIntent.Kind,
		State:         store.ActionStateIntent,
		IntentDigest:  stopIntent.IntentDigest,
	}}

	cursor, sequence, resolved, err := settleUpgradeImage(
		context.Background(),
		mutation,
		runtime,
		bootstrapCredentials{},
	)
	if err != nil || cursor != 0 || sequence != 1 || resolved {
		t.Fatalf("settleUpgradeImage(resume) = %d, %d, %t, %v", cursor, sequence, resolved, err)
	}

	bad := mutation.preparation.Actions[0]
	bad.IntentDigest = domain.Hash([]byte("bad"))
	if _, _, err = settleUpgradeTransition(
		context.Background(),
		mutation,
		runtime,
		bad,
		1,
		journey.stop,
	); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleUpgradeTransition(conflict) = %v", err)
	}

	satisfiedDigest := workloadTransitionPostcondition(journey.stop, true).Digest
	completed := mutation.preparation.Actions[0]
	completed.State = store.ActionStateCompleted
	completed.PostconditionDigest = &satisfiedDigest
	satisfied, completedEvidence, err := settleUpgradeTransition(
		context.Background(),
		mutation,
		runtime,
		completed,
		1,
		journey.stop,
	)
	if err != nil || !satisfied || !completedEvidence {
		t.Fatalf("settleUpgradeTransition(completed) = %t, %t, %v", satisfied, completedEvidence, err)
	}
}

func TestUpgradeFailureResolutionHelpersFailClosed(t *testing.T) {
	t.Parallel()

	if !errors.Is(effectResultError(EffectPostcondition{}, nil), ErrConflictingState) {
		t.Fatal("negative effect result succeeded")
	}
	if !errors.Is(resolveTransitionFailure(
		context.Background(),
		nil,
		store.TransactionFailed,
		false,
		false,
		nil,
	), ErrConflictingState) {
		t.Fatal("unresolved transition without cause succeeded")
	}
	if !errors.Is(resolveEffectFailure(
		context.Background(),
		nil,
		EffectPostcondition{},
		errTestBoundary,
	), errTestBoundary) {
		t.Fatal("unresolved effect changed cause")
	}
	if !errors.Is(resolveUpgradeFailure(
		context.Background(),
		nil,
		store.TransactionFailed,
		errTestBoundary,
	), ErrInvalidRequest) {
		t.Fatal("nil failure resolver succeeded")
	}
}

func TestUpgradeValidationRejectsInvalidMutationEvidence(t *testing.T) {
	t.Parallel()

	if validUpgradeMutation(nil) {
		t.Fatal("nil upgrade mutation accepted")
	}

	state, mutation, _ := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)

	invalid := *mutation
	invalid.preparation.HasApplied = false
	if validUpgradeMutation(&invalid) {
		t.Fatal("upgrade without applied baseline accepted")
	}

	invalid = *mutation
	invalid.preparation.Transaction.PredecessorWorkloadID = "other"
	if validUpgradeMutation(&invalid) {
		t.Fatal("upgrade with predecessor drift accepted")
	}

	if !validUpgradeActions([]store.Action{{Kind: imagePullActionKind}}) {
		t.Fatal("valid image-first upgrade prefix rejected")
	}
}

func TestUpgradeCompletedNegativeWorkloadActionsDegrade(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{workloadCreateActionKind, workloadStartActionKind} {
		t.Run(kind, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			run := completedNegativeWorkloadStage(mutation, runtime, kind)

			if err := run(context.Background()); !errors.Is(err, ErrConflictingState) ||
				mutation.preparation.Transaction.State != store.TransactionDegraded {
				t.Fatalf("completed negative %s = %v, transaction %#v", kind, err, mutation.preparation.Transaction)
			}
		})
	}
}

func completedNegativeWorkloadStage(
	mutation *boundMutation,
	runtime *upgradeRuntimeFixture,
	kind string,
) func(context.Context) error {
	transaction := mutation.preparation.Transaction.ID.String()
	execution := &upgradeExecution{
		mutation: mutation,
		runtime:  runtime,
		journey:  newUpgradeJourney(mutation.preparation),
		sequence: 1,
	}
	if kind == workloadCreateActionKind {
		missing := workloadEffectDigest(
			workloadEffectMissing,
			kind,
			mutation.preparation.Workload,
			transaction,
			"",
		)
		execution.actions = []store.Action{completedEffectAction(
			mutation.preparation.Transaction.ID,
			workloadCreateIntent(1, mutation.preparation.Workload, transaction),
			missing,
		)}

		return execution.create
	}

	evidence := createdWorkloadEffectEvidence(
		mutation.preparation.Workload,
		transaction,
	)
	runtime.workload = WorkloadEffectProbe{
		State:    WorkloadEffectProbeObserved,
		Workload: evidence,
	}
	unstarted := workloadObservedEffectDigest(
		workloadEffectUnstarted,
		kind,
		mutation.preparation.Workload,
		transaction,
		testWorkloadEffectID,
		evidence.StorageDigest,
	)
	execution.actions = []store.Action{completedEffectAction(
		mutation.preparation.Transaction.ID,
		workloadStartIntent(1, mutation.preparation.Workload, transaction),
		unstarted,
	)}

	return execution.start
}

func TestUpgradeImagePullResumesThroughExistingAction(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}
	runtime.imageProbeErrAt[2] = errTestBoundary
	if err := runUpgrade(
		context.Background(),
		mutation,
		runtime,
		bootstrapCredentials{},
	); !errors.Is(err, errTestBoundary) {
		t.Fatalf("runUpgrade(unknown image) = %v", err)
	}

	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
	delete(runtime.imageProbeErrAt, 2)
	cursor, sequence, resolved, err := settleUpgradeImage(
		context.Background(),
		mutation,
		runtime,
		bootstrapCredentials{},
	)
	if err != nil || cursor != afterImagePullCursor ||
		sequence != afterImagePullActionSequence || resolved {
		t.Fatalf("settleUpgradeImage(resume) = %d, %d, %t, %v", cursor, sequence, resolved, err)
	}
}

func TestUpgradeFailureResolutionContainsClosedLock(t *testing.T) {
	t.Parallel()

	state, mutation, _ := newUpgradeMutation(t)
	if err := mutation.lock.Close(); err != nil {
		t.Fatalf("Close(service lock) error = %v", err)
	}
	if err := resolveUpgradeFailure(
		context.Background(),
		mutation,
		store.TransactionFailed,
		errTestBoundary,
	); !errors.Is(err, errTestBoundary) || !errors.Is(err, store.ErrInvalidState) {
		t.Fatalf("resolveUpgradeFailure(closed lock) = %v", err)
	}
	mutation.lock = nil
	closeMutationTestStore(t, state)
}

func TestDiscardProbeReportsMatchingReplacementPresent(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	transaction := store.TransactionID{1}.String()
	effect := workloadDiscardEffect{
		runtime: discardRuntimeStub{probe: WorkloadEffectProbe{
			State:    WorkloadEffectProbeObserved,
			Workload: createdWorkloadEffectEvidence(workload, transaction),
		}},
		workload:    workload,
		transaction: transaction,
	}

	postcondition, err := effect.Probe(context.Background())
	if err != nil || postcondition.Satisfied || postcondition.Digest == (domain.Digest{}) {
		t.Fatalf("ProbeDiscardedWorkload(present) = %#v, %v", postcondition, err)
	}
}

func completedEffectAction(
	transaction store.TransactionID,
	intent store.ActionIntent,
	postcondition domain.Digest,
) store.Action {
	return store.Action{
		TransactionID:       transaction,
		Sequence:            intent.Sequence,
		Kind:                intent.Kind,
		State:               store.ActionStateCompleted,
		IntentDigest:        intent.IntentDigest,
		PostconditionDigest: &postcondition,
	}
}

func startedWorkloadEffectEvidenceProbe(
	workload domain.DesiredWorkload,
	transaction string,
) WorkloadEffectProbe {
	return WorkloadEffectProbe{
		State:    WorkloadEffectProbeObserved,
		Workload: startedWorkloadEffectEvidence(workload, transaction),
	}
}
