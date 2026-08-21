package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type mismatchTest struct {
	name        string
	transaction store.Transaction
	actions     []store.Action
}

func TestPrepareRejectsMismatchedTransactionsAndActions(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	baseline, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare(baseline) error = %v", err)
	}

	exact := exactTransaction(baseline, store.TransactionActive)
	for _, test := range mismatchTests(exact) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testMismatch(t, test)
		})
	}
}

func mismatchTests(exact store.Transaction) []mismatchTest {
	tests := transactionMismatchTests(exact)

	return append(tests, actionMismatchTests(exact)...)
}

func transactionMismatchTests(exact store.Transaction) []mismatchTest {
	return []mismatchTest{
		mismatchTransaction("runtime", exact, func(value *store.Transaction) {
			value.Runtime = domain.RuntimePodman
		}),
		mismatchTransaction("source", exact, func(value *store.Transaction) {
			value.SourceDigest = domain.Hash([]byte(testOtherValue))
		}),
		mismatchTransaction("desired", exact, func(value *store.Transaction) {
			value.EffectiveDigest = domain.Hash([]byte(testOtherValue))
		}),
		mismatchTransaction("execution", exact, func(value *store.Transaction) {
			value.ExecutionDigest = domain.Hash([]byte(testOtherValue))
		}),
		mismatchTransaction("terminal", exact, func(value *store.Transaction) {
			value.State = store.TransactionSucceeded
		}),
	}
}

func actionMismatchTests(exact store.Transaction) []mismatchTest {
	wrongTransaction := mutateAction(action(exact, 1, store.ActionStateIntent), func(value *store.Action) {
		value.TransactionID = store.TransactionID{2}
	})
	missingPostcondition := mutateAction(action(exact, 1, store.ActionStateCompleted), func(value *store.Action) {
		value.PostconditionDigest = nil
	})
	pendingPostcondition := mutateAction(action(exact, 1, store.ActionStateIntent), func(value *store.Action) {
		postcondition := domain.Hash([]byte("postcondition"))
		value.PostconditionDigest = &postcondition
	})
	degraded := mutateTransaction(exact, func(value *store.Transaction) {
		value.State = store.TransactionDegraded
	})

	return []mismatchTest{
		{name: "wrong transaction action", transaction: exact, actions: []store.Action{wrongTransaction}},
		{name: "sequence gap", transaction: exact, actions: []store.Action{
			action(exact, 2, store.ActionStateIntent),
		}},
		{name: "multiple pending", transaction: exact, actions: []store.Action{
			action(exact, 1, store.ActionStateIntent),
			action(exact, 2, store.ActionStateEffectOutcomeUnknown),
		}},
		{name: "completed after pending", transaction: exact, actions: []store.Action{
			action(exact, 1, store.ActionStateIntent), action(exact, 2, store.ActionStateCompleted),
		}},
		{name: "invalid action state", transaction: exact, actions: []store.Action{
			action(exact, 1, store.ActionState(testInvalidValue)),
		}},
		{name: "completed without postcondition", transaction: exact, actions: []store.Action{missingPostcondition}},
		{name: "pending with postcondition", transaction: exact, actions: []store.Action{pendingPostcondition}},
		{name: "degraded pending", transaction: degraded, actions: []store.Action{
			action(degraded, 1, store.ActionStateIntent),
		}},
	}
}

func mismatchTransaction(
	name string,
	exact store.Transaction,
	mutate func(*store.Transaction),
) mismatchTest {
	return mismatchTest{name: name, transaction: mutateTransaction(exact, mutate), actions: nil}
}

func testMismatch(t *testing.T, test mismatchTest) {
	t.Helper()

	operation := newTestOperation(t)
	operation.transactions.unresolved = func(
		context.Context,
		string,
		string,
	) (store.Transaction, bool, error) {
		return test.transaction, true, nil
	}
	operation.transactions.actions = func(
		context.Context,
		store.TransactionID,
	) ([]store.Action, error) {
		return test.actions, nil
	}

	_, err := operation.service.Prepare(context.Background(), operation.request)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("Prepare() error = %v, want ErrConflictingState", err)
	}
}

