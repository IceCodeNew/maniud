//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"testing"
)

func TestExecuteSchemaMigrationRejectsInvalidContractAndCancellation(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	migration := testSchemaMigration()

	invalid := migration
	invalid.target = invalid.source + 2

	err := executeSchemaMigration(context.Background(), database, anchor, invalid)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigration(nonsequential) = %v", err)
	}

	err = executeSchemaMigration(context.Background(), nil, anchor, migration)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigration(nil database) = %v", err)
	}

	wrongSource := migration
	wrongSource.source++
	wrongSource.target++

	err = executeSchemaMigration(context.Background(), database, anchor, wrongSource)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigration(wrong source) = %v", err)
	}

	invalidSource := migration
	invalidSource.validateSource = func(context.Context, *sql.DB) error { return ErrInvalidState }

	err = executeSchemaMigration(context.Background(), database, anchor, invalidSource)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigration(invalid source) = %v", err)
	}

	err = executeSchemaMigrationWithOps(
		context.Background(),
		database,
		anchor,
		migration,
		schemaMigrationOps{
			createSnapshot: nil,
			publishBackup:  nil,
		},
	)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("executeSchemaMigrationWithOps(empty operations) = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err = executeSchemaMigration(cancelled, database, anchor, migration)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("executeSchemaMigration(cancelled) = %v", err)
	}

	assertNoMigrationBackup(t, anchor.directoryPath)
}

func TestClassifySQLiteProbePreservesAvailability(t *testing.T) {
	t.Parallel()

	err := classifySQLiteProbe(context.Background(), sqliteCodeError(sqliteResultBusy))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("classifySQLiteProbe(busy) = %v", err)
	}
}

func TestExecuteSchemaMigrationContainsBackupPreparationFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*schemaMigrationOps)
	}{
		{
			name: "snapshot",
			configure: func(operations *schemaMigrationOps) {
				operations.createSnapshot = func(
					context.Context,
					*sql.DB,
					*stateAnchor,
					int,
					int,
				) (*migrationSnapshot, error) {
					return nil, ErrUnavailable
				}
			},
		},
		{
			name: "publication",
			configure: func(operations *schemaMigrationOps) {
				operations.publishBackup = func(
					_ context.Context,
					snapshot *migrationSnapshot,
				) (migrationBackupManifest, error) {
					return emptyMigrationBackupManifest(), errors.Join(ErrUnavailable, snapshot.Close())
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			anchor, database := testMigrationDatabase(t)
			operations := standardSchemaMigrationOps()
			test.configure(&operations)

			err := executeSchemaMigrationWithOps(
				context.Background(),
				database,
				anchor,
				testSchemaMigration(),
				operations,
			)
			if !errors.Is(err, ErrUnavailable) {
				t.Fatalf("executeSchemaMigrationWithOps() = %v", err)
			}

			version, versionErr := storedSchemaVersion(context.Background(), database)
			if versionErr != nil || version != testSchemaMigration().source {
				t.Fatalf("schema version after preparation failure = %d, %v", version, versionErr)
			}

			assertNoMigrationBackup(t, anchor.directoryPath)
		})
	}
}

func TestApplySchemaMigrationRollsBackFailures(t *testing.T) {
	t.Parallel()

	for _, test := range schemaMigrationApplyFailures() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, database := testMigrationDatabase(t)
			migration := testSchemaMigration()
			migration.apply = test.apply

			err := applySchemaMigration(context.Background(), database, migration)
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("applySchemaMigration() = %v", err)
			}

			version, versionErr := storedSchemaVersion(context.Background(), database)
			if versionErr != nil || version != migration.source {
				t.Fatalf("schema version after rollback = %d, %v", version, versionErr)
			}
		})
	}
}

func TestApplySchemaMigrationRejectsClosedDatabase(t *testing.T) {
	t.Parallel()

	_, database := testMigrationDatabase(t)
	requireNoError(t, database.Close())

	err := applySchemaMigration(context.Background(), database, testSchemaMigration())
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("applySchemaMigration(closed database) = %v", err)
	}
}

func wrapMigrationFixtureError(operation string, err error) error {
	if err != nil {
		return fmt.Errorf("%s: %w", operation, err)
	}

	return nil
}

type schemaMigrationApplyFailure struct {
	name  string
	apply func(context.Context, *sql.Tx) error
}

func schemaMigrationApplyFailures() []schemaMigrationApplyFailure {
	return []schemaMigrationApplyFailure{
		{
			name: "apply",
			apply: func(context.Context, *sql.Tx) error {
				return ErrUnavailable
			},
		},
		{
			name: "version update",
			apply: func(ctx context.Context, transaction *sql.Tx) error {
				_, err := transaction.ExecContext(ctx, "DROP TABLE schema_version")

				return wrapMigrationFixtureError("drop schema table", err)
			},
		},
		{
			name: "version mismatch",
			apply: func(ctx context.Context, transaction *sql.Tx) error {
				_, err := transaction.ExecContext(ctx, "UPDATE schema_version SET version = 99")

				return wrapMigrationFixtureError("change schema version", err)
			},
		},
		{
			name:  "commit",
			apply: applyDeferredConstraintFailure,
		},
	}
}

func applyDeferredConstraintFailure(ctx context.Context, transaction *sql.Tx) error {
	_, err := transaction.ExecContext(
		ctx,
		"CREATE TABLE parent (id INTEGER PRIMARY KEY); "+
			"CREATE TABLE child (parent_id INTEGER REFERENCES parent(id) "+
			"DEFERRABLE INITIALLY DEFERRED); "+
			"INSERT INTO child (parent_id) VALUES (1)",
	)

	return wrapMigrationFixtureError("create deferred constraint failure", err)
}
