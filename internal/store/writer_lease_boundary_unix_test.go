//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"math"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestOpenServiceLockRejectsInvalidAnchorAndAcquirer(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	state.anchor.directoryID = fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}

	lock, err := state.openServiceLockWith(
		context.Background(),
		"project",
		"api",
		newWriterLease,
	)
	if lock != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("openServiceLockWith(invalid anchor) = %#v, %v", lock, err)
	}

	lock, err = state.openServiceLockWith(context.Background(), "project", "api", nil)
	if lock != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("openServiceLockWith(nil acquisition) = %#v, %v", lock, err)
	}
}

func TestOpenServiceLockContainsLeaseAcquisitionFailure(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))

	lock, err := state.openServiceLockWith(
		context.Background(),
		"project",
		"api",
		func(context.Context, *sql.DB, [32]byte) (writerLease, error) {
			return writerLease{}, ErrUnavailable
		},
	)
	if lock != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("openServiceLockWith(failed acquisition) = %#v, %v", lock, err)
	}

	owner := requireTryServiceLock(t, state, "project", "api")
	requireNoError(t, owner.Close())
}

func TestOpenServiceLockRejectsPostAcquisitionReplacement(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))

	name, valid := serviceLockName(state.anchor.databaseName, "project", "api")
	if !valid {
		t.Fatal("serviceLockName() rejected a valid identity")
	}

	lock, err := state.openServiceLockWith(
		context.Background(),
		"project",
		"api",
		func(ctx context.Context, database *sql.DB, serviceID [32]byte) (writerLease, error) {
			lease, acquireErr := newWriterLease(ctx, database, serviceID)
			if acquireErr != nil {
				return writerLease{}, acquireErr
			}

			entry := filepath.Join(directory, name)

			renameErr := os.Rename(entry, entry+".replaced")
			if renameErr != nil {
				return writerLease{}, ErrInvalidState
			}

			writeErr := os.WriteFile(entry, nil, privateFileMode)
			if writeErr != nil {
				return writerLease{}, ErrInvalidState
			}

			return lease, nil
		},
	)
	if lock != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("openServiceLockWith(replaced) = %#v, %v", lock, err)
	}
}

func TestCloseFilesystemRejectsNilLock(t *testing.T) {
	t.Parallel()

	if !errors.Is((*ServiceLock)(nil).closeFilesystem(), ErrUnavailable) {
		t.Fatal("nil ServiceLock.closeFilesystem() succeeded")
	}
}

func TestAcquireWriterLeaseRejectsUnsafeState(t *testing.T) {
	t.Parallel()

	serviceID, valid := serviceIdentity("project", "api")
	if !valid {
		t.Fatal("serviceIdentity() rejected valid names")
	}

	_, err := acquireWriterLease(
		context.Background(),
		nil,
		serviceID,
		strings.NewReader("owner-owner-owner"),
	)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("acquireWriterLease(nil database) = %v", err)
	}

	database := writerLeaseFixtureDatabase(t, "")

	_, err = acquireWriterLease(context.Background(), database, serviceID, nil)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("acquireWriterLease(nil random) = %v", err)
	}

	_, err = acquireWriterLease(context.Background(), database, serviceID, strings.NewReader("short"))
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("acquireWriterLease(short random) = %v", err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	_, err = acquireWriterLease(cancelled, database, serviceID, fixedWriterOwner())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("acquireWriterLease(cancelled) = %v", err)
	}

	missingTable := testDatabase(t, "file::memory:")
	missingTable.SetMaxOpenConns(1)
	missingTable.SetMaxIdleConns(1)

	_, err = acquireWriterLease(context.Background(), missingTable, serviceID, fixedWriterOwner())
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("acquireWriterLease(missing table) = %v", err)
	}

	requireNoError(t, missingTable.Close())
}