func TestPrepareContainsActionReadFailure(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)

	baseline, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare(baseline) error = %v", err)
	}

	transaction := exactTransaction(baseline, store.TransactionActive)
	operation.transactions.unresolved = func(context.Context, string, string) (store.Transaction, bool, error) {
		return transaction, true, nil
	}
	operation.transactions.actions = func(context.Context, store.TransactionID) ([]store.Action, error) {
		return nil, errTestBoundary
	}

	_, err = operation.service.Prepare(context.Background(), operation.request)
	if !errors.Is(err, errTestBoundary) {
		t.Fatalf("Prepare() error = %v, want boundary failure", err)
	}
}

func TestClassifiersRejectUnknownEnumValues(t *testing.T) {
	t.Parallel()

	unknownObservation := emptyObservation()

	unknownObservation.State = WorkloadObservationState(255)
	if validWorkloadObservation(unknownObservation, emptyDesiredWorkload()) {
		t.Fatal("validWorkloadObservation(unknown) = true")
	}

	_, err := classifyNewApply(
		unknownObservation,
		emptyDesiredWorkload(),
		RuntimeEvidence{},
		store.AppliedService{},
		false,
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyNewApply(unknown) error = %v", err)
	}

	_, err = classifyNewApply(
		emptyObservation(),
		emptyDesiredWorkload(),
		RuntimeEvidence{},
		store.AppliedService{},
		false,
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyNewApply(zero) error = %v", err)
	}

	transaction := emptyTransaction()
	transaction.State = store.TransactionState("unknown")

	_, err = classifyRecovery(transaction, nil)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyRecovery(unknown) error = %v", err)
	}

	for _, state := range []store.ActionState{store.ActionStateCompleted, store.ActionState("unknown")} {
		pending := action(transaction, 1, state)

		_, err = classifyActiveRecovery(&pending)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("classifyActiveRecovery(%q) error = %v", state, err)
		}
	}
}

func TestObservedApplyRejectsConflictingEvidence(t *testing.T) {
	t.Parallel()

	for _, observation := range []WorkloadObservation{
		presentObservation(true, true, domain.OwnershipConflicting),
		presentObservation(true, false, domain.OwnershipUnmanaged),
		presentObservation(false, true, domain.OwnershipUnmanaged),
		presentObservation(true, true, domain.OwnershipManaged),
		presentObservation(true, true, domain.OwnershipStatus(255)),
	} {
		_, err := classifyUnappliedWorkload(observation)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("classifyUnappliedWorkload(%#v) error = %v", observation, err)
		}
	}
}

func TestAppliedAndRecoveryRelationshipsRejectDrift(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	execution := testExecutionEvidence()
	observation := matchingManagedObservation(workload)
	applied := appliedServiceForObservation(workload, execution, observation)

	drifted := observation
	drifted.ID = testDifferentWorkload
	_, err := classifyAppliedWorkload(drifted, workload, execution, applied)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyAppliedWorkload(drift) error = %v", err)
	}

	upgrade := store.Transaction{
		Kind:                  store.TransactionUpgrade,
		BaseTransactionID:     applied.TransactionID,
		HasBaseTransaction:    true,
		PredecessorWorkloadID: applied.WorkloadID,
	}
	if !transactionMatchesApplied(upgrade, applied, true) {
		t.Fatal("transactionMatchesApplied() rejected an exact upgrade baseline")
	}

	upgrade.PredecessorWorkloadID = testDifferentWorkload
	if transactionMatchesApplied(upgrade, applied, true) ||
		transactionMatchesApplied(store.Transaction{}, applied, true) {
		t.Fatal("transactionMatchesApplied() accepted baseline drift")
	}
}

func TestAdoptedBaselineRetainsUnmanagedWorkload(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	execution := testExecutionEvidence()
	observation := presentObservation(true, true, domain.OwnershipUnmanaged)
	applied := appliedServiceForObservation(workload, execution, observation)
	applied.Kind = store.TransactionAdopt
	applied.EffectiveDigest = workload.EffectiveDigest
	applied.ReferenceDigest = workload.Image.ReferenceDigest
	applied.PlatformManifestDigest = workload.Image.PlatformManifest
	applied.ImageConfigDigest = workload.Image.ImageConfig

	kind, err := classifyAppliedWorkload(observation, workload, execution, applied)
	if err != nil || kind != PlanUnchanged {
		t.Fatalf("classifyAppliedWorkload(adopted) = %q, %v", kind, err)
	}

	observation.Ownership.Service = workload.ServiceName
	_, err = classifyAppliedWorkload(observation, workload, execution, applied)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyAppliedWorkload(adopted labels) = %v", err)
	}
}

