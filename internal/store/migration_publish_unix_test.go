//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestPublishMigrationBackupCreatesDurablePair(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot, err := createMigrationSnapshot(
		context.Background(),
		database,
		anchor,
		currentSchemaVersion,
		currentSchemaVersion+1,
	)
	requireNoError(t, err)

	if snapshot == nil {
		t.Fatal("createMigrationSnapshot() returned nil")
	}

	wantPlan, valid := planMigrationBackup(
		anchor.databaseName,
		snapshot.sourceSchema,
		snapshot.targetSchema,
		snapshot.size,
		snapshot.digest,
	)
	if !valid {
		t.Fatal("planMigrationBackup() rejected snapshot")
	}

	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	if err != nil || manifest != wantPlan.manifest {
		t.Fatalf("publishMigrationBackup() = %#v, %v", manifest, err)
	}

	if snapshot.file != nil {
		t.Fatal("published snapshot retained its descriptor")
	}

	artifact := readAnchoredFile(t, anchor.directory, wantPlan.artifactName)

	manifestContent := readAnchoredFile(t, anchor.directory, wantPlan.manifestName)
	if int64(len(artifact)) != manifest.Size || manifestContent != string(wantPlan.content) {
		t.Fatalf("published pair = %d bytes, %q", len(artifact), manifestContent)
	}

	assertNoMigrationTemporary(t, anchor.directoryPath)
}

func TestPublishMigrationBackupAcceptsExactExistingPair(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	first := newMigrationSnapshot(t, database, anchor)
	firstManifest, err := publishMigrationBackup(context.Background(), first)
	requireNoError(t, err)

	second := newMigrationSnapshot(t, database, anchor)

	secondManifest, err := publishMigrationBackup(context.Background(), second)
	if err != nil || secondManifest != firstManifest {
		t.Fatalf("idempotent publish = %#v, %v", secondManifest, err)
	}

	assertNoMigrationTemporary(t, anchor.directoryPath)
}

func TestPublishMigrationBackupRejectsConflictingArtifact(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)

	plan, valid := planMigrationBackup(
		anchor.databaseName,
		snapshot.sourceSchema,
		snapshot.targetSchema,
		snapshot.size,
		snapshot.digest,
	)
	if !valid {
		t.Fatal("planMigrationBackup() rejected snapshot")
	}

	writeAnchoredFile(t, anchor.directory, plan.artifactName, "conflict")

	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("conflicting artifact publish = %#v, %v", manifest, err)
	}

	if content := readAnchoredFile(t, anchor.directory, plan.artifactName); content != "conflict" {
		t.Fatalf("conflicting artifact = %q", content)
	}

	assertNoMigrationTemporary(t, anchor.directoryPath)
}

func TestPublishMigrationBackupRejectsConflictingManifest(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	first := newMigrationSnapshot(t, database, anchor)

	plan, valid := planMigrationBackup(
		anchor.databaseName,
		first.sourceSchema,
		first.targetSchema,
		first.size,
		first.digest,
	)
	if !valid {
		t.Fatal("planMigrationBackup() rejected snapshot")
	}

	_, err := publishMigrationBackup(context.Background(), first)
	requireNoError(t, err)
	requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, plan.manifestName)))
	writeAnchoredFile(t, anchor.directory, plan.manifestName, "{}\n")

	second := newMigrationSnapshot(t, database, anchor)

	manifest, err := publishMigrationBackup(context.Background(), second)
	if manifest != emptyMigrationBackupManifest() || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("conflicting manifest publish = %#v, %v", manifest, err)
	}

	if content := readAnchoredFile(t, anchor.directory, plan.manifestName); content != "{}\n" {
		t.Fatalf("conflicting manifest = %q", content)
	}

	assertNoMigrationTemporary(t, anchor.directoryPath)
}

func TestPublishMigrationBackupContainsCancellationAtEffectBoundary(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	snapshot := newMigrationSnapshot(t, database, anchor)

	manifest, err := publishMigrationBackup(cancelled, snapshot)
	if manifest != emptyMigrationBackupManifest() || !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled publish = %#v, %v", manifest, err)
	}

	assertNoMigrationTemporary(t, anchor.directoryPath)

	ctx, cancelAfterArtifact := context.WithCancel(context.Background())
	snapshot = newMigrationSnapshot(t, database, anchor)

	manifest, err = publishMigrationBackupWithOps(ctx, snapshot, migrationPublishOps{
		publishArtifact: func(snapshot *migrationSnapshot, plan migrationBackupPlan) (bool, error) {
			established, publishErr := publishMigrationArtifact(snapshot, plan)

			cancelAfterArtifact()

			return established, publishErr
		},
		publishManifest: publishMigrationManifest,
	})
	if err != nil || manifest == emptyMigrationBackupManifest() {
		t.Fatalf("post-effect cancellation = %#v, %v", manifest, err)
	}

	assertNoMigrationTemporary(t, anchor.directoryPath)
}

func newMigrationSnapshot(t *testing.T, database *sql.DB, anchor *stateAnchor) *migrationSnapshot {
	t.Helper()

	snapshot, err := createMigrationSnapshot(
		context.Background(),
		database,
		anchor,
		currentSchemaVersion,
		currentSchemaVersion+1,
	)
	requireNoError(t, err)

	if snapshot == nil {
		t.Fatal("createMigrationSnapshot() returned nil")
	}

	return snapshot
}

func assertNoMigrationTemporary(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	requireNoError(t, err)

	for _, entry := range entries {
		if strings.HasPrefix(entry.Name(), migrationSnapshotPrefix) ||
			strings.HasPrefix(entry.Name(), migrationManifestTempPrefix) {
			t.Fatalf("publication left temporary entry %q", entry.Name())
		}
	}
}
