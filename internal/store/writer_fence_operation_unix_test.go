//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

var (
	errPrivateWriterTest = errors.New("private writer test error")
	errWriterRowsTest    = errors.New("writer rows test error")
)

func TestFencedWriteContainsOperationFailures(t *testing.T) {
	t.Parallel()

	t.Run("private error", func(t *testing.T) {
		t.Parallel()

		_, lock := fencedWriteTestOwner(t)

		err := lock.withFencedWrite(
			context.Background(),
			func(context.Context, *sql.Tx, writerLease) error { return errPrivateWriterTest },
		)
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("withFencedWrite(private error) = %v", err)
		}
	})

	t.Run("cancelled operation", func(t *testing.T) {
		t.Parallel()

		_, lock := fencedWriteTestOwner(t)
		ctx, cancel := context.WithCancel(context.Background())

		err := lock.withFencedWrite(ctx, func(context.Context, *sql.Tx, writerLease) error {
			cancel()

			return errPrivateWriterTest
		})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("withFencedWrite(cancelled operation) = %v", err)
		}
	})

	t.Run("cancelled before commit", func(t *testing.T) {
		t.Parallel()

		_, lock := fencedWriteTestOwner(t)
		ctx, cancel := context.WithCancel(context.Background())

		err := lock.withFencedWrite(ctx, func(context.Context, *sql.Tx, writerLease) error {
			cancel()

			return nil
		})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("withFencedWrite(pre-commit cancellation) = %v", err)
		}
	})
}

func TestFencedWriteRejectsOwnershipChangedByOperation(t *testing.T) {
	t.Parallel()

	_, lock := fencedWriteTestOwner(t)

	err := lock.withFencedWrite(context.Background(), clearWriterLeaseOwner)
	if !errors.Is(err, ErrOwnershipLost) {
		t.Fatalf("withFencedWrite(ownership changed) = %v", err)
	}

	requireNoError(t, lock.Fence(context.Background()))
}

func TestFencedWriteContainsIdentityAndCommitFailures(t *testing.T) {
	t.Parallel()

	t.Run("replacement before commit", func(t *testing.T) {
		t.Parallel()

		directory := privateTempDir(t)
		state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")

		err := lock.withFencedWrite(context.Background(), func(context.Context, *sql.Tx, writerLease) error {
			replaceServiceLockEntry(t, directory, lock)

			return nil
		})
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("withFencedWrite(pre-commit replacement) = %v", err)
		}

		if !errors.Is(lock.Close(), ErrInvalidState) {
			t.Fatal("Close() accepted a replaced lock")
		}
	})

	t.Run("commit", func(t *testing.T) {
		t.Parallel()

		state, lock := fencedWriteTestOwner(t)
		_, err := state.database.ExecContext(
			context.Background(),
			"CREATE TABLE fence_parent (id INTEGER PRIMARY KEY); "+
				"CREATE TABLE fence_child (parent_id INTEGER REFERENCES fence_parent(id) "+
				"DEFERRABLE INITIALLY DEFERRED)",
		)
		requireNoError(t, err)

		err = lock.withFencedWrite(context.Background(), insertInvalidFencedChild)
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("withFencedWrite(commit failure) = %v", err)
		}
	})
}

func TestWriterLeaseResultAndOperationClassifiers(t *testing.T) {
	t.Parallel()

	lease := writerLease{
		serviceID: [32]byte{},
		epoch:     1,
		owner:     [writerOwnerBytes]byte{},
	}
	if !errors.Is(proveWriterLease(context.Background(), nil, lease), ErrOwnershipLost) {
		t.Fatal("proveWriterLease(nil transaction) succeeded")
	}

	missingTable := testDatabase(t, "file::memory:")
	missingTable.SetMaxOpenConns(1)
	missingTable.SetMaxIdleConns(1)
	transaction, err := missingTable.BeginTx(context.Background(), nil)
	requireNoError(t, err)

	if !errors.Is(proveWriterLease(context.Background(), transaction, lease), ErrInvalidState) {
		t.Fatal("proveWriterLease(missing table) succeeded")
	}

	requireNoError(t, transaction.Rollback())
	requireNoError(t, missingTable.Close())

	if !errors.Is(requireWriterLeaseResult(nil), ErrInvalidState) {
		t.Fatal("requireWriterLeaseResult(nil) succeeded")
	}

	if !errors.Is(
		requireWriterLeaseResult(writerLeaseSQLResult{rows: 0, err: errWriterRowsTest}),
		ErrInvalidState,
	) {
		t.Fatal("requireWriterLeaseResult(rows error) succeeded")
	}

	if !errors.Is(
		requireWriterLeaseResult(writerLeaseSQLResult{rows: 0, err: nil}),
		ErrOwnershipLost,
	) {
		t.Fatal("requireWriterLeaseResult(zero rows) succeeded")
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = classifyWriterLeaseOperation(ctx, errPrivateWriterTest)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("classifyWriterLeaseOperation(cancelled) = %v", err)
	}

	if !errors.Is(classifyWriterLeaseOperation(context.Background(), ErrOwnershipLost), ErrOwnershipLost) {
		t.Fatal("classifyWriterLeaseOperation() replaced a stable error")
	}

	if !errors.Is(classifyWriterLeaseOperation(context.Background(), errPrivateWriterTest), ErrInvalidState) {
		t.Fatal("classifyWriterLeaseOperation() exposed a private error")
	}
}

func fencedWriteTestOwner(t *testing.T) (*Store, *ServiceLock) {
	t.Helper()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	lock := requireTryServiceLock(t, state, "project", "api")
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
	})

	return state, lock
}

func insertInvalidFencedChild(ctx context.Context, transaction *sql.Tx, lease writerLease) error {
	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO fence_child (parent_id) SELECT 1 "+
			"WHERE EXISTS (SELECT 1 FROM writer_leases "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?)",
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	return requireWriterLeaseResult(result)
}

func clearWriterLeaseOwner(ctx context.Context, transaction *sql.Tx, lease writerLease) error {
	result, err := transaction.ExecContext(
		ctx,
		"UPDATE writer_leases SET owner = NULL "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?",
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	return requireWriterLeaseResult(result)
}

func replaceServiceLockEntry(t *testing.T, directory string, lock *ServiceLock) {
	t.Helper()

	entry := filepath.Join(directory, lock.name)
	requireNoError(t, os.Rename(entry, entry+".replaced"))
	requireNoError(t, os.WriteFile(entry, nil, privateFileMode))
}

type writerLeaseSQLResult struct {
	rows int64
	err  error
}

func (result writerLeaseSQLResult) LastInsertId() (int64, error) {
	return 0, result.err
}

func (result writerLeaseSQLResult) RowsAffected() (int64, error) {
	return result.rows, result.err
}
