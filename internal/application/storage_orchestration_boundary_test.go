package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestStorageBackupStageRejectsIncompleteState(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	execution := &upgradeExecution{
		mutation: fixture.mutation, sources: []backedStorageSource{fixture.source}, archives: [][]byte{fixture.archive},
	}
	originalRoot := fixture.mutation.backupRoot
	fixture.mutation.backupRoot = ""
	if err := execution.backup(context.Background()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("backup(empty root) error = %v", err)
	}
	fixture.mutation.backupRoot = originalRoot
	if err := execution.backup(context.Background()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("backup(empty manifest) error = %v", err)
	}
}

func TestStorageBackupStageContainsEffectFailure(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	execution := &upgradeExecution{
		mutation: fixture.mutation, sources: []backedStorageSource{fixture.source}, archives: [][]byte{fixture.archive},
		manifest: fixture.manifest, sequence: 1,
	}
	if err := execution.backup(context.Background()); err == nil {
		t.Fatal("backup() accepted an unprepared publication capacity")
	}
}

func TestStorageStagesResolveCompletedNegativeEvidence(t *testing.T) {
	t.Parallel()

	backupFixture := newStorageTestFixture(t, false)
	backupIntent := storageBackupIntent(1, backupFixture.manifest)
	backupNegative := storageBackupPostcondition(backupFixture.manifest, backup.Publication{}, false)
	backupExecution := &upgradeExecution{
		mutation: backupFixture.mutation, sources: []backedStorageSource{backupFixture.source},
		archives: [][]byte{backupFixture.archive}, manifest: backupFixture.manifest, sequence: 1,
		actions: []store.Action{storageCompletedAction(backupFixture.mutation, backupIntent, backupNegative.Digest)},
	}
	if err := backupExecution.backup(context.Background()); err == nil {
		t.Fatal("backup() accepted a completed negative postcondition")
	}

	restoreFixture := newStorageTestFixture(t, true)
	restoreFixture.upgradeRuntime.archives = map[string][]byte{
		testVolumeTarget: upgradeTestArchive(t, "different"),
	}
	restoreIntent := storageRestoreIntent(
		1,
		restoreFixture.mutation.preparation.Transaction.ID.String(),
		restoreFixture.manifest,
	)
	restoreNegative := storageRestorePostcondition(
		restoreFixture.mutation.preparation.Transaction.ID.String(),
		restoreFixture.manifest,
		false,
	)
	restoreExecution := &upgradeExecution{
		mutation: restoreFixture.mutation, runtime: restoreFixture.upgradeRuntime,
		sources:  []backedStorageSource{restoreFixture.source},
		archives: [][]byte{restoreFixture.archive}, publication: restoreFixture.publication, sequence: 1,
		actions: []store.Action{storageCompletedAction(restoreFixture.mutation, restoreIntent, restoreNegative.Digest)},
	}
	if err := restoreExecution.restore(context.Background()); err == nil {
		t.Fatal("restore() accepted a completed negative postcondition")
	}
}

func TestStorageSettlersResumeCompletedEvidence(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, true)
	backupPostcondition := storageBackupPostcondition(fixture.manifest, fixture.publication, true)
	backupAction := storageCompletedAction(
		fixture.mutation,
		storageBackupIntent(1, fixture.manifest),
		backupPostcondition.Digest,
	)
	postcondition, publication, err := settleStorageBackup(
		context.Background(), fixture.mutation, backupAction, 1, fixture.mutation.backupRoot,
		fixture.manifest, [][]byte{fixture.archive}, backup.PublicationCapacity{},
	)
	if err != nil || !postcondition.Satisfied || publication.ManifestDigest != fixture.publication.ManifestDigest {
		t.Fatalf("settleStorageBackup(completed) = %#v, %#v, %v", postcondition, publication, err)
	}

	restorePostcondition := storageRestorePostcondition(
		fixture.mutation.preparation.Transaction.ID.String(), fixture.manifest, true,
	)
	restoreAction := storageCompletedAction(
		fixture.mutation,
		storageRestoreIntent(2, fixture.mutation.preparation.Transaction.ID.String(), fixture.manifest),
		restorePostcondition.Digest,
	)
	postcondition, err = settleStorageRestore(
		context.Background(), fixture.mutation, fixture.runtime, restoreAction, 2,
		fixture.publication, [][]byte{fixture.archive},
	)
	if err != nil || !postcondition.Satisfied {
		t.Fatalf("settleStorageRestore(completed) = %#v, %v", postcondition, err)
	}
}

func TestStoragePublicationFunctionsContainCancellation(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	execution := &upgradeExecution{
		mutation:    fixture.mutation,
		sources:     []backedStorageSource{fixture.source},
		inventories: []backup.Inventory{fixture.inventory},
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := execution.backupManifest(cancelled); err == nil {
		t.Fatal("backupManifest() ignored cancellation")
	}
	if _, err := execution.publishedBackup(cancelled); err == nil {
		t.Fatal("publishedBackup() ignored cancellation")
	}
	execution.runtime = fixture.upgradeRuntime
	if err := execution.restore(cancelled); err == nil {
		t.Fatal("restore() ignored publication failure")
	}
}
