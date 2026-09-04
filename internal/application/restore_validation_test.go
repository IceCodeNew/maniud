//nolint:funlen // The malformed suffix matrix is clearer as one contiguous state-machine scenario.
package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestPrepareRestoreJourneyRejectsInvalidCoreEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*Preparation, upgradeJourney)
	}{
		{
			name: "pending core action",
			mutate: func(preparation *Preparation, _ upgradeJourney) {
				preparation.Actions[0].State = store.ActionStateEffectOutcomeUnknown
				preparation.Actions[0].PostconditionDigest = nil
			},
		},
		{
			name: "stop digest",
			mutate: func(preparation *Preparation, _ upgradeJourney) {
				bad := domain.Hash([]byte("bad stop"))
				preparation.Actions[0].PostconditionDigest = &bad
			},
		},
		{
			name: "rename digest",
			mutate: func(preparation *Preparation, _ upgradeJourney) {
				bad := domain.Hash([]byte("bad rename"))
				preparation.Actions[1].PostconditionDigest = &bad
			},
		},
		{
			name: "create without rename",
			mutate: func(preparation *Preparation, journey upgradeJourney) {
				negative := workloadTransitionPostcondition(journey.rename, false).Digest
				preparation.Actions[1].PostconditionDigest = &negative
			},
		},
		{
			name: "satisfied remove",
			mutate: func(preparation *Preparation, journey upgradeJourney) {
				satisfied := workloadTransitionPostcondition(journey.remove, true).Digest
				preparation.Actions[len(preparation.Actions)-1].PostconditionDigest = &satisfied
			},
		},
		{
			name: "oversized recovery suffix",
			mutate: func(preparation *Preparation, _ upgradeJourney) {
				preparation.Actions = append(
					preparation.Actions,
					store.Action{Kind: workloadDiscardActionKind},
					store.Action{Kind: workloadDiscardActionKind},
					store.Action{Kind: workloadDiscardActionKind},
					store.Action{Kind: workloadDiscardActionKind},
				)
			},
		},
	}

	runInvalidRestoreCoreCases(t, tests)
}

func TestPrepareRestoreJourneyRejectsMissingStopAndInvalidSuffix(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.transitionSkip[WorkloadTransitionRename] = true
	degradeUpgrade(t, state, mutation, runtime)

	pull := imagePullIntent(1, mutation.preparation.Workload.Image)
	observed := imageEffectDigest(imageEffectObserved, mutation.preparation.Workload.Image)
	mutation.preparation.Actions = []store.Action{{
		TransactionID:       mutation.preparation.Transaction.ID,
		Sequence:            pull.Sequence,
		Kind:                pull.Kind,
		State:               store.ActionStateCompleted,
		IntentDigest:        pull.IntentDigest,
		PostconditionDigest: &observed,
	}}
	if _, err := prepareRestoreJourney(context.Background(), mutation); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("prepareRestoreJourney(no stop) = %v", err)
	}

	prepareRestoreMutation(t, state, mutation)
	sequence := int64(len(mutation.preparation.Actions) + 1)
	healthStop := workloadHealthStopIntent(
		sequence,
		mutation.preparation.Workload,
		mutation.preparation.Transaction.ID.String(),
	)
	mutation.preparation.Actions = append(mutation.preparation.Actions, store.Action{
		TransactionID: mutation.preparation.Transaction.ID,
		Sequence:      sequence,
		Kind:          healthStop.Kind,
		State:         store.ActionStateIntent,
		IntentDigest:  healthStop.IntentDigest,
	})
	if _, err := prepareRestoreJourney(t.Context(), mutation); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("prepareRestoreJourney(incomplete health stop) = %v", err)
	}

	prepareRestoreMutation(t, state, mutation)
	mutation.preparation.Actions = append(mutation.preparation.Actions, store.Action{
		TransactionID: mutation.preparation.Transaction.ID,
		Sequence:      int64(len(mutation.preparation.Actions) + 1),
		Kind:          workloadDiscardActionKind,
		State:         store.ActionStateIntent,
		IntentDigest:  domain.Hash([]byte("wrong suffix")),
	})
	if _, err := prepareRestoreJourney(context.Background(), mutation); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("prepareRestoreJourney(bad suffix) = %v", err)
	}
}

