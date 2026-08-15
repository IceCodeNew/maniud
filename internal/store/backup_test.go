package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

const backupFixtureLast = 1024

func TestBackupSQLiteCreatesConsistentSnapshot(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)

	state, err := Open(context.Background(), filepath.Join(directory, "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	payload := strings.Repeat("x", 1024)
	seedBackupFixture(t, state.database, payload)
	stopWriter, writerDone := startBackupWriter(state.database, payload)

	destination := filepath.Join(directory, "snapshot.db")
	requireNoError(t, os.WriteFile(destination, nil, 0o600))
	requireNoError(t, backupSQLite(context.Background(), state.database, destination))
	stopWriter()

	writerErr := <-writerDone
	if writerErr != nil && !errors.Is(writerErr, context.Canceled) {
		t.Fatalf("concurrent writer error = %v", writerErr)
	}

	assertBackupSnapshot(t, destination)
}

func assertBackupSnapshot(t *testing.T, destination string) {
	t.Helper()

	snapshot := testDatabase(t, sqliteURI(destination))
	t.Cleanup(func() {
		requireNoError(t, snapshot.Close())
	})

	assertSnapshotMetadata(t, snapshot)
	assertSnapshotFixture(t, snapshot)
}

func assertSnapshotMetadata(t *testing.T, snapshot *sql.DB) {
	t.Helper()

	var (
		integrity string
		version   int
	)

	err := snapshot.QueryRowContext(context.Background(), "PRAGMA integrity_check").Scan(&integrity)
	if err != nil || integrity != "ok" {
		t.Fatalf("integrity_check = %q, %v", integrity, err)
	}

	err = snapshot.QueryRowContext(
		context.Background(),
		"SELECT version FROM schema_version WHERE singleton = 1",
	).Scan(&version)
	if err != nil || version != currentSchemaVersion {
		t.Fatalf("schema version = %d, %v", version, err)
	}
}

func assertSnapshotFixture(t *testing.T, snapshot *sql.DB) {
	t.Helper()

	var (
		count   int
		minimum int
		maximum int
	)

	err := snapshot.QueryRowContext(
		context.Background(),
		"SELECT count(*), min(sequence), max(sequence) FROM backup_fixture",
	).Scan(&count, &minimum, &maximum)
	if err != nil || count < 512 || minimum != 1 || maximum != count {
		t.Fatalf("backup prefix = count %d, range %d..%d, %v", count, minimum, maximum, err)
	}

	rows, err := snapshot.QueryContext(context.Background(), "PRAGMA foreign_key_check")
	if err != nil {
		t.Fatal(err)
	}
	defer func() {
		requireNoError(t, rows.Close())
	}()

	if rows.Next() {
		t.Fatal("backup contains a foreign-key violation")
	}

	err = rows.Err()
	if err != nil {
		t.Fatal(err)
	}
}

func seedBackupFixture(t *testing.T, database *sql.DB, payload string) {
	t.Helper()

	_, err := database.ExecContext(
		context.Background(),
		"CREATE TABLE backup_fixture (sequence INTEGER PRIMARY KEY, payload TEXT NOT NULL)",
	)
	requireNoError(t, err)

	for sequence := 1; sequence <= 512; sequence++ {
		_, err = database.ExecContext(
			context.Background(),
			"INSERT INTO backup_fixture (sequence, payload) VALUES (?, ?)",
			sequence,
			payload,
		)
		requireNoError(t, err)
	}
}

func startBackupWriter(database *sql.DB, payload string) (context.CancelFunc, <-chan error) {
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)

	go writeBackupFixture(ctx, database, 513, payload, done)

	return cancel, done
}

func TestBackupSQLiteContainsCancellation(t *testing.T) {
	t.Parallel()

	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = backupSQLite(ctx, state.database, filepath.Join(privateTempDir(t), "snapshot.db"))
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("backupSQLite() error = %v", err)
	}
}

func TestBackupSQLiteContainsDriverFailure(t *testing.T) {
	t.Parallel()

	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	err = backupSQLite(
		context.Background(),
		state.database,
		filepath.Join(t.TempDir(), "missing", "snapshot.db"),
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("backupSQLite() error = %v", err)
	}
}

func TestRunOnlineBackupRejectsWrongDriverAndDestination(t *testing.T) {
	t.Parallel()

	err := runOnlineBackup(context.Background(), struct{}{}, filepath.Join(privateTempDir(t), "snapshot.db"))
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("runOnlineBackup(wrong driver) error = %v", err)
	}

	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	connection, err := state.database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		requireNoError(t, connection.Close())
	})

	err = connection.Raw(func(raw any) error {
		return runOnlineBackup(context.Background(), raw, filepath.Join(t.TempDir(), "missing", "snapshot.db"))
	})
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("runOnlineBackup(missing destination) error = %v", err)
	}
}

func TestRunOnlineBackupStopsBeforeFirstStep(t *testing.T) {
	t.Parallel()

	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	connection, err := state.database.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}

	t.Cleanup(func() {
		requireNoError(t, connection.Close())
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	destination := filepath.Join(privateTempDir(t), "snapshot.db")

	err = connection.Raw(func(raw any) error {
		return runOnlineBackup(ctx, raw, destination)
	})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("runOnlineBackup() error = %v", err)
	}
}