func TestMountProbeFallbackWarningPolicy(t *testing.T) {
	t.Parallel()

	storage := persistentStorageUpgradePreparation()
	tests := []struct {
		name        string
		preparation Preparation
		warn        bool
	}{
		{name: "new storage upgrade", preparation: storage, warn: true},
		{name: "new upgrade without storage", preparation: Preparation{Plan: Plan{Kind: PlanUpgrade}}},
		{
			name:        "resumed storage action",
			preparation: recoveryWarningPreparation(PlanResume, store.TransactionUpgrade, storageRestoreActionKind),
			warn:        true,
		},
		{
			name: "unknown storage action", preparation: recoveryWarningPreparation(
				PlanProbeUnknownEffect, store.TransactionUpgrade, storageRestoreActionKind,
			),
			warn: true,
		},
		{
			name:        "restore storage action",
			preparation: recoveryWarningPreparation(PlanRestore, store.TransactionUpgrade, storageRestoreActionKind),
			warn:        true,
		},
		{
			name: "bootstrap recovery", preparation: recoveryWarningPreparation(
				PlanResume, store.TransactionBootstrap, storageRestoreActionKind,
			),
		},
		{
			name: "stable plan", preparation: recoveryWarningPreparation(
				PlanUnchanged, store.TransactionUpgrade, storageRestoreActionKind,
			),
		},
		{
			name:        "upgrade recovery without storage",
			preparation: recoveryWarningPreparation(PlanResume, store.TransactionUpgrade, workloadStopActionKind),
		},
		{
			name: "invalid plan", preparation: recoveryWarningPreparation(
				PlanKind(testInvalidValue), store.TransactionUpgrade, storageRestoreActionKind,
			),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertMountProbeFallbackWarnings(t, test.preparation, test.warn)
		})
	}
}

func assertMountProbeFallbackWarnings(t *testing.T, preparation Preparation, warn bool) {
	t.Helper()

	warnings := mountProbeFallbackWarnings(preparation)
	if len(warnings) == 0 {
		if warn {
			t.Fatalf("mountProbeFallbackWarnings() = %#v", warnings)
		}

		return
	}
	if !warn || len(warnings) != 1 {
		t.Fatalf("mountProbeFallbackWarnings() = %#v", warnings)

		return
	}
	if warnings[0].Code != WarningDaemonMountProbeUnavailable ||
		warnings[0].Message != downgradedMountProbeMessage {
		t.Fatalf("mountProbeFallbackWarnings() = %#v", warnings)
	}
}

func persistentStorageUpgradePreparation() Preparation {
	return Preparation{
		Plan: Plan{
			Kind: PlanUpgrade,
			Observation: WorkloadObservation{
				State: WorkloadObservationPresent,
				RuntimeMounts: []domain.RuntimeMount{{
					Kind: domain.MountVolume, Name: testVolumeName, Source: testVolumeSource,
					Target: testVolumeTarget,
				}},
			},
		},
		Workload: domain.DesiredWorkload{WorkloadSpec: domain.WorkloadSpec{
			Mounts: []domain.Mount{{Kind: domain.MountVolume, Target: testVolumeTarget}},
		}},
	}
}

func recoveryWarningPreparation(
	kind PlanKind,
	transactionKind store.TransactionKind,
	actionKinds ...string,
) Preparation {
	actions := make([]store.Action, len(actionKinds))
	for index, actionKind := range actionKinds {
		actions[index].Kind = actionKind
	}

	return Preparation{
		Plan:        Plan{Kind: kind},
		Transaction: store.Transaction{Kind: transactionKind},
		Actions:     actions,
	}
}

func mutateTransaction(
	value store.Transaction,
	mutate func(*store.Transaction),
) store.Transaction {
	mutate(&value)

	return value
}

func mutateAction(value store.Action, mutate func(*store.Action)) store.Action {
	mutate(&value)

	return value
}
