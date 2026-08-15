package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
	modernsqlite "modernc.org/sqlite"
)

func TestOpenAnchorsPrivateSQLite(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	path := filepath.Join(directory, "state file.db")

	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	for _, suffix := range []string{"", ".lock", "-wal", "-shm"} {
		assertPrivateRegular(t, path+suffix)
	}

	if state.anchor.databasePath() != platformDatabasePath(state.anchor) || !state.anchor.valid() {
		t.Fatalf("state anchor = %#v", state.anchor)
	}

	requireNoError(t, state.Close())
}

func TestOpenAcceptsExistingSQLite(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")

	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	requireNoError(t, state.Close())

	reopened, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open(existing) error = %v", err)
	}

	requireNoError(t, reopened.Close())
}

func TestOpenContainsCancelledSQLiteStartup(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	state, err := Open(ctx, filepath.Join(privateTempDir(t), "state.db"))
	if state != nil || !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open(cancelled) = %#v, %v", state, err)
	}
}

func TestFinishOpenRejectsChangedAnchor(t *testing.T) {
	t.Parallel()

	anchor, database := testAnchoredDatabase(t)
	anchor.directoryID = fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}

	state, err := finishOpen(context.Background(), database, anchor)
	if state != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("finishOpen() = %#v, %v", state, err)
	}
}

func TestFinishOpenRejectsUnreadyDatabase(t *testing.T) {
	t.Parallel()

	anchor, database := testAnchoredDatabase(t)
	requireNoError(t, database.Close())

	state, err := finishOpen(context.Background(), database, anchor)
	if state != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("finishOpen(closed database) = %#v, %v", state, err)
	}
}

func TestFinishOpenReportsUnlockFailure(t *testing.T) {
	t.Parallel()

	anchor, database := testAnchoredDatabase(t)
	requireNoError(t, ready(context.Background(), database))
	requireNoError(t, unix.Close(anchor.lock))
	anchor.lock = -1

	state, err := finishOpen(context.Background(), database, anchor)
	if state != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("finishOpen() = %#v, %v", state, err)
	}
}

func TestReadyRejectsClosedDatabase(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	requireNoError(t, database.Close())

	err := ready(context.Background(), database)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ready() error = %v", err)
	}
}

func TestReadyRejectsWrongSQLiteSettings(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})

	err := ready(context.Background(), database)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("ready() error = %v", err)
	}
}

func TestValidateConnectionContainsCancellation(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	requireNoError(t, os.WriteFile(path, nil, 0o600))
	database := testDatabase(t, sqliteURI(path))

	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})

	connection, err := database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		requireNoError(t, connection.Close())
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = validateConnection(ctx, connection)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("validateConnection() error = %v", err)
	}
}

func TestSQLiteURIUsesOnlyAnchoredConfiguration(t *testing.T) {
	t.Parallel()

	path := "/anchored/state file.db"
	value := sqliteURI(path)

	parsed, err := url.Parse(value)
	if err != nil {
		t.Fatal(err)
	}

	if parsed.Scheme != "file" || parsed.Path != path || parsed.Query().Get("mode") != "rw" {
		t.Fatalf("sqliteURI() = %q", value)
	}

	got := parsed.Query()["_pragma"]
	if len(got) != 4 {
		t.Fatalf("sqliteURI() pragmas = %#v", got)
	}
}

func privateTempDir(t *testing.T) string {
	t.Helper()

	directory := t.TempDir()
	requireNoError(t, unix.Chmod(directory, 0o700))

	// Darwin's temporary directory can be below a symlinked system path, while
	// state paths intentionally reject symlinks in every directory component.
	physicalDirectory, err := filepath.EvalSymlinks(directory)
	requireNoError(t, err)

	return physicalDirectory
}

func assertPrivateRegular(t *testing.T, path string) {
	t.Helper()

	metadata, err := os.Stat(path)
	if err != nil || !metadata.Mode().IsRegular() || metadata.Mode().Perm()&0o077 != 0 {
		t.Fatalf("state file %q = %#v, %v", filepath.Base(path), metadata, err)
	}
}

func requireNoError(t *testing.T, err error) {
	t.Helper()

	if err != nil {
		t.Fatal(err)
	}
}

func testAnchoredDatabase(t *testing.T) (*stateAnchor, *sql.DB) {
	t.Helper()

	path := filepath.Join(privateTempDir(t), "state.db")

	anchor, err := openStateAnchor(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}

	database := testDatabase(t, sqliteURI(anchor.databasePath()))
	database.SetMaxOpenConns(maximumConnections)
	database.SetMaxIdleConns(maximumConnections)

	return anchor, database
}

func testDatabase(t *testing.T, dataSourceName string) *sql.DB {
	t.Helper()

	connector, err := modernsqlite.NewConnector(dataSourceName)
	if err != nil {
		t.Fatal(err)
	}

	return sql.OpenDB(connector)
}
