//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverDiscoveredSchemaMigrationRetriesSource(t *testing.T) {
	t.Parallel()

	anchor, database, pair := testRecoveryPair(t)
	migration := testSchemaMigration()

	err := recoverDiscoveredSchemaMigration(
		context.Background(),
		database,
		pair,
		[]schemaMigration{migration},
	)
	requireNoError(t, err)

	if migration.validateTarget(context.Background(), database) != nil {
		t.Fatal("recovered migration target is invalid")
	}

	assertNoMigrationBackup(t, anchor.directoryPath)
}

func TestRecoverDiscoveredSchemaMigrationCleansValidTarget(t *testing.T) {
	t.Parallel()

	anchor, database, pair := testRecoveryPair(t)
	migration := testSchemaMigration()
	requireNoError(t, applySchemaMigration(context.Background(), database, migration))

	applied := false
	migration.apply = func(context.Context, *sql.Tx) error {
		applied = true

		return nil
	}

	err := recoverDiscoveredSchemaMigration(
		context.Background(),
		database,
		pair,
		[]schemaMigration{migration},
	)
	requireNoError(t, err)

	if applied || migration.validateTarget(context.Background(), database) != nil {
		t.Fatalf("target cleanup reapplied migration: applied=%t", applied)
	}

	assertNoMigrationBackup(t, anchor.directoryPath)
}

func TestRecoverDiscoveredSchemaMigrationRestoresInvalidDatabase(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		corrupt func(*testing.T, *sql.DB, *schemaMigration)
	}{
		{
			name: "source schema",
			corrupt: func(t *testing.T, database *sql.DB, migration *schemaMigration) {
				t.Helper()
				requireNoError(t, executeRecoveryFixture(database, "CREATE TABLE source_corruption (id INTEGER)"))

				migration.validateSource = validRecoverySourceWithoutCorruption
			},
		},
		{
			name: "missing version",
			corrupt: func(t *testing.T, database *sql.DB, _ *schemaMigration) {
				t.Helper()
				requireNoError(t, executeRecoveryFixture(database, "DROP TABLE schema_version"))
			},
		},
		{
			name: "target schema",
			corrupt: func(t *testing.T, database *sql.DB, migration *schemaMigration) {
				t.Helper()
				requireNoError(t, applySchemaMigration(context.Background(), database, *migration))
				requireNoError(t, executeRecoveryFixture(database, "DROP TABLE migration_fixture"))
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			anchor, database, pair := testRecoveryPair(t)
			migration := testSchemaMigration()
			test.corrupt(t, database, &migration)

			err := recoverDiscoveredSchemaMigration(
				context.Background(),
				database,
				pair,
				[]schemaMigration{migration},
			)
			requireNoError(t, err)

			if migration.validateTarget(context.Background(), database) != nil {
				t.Fatal("restored migration target is invalid")
			}

			assertNoMigrationBackup(t, anchor.directoryPath)
		})
	}
}

func TestFinishOpenRecoversBeforeUnlockingWrites(t *testing.T) {
	t.Parallel()

	anchor, database := testAnchoredDatabase(t)
	requireNoError(t, ready(context.Background(), database))
	requireNoError(t, reconcileSchema(context.Background(), database, anchor, currentSchemaMigrations()))

	snapshot := newMigrationSnapshot(t, database, anchor)
	_, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	state, err := finishOpenWithMigrations(
		context.Background(),
		database,
		anchor,
		[]schemaMigration{testSchemaMigration()},
	)
	if err != nil || state == nil {
		t.Fatalf("finishOpenWithMigrations() = %#v, %v", state, err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	if anchor.locked || testSchemaMigration().validateTarget(context.Background(), database) != nil {
		t.Fatal("finishOpenWithMigrations() unlocked before completing recovery")
	}

	assertNoMigrationBackup(t, anchor.directoryPath)
}

func TestOpenRejectsIncompleteMigrationRecovery(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	path := filepath.Join(directory, "state.db")

	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	requireNoError(t, state.Close())

	requireNoError(t, executeRecoveryFileFixture(directory, migrationSnapshotPrefix+"orphan"))

	state, err = Open(context.Background(), path)
	if state != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Open(incomplete migration) = %#v, %v", state, err)
	}
}

func testRecoveryPair(t *testing.T) (*stateAnchor, *sql.DB, *migrationBackupPair) {
	t.Helper()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)
	_, err := publishMigrationBackup(context.Background(), snapshot)
	requireNoError(t, err)

	pair, found, err := discoverMigrationBackup(anchor)
	if err != nil || !found || pair == nil {
		t.Fatalf("discoverMigrationBackup() = %#v, %t, %v", pair, found, err)
	}

	return anchor, database, pair
}

func validRecoverySourceWithoutCorruption(ctx context.Context, database *sql.DB) error {
	version, err := storedSchemaVersion(ctx, database)
	if err != nil {
		return err
	}

	if version != currentSchemaVersion {
		return ErrInvalidState
	}

	var objects int

	err = database.QueryRowContext(
		ctx,
		"SELECT count(*) FROM sqlite_schema WHERE name = 'source_corruption'",
	).Scan(&objects)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	if objects != 0 {
		return ErrInvalidState
	}

	return nil
}

func executeRecoveryFixture(database *sql.DB, statement string) error {
	_, err := database.ExecContext(context.Background(), statement)
	if err != nil {
		return fmt.Errorf("execute recovery fixture: %w", err)
	}

	return nil
}

func executeRecoveryFileFixture(directory, name string) error {
	err := os.WriteFile(filepath.Join(directory, name), []byte("orphan"), privateFileMode)
	if err != nil {
		return fmt.Errorf("write recovery fixture: %w", err)
	}

	return nil
}
