package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

type releasedState interface {
	UnresolvedTransaction(ctx context.Context, project, service string) (Transaction, bool, error)
	Actions(ctx context.Context, identifier TransactionID) ([]Action, error)
	AppliedService(ctx context.Context, project, service string) (AppliedService, bool, error)
	BackupIndex(ctx context.Context, identifier TransactionID) (BackupIndex, bool, error)
	Close() error
}

func TestReleasedV1StatePreservesUnresolvedWork(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name string
		open func(context.Context, string) (releasedState, error)
	}{
		{"writer", func(ctx context.Context, path string) (releasedState, error) { return Open(ctx, path) }},
		{"reader", func(ctx context.Context, path string) (releasedState, error) { return OpenReader(ctx, path) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, want := releasedV1State(t)
			applied, backup := addReleasedV1History(t, path)
			before, err := os.ReadFile(path) //nolint:gosec // Test-owned database.
			requireNoError(t, err)
			for range 2 {
				state, openErr := test.open(t.Context(), path)
				requireNoError(t, openErr)
				record, found, queryErr := state.UnresolvedTransaction(t.Context(), "project", "api")
				requireNoError(t, queryErr)
				if !found || record != want {
					t.Fatalf("released transaction = %#v, %t; want %#v", record, found, want)
				}
				actions, actionErr := state.Actions(t.Context(), want.ID)
				requireNoError(t, actionErr)
				wantAction := Action{
					TransactionID:       want.ID,
					Sequence:            1,
					Kind:                "workload.create",
					State:               ActionStateEffectOutcomeUnknown,
					IntentDigest:        want.EffectiveDigest,
					PostconditionDigest: nil,
				}
				if len(actions) != 1 || actions[0] != wantAction {
					t.Fatalf("released unknown-effect action = %#v", actions)
				}
				assertReleasedHistory(t, state, applied, backup)
				requireNoError(t, state.Close())
			}
			if test.name == "reader" {
				after, readErr := os.ReadFile(path) //nolint:gosec // Test-owned database.
				requireNoError(t, readErr)
				if !bytes.Equal(before, after) {
					t.Fatal("read-only inspection changed the released database")
				}
			}
		})
	}
}

func releasedV1State(t *testing.T) (string, Transaction) {
	t.Helper()
	// Frozen initialSchemaSQL from the published v0.2.0 schema.go, not derived
	// from the schema under test. Preserve its original CREATE definitions.
	schema, err := os.ReadFile("testdata/schema-v1.sql")
	requireNoError(t, err)
	path := filepath.Join(privateTempDir(t), "state.db")
	requireNoError(t, os.WriteFile(path, nil, 0o600))
	requireNoError(t, os.WriteFile(path+".lock", nil, 0o600))
	database := testDatabase(t, sqliteURI(path))
	_, err = database.ExecContext(t.Context(), string(schema))
	requireNoError(t, err)
	serviceID, valid := serviceIdentity("project", "api")
	if !valid {
		t.Fatal("invalid fixture service")
	}
	var want Transaction
	want.ID = TransactionID{17}
	want.Kind = TransactionBootstrap
	want.State = TransactionActive
	want.Runtime = domain.RuntimeDocker
	want.SourceDigest = domain.Digest{31}
	want.EffectiveDigest = domain.Digest{47}
	want.ExecutionDigest = domain.Digest{59}
	_, err = database.ExecContext(t.Context(), "INSERT INTO writer_leases VALUES (?, 7, NULL)", serviceID[:])
	requireNoError(t, err)
	_, err = database.ExecContext(t.Context(),
		"INSERT INTO journal_transactions VALUES (?, ?, 'bootstrap', 'active', 'docker', ?, ?, ?, NULL, NULL)",
		want.ID[:], serviceID[:], want.SourceDigest[:], want.EffectiveDigest[:], want.ExecutionDigest[:])
	requireNoError(t, err)
	_, err = database.ExecContext(t.Context(),
		"INSERT INTO journal_actions VALUES (?, 1, 'workload.create', 'effect_outcome_unknown', ?, NULL)",
		want.ID[:], want.EffectiveDigest[:])
	requireNoError(t, err)
	requireNoError(t, database.Close())

	return path, want
}