func TestAcquireWriterLeaseRejectsEpochAndCommitFailures(t *testing.T) {
	t.Parallel()

	serviceID, _ := serviceIdentity("project", "api")

	t.Run("exhausted epoch", func(t *testing.T) {
		t.Parallel()

		database := writerLeaseFixtureDatabase(t, "")
		_, err := database.ExecContext(
			context.Background(),
			"INSERT INTO writer_leases (service_id, epoch, owner) VALUES (?, ?, NULL)",
			serviceID[:],
			int64(math.MaxInt64),
		)
		requireNoError(t, err)

		_, err = acquireWriterLease(context.Background(), database, serviceID, fixedWriterOwner())
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("acquireWriterLease(exhausted) = %v", err)
		}
	})

	t.Run("deleted row", func(t *testing.T) {
		t.Parallel()

		database := writerLeaseFixtureDatabase(t,
			"CREATE TRIGGER delete_lease AFTER INSERT ON writer_leases "+
				"BEGIN DELETE FROM writer_leases WHERE service_id = NEW.service_id; END",
		)

		_, err := acquireWriterLease(context.Background(), database, serviceID, fixedWriterOwner())
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("acquireWriterLease(deleted row) = %v", err)
		}
	})

	t.Run("invalid epoch", func(t *testing.T) {
		t.Parallel()

		database := writerLeaseFixtureDatabase(t,
			"CREATE TRIGGER zero_epoch AFTER INSERT ON writer_leases "+
				"BEGIN UPDATE writer_leases SET epoch = 0 WHERE service_id = NEW.service_id; END",
		)

		_, err := acquireWriterLease(context.Background(), database, serviceID, fixedWriterOwner())
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("acquireWriterLease(zero epoch) = %v", err)
		}
	})

	t.Run("commit", func(t *testing.T) {
		t.Parallel()

		database := writerLeaseFixtureDatabase(t, deferredWriterLeaseFailure("INSERT"))

		_, err := acquireWriterLease(context.Background(), database, serviceID, fixedWriterOwner())
		if !errors.Is(err, ErrInvalidState) {
			t.Fatalf("acquireWriterLease(commit) = %v", err)
		}
	})
}

func TestReleaseAndCheckWriterLeaseContainFailures(t *testing.T) {
	t.Parallel()

	serviceID, _ := serviceIdentity("project", "api")
	zero := writerLease{
		serviceID: serviceID,
		epoch:     0,
		owner:     [writerOwnerBytes]byte{},
	}

	if !errors.Is(checkWriterLease(context.Background(), nil, zero), ErrOwnershipLost) ||
		!errors.Is(releaseWriterLease(context.Background(), nil, zero), ErrOwnershipLost) {
		t.Fatal("zero writer lease passed check or release")
	}

	database := writerLeaseFixtureDatabase(t, "")
	lease, err := acquireWriterLease(context.Background(), database, serviceID, fixedWriterOwner())
	requireNoError(t, err)
	requireNoError(t, releaseWriterLease(context.Background(), database, lease))

	if !errors.Is(checkWriterLease(context.Background(), database, lease), ErrOwnershipLost) ||
		!errors.Is(releaseWriterLease(context.Background(), database, lease), ErrOwnershipLost) {
		t.Fatal("released writer lease remained current")
	}

	missingTable := testDatabase(t, "file::memory:")
	missingTable.SetMaxOpenConns(1)
	missingTable.SetMaxIdleConns(1)

	if !errors.Is(checkWriterLease(context.Background(), missingTable, lease), ErrInvalidState) ||
		!errors.Is(releaseWriterLease(context.Background(), missingTable, lease), ErrInvalidState) {
		t.Fatal("missing writer lease table was accepted")
	}

	requireNoError(t, missingTable.Close())

	closed := writerLeaseFixtureDatabase(t, "")
	requireNoError(t, closed.Close())

	if !errors.Is(releaseWriterLease(context.Background(), closed, lease), ErrInvalidState) {
		t.Fatal("releaseWriterLease(closed) was accepted")
	}
}

func TestReleaseWriterLeaseContainsCommitFailure(t *testing.T) {
	t.Parallel()

	serviceID, _ := serviceIdentity("project", "api")
	database := writerLeaseFixtureDatabase(t, deferredWriterLeaseFailure("UPDATE OF owner"))

	lease, err := acquireWriterLease(context.Background(), database, serviceID, fixedWriterOwner())
	requireNoError(t, err)

	err = releaseWriterLease(context.Background(), database, lease)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("releaseWriterLease(commit) = %v", err)
	}
}

func writerLeaseFixtureDatabase(t *testing.T, extraSQL string) *sql.DB {
	t.Helper()

	database := testDatabase(
		t,
		"file::memory:?_pragma=foreign_keys(ON)&_txlock=immediate",
	)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	_, err := database.ExecContext(
		context.Background(),
		"CREATE TABLE writer_leases (service_id BLOB PRIMARY KEY, epoch INTEGER NOT NULL, owner BLOB); "+extraSQL,
	)
	requireNoError(t, err)

	t.Cleanup(func() {
		_ = database.Close()
	})

	return database
}

func deferredWriterLeaseFailure(event string) string {
	return "CREATE TABLE lease_parent (id INTEGER PRIMARY KEY); " +
		"CREATE TABLE lease_child (parent_id INTEGER REFERENCES lease_parent(id) " +
		"DEFERRABLE INITIALLY DEFERRED); " +
		"CREATE TRIGGER fail_lease_commit AFTER " + event + " ON writer_leases " +
		"BEGIN INSERT INTO lease_child (parent_id) VALUES (1); END"
}

func fixedWriterOwner() *bytes.Reader {
	return bytes.NewReader(bytes.Repeat([]byte{0x5a}, writerOwnerBytes))
}
