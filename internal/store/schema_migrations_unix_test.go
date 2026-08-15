//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"testing"
)

func TestSchemaMigrationFromRequiresOneValidMatch(t *testing.T) {
	t.Parallel()

	migrations := currentSchemaMigrations()
	migration := migrations[0]

	match, found := schemaMigrationFrom([]schemaMigration{migration}, migration.source)
	if !found || match.source != migration.source || match.target != migration.target {
		t.Fatalf("schemaMigrationFrom() = %#v, %t", match, found)
	}

	invalid := migration
	invalid.target = invalid.source + 2

	for _, migrations := range [][]schemaMigration{
		nil,
		{migration, migration},
		{invalid},
		{testSchemaMigration()},
	} {
		if _, found = schemaMigrationFrom(migrations, migration.source); found {
			t.Fatalf("schemaMigrationFrom(%#v) found a migration", migrations)
		}
	}
}

func TestCurrentSchemaMigrationsAreOrdered(t *testing.T) {
	t.Parallel()

	migrations := currentSchemaMigrations()
	if len(migrations) != 2 || migrations[0].source != 1 ||
		migrations[0].target != writerLeaseSchemaVersion ||
		migrations[1].source != writerLeaseSchemaVersion ||
		migrations[1].target != currentSchemaVersion {
		t.Fatalf("currentSchemaMigrations() = %#v", migrations)
	}
}

func TestReconcileSchemaRejectsMissingAndFailedMigration(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name       string
		migrations []schemaMigration
	}{
		{name: "missing", migrations: nil},
		{
			name: "failed",
			migrations: []schemaMigration{{
				source: 1,
				target: 2,
				apply: func(context.Context, *sql.Tx) error {
					return ErrInvalidState
				},
				validateSource: validateSchemaVersion(1),
				validateTarget: validateSchemaVersion(2),
			}},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			anchor, database := testAnchoredDatabase(t)
			requireNoError(t, ready(context.Background(), database))
			_, err := database.ExecContext(
				context.Background(),
				schemaTableSQL+"; INSERT INTO schema_version (singleton, version) VALUES (1, 1)",
			)
			requireNoError(t, err)

			t.Cleanup(func() {
				_ = database.Close()
				_ = anchor.close()
			})

			err = reconcileSchema(context.Background(), database, anchor, test.migrations)
			if !errors.Is(err, ErrInvalidState) {
				t.Fatalf("reconcileSchema() error = %v", err)
			}
		})
	}
}

func TestReconcileSchemaRejectsClosedDatabase(t *testing.T) {
	t.Parallel()

	anchor, database := testAnchoredDatabase(t)
	requireNoError(t, database.Close())
	t.Cleanup(func() {
		_ = anchor.close()
	})

	err := reconcileSchema(context.Background(), database, anchor, currentSchemaMigrations())
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reconcileSchema(closed) error = %v", err)
	}
}

func TestReconcileSchemaContainsInitializationFailure(t *testing.T) {
	t.Parallel()

	anchor, database := testAnchoredDatabase(t)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	_, err := database.ExecContext(context.Background(), "PRAGMA query_only = ON")
	requireNoError(t, err)

	t.Cleanup(func() {
		_ = database.Close()
		_ = anchor.close()
	})

	err = reconcileSchema(context.Background(), database, anchor, currentSchemaMigrations())
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("reconcileSchema(read-only) error = %v", err)
	}
}

func TestAddWriterLeaseTableContainsSQLiteFailure(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	requireNoError(t, initializeSchema(context.Background(), database))

	transaction, err := database.BeginTx(context.Background(), nil)
	requireNoError(t, err)
	requireNoError(t, addWriterLeaseTable(context.Background(), transaction))

	err = addWriterLeaseTable(context.Background(), transaction)
	if err == nil {
		t.Fatal("addWriterLeaseTable() replaced an existing table")
	}

	requireNoError(t, transaction.Rollback())
	requireNoError(t, database.Close())
}

func TestAddJournalTablesContainsSQLiteFailure(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	requireNoError(t, initializeSchema(context.Background(), database))

	transaction, err := database.BeginTx(context.Background(), nil)
	requireNoError(t, err)
	requireNoError(t, addWriterLeaseTable(context.Background(), transaction))
	requireNoError(t, addJournalTables(context.Background(), transaction))

	err = addJournalTables(context.Background(), transaction)
	if err == nil {
		t.Fatal("addJournalTables() replaced an existing table")
	}

	requireNoError(t, transaction.Rollback())
	requireNoError(t, database.Close())
}