func addReleasedV1History(t *testing.T, path string) (AppliedService, BackupIndex) {
	t.Helper()
	database := testDatabase(t, sqliteURI(path))
	defer func() { requireNoError(t, database.Close()) }()
	serviceID, valid := serviceIdentity("project", "archived")
	if !valid {
		t.Fatal("invalid fixture service")
	}
	base := TransactionID{29}
	var applied AppliedService
	applied.TransactionID = TransactionID{41}
	applied.Kind = TransactionUpgrade
	applied.Runtime = domain.RuntimeDocker
	applied.SourceDigest = domain.Digest{61}
	applied.EffectiveDigest = domain.Digest{67}
	applied.ExecutionDigest = domain.Digest{71}
	applied.WorkloadID = "new-workload"
	applied.ConfigurationDigest = domain.Digest{73}
	applied.StorageDigest = domain.Digest{79}
	applied.ReferenceDigest = domain.Digest{83}
	applied.PlatformManifestDigest = domain.Digest{89}
	applied.ImageConfigDigest = domain.Digest{97}
	applied.HealthcheckUnknown = true
	backup := BackupIndex{
		TransactionID: applied.TransactionID, Runtime: domain.RuntimeDocker,
		ManifestPath:   applied.TransactionID.String() + "/manifest.json",
		ManifestDigest: domain.Digest{101}, CreatedUnix: 1009,
	}
	_, err := database.ExecContext(t.Context(), "INSERT INTO writer_leases VALUES (?, 11, NULL)", serviceID[:])
	requireNoError(t, err)
	_, err = database.ExecContext(t.Context(),
		"INSERT INTO journal_transactions VALUES (?, ?, 'bootstrap', 'succeeded', 'docker', ?, ?, ?, NULL, NULL)",
		base[:], serviceID[:], applied.SourceDigest[:], applied.EffectiveDigest[:], applied.ExecutionDigest[:])
	requireNoError(t, err)
	_, err = database.ExecContext(t.Context(),
		"INSERT INTO journal_transactions VALUES (?, ?, 'upgrade', 'succeeded', 'docker', ?, ?, ?, ?, 'old-workload')",
		applied.TransactionID[:], serviceID[:], applied.SourceDigest[:], applied.EffectiveDigest[:],
		applied.ExecutionDigest[:], base[:])
	requireNoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO applied_services VALUES (?, ?, ?, ?, ?, ?, ?, ?)",
		serviceID[:], applied.TransactionID[:], applied.WorkloadID, applied.ConfigurationDigest[:],
		applied.StorageDigest[:], applied.ReferenceDigest[:], applied.PlatformManifestDigest[:], applied.ImageConfigDigest[:])
	requireNoError(t, err)
	_, err = database.ExecContext(t.Context(), "INSERT INTO workload_backups VALUES (?, ?, ?, ?, ?)",
		applied.TransactionID[:], serviceID[:], backup.ManifestPath, backup.ManifestDigest[:], backup.CreatedUnix)
	requireNoError(t, err)

	return applied, backup
}

func assertReleasedHistory(t *testing.T, state releasedState, wantApplied AppliedService, wantBackup BackupIndex) {
	t.Helper()
	applied, found, err := state.AppliedService(t.Context(), "project", "archived")
	requireNoError(t, err)
	if !found || applied != wantApplied {
		t.Fatalf("released applied generation = %#v, %t; want %#v", applied, found, wantApplied)
	}
	backup, found, err := state.BackupIndex(t.Context(), wantBackup.TransactionID)
	requireNoError(t, err)
	if !found || backup != wantBackup {
		t.Fatalf("released backup = %#v, %t; want %#v", backup, found, wantBackup)
	}
}

func TestReleasedV1MigrationRollsBackAndRetries(t *testing.T) {
	t.Parallel()
	for _, test := range []struct{ name, setup, cleanup string }{
		{"copy", "CREATE TEMP TABLE migration_actions (value INTEGER)", "DROP TABLE migration_actions"},
		{"version-write", `CREATE TEMP TRIGGER fail_migration BEFORE UPDATE ON schema_version
BEGIN SELECT RAISE(ABORT, 'injected migration failure'); END`, "DROP TRIGGER fail_migration"},
		{"postcondition", `CREATE TEMP TRIGGER fail_migration AFTER UPDATE ON schema_version
BEGIN DELETE FROM writer_leases; END`, "DROP TRIGGER fail_migration"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			path, _ := releasedV1State(t)
			applied, backup := addReleasedV1History(t, path)
			database := testDatabase(t, sqliteURI(path))
			database.SetMaxOpenConns(1)
			before, err := readSchemaFactsAtVersion(t.Context(), database, 1)
			requireNoError(t, err)
			_, err = database.ExecContext(t.Context(), test.setup)
			requireNoError(t, err)
			if err = migrateVersion1(t.Context(), database); !errors.Is(err, ErrInvalidState) {
				t.Fatalf("migration failure = %v", err)
			}
			after, err := readSchemaFactsAtVersion(t.Context(), database, 1)
			requireNoError(t, err)
			if before != after {
				t.Fatal("failed migration changed the released schema")
			}
			assertMigrationRows(t, database)
			_, err = database.ExecContext(t.Context(), test.cleanup)
			requireNoError(t, err)
			requireNoError(t, migrateVersion1(t.Context(), database))
			requireNoError(t, validateSchema(t.Context(), database))
			requireNoError(t, database.Close())
			state, err := Open(t.Context(), path)
			requireNoError(t, err)
			assertReleasedHistory(t, state, applied, backup)
			requireNoError(t, state.Close())
		})
	}
}