func TestRestoreExecutionRejectsCompletedNegativeTransitions(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	runtime.startUnchanged = true
	degradeUpgrade(t, state, mutation, runtime)
	journey, err := prepareRestoreJourney(context.Background(), mutation)
	if err != nil {
		t.Fatalf("prepareRestoreJourney() error = %v", err)
	}

	execution := restoreExecution{
		mutation: mutation,
		runtime:  runtime,
		journey:  journey,
		sequence: journey.nextSequence + 1,
	}
	negativeRename := workloadTransitionPostcondition(journey.rename, false).Digest
	execution.actions = []store.Action{completedTransitionAction(
		mutation.preparation.Transaction.ID,
		execution.sequence,
		journey.rename,
		negativeRename,
	)}
	if err = execution.rename(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("restore rename negative = %v", err)
	}

	execution.cursor = 0
	execution.sequence = journey.nextSequence
	execution.journey.reverseRename = false
	negativeStart := workloadTransitionPostcondition(journey.restoreStart, false).Digest
	execution.actions = []store.Action{completedTransitionAction(
		mutation.preparation.Transaction.ID,
		execution.sequence,
		journey.restoreStart,
		negativeStart,
	)}
	if err = execution.start(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("restore start negative = %v", err)
	}
}

func TestPrepareRestoreJourneyRejectsMalformedRetrySuffix(t *testing.T) {
	t.Parallel()

	state, mutation, _ := newStoppedRestoreRetryMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	journey, err := prepareRestoreJourney(t.Context(), mutation)
	if err != nil {
		t.Fatalf("prepareRestoreJourney() = %v", err)
	}
	sequence := int64(len(mutation.preparation.Actions) + 1)
	satisfied := workloadTransitionPostcondition(journey.restoreStart, true).Digest
	retry := completedTransitionAction(
		mutation.preparation.Transaction.ID,
		sequence,
		journey.restoreStart,
		satisfied,
	)
	validRetries := mutation.preparation
	validRetries.Actions = append(append([]store.Action(nil), validRetries.Actions...), retry)
	validRetries.Actions = append(validRetries.Actions, completedTransitionAction(
		validRetries.Transaction.ID,
		sequence+1,
		journey.restoreStart,
		satisfied,
	))
	validMutation := *mutation
	validMutation.preparation = validRetries
	if _, prepareErr := prepareRestoreJourney(t.Context(), &validMutation); prepareErr != nil {
		t.Fatalf("prepareRestoreJourney(completed retries) = %v", prepareErr)
	}

	for _, mutate := range []func(*store.Action){
		func(action *store.Action) { action.IntentDigest = domain.Digest{} },
		func(action *store.Action) { action.State = store.ActionStateIntent },
		func(action *store.Action) { action.PostconditionDigest = nil },
	} {
		candidate := mutation.preparation
		candidate.Actions = append(append([]store.Action(nil), candidate.Actions...), retry)
		mutate(&candidate.Actions[len(candidate.Actions)-1])
		candidate.Actions = append(candidate.Actions, completedTransitionAction(
			candidate.Transaction.ID,
			sequence+1,
			journey.restoreStart,
			satisfied,
		))
		copyMutation := *mutation
		copyMutation.preparation = candidate
		if _, prepareErr := prepareRestoreJourney(t.Context(), &copyMutation); !errors.Is(prepareErr, ErrConflictingState) {
			t.Fatalf("prepareRestoreJourney(malformed retry) = %v", prepareErr)
		}
	}

	candidate := mutation.preparation
	candidate.Actions = append([]store.Action(nil), candidate.Actions...)
	for index := range candidate.Actions {
		if candidate.Actions[index].Kind == workloadRestoreStartActionKind {
			candidate.Actions[index].State = store.ActionStateIntent

			break
		}
	}
	candidate.Actions = append(candidate.Actions, retry)
	copyMutation := *mutation
	copyMutation.preparation = candidate
	if _, prepareErr := prepareRestoreJourney(t.Context(), &copyMutation); !errors.Is(prepareErr, ErrConflictingState) {
		t.Fatalf("prepareRestoreJourney(incomplete initial start) = %v", prepareErr)
	}
}

