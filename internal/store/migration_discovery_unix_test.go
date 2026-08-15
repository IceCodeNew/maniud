//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestDiscoverMigrationBackupFindsPublishedPair(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)

	manifest, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	pair, found, err := discoverMigrationBackup(anchor)
	requireNoError(t, err)

	if !found || pair == nil || !pair.Valid() || pair.plan.manifest != manifest {
		t.Fatalf("discoverMigrationBackup() = %#v", pair)
	}

	requireNoError(t, pair.Close())
	removeMigrationPlanFiles(t, anchor, pair.plan)
}

func TestDiscoverMigrationBackupAcceptsNoPair(t *testing.T) {
	t.Parallel()

	anchor, _ := testMigrationDatabase(t)

	pair, found, err := discoverMigrationBackup(anchor)
	if err != nil || found || pair != nil {
		t.Fatalf("discoverMigrationBackup() = %#v, %t, %v", pair, found, err)
	}
}

func TestDiscoverMigrationBackupRejectsIncompleteAndUnrecognizedEntries(t *testing.T) {
	t.Parallel()

	for _, test := range incompleteMigrationEntryFixtures() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			anchor, _ := testMigrationDatabase(t)
			test.create(t, anchor)

			pair, found, err := discoverMigrationBackup(anchor)
			if pair != nil || found || !errors.Is(err, ErrInvalidState) {
				t.Fatalf("discoverMigrationBackup() = %#v, %t, %v", pair, found, err)
			}
		})
	}
}

type incompleteMigrationEntryFixture struct {
	name   string
	create func(*testing.T, *stateAnchor)
}

func incompleteMigrationEntryFixtures() []incompleteMigrationEntryFixture {
	return []incompleteMigrationEntryFixture{
		{
			name: "artifact without manifest",
			create: func(t *testing.T, anchor *stateAnchor) {
				t.Helper()

				name := migrationArtifactName(anchor.databaseName, 1, 2, strings.Repeat("0", 64))
				writePrivateMigrationFixture(t, anchor, name, "artifact")
			},
		},
		{
			name: "manifest without artifact",
			create: func(t *testing.T, anchor *stateAnchor) {
				t.Helper()

				name := anchor.databaseName + ".schema-1-to-2.sha256-missing.manifest.json"
				writePrivateMigrationFixture(t, anchor, name, "{}")
			},
		},
		{
			name: "snapshot temporary",
			create: func(t *testing.T, anchor *stateAnchor) {
				t.Helper()

				writePrivateMigrationFixture(
					t,
					anchor,
					migrationSnapshotPrefix+"ab"+migrationSnapshotSuffix,
					"temporary",
				)
			},
		},
		{
			name: "manifest temporary",
			create: func(t *testing.T, anchor *stateAnchor) {
				t.Helper()

				writePrivateMigrationFixture(
					t,
					anchor,
					migrationManifestTempPrefix+"ab"+migrationManifestTempSuffix,
					"temporary",
				)
			},
		},
		{
			name: "unknown migration entry",
			create: func(t *testing.T, anchor *stateAnchor) {
				t.Helper()

				writePrivateMigrationFixture(t, anchor, anchor.databaseName+".schema-unknown", "unknown")
			},
		},
	}
}

func TestDiscoverMigrationBackupRejectsMalformedAndMultiplePairs(t *testing.T) {
	t.Parallel()

	t.Run("malformed manifest", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		snapshot, plan := migrationPublicationFixture(t, database, anchor)
		requireNoError(t, snapshot.Close())
		writePrivateMigrationFixture(t, anchor, plan.artifactName, "artifact")
		writePrivateMigrationFixture(t, anchor, plan.manifestName, "{}")

		pair, found, err := discoverMigrationBackup(anchor)
		if pair != nil || found || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("discoverMigrationBackup() = %#v, %t, %v", pair, found, err)
		}
	})

	t.Run("multiple pairs", func(t *testing.T) {
		t.Parallel()

		anchor, database := testMigrationDatabase(t)
		first := newMigrationSnapshot(t, database, anchor)
		_, err := publishMigrationBackup(context.Background(), first)
		requireNoError(t, err)

		_, err = database.ExecContext(context.Background(), "CREATE TABLE second_snapshot (id INTEGER)")
		requireNoError(t, err)

		second := newMigrationSnapshot(t, database, anchor)
		_, err = publishMigrationBackup(context.Background(), second)
		requireNoError(t, err)

		pair, found, err := discoverMigrationBackup(anchor)
		if pair != nil || found || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("discoverMigrationBackup() = %#v, %t, %v", pair, found, err)
		}
	})
}

func writePrivateMigrationFixture(t *testing.T, anchor *stateAnchor, name, content string) {
	t.Helper()

	path := filepath.Join(anchor.directoryPath, name)
	requireNoError(t, os.WriteFile(path, []byte(content), privateFileMode))
}
