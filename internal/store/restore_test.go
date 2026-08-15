package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRestoreSQLiteReplacesDatabaseFromSnapshot(t *testing.T) {
	t.Parallel()

	statePath := filepath.Join(privateTempDir(t), "state.db")

	state, err := Open(context.Background(), statePath)
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	_, err = state.database.ExecContext(
		context.Background(),
		"CREATE TABLE restore_fixture (id INTEGER PRIMARY KEY); "+
			"INSERT INTO restore_fixture (id) VALUES (1)",
	)
	requireNoError(t, err)

	snapshotPath := filepath.Join(privateTempDir(t), "snapshot.db")
	requireNoError(t, os.WriteFile(snapshotPath, nil, privateFileMode))
	requireNoError(t, backupSQLite(context.Background(), state.database, snapshotPath))

	_, err = state.database.ExecContext(
		context.Background(),
		"INSERT INTO restore_fixture (id) VALUES (2)",
	)
	requireNoError(t, err)

	requireNoError(t, restoreSQLite(context.Background(), state.database, sqliteReadOnlyURI(snapshotPath)))
	requireNoError(t, ready(context.Background(), state.database))

	var rows int

	err = state.database.QueryRowContext(
		context.Background(),
		"SELECT count(*) FROM restore_fixture",
	).Scan(&rows)
	if err != nil || rows != 1 {
		t.Fatalf("restored rows = %d, %v", rows, err)
	}
}

func TestRestoreSQLiteContainsDriverSourceAndCancellationFailures(t *testing.T) {
	t.Parallel()

	err := runOnlineRestore(context.Background(), struct{}{}, "missing")
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("runOnlineRestore(wrong driver) = %v", err)
	}

	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	err = restoreSQLite(
		context.Background(),
		state.database,
		sqliteReadOnlyURI(filepath.Join(privateTempDir(t), "missing.db")),
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("restoreSQLite(missing source) = %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = restoreSQLite(ctx, state.database, "missing")
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("restoreSQLite(cancelled) = %v", err)
	}

	if state.database.Stats().MaxOpenConnections != maximumConnections {
		t.Fatalf("restored pool limit = %d", state.database.Stats().MaxOpenConnections)
	}
}