func assertMigrationRows(t *testing.T, database *sql.DB) {
	t.Helper()
	query, err := readVersion1Query(t.Context(), database)
	requireNoError(t, err)
	var epochs, transactions, actions, applied, backups int
	err = query.QueryRowContext(t.Context(), `SELECT
  (SELECT sum(epoch) FROM writer_leases), (SELECT count(*) FROM journal_transactions),
  (SELECT count(*) FROM journal_actions), (SELECT count(*) FROM applied_services),
  (SELECT count(*) FROM workload_backups)`).Scan(&epochs, &transactions, &actions, &applied, &backups)
	requireNoError(t, err)
	if [5]int{epochs, transactions, actions, applied, backups} != [5]int{18, 3, 1, 1, 1} {
		t.Fatalf("rollback lost rows: %d %d %d %d %d", epochs, transactions, actions, applied, backups)
	}
}

func TestReleasedV1RejectsCorruptStateBeforeMigration(t *testing.T) {
	t.Parallel()
	for _, statement := range []string{
		"DROP INDEX journal_one_unresolved_transaction_per_service",
		"ALTER TABLE journal_transactions ADD COLUMN unexpected INTEGER",
		"DROP INDEX journal_one_unresolved_transaction_per_service; " +
			"CREATE INDEX journal_unresolved_repository_inventory ON journal_transactions(service_id)",
		"UPDATE schema_version SET version = 99",
		"PRAGMA ignore_check_constraints = ON; UPDATE writer_leases SET epoch = 0",
		"PRAGMA ignore_check_constraints = ON; UPDATE journal_transactions SET state = 'unknown'",
	} {
		t.Run(statement, func(t *testing.T) {
			t.Parallel()
			path, _ := releasedV1State(t)
			database := testDatabase(t, sqliteURI(path))
			_, err := database.ExecContext(t.Context(), statement)
			requireNoError(t, err)
			requireNoError(t, database.Close())
			reader, err := OpenReader(t.Context(), path)
			if reader != nil || !errors.Is(err, ErrInvalidState) {
				t.Fatalf("reader accepted corrupt released state: %v", err)
			}
			state, err := Open(t.Context(), path)
			if state != nil || !errors.Is(err, ErrInvalidState) {
				t.Fatalf("writer accepted corrupt released state: %v", err)
			}
		})
	}
}

func TestVersion1RepositoryIdentitySurvivesVersionUpgrade(t *testing.T) {
	t.Parallel()
	path := filepath.Join(privateTempDir(t), "state.db")
	state, lock := openJournalTestStore(t, path)
	intent := testTransactionIntent(domain.RuntimeDocker)
	intent.HasRepository = true
	intent.RepositoryVersion = 1
	intent.RepositoryScopeDigest = domain.Digest{107}
	intent.RepositoryLocationDigest = domain.Digest{109}
	want, err := lock.BeginTransaction(t.Context(), intent)
	requireNoError(t, err)
	requireNoError(t, lock.Close())
	_, err = state.database.ExecContext(t.Context(), "UPDATE schema_version SET version = 1")
	requireNoError(t, err)
	requireNoError(t, state.Close())
	reader := requireOpenReader(t, path)
	records, err := reader.UnresolvedRepositoryTransactions(t.Context(), intent.RepositoryScopeDigest)
	requireNoError(t, err)
	if len(records) != 1 || records[0] != want {
		t.Fatalf("read-only version-1 repository association = %#v", records)
	}
	requireNoError(t, reader.Close())
	state = openJournalStore(t, path)
	defer func() { requireNoError(t, state.Close()) }()
	records, err = state.UnresolvedRepositoryTransactions(t.Context(), intent.RepositoryScopeDigest)
	requireNoError(t, err)
	if len(records) != 1 || records[0] != want {
		t.Fatalf("upgraded repository association = %#v", records)
	}
	requireNoError(t, validateSchema(t.Context(), state.database))
}

func TestReleasedV1CancelledMigrationLeavesVersionIntact(t *testing.T) {
	t.Parallel()
	path, _ := releasedV1State(t)
	database := testDatabase(t, sqliteURI(path))
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if err := migrateVersion1(ctx, database); !errors.Is(err, context.Canceled) {
		t.Fatalf("cancelled migration = %v", err)
	}
	_, err := readVersion1Query(t.Context(), database)
	requireNoError(t, err)
}

func TestReleasedV1ReaderBlocksMigrationUntilClose(t *testing.T) {
	t.Parallel()
	path, want := releasedV1State(t)
	applied, backup := addReleasedV1History(t, path)
	reader := requireOpenReader(t, path)
	state, err := Open(t.Context(), path)
	if state != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writer bypassed the reader's startup lock: %v", err)
	}
	record, found, err := reader.UnresolvedTransaction(t.Context(), "project", "api")
	requireNoError(t, err)
	if !found || record != want {
		t.Fatalf("migration changed the existing reader snapshot: %#v, %t", record, found)
	}
	assertReleasedHistory(t, reader, applied, backup)
	requireNoError(t, reader.Close())
	state = openJournalStore(t, path)
	defer func() { requireNoError(t, state.Close()) }()
	assertReleasedHistory(t, state, applied, backup)
	requireNoError(t, validateSchema(t.Context(), state.database))
}
