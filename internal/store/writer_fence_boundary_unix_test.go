//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"
)

func TestWriterFenceContainsCancellationAndCheckFailure(t *testing.T) {
	t.Parallel()

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		t.Cleanup(func() {
			requireNoError(t, lock.Close())
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := lock.Fence(ctx)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("Fence(cancelled) = %v", err)
		}
	})

	t.Run("check failure", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		t.Cleanup(func() {
			requireNoError(t, lock.Close())
		})

		err := lock.fenceWith(
			context.Background(),
			func(context.Context, *sql.DB, writerLease) error { return ErrUnavailable },
		)
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("fenceWith(check failure) = %v", err)
		}
	})

	t.Run("cancelled after check", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		t.Cleanup(func() {
			requireNoError(t, lock.Close())
		})

		ctx, cancel := context.WithCancel(context.Background())

		err := lock.fenceWith(ctx, func(context.Context, *sql.DB, writerLease) error {
			cancel()

			return nil
		})
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("fenceWith(post-check cancellation) = %v", err)
		}
	})
}

func TestWriterFenceDetectsFilesystemDrift(t *testing.T) {
	t.Parallel()

	t.Run("invalid before check", func(t *testing.T) {
		t.Parallel()

		directory := privateTempDir(t)
		state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		replaceServiceLockEntry(t, directory, lock)

		if !errors.Is(lock.Fence(context.Background()), ErrInvalidState) {
			t.Fatal("Fence() accepted a replaced filesystem lock")
		}

		if !errors.Is(lock.Close(), ErrInvalidState) {
			t.Fatal("Close() accepted a replaced filesystem lock")
		}
	})

	t.Run("replacement after check", func(t *testing.T) {
		t.Parallel()

		directory := privateTempDir(t)
		state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")

		err := lock.fenceWith(context.Background(), func(context.Context, *sql.DB, writerLease) error {
			replaceServiceLockEntry(t, directory, lock)

			return nil
		})
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("fenceWith(post-check replacement) = %v", err)
		}

		if !errors.Is(lock.Close(), ErrInvalidState) {
			t.Fatal("Close() accepted a replaced filesystem lock")
		}
	})

	if !errors.Is((*ServiceLock)(nil).fenceWith(context.Background(), nil), ErrOwnershipLost) {
		t.Fatal("nil ServiceLock.fenceWith() succeeded")
	}
}

func TestWriterFenceDetectsSQLiteOwnershipLoss(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	lock := requireTryServiceLock(t, state, "project", "api")

	_, err := state.database.ExecContext(
		context.Background(),
		"UPDATE writer_leases SET owner = ? WHERE service_id = ?",
		[]byte("different-owner!"),
		lock.lease.serviceID[:],
	)
	requireNoError(t, err)

	if !errors.Is(lock.Fence(context.Background()), ErrOwnershipLost) {
		t.Fatal("Fence() accepted a replaced SQLite owner")
	}

	if !errors.Is(lock.Close(), ErrOwnershipLost) {
		t.Fatal("Close() accepted a replaced SQLite owner")
	}
}

func TestFencedWriteContainsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	operation := func(context.Context, *sql.Tx, writerLease) error { return nil }
	if !errors.Is((*ServiceLock)(nil).withFencedWrite(context.Background(), operation), ErrInvalidState) {
		t.Fatal("nil ServiceLock.withFencedWrite() succeeded")
	}

	t.Run("nil operation", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		t.Cleanup(func() {
			requireNoError(t, lock.Close())
		})

		if !errors.Is(lock.withFencedWrite(context.Background(), nil), ErrInvalidState) {
			t.Fatal("withFencedWrite(nil operation) succeeded")
		}
	})

	t.Run("cancelled", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		t.Cleanup(func() {
			requireNoError(t, lock.Close())
		})

		ctx, cancel := context.WithCancel(context.Background())
		cancel()

		err := lock.withFencedWrite(ctx, operation)
		if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
			t.Fatalf("withFencedWrite(cancelled) = %v", err)
		}
	})

	t.Run("invalid lock", func(t *testing.T) {
		t.Parallel()

		directory := privateTempDir(t)
		state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		replaceServiceLockEntry(t, directory, lock)

		if !errors.Is(lock.withFencedWrite(context.Background(), operation), ErrInvalidState) {
			t.Fatal("withFencedWrite() accepted a replaced lock")
		}

		if !errors.Is(lock.Close(), ErrInvalidState) {
			t.Fatal("Close() accepted a replaced lock")
		}
	})
}

func TestFencedWriteRejectsClosedDatabaseAndStaleOwner(t *testing.T) {
	t.Parallel()

	operation := func(context.Context, *sql.Tx, writerLease) error { return nil }

	t.Run("closed database", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		closed := testDatabase(t, "file::memory:")
		requireNoError(t, closed.Close())

		originalStore := lock.store
		lock.store = &Store{database: closed, anchor: state.anchor}

		if !errors.Is(lock.withFencedWrite(context.Background(), operation), ErrInvalidState) {
			t.Fatal("withFencedWrite() accepted a closed database")
		}

		lock.store = originalStore
		requireNoError(t, lock.Close())
	})

	t.Run("stale owner", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		lock := requireTryServiceLock(t, state, "project", "api")
		_, err := state.database.ExecContext(
			context.Background(),
			"UPDATE writer_leases SET owner = NULL WHERE service_id = ?",
			lock.lease.serviceID[:],
		)
		requireNoError(t, err)

		if !errors.Is(lock.withFencedWrite(context.Background(), operation), ErrOwnershipLost) {
			t.Fatal("withFencedWrite() accepted a stale owner")
		}

		if !errors.Is(lock.Close(), ErrOwnershipLost) {
			t.Fatal("Close() accepted a stale owner")
		}
	})
}
