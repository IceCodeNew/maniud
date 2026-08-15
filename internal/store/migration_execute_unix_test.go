//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
)

func TestExecuteSchemaMigrationCommitsAndCleansBackup(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	migration := testSchemaMigration()

	err := executeSchemaMigration(context.Background(), database, anchor, migration)
	if err != nil {
		version, versionErr := storedSchemaVersion(context.Background(), database)
		t.Fatalf(
			"executeSchemaMigration() = %v; schema version = %d, %v; target validation = %v",
			err,
			version,
			versionErr,
			migration.validateTarget(context.Background(), database),
		)
	}

	if migration.validateTarget(context.Background(), database) != nil {
		t.Fatal("migration target failed validation")
	}

	assertNoMigrationBackup(t, anchor.directoryPath)
}

func TestExecuteSchemaMigrationPreservesBackupOnApplyFailure(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	migration := testSchemaMigration()
	migration.apply = func(context.Context, *sql.Tx) error { return ErrUnavailable }

	err := executeSchemaMigration(context.Background(), database, anchor, migration)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigration(apply failure) = %v", err)
	}

	version, err := storedSchemaVersion(context.Background(), database)
	if err != nil || version != migration.source {
		t.Fatalf("schema version after rollback = %d, %v", version, err)
	}

	assertMigrationBackupExists(t, anchor.directoryPath)
}

func TestExecuteSchemaMigrationPreservesBackupOnValidationFailure(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	migration := testSchemaMigration()
	migration.validateTarget = func(context.Context, *sql.DB) error { return ErrInvalidState }

	err := executeSchemaMigration(context.Background(), database, anchor, migration)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigration(validation failure) = %v", err)
	}

	version, err := storedSchemaVersion(context.Background(), database)
	if err != nil || version != migration.target {
		t.Fatalf("schema version after commit = %d, %v", version, err)
	}

	assertMigrationBackupExists(t, anchor.directoryPath)
}

func TestExecuteSchemaMigrationPreservesBackupOnCommitFailure(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	migration := testSchemaMigration()
	migration.apply = applyDeferredConstraintFailure

	err := executeSchemaMigration(context.Background(), database, anchor, migration)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigration(commit failure) = %v", err)
	}

	version, err := storedSchemaVersion(context.Background(), database)
	if err != nil || version != migration.source {
		t.Fatalf("schema version after failed commit = %d, %v", version, err)
	}

	assertMigrationBackupExists(t, anchor.directoryPath)
}

func TestApplySchemaMigrationCommitsAtomically(t *testing.T) {
	t.Parallel()

	_, database := testMigrationDatabase(t)
	migration := testSchemaMigration()

	err := applySchemaMigration(context.Background(), database, migration)
	requireNoError(t, err)

	if migration.validateTarget(context.Background(), database) != nil {
		t.Fatal("committed migration target failed validation")
	}
}

func testSchemaMigration() schemaMigration {
	return schemaMigration{
		source: currentSchemaVersion,
		target: currentSchemaVersion + 1,
		apply: func(ctx context.Context, transaction *sql.Tx) error {
			_, err := transaction.ExecContext(ctx, "CREATE TABLE migration_fixture (value TEXT NOT NULL)")
			if err != nil {
				return fmt.Errorf("create migration fixture: %w", err)
			}

			return nil
		},
		validateSource: func(ctx context.Context, database *sql.DB) error {
			version, err := storedSchemaVersion(ctx, database)
			if err != nil {
				return err
			}

			if version != currentSchemaVersion {
				return ErrInvalidState
			}

			return nil
		},
		validateTarget: func(ctx context.Context, database *sql.DB) error {
			version, err := storedSchemaVersion(ctx, database)
			if err != nil {
				return err
			}

			if version != currentSchemaVersion+1 {
				return ErrInvalidState
			}

			var definition string

			err = database.QueryRowContext(
				ctx,
				"SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'migration_fixture'",
			).Scan(&definition)
			if err != nil {
				return classifySQLiteProbe(ctx, err)
			}

			if definition != "CREATE TABLE migration_fixture (value TEXT NOT NULL)" {
				return ErrInvalidState
			}

			return nil
		},
	}
}

func assertNoMigrationBackup(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	requireNoError(t, err)

	for _, entry := range entries {
		if strings.HasSuffix(entry.Name(), ".manifest.json") || strings.Contains(entry.Name(), ".schema-") {
			t.Fatalf("migration left %q", entry.Name())
		}
	}
}

func assertMigrationBackupExists(t *testing.T, directory string) {
	t.Helper()

	entries, err := os.ReadDir(directory)
	requireNoError(t, err)

	foundArtifact := false
	foundManifest := false

	for _, entry := range entries {
		foundArtifact = foundArtifact || strings.HasSuffix(entry.Name(), ".sqlite") &&
			strings.Contains(entry.Name(), ".schema-")
		foundManifest = foundManifest || strings.HasSuffix(entry.Name(), ".manifest.json")
	}

	if !foundArtifact || !foundManifest {
		t.Fatalf("migration backup pair = artifact:%t manifest:%t", foundArtifact, foundManifest)
	}
}
