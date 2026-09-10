package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestOpenInitializesStrictSchemaVersion(t *testing.T) {
	t.Parallel()

	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	objectCount, err := schemaObjectSummary(context.Background(), state.database)
	if err != nil {
		t.Fatal(err)
	}

	if objectCount != currentObjectCount {
		t.Fatalf("schema objects = %d", objectCount)
	}

	var version int

	err = state.database.QueryRowContext(
		context.Background(),
		"SELECT version FROM schema_version WHERE singleton = 1",
	).Scan(&version)
	if err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
}

func TestOpenRejectsUnknownOrMalformedSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		statement string
	}{
		{name: "unknown table", statement: "CREATE TABLE unrelated (value TEXT)"},
		{name: "malformed version table", statement: "CREATE TABLE schema_version (version INTEGER)"},
		{
			name: "malformed lease table",
			statement: schemaTableSQL + "; " +
				"INSERT INTO schema_version (singleton, version) VALUES (1, 1); " +
				"CREATE TABLE writer_leases (service_id BLOB PRIMARY KEY)",
		},
		{
			name: "malformed journal table",
			statement: schemaTableSQL + "; " +
				"INSERT INTO schema_version (singleton, version) VALUES (1, 1); " +
				writerLeaseTableSQL + "; " +
				journalTransactionTableSQL + "; " +
				"CREATE UNIQUE INDEX journal_one_unresolved_transaction_per_service " +
				"ON journal_transactions(service_id); " +
				journalActionTableSQL,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			path := filepath.Join(privateTempDir(t), "state.db")
			requireNoError(t, os.WriteFile(path, nil, 0o600))
			database := testDatabase(t, sqliteURI(path))
			_, err := database.ExecContext(context.Background(), test.statement)
			requireNoError(t, err)
			requireNoError(t, database.Close())

			state, err := Open(context.Background(), path)
			if state != nil || !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Open() = %#v, %v", state, err)
			}
		})
	}
}

func TestOpenRejectsInvalidWriterLeaseRows(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")

	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	connection, err := state.database.Conn(context.Background())
	requireNoError(t, err)

	_, err = connection.ExecContext(context.Background(), "PRAGMA ignore_check_constraints = ON")
	requireNoError(t, err)
	_, err = connection.ExecContext(
		context.Background(),
		"INSERT INTO writer_leases (service_id, epoch, owner) VALUES (?, 0, NULL)",
		[]byte("short"),
	)
	requireNoError(t, err)
	requireNoError(t, connection.Close())
	requireNoError(t, state.Close())

	state, err = Open(context.Background(), path)
	if state != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Open(invalid lease) = %#v, %v", state, err)
	}
}

func TestValidateJournalRowsRejectsRelationalCorruption(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")

	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	lock, err := state.TryLockService("project", "service")
	if err != nil {
		t.Fatalf("TryLockService() error = %v", err)
	}

	requireNoError(t, lock.Close())

	transactionID := make([]byte, 16)
	digest := make([]byte, 32)
	digest[0] = 1

	_, err = state.database.ExecContext(
		context.Background(),
		"INSERT INTO journal_transactions "+
			"(transaction_id, service_id, kind, state, runtime, source_digest, effective_digest, execution_digest) "+
			"VALUES (?, ?, 'bootstrap', 'failed', 'docker', ?, ?, ?)",
		transactionID,
		lock.lease.serviceID[:],
		digest,
		digest,
		digest,
	)
	requireNoError(t, err)
	_, err = state.database.ExecContext(
		context.Background(),
		"INSERT INTO journal_actions "+
			"(transaction_id, sequence, kind, state, intent_digest, postcondition_digest) "+
			"VALUES (?, 1, 'create', 'intent', ?, NULL)",
		transactionID,
		digest,
	)
	requireNoError(t, err)

	if !errors.Is(validateJournalRows(context.Background(), state.database), ErrInvalidState) {
		t.Fatal("validateJournalRows() accepted a pending action in a terminal transaction")
	}

	requireNoError(t, state.Close())

	state, err = Open(context.Background(), path)
	if state != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Open(corrupt journal) = %#v, %v", state, err)
	}
}

func TestValidateJournalRowsRequiresAppliedSuccessfulGeneration(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)
	_, err = lock.CommitAppliedService(context.Background(), transaction.ID, testAppliedServiceIntent())
	requireNoError(t, err)

	_, err = state.database.ExecContext(context.Background(), "DELETE FROM applied_services")
	requireNoError(t, err)

	if !errors.Is(validateJournalRows(context.Background(), state.database), ErrInvalidState) {
		t.Fatal("validateJournalRows() accepted a successful generation without an applied baseline")
	}

	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestValidateJournalRowsContainsSQLiteFailure(t *testing.T) {
	t.Parallel()

	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	requireNoError(t, state.database.Close())

	if !errors.Is(validateJournalRows(context.Background(), state.database), ErrInvalidState) {
		t.Fatal("validateJournalRows() exposed a SQLite failure")
	}

	requireNoError(t, state.anchor.close())
}

func TestEnsureInitialSchemaContainsCancellation(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ensureInitialSchema(ctx, database)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ensureInitialSchema() error = %v", err)
	}
}

func TestEnsureInitialSchemaCreatesVersionOne(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})

	requireNoError(t, ensureInitialSchema(context.Background(), database))
	requireNoError(t, validateSchema(context.Background(), database))
}

func TestInitializeSchemaContainsDatabaseFailures(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	requireNoError(t, database.Close())

	err := initializeSchema(context.Background(), database)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("initializeSchema(closed) error = %v", err)
	}

	database = testDatabase(t, "file::memory:")
	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})
	requireNoError(t, initializeSchema(context.Background(), database))
	requireNoError(t, ensureInitialSchema(context.Background(), database))

	err = initializeSchema(context.Background(), database)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("initializeSchema(existing) error = %v", err)
	}

	err = classifySchemaResult(context.Background(), ErrUnavailable)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("classifySchemaResult() error = %v", err)
	}
}

func TestValidateSchemaRejectsIncompleteFactsAndClosedDatabase(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	requireNoError(t, initializeSchema(context.Background(), database))

	facts, err := readSchemaFacts(context.Background(), database)
	requireNoError(t, err)
	facts.repositoryInventoryDefinition = ""
	if facts.valid() {
		t.Fatal("schemaFacts.valid() accepted a missing repository inventory index")
	}

	incompleteFacts := schemaFacts{objectCount: 0, schemaDefinition: schemaTableSQL, schemaRows: 1}
	if incompleteFacts.valid() {
		t.Fatal("schemaFacts.valid() accepted an incomplete schema")
	}

	requireNoError(t, database.Close())

	if !errors.Is(validateSchema(context.Background(), database), ErrInvalidState) {
		t.Fatal("validateSchema() accepted a closed database")
	}

	if !errors.Is(validateWriterLeaseRows(context.Background(), database), ErrInvalidState) {
		t.Fatal("validateWriterLeaseRows() accepted a closed database")
	}
}
