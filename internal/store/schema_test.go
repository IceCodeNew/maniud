package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
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

	objectCount, objectName, err := schemaObjectSummary(context.Background(), state.database)
	if err != nil {
		t.Fatal(err)
	}

	if objectCount != 2 || objectName != schemaTableName {
		t.Fatalf("schema objects = %d, %q", objectCount, objectName)
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

func TestOpenMigratesLegacySchema(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	path := filepath.Join(directory, "state.db")
	requireNoError(t, os.WriteFile(path, nil, 0o600))

	database := testDatabase(t, sqliteURI(path))
	_, err := database.ExecContext(
		context.Background(),
		schemaTableSQL+"; INSERT INTO schema_version (singleton, version) VALUES (1, 1)",
	)
	requireNoError(t, err)
	requireNoError(t, database.Close())

	state, err := Open(context.Background(), path)
	if err != nil || state == nil {
		t.Fatalf("Open(legacy) = %#v, %v", state, err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	requireNoError(t, validateSchema(context.Background(), state.database, currentSchemaVersion))
	assertNoMigrationBackup(t, directory)
}

func TestOpenRejectsUnknownOrMalformedSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		statement string
	}{
		{name: "unknown table", statement: "CREATE TABLE legacy (value TEXT)"},
		{name: "malformed version table", statement: "CREATE TABLE schema_version (version INTEGER)"},
		{
			name: "malformed lease table",
			statement: schemaTableSQL + "; " +
				"INSERT INTO schema_version (singleton, version) VALUES (1, 2); " +
				"CREATE TABLE writer_leases (service_id BLOB PRIMARY KEY)",
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

func TestOpenRejectsUnsupportedSchemaVersion(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")

	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	_, err = state.database.ExecContext(
		context.Background(),
		"UPDATE schema_version SET version = ? WHERE singleton = 1",
		currentSchemaVersion+1,
	)
	requireNoError(t, err)

	if !errors.Is(
		validateSchema(context.Background(), state.database, currentSchemaVersion),
		ErrInvalidState,
	) {
		t.Fatal("validateSchema() accepted a mismatched version row")
	}

	requireNoError(t, state.Close())

	state, err = Open(context.Background(), path)
	if state != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Open(newer) = %#v, %v", state, err)
	}
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
	requireNoError(t, validateSchema(context.Background(), database, 1))
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

func TestValidateSchemaRejectsInvalidVersionAndClosedDatabase(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	requireNoError(t, initializeSchema(context.Background(), database))

	if !errors.Is(validateSchema(context.Background(), database, 99), ErrInvalidState) {
		t.Fatal("validateSchema() accepted an unknown schema version")
	}

	requireNoError(t, database.Close())

	if !errors.Is(validateSchema(context.Background(), database, currentSchemaVersion), ErrInvalidState) {
		t.Fatal("validateSchema() accepted a closed database")
	}

	if !errors.Is(validateWriterLeaseRows(context.Background(), database), ErrInvalidState) {
		t.Fatal("validateWriterLeaseRows() accepted a closed database")
	}
}
