// Package store owns maniud's SQLite state and its filesystem identity.
package store

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"path/filepath"
	"strconv"
	"time"

	modernsqlite "modernc.org/sqlite"
)

const (
	busyTimeout         = 250 * time.Millisecond
	maximumConnections  = 8
	sqliteModeParameter = "mode"
	sqliteResultMask    = 0xff
	sqliteResultBusy    = 5
	sqliteResultLocked  = 6
)

var (
	// ErrInvalidPath reports a state path outside maniud's anchored-file grammar.
	ErrInvalidPath = errors.New("state path is invalid")
	// ErrInvalidState reports unsafe, corrupt, or replaced managed state.
	ErrInvalidState = errors.New("managed state is invalid")
	// ErrOwnershipLost reports a stale writer lease or a failed fence proof.
	ErrOwnershipLost = errors.New("writer ownership is lost")
	// ErrUnavailable reports a bounded lock or SQLite availability failure.
	ErrUnavailable = errors.New("managed state is unavailable")
)

// Store owns one anchored SQLite connection pool.
type Store struct {
	database *sql.DB
	anchor   *stateAnchor
}

// Open anchors path, acquires the startup lock, and opens SQLite with the
// required settings on every physical connection.
func Open(ctx context.Context, path string) (*Store, error) {
	err := ensureStateDirectory(ctx, path)
	if err != nil {
		return nil, err
	}

	anchor, err := openStateAnchor(ctx, path)
	if err != nil {
		return nil, err
	}

	connector, _ := modernsqlite.NewConnector(sqliteURI(anchor.databasePath()))

	database := sql.OpenDB(connector)

	database.SetMaxOpenConns(maximumConnections)
	database.SetMaxIdleConns(maximumConnections)

	return finishOpen(ctx, database, anchor)
}

func finishOpen(ctx context.Context, database *sql.DB, anchor *stateAnchor) (*Store, error) {
	err := ready(ctx, database)
	if err == nil {
		err = ensureInitialSchema(ctx, database)
	}

	if err != nil {
		_ = database.Close()
		_ = anchor.close()

		return nil, err
	}

	startupErr := ErrInvalidState
	if anchor.valid() {
		startupErr = anchor.unlock()
	}

	if startupErr != nil {
		_ = database.Close()
		_ = anchor.close()

		return nil, startupErr
	}

	return &Store{database: database, anchor: anchor}, nil
}

func classifySQLiteProbe(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	if retryableSQLiteError(err) {
		return ErrUnavailable
	}

	return ErrInvalidState
}

func retryableSQLiteError(err error) bool {
	var result interface{ Code() int }
	if !errors.As(err, &result) {
		return false
	}

	code := result.Code() & sqliteResultMask

	return code == sqliteResultBusy || code == sqliteResultLocked
}

// BackupRoot returns the private backup directory beside the state database.
func (store *Store) BackupRoot() (string, error) {
	if store == nil || store.anchor == nil || !store.anchor.valid() {
		return "", ErrInvalidState
	}

	return filepath.Join(store.anchor.directoryPath, "backups"), nil
}

// Close releases SQLite before closing its anchored directory descriptor.
func (store *Store) Close() error {
	databaseErr := store.database.Close()
	anchorErr := store.anchor.close()

	if databaseErr != nil || anchorErr != nil {
		return ErrUnavailable
	}

	return nil
}

func ready(ctx context.Context, database *sql.DB) error {
	connections := make([]*sql.Conn, 0, maximumConnections)
	defer func() {
		for _, connection := range connections {
			_ = connection.Close()
		}
	}()

	for range maximumConnections {
		connection, err := database.Conn(ctx)
		if err != nil {
			return classifyContext(ctx)
		}

		connections = append(connections, connection)

		err = validateConnection(ctx, connection)
		if err != nil {
			return err
		}
	}

	return nil
}

func validateConnection(ctx context.Context, connection *sql.Conn) error {
	if validSettings(ctx, connection) {
		return nil
	}

	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	return ErrInvalidState
}

func validSettings(ctx context.Context, connection *sql.Conn) bool {
	var (
		foreignKeys int
		journalMode string
		synchronous int
		lockWait    int
	)

	err := connection.QueryRowContext(
		ctx,
		"SELECT foreign_keys, journal_mode, synchronous, timeout "+
			"FROM pragma_foreign_keys, pragma_journal_mode, pragma_synchronous, pragma_busy_timeout",
	).Scan(&foreignKeys, &journalMode, &synchronous, &lockWait)

	return err == nil && foreignKeys == 1 && journalMode == "wal" && synchronous == 2 &&
		lockWait == int(busyTimeout/time.Millisecond)
}

func sqliteURI(path string) string {
	query := url.Values{
		sqliteModeParameter: []string{"rw"},
		"_txlock":           []string{"immediate"},
	}
	for _, pragma := range []string{
		"foreign_keys(ON)",
		"journal_mode(WAL)",
		"synchronous(FULL)",
		"busy_timeout(" + strconv.FormatInt(busyTimeout.Milliseconds(), 10) + ")",
	} {
		query.Add("_pragma", pragma)
	}

	return sqliteFileURI(path, query)
}

func sqliteJournalReadOnlyURI(path string, immutable bool) string {
	query := url.Values{
		sqliteModeParameter: []string{"ro"},
	}
	if immutable {
		query.Set("immutable", "1")
	}

	for _, pragma := range []string{
		"foreign_keys(ON)",
		"query_only(ON)",
		"busy_timeout(" + strconv.FormatInt(busyTimeout.Milliseconds(), 10) + ")",
	} {
		query.Add("_pragma", pragma)
	}

	return sqliteFileURI(path, query)
}

func sqliteFileURI(path string, query url.Values) string {
	uri := &url.URL{
		Scheme:      "file",
		Opaque:      "",
		User:        nil,
		Host:        "",
		Path:        path,
		RawPath:     "",
		OmitHost:    false,
		ForceQuery:  false,
		RawQuery:    query.Encode(),
		Fragment:    "",
		RawFragment: "",
	}

	return uri.String()
}

func classifyContext(ctx context.Context) error {
	if ctx.Err() != nil {
		return errors.Join(ErrUnavailable, ctx.Err())
	}

	return ErrUnavailable
}