func TestPerformOnlineBackupContainsStepAndFinishFailures(t *testing.T) {
	t.Parallel()

	stepFailure := &fakeBackup{
		steps:     []fakeBackupStep{{more: false, err: ErrInvalidState}},
		finishErr: nil,
		finished:  0,
	}

	err := performOnlineBackup(context.Background(), stepFailure)
	if !errors.Is(err, ErrUnavailable) || stepFailure.finished != 1 {
		t.Fatalf("performOnlineBackup(step) = %v, finishes %d", err, stepFailure.finished)
	}

	finishFailure := &fakeBackup{
		steps:     []fakeBackupStep{{more: false, err: nil}},
		finishErr: ErrInvalidState,
		finished:  0,
	}

	err = performOnlineBackup(context.Background(), finishFailure)
	if !errors.Is(err, ErrUnavailable) || finishFailure.finished != 1 {
		t.Fatalf("performOnlineBackup(finish) = %v, finishes %d", err, finishFailure.finished)
	}
}

func TestPerformOnlineBackupRetriesContention(t *testing.T) {
	t.Parallel()

	backup := &fakeBackup{
		steps: []fakeBackupStep{
			{more: false, err: sqliteCodeError(sqliteResultBusy)},
			{more: false, err: sqliteCodeError(sqliteResultLocked | 1<<8)},
			{more: false, err: nil},
		},
		finishErr: nil,
		finished:  0,
	}
	waits := 0

	err := performOnlineBackupWithWait(context.Background(), backup, func(context.Context) bool {
		waits++

		return true
	})
	if err != nil || waits != 2 || backup.finished != 1 {
		t.Fatalf("performOnlineBackupWithWait() = %v, waits %d, finishes %d", err, waits, backup.finished)
	}
}

func TestPerformOnlineBackupBoundsContentionAndCancellation(t *testing.T) {
	t.Parallel()

	steps := make([]fakeBackupStep, backupRetryLimit+1)
	for index := range steps {
		steps[index] = fakeBackupStep{more: false, err: sqliteCodeError(sqliteResultBusy)}
	}

	backup := &fakeBackup{steps: steps, finishErr: nil, finished: 0}
	waits := 0

	err := performOnlineBackupWithWait(context.Background(), backup, func(context.Context) bool {
		waits++

		return true
	})
	if !errors.Is(err, ErrUnavailable) || waits != backupRetryLimit || backup.finished != 1 {
		t.Fatalf("bounded backup = %v, waits %d, finishes %d", err, waits, backup.finished)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancelled := &fakeBackup{
		steps:     []fakeBackupStep{{more: false, err: sqliteCodeError(sqliteResultLocked)}},
		finishErr: nil,
		finished:  0,
	}

	err = performOnlineBackupWithWait(ctx, cancelled, func(context.Context) bool {
		cancel()

		return false
	})
	if !errors.Is(err, context.Canceled) || cancelled.finished != 1 {
		t.Fatalf("cancelled backup = %v, finishes %d", err, cancelled.finished)
	}
}

func TestRetryableBackupErrorAndWait(t *testing.T) {
	t.Parallel()

	if retryableBackupError(sqliteCodeError(1)) || !retryableBackupError(sqliteCodeError(sqliteResultBusy)) {
		t.Fatal("retryableBackupError() misclassified a SQLite result")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	if waitForBackupRetry(ctx) {
		t.Fatal("waitForBackupRetry(cancelled) succeeded")
	}

	if !waitForBackupRetry(context.Background()) {
		t.Fatal("waitForBackupRetry(background) failed")
	}
}

func TestClassifyBackupResult(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := classifyBackupResult(ctx, ErrUnavailable, nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("classifyBackupResult(cancelled) error = %v", err)
	}

	tests := []struct {
		name      string
		operation error
		close     error
		want      error
	}{
		{name: "invalid", operation: ErrInvalidState, close: nil, want: ErrInvalidState},
		{name: "operation", operation: ErrUnavailable, close: nil, want: ErrUnavailable},
		{name: "close", operation: nil, close: ErrInvalidState, want: ErrUnavailable},
		{name: "success", operation: nil, close: nil, want: nil},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := classifyBackupResult(context.Background(), test.operation, test.close)
			if !errors.Is(err, test.want) {
				t.Fatalf("classifyBackupResult() error = %v, want %v", err, test.want)
			}
		})
	}
}

type fakeBackupStep struct {
	more bool
	err  error
}

type fakeBackup struct {
	steps     []fakeBackupStep
	finishErr error
	finished  int
}

type sqliteCodeError int

func (err sqliteCodeError) Error() string {
	return "sqlite result " + strconv.Itoa(int(err))
}

func (err sqliteCodeError) Code() int {
	return int(err)
}

func writeBackupFixture(ctx context.Context, database *sql.DB, first int, payload string, done chan<- error) {
	for sequence := first; sequence <= backupFixtureLast; sequence++ {
		_, err := database.ExecContext(
			ctx,
			"INSERT INTO backup_fixture (sequence, payload) VALUES (?, ?)",
			sequence,
			payload,
		)
		if err != nil {
			done <- err

			return
		}
	}

	done <- nil
}

func (backup *fakeBackup) Step(_ int32) (bool, error) {
	step := backup.steps[0]
	backup.steps = backup.steps[1:]

	return step.more, step.err
}

func (backup *fakeBackup) Finish() error {
	backup.finished++

	return backup.finishErr
}
