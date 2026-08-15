//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestOpenMigrationBackupPairRejectsInvalidState(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)
	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
	if !valid {
		t.Fatal("planExistingMigrationBackup() rejected manifest")
	}

	assertMigrationPairInvalid(t, nil, manifest, "nil")

	invalid := manifest

	invalid.Kind = "invalid"
	assertMigrationPairInvalid(t, anchor, invalid, "invalid")

	requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, plan.artifactName)))
	assertMigrationPairInvalid(t, anchor, manifest, "missing artifact")

	writeAnchoredFile(t, anchor.directory, plan.artifactName, "replacement")
	assertMigrationPairInvalid(t, anchor, manifest, "conflict")

	requireNoError(t, os.Remove(filepath.Join(anchor.directoryPath, plan.manifestName)))
	assertMigrationPairInvalid(t, anchor, manifest, "missing manifest")
}

//nolint:cyclop,funlen // Each case injects one ordered cleanup boundary failure.
func TestMigrationBackupPairContainsCleanupFailures(t *testing.T) {
	t.Parallel()

	t.Run("invalid pair", func(t *testing.T) {
		t.Parallel()

		if (*migrationBackupPair)(nil).Valid() || (*migrationBackupPair)(nil).Close() != nil {
			t.Fatal("nil migration backup pair accepted an operation")
		}

		var pair migrationBackupPair
		if !errors.Is(pair.remove(), ErrInvalidState) {
			t.Fatal("invalid migration backup pair was removed")
		}
	})

	t.Run("manifest unlink", func(t *testing.T) {
		t.Parallel()

		pair := openPublishedMigrationPair(t)

		err := pair.removeWithOps(migrationBackupCleanupOps{
			unlink:        func(int, string) error { return ErrUnavailable },
			syncDirectory: syncMigrationSnapshotDirectory,
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest unlink failure = %v", err)
		}
	})

	t.Run("manifest sync", func(t *testing.T) {
		t.Parallel()

		pair := openPublishedMigrationPair(t)

		err := pair.removeWithOps(migrationBackupCleanupOps{
			unlink:        unlinkMigrationSnapshot,
			syncDirectory: func(int) error { return ErrUnavailable },
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("manifest sync failure = %v", err)
		}
	})

	t.Run("artifact identity", func(t *testing.T) {
		t.Parallel()

		pair := openPublishedMigrationPair(t)
		calls := 0

		err := pair.removeWithOps(migrationBackupCleanupOps{
			unlink: unlinkMigrationSnapshot,
			syncDirectory: func(int) error {
				calls++
				if calls == 1 {
					requireNoError(t, os.Remove(filepath.Join(pair.anchor.directoryPath, pair.plan.artifactName)))
					writeAnchoredFile(t, pair.anchor.directory, pair.plan.artifactName, "replacement")
				}

				return nil
			},
		})
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("artifact identity failure = %v", err)
		}
	})

	t.Run("artifact unlink", func(t *testing.T) {
		t.Parallel()

		pair := openPublishedMigrationPair(t)

		err := pair.removeWithOps(migrationBackupCleanupOps{
			unlink: func(directory int, name string) error {
				if name == pair.plan.artifactName {
					return ErrUnavailable
				}

				return unlinkMigrationSnapshot(directory, name)
			},
			syncDirectory: syncMigrationSnapshotDirectory,
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("artifact unlink failure = %v", err)
		}
	})

	t.Run("artifact sync", func(t *testing.T) {
		t.Parallel()

		pair := openPublishedMigrationPair(t)
		calls := 0

		err := pair.removeWithOps(migrationBackupCleanupOps{
			unlink: unlinkMigrationSnapshot,
			syncDirectory: func(directory int) error {
				calls++
				if calls == 2 {
					return ErrUnavailable
				}

				return syncMigrationSnapshotDirectory(directory)
			},
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("artifact sync failure = %v", err)
		}
	})

	t.Run("descriptor close", func(t *testing.T) {
		t.Parallel()

		pair := openPublishedMigrationPair(t)
		calls := 0

		err := pair.removeWithOps(migrationBackupCleanupOps{
			unlink: unlinkMigrationSnapshot,
			syncDirectory: func(directory int) error {
				calls++
				if calls == 2 {
					requireNoError(t, pair.artifact.Close())
				}

				return syncMigrationSnapshotDirectory(directory)
			},
		})
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("descriptor close failure = %v", err)
		}
	})
}

func openPublishedMigrationPair(t *testing.T) *migrationBackupPair {
	t.Helper()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)
	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
	if !valid {
		t.Fatal("planExistingMigrationBackup() rejected manifest")
	}

	removeMigrationPlanFiles(t, anchor, plan)

	pair, err := openMigrationBackupPair(anchor, manifest)
	requireNoError(t, err)

	if pair == nil {
		t.Fatal("openMigrationBackupPair() returned nil")
	}

	return pair
}

func assertMigrationPairInvalid(
	t *testing.T,
	anchor *stateAnchor,
	manifest migrationBackupManifest,
	label string,
) {
	t.Helper()

	pair, err := openMigrationBackupPair(anchor, manifest)
	if pair != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("openMigrationBackupPair(%s) = %#v, %v", label, pair, err)
	}
}
