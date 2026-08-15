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

	if objectCount != 1 || objectName != schemaTableName {
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

func TestOpenRejectsUnknownOrMalformedSchema(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		statement string
	}{
		{name: "unknown table", statement: "CREATE TABLE legacy (value TEXT)"},
		{name: "malformed version table", statement: "CREATE TABLE schema_version (version INTEGER)"},
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
	requireNoError(t, state.Close())

	state, err = Open(context.Background(), path)
	if state != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("Open(newer) = %#v, %v", state, err)
	}
}

func TestEnsureSchemaContainsCancellation(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := ensureSchema(ctx, database)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ensureSchema() error = %v", err)
	}
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

	err = initializeSchema(context.Background(), database)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("initializeSchema(existing) error = %v", err)
	}

	err = classifySchemaResult(context.Background(), ErrUnavailable)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("classifySchemaResult() error = %v", err)
	}
}