func TestCompleteRestoreContainsProbeAndStateFailures(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	journey := newUpgradeJourney(mutation.preparation)
	restore := newRestoreJourney(journey, 1, false, false).restoreStart
	runtime.predecessor = restore.After
	runtime.transitionProbeAt[1] = errTestBoundary
	if err := completeRestore(context.Background(), mutation, runtime, restore); !errors.Is(err, errTestBoundary) {
		t.Fatalf("completeRestore(probe) = %v", err)
	}

	delete(runtime.transitionProbeAt, 1)
	runtime.predecessor = restore.Before
	if err := completeRestore(context.Background(), mutation, runtime, restore); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completeRestore(mismatch) = %v", err)
	}

	runtime.predecessor = restore.After
	mutation.preparation.Applied.Healthcheck = true
	if err := completeRestore(t.Context(), mutation, runtime, restore); !errors.Is(err, ErrHealthDegraded) {
		t.Fatalf("completeRestore(unobservable health) = %v", err)
	}
	mutation.preparation.Applied.Healthcheck = false
	if err := mutation.lock.Close(); err != nil {
		t.Fatalf("Close(service lock) error = %v", err)
	}
	if err := completeRestore(context.Background(), mutation, runtime, restore); err == nil {
		t.Fatal("completeRestore() succeeded with closed lock")
	}
	mutation.lock = nil
	closeMutationTestStore(t, state)
}

func TestRestoreSettlersRejectConflictingCompletedEvidence(t *testing.T) {
	t.Parallel()

	state, mutation, runtime := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	bad := domain.Hash([]byte("bad discard"))
	action := store.Action{PostconditionDigest: &bad}
	if err := settleWorkloadDiscard(
		context.Background(),
		mutation,
		runtime,
		action,
		1,
	); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleWorkloadDiscard(conflict) = %v", err)
	}
	intent := workloadDiscardIntent(
		1,
		mutation.preparation.Workload,
		mutation.preparation.Transaction.ID.String(),
	)
	action = completedEffectAction(mutation.preparation.Transaction.ID, intent, bad)
	if err := settleWorkloadDiscard(
		context.Background(),
		mutation,
		runtime,
		action,
		1,
	); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleWorkloadDiscard(bad digest) = %v", err)
	}

	if satisfied, err := restoreRenameSatisfied(nil, WorkloadTransition{}, 0, 0); err != nil || satisfied {
		t.Fatalf("restoreRenameSatisfied(absent) = %t, %v", satisfied, err)
	}
}

func completedTransitionAction(
	transaction store.TransactionID,
	sequence int64,
	transition WorkloadTransition,
	postcondition domain.Digest,
) store.Action {
	intent := workloadTransitionIntent(sequence, transition)

	return store.Action{
		TransactionID:       transaction,
		Sequence:            sequence,
		Kind:                intent.Kind,
		State:               store.ActionStateCompleted,
		IntentDigest:        intent.IntentDigest,
		PostconditionDigest: &postcondition,
	}
}

func runInvalidRestoreCoreCases(
	t *testing.T,
	tests []struct {
		name   string
		mutate func(*Preparation, upgradeJourney)
	},
) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			runtime.transitionSkip[WorkloadTransitionRemove] = true
			degradeUpgrade(t, state, mutation, runtime)
			test.mutate(&mutation.preparation, newUpgradeJourney(mutation.preparation))

			if _, err := prepareRestoreJourney(context.Background(), mutation); err == nil {
				t.Fatal("prepareRestoreJourney() accepted invalid core evidence")
			}
		})
	}
}
