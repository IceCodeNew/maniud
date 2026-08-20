package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestApplicationTransactionBranchMatrix(t *testing.T) {
	t.Parallel()

	if !transactionMatchesApplied(store.Transaction{Kind: store.TransactionAdopt}, store.AppliedService{}, false) {
		t.Fatal("adopt transaction did not match an absent baseline")
	}
	for _, kind := range []PlanKind{
		PlanBootstrap,
		PlanAdopt,
		PlanUpgrade,
		PlanUnchanged,
		PlanResume,
		PlanProbeUnknownEffect,
		PlanRestore,
	} {
		preparation := Preparation{Plan: Plan{Kind: kind}}
		_ = transactionIntent(preparation)
		_ = recoveryMutationPlan(kind)
	}
	_ = transactionIntent(Preparation{Plan: Plan{Kind: PlanKind(testInvalidValue)}})

	if _, err := classifyRecovery(store.Transaction{State: store.TransactionFailed}, nil); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("classifyRecovery(failed) error = %v", err)
	}
	if validRestorePendingAction(
		[]store.Action{{Kind: testOtherValue, State: store.ActionStateCompleted}},
		store.Action{Kind: workloadRenameActionKind},
	) {
		t.Fatal("rename without completed discard was accepted")
	}
}

func TestStorageUpgradeActionBranchMatrix(t *testing.T) {
	t.Parallel()

	for _, kind := range []string{
		storageInventoryActionKind,
		storageBackupActionKind,
		storageRestoreActionKind,
	} {
		if !containsStorageUpgradeAction([]string{kind}) {
			t.Fatalf("containsStorageUpgradeAction(%q) = false", kind)
		}
	}
	if containsStorageUpgradeAction([]string{testOtherValue}) {
		t.Fatal("containsStorageUpgradeAction(other) = true")
	}

	invalidKind := WorkloadTransitionKind(99)
	if validWorkloadTransition(WorkloadTransition{Kind: invalidKind, Before: validExistingWorkloadForBranch(t)}) ||
		workloadTransitionActionKind(invalidKind) != "" {
		t.Fatal("unknown workload transition was accepted")
	}
}

func validExistingWorkloadForBranch(t *testing.T) ExistingWorkload {
	t.Helper()

	state, mutation, _ := newUpgradeMutation(t)
	t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

	return newUpgradeJourney(mutation.preparation).stop.Before
}

func TestRestoreStorageIntentAndPublicationBranches(t *testing.T) {
	t.Parallel()

	published := newStorageTestFixture(t, true)
	preparation := published.mutation.preparation
	journey := newUpgradeJourney(preparation)
	intents := upgradeCoreIntents(preparation, journey, published.publication)
	if len(intents) != 8 {
		t.Fatalf("upgradeCoreIntents(with storage) = %d", len(intents))
	}
	if manifest := restoreBackupManifest(preparation, published.publication); manifest.OperationToken != published.manifest.OperationToken {
		t.Fatalf("restoreBackupManifest(publication) = %#v", manifest)
	}

	preparation.Plan.Observation.RuntimeMounts = []domain.RuntimeMount{{
		Kind: domain.MountBind, Source: "/old", Target: "/data",
	}}
	preparation.Workload.Mounts = []domain.Mount{{
		Kind: domain.MountBind, Source: "/new", Target: "/data",
	}}
	manifest := restoreBackupManifest(preparation, backup.Publication{})
	if len(manifest.Artifacts) != 1 ||
		manifest.Artifacts[0].ProvenanceDigest != preparation.Applied.SourceDigest {
		t.Fatalf("restoreBackupManifest(bind) = %#v", manifest)
	}
	preparation.Plan.Observation.RuntimeMounts[0].Kind = domain.MountVolume
	preparation.Plan.Observation.RuntimeMounts[0].Name = testVolumeName
	preparation.Workload.Mounts[0].Kind = domain.MountVolume
	preparation.Workload.Mounts[0].Source = ""
	if manifest = restoreBackupManifest(preparation, backup.Publication{}); len(manifest.Artifacts) != 1 ||
		manifest.Artifacts[0].ProvenanceDigest != (domain.Digest{}) {
		t.Fatalf("restoreBackupManifest(volume) = %#v", manifest)
	}

	published.mutation.preparation.Actions = []store.Action{{Kind: storageBackupActionKind}}
	publication, err := loadRestorePublication(context.Background(), published.mutation)
	if err != nil || publication.ManifestDigest != published.publication.ManifestDigest {
		t.Fatalf("loadRestorePublication() = %#v, %v", publication, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err = loadRestorePublication(cancelled, published.mutation); err == nil {
		t.Fatal("loadRestorePublication(cancelled) succeeded")
	}

	missing := newStorageTestFixture(t, false)
	missing.mutation.preparation.Actions = []store.Action{{Kind: storageBackupActionKind}}
	if _, err = loadRestorePublication(context.Background(), missing.mutation); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("loadRestorePublication(missing) error = %v", err)
	}
}

func TestPrepareRestoreJourneyContainsPublicationFailure(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, true)
	fixture.mutation.preparation.Plan.Kind = PlanRestore
	fixture.mutation.preparation.Transaction.State = store.TransactionDegraded
	fixture.mutation.preparation.Actions = []store.Action{{Kind: storageBackupActionKind}}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := prepareRestoreJourney(cancelled, fixture.mutation); err == nil {
		t.Fatal("prepareRestoreJourney() ignored publication read failure")
	}
}

func TestNewBoundMutationContainsClosedStore(t *testing.T) {
	t.Parallel()

	state := openMutationTestStore(t)
	lock, err := state.TryLockService(testProjectName, testServiceName)
	if err != nil {
		t.Fatalf("TryLockService() error = %v", err)
	}
	if err = state.Close(); err != nil {
		t.Fatalf("Store.Close() error = %v", err)
	}
	mutation, err := newBoundMutation(lock, state, Preparation{}, Request{})
	if mutation != nil || err == nil {
		t.Fatalf("newBoundMutation(closed store) = %#v, %v", mutation, err)
	}
}

func TestBindPreparedTransactionRejectsEveryRecoveryPlan(t *testing.T) {
	t.Parallel()

	state, mutation, _ := newUpgradeMutation(t)
	defer closeBootstrapMutation(t, state, mutation)
	preparation := mutation.preparation
	preparation.HasTransaction = false
	for _, kind := range []PlanKind{PlanProbeUnknownEffect, PlanRestore} {
		preparation.Plan.Kind = kind
		if _, err := bindPreparedTransaction(context.Background(), mutation.lock, preparation); !errors.Is(err, ErrConflictingState) {
			t.Fatalf("bindPreparedTransaction(%q) error = %v", kind, err)
		}
	}
}
