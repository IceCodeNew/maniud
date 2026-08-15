//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRemoveMigrationBackupDeletesManifestBeforeArtifact(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)
	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	err = removeMigrationBackup(context.Background(), anchor, manifest)
	requireNoError(t, err)

	plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
	if !valid {
		t.Fatal("planExistingMigrationBackup() rejected manifest")
	}

	for _, name := range []string{plan.manifestName, plan.artifactName} {
		_, statErr := os.Lstat(filepath.Join(anchor.directoryPath, name))
		if !errors.Is(statErr, os.ErrNotExist) {
			t.Fatalf("removed migration entry %q error = %v", name, statErr)
		}
	}
}

func TestRemoveMigrationBackupRejectsInvalidPair(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)
	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
	if !valid {
		t.Fatal("planExistingMigrationBackup() rejected manifest")
	}

	requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, plan.artifactName)))
	writeAnchoredFile(t, anchor.directory, plan.artifactName, "replacement")

	err = removeMigrationBackup(context.Background(), anchor, manifest)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("removeMigrationBackup(replacement) = %v", err)
	}

	if content := readAnchoredFile(t, anchor.directory, plan.manifestName); content != string(plan.content) {
		t.Fatalf("preserved manifest = %q", content)
	}

	if content := readAnchoredFile(t, anchor.directory, plan.artifactName); content != "replacement" {
		t.Fatalf("preserved artifact = %q", content)
	}
}

func TestRemoveMigrationBackupContainsCancellation(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)
	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err = removeMigrationBackup(cancelled, anchor, manifest)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("removeMigrationBackup(cancelled) = %v", err)
	}

	plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
	if !valid || !existingMigrationManifestMatches(anchor, plan) || !existingMigrationArtifactMatches(anchor, plan) {
		t.Fatal("cancelled cleanup changed the migration pair")
	}
}
