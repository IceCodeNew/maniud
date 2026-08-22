//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestBackupIndexLockReplacesCompleteIndex(t *testing.T) {
	t.Parallel()

	state := openJournalStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() { requireNoError(t, state.Close()) })

	api := committedBackupCandidate(t, state, "api")
	worker := committedBackupCandidate(t, state, "worker")
	_, err := state.database.ExecContext(context.Background(), "DELETE FROM workload_backups")
	requireNoError(t, err)

	lock, err := state.LockBackupIndex(context.Background())
	if err != nil || lock == nil {
		t.Fatalf("LockBackupIndex() = %#v, %v", lock, err)
	}
	requireNoError(t, lock.ReplaceBackupIndex(context.Background(), []BackupIndexCandidate{api, worker}))
	requireNoError(t, lock.Close())

	assertBackupCandidateIndexed(t, state, api)
	assertBackupCandidateIndexed(t, state, worker)

	lock, err = state.LockBackupIndex(context.Background())
	if err != nil || lock == nil {
		t.Fatalf("LockBackupIndex() = %#v, %v", lock, err)
	}
	requireNoError(t, lock.ReplaceBackupIndex(context.Background(), nil))
	requireNoError(t, lock.Close())

	_, found, err := state.BackupIndex(context.Background(), api.TransactionID)
	if err != nil || found {
		t.Fatalf("BackupIndex() after empty replacement = %t, %v", found, err)
	}
}

func TestBackupIndexReplacementRollsBackWholeIndex(t *testing.T) {
	t.Parallel()

	state := openJournalStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() { requireNoError(t, state.Close()) })

	api := committedBackupCandidate(t, state, "api")
	worker := committedBackupCandidate(t, state, "worker")
	worker.SourceDigest = domain.Hash([]byte("different source"))

	lock, err := state.LockBackupIndex(context.Background())
	if err != nil || lock == nil {
		t.Fatalf("LockBackupIndex() = %#v, %v", lock, err)
	}
	err = lock.ReplaceBackupIndex(context.Background(), []BackupIndexCandidate{api, worker})
	assertErrorIs(t, err, ErrInvalidState)
	requireNoError(t, lock.Close())

	assertBackupCandidateIndexed(t, state, api)
	worker.SourceDigest = testTransactionIntent(domain.RuntimeDocker).SourceDigest
	assertBackupCandidateIndexed(t, state, worker)

	_, err = state.database.ExecContext(
		context.Background(),
		"CREATE TRIGGER reject_rebuild BEFORE INSERT ON workload_backups "+
			"BEGIN SELECT RAISE(ABORT, 'rejected'); END",
	)
	requireNoError(t, err)
	lock, err = state.LockBackupIndex(context.Background())
	if err != nil || lock == nil {
		t.Fatalf("LockBackupIndex() = %#v, %v", lock, err)
	}
	assertErrorIs(t, lock.ReplaceBackupIndex(context.Background(), []BackupIndexCandidate{api}), ErrInvalidState)
	requireNoError(t, lock.Close())
	assertBackupCandidateIndexed(t, state, api)
	assertBackupCandidateIndexed(t, state, worker)
}

func TestBackupIndexLockExcludesAndDetectsWriters(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	state := openJournalStore(t, filepath.Join(directory, "state.db"))
	t.Cleanup(func() { requireNoError(t, state.Close()) })

	service := requireTryServiceLock(t, state, "project", "api")
	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Millisecond)
	defer cancel()

	lock, err := state.LockBackupIndex(ctx)
	if lock != nil || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("LockBackupIndex(service writer) = %#v, %v", lock, err)
	}
	requireNoError(t, service.Close())

	lock, err = state.LockBackupIndex(context.Background())
	if err != nil || lock == nil {
		t.Fatalf("LockBackupIndex() = %#v, %v", lock, err)
	}
	service, err = state.TryLockService("project", "api")
	if service != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("TryLockService(backup index lock) = %#v, %v", service, err)
	}

	entry := filepath.Join(directory, lock.operation.name)
	requireNoError(t, os.Rename(entry, entry+".replaced"))
	requireNoError(t, os.WriteFile(entry, nil, privateFileMode))

	assertErrorIs(t, lock.ReplaceBackupIndex(context.Background(), nil), ErrInvalidState)
	assertErrorIs(t, lock.Close(), ErrInvalidState)
}

func TestBackupIndexLockRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	state := openJournalStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() { requireNoError(t, state.Close()) })

	if lock, err := (*Store)(nil).LockBackupIndex(context.Background()); lock != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("nil Store.LockBackupIndex() = %#v, %v", lock, err)
	}
	if lock, err := state.lockBackupIndexWith(context.Background(), nil); lock != nil ||
		!errors.Is(err, ErrInvalidState) {
		t.Fatalf("lockBackupIndexWith(nil acquisition) = %#v, %v", lock, err)
	}
	if lock, err := state.lockBackupIndexWith(
		context.Background(),
		func(context.Context, *stateAnchor) (*stateOperationLock, error) {
			return &stateOperationLock{descriptor: -1}, nil
		},
	); lock != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("lockBackupIndexWith(invalid acquisition) = %#v, %v", lock, err)
	}
	if !errors.Is((*BackupIndexLock)(nil).ReplaceBackupIndex(context.Background(), nil), ErrInvalidState) ||
		!errors.Is((*BackupIndexLock)(nil).Close(), ErrUnavailable) {
		t.Fatal("nil BackupIndexLock accepted an operation")
	}
	if !errors.Is(replaceBackupIndex(context.Background(), nil, nil), ErrInvalidState) {
		t.Fatal("replaceBackupIndex(nil transaction) succeeded")
	}
}

func TestBackupIndexReplacementRejectsInvalidCandidates(t *testing.T) {
	t.Parallel()

	state := openJournalStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() { requireNoError(t, state.Close()) })
	candidate := committedBackupCandidate(t, state, "api")
	lock, err := state.LockBackupIndex(context.Background())
	if err != nil || lock == nil {
		t.Fatalf("LockBackupIndex() = %#v, %v", lock, err)
	}

	invalid := candidate
	invalid.Project = ""
	assertErrorIs(t, lock.ReplaceBackupIndex(context.Background(), []BackupIndexCandidate{invalid}), ErrInvalidState)
	assertErrorIs(t, lock.replaceBackupIndexWith(context.Background(), nil, nil), ErrInvalidState)
	assertErrorIs(
		t,
		lock.ReplaceBackupIndex(context.Background(), []BackupIndexCandidate{candidate, candidate}),
		ErrInvalidState,
	)

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	err = lock.ReplaceBackupIndex(cancelled, []BackupIndexCandidate{candidate})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ReplaceBackupIndex(cancelled) = %v", err)
	}

	requireNoError(t, lock.Close())
}

func TestBackupIndexReplacementContainsTransactionBoundaries(t *testing.T) {
	t.Parallel()

	state := openJournalStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() { requireNoError(t, state.Close()) })
	candidate := committedBackupCandidate(t, state, "api")

	lock, err := state.LockBackupIndex(context.Background())
	if err != nil || lock == nil {
		t.Fatalf("LockBackupIndex() = %#v, %v", lock, err)
	}

	closed := testDatabase(t, "file::memory:")
	requireNoError(t, closed.Close())
	database := lock.store.database
	lock.store.database = closed
	assertErrorIs(t, lock.ReplaceBackupIndex(context.Background(), []BackupIndexCandidate{candidate}), ErrInvalidState)
	lock.store.database = database

	cancelled, cancel := context.WithCancel(context.Background())
	err = lock.replaceBackupIndexWith(
		cancelled,
		[]BackupIndexCandidate{candidate},
		func(context.Context, *sql.Tx, []BackupIndexCandidate) error {
			cancel()

			return nil
		},
	)
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("replaceBackupIndexWith(post-operation cancellation) = %v", err)
	}

	entry := filepath.Join(state.anchor.directoryPath, lock.operation.name)
	err = lock.replaceBackupIndexWith(
		context.Background(),
		[]BackupIndexCandidate{candidate},
		func(context.Context, *sql.Tx, []BackupIndexCandidate) error {
			requireNoError(t, os.Rename(entry, entry+".replaced"))
			requireNoError(t, os.WriteFile(entry, nil, privateFileMode))

			return nil
		},
	)
	assertErrorIs(t, err, ErrInvalidState)
	assertErrorIs(t, lock.Close(), ErrInvalidState)

	missing := testDatabase(t, "file::memory:")
	transaction, err := missing.BeginTx(context.Background(), nil)
	requireNoError(t, err)
	assertErrorIs(t, replaceBackupIndex(context.Background(), transaction, nil), ErrInvalidState)
	requireNoError(t, transaction.Rollback())
	requireNoError(t, missing.Close())
}

func committedBackupCandidate(
	t *testing.T,
	state *Store,
	service string,
) BackupIndexCandidate {
	t.Helper()

	lock := requireTryServiceLock(t, state, "project", service)
	_, upgrade := beginUpgradeForBackupTest(t, lock)
	intent := testAppliedServiceIntent()
	intent.WorkloadID = replacementWorkloadID
	intent.Backup = testBackupIndexIntent(upgrade.ID)
	_, err := lock.CommitAppliedService(context.Background(), upgrade.ID, intent)
	requireNoError(t, err)
	requireNoError(t, lock.Close())

	return BackupIndexCandidate{
		TransactionID:         upgrade.ID,
		Project:               "project",
		Service:               service,
		Runtime:               upgrade.Runtime,
		SourceDigest:          upgrade.SourceDigest,
		EffectiveDigest:       upgrade.EffectiveDigest,
		ExecutionDigest:       upgrade.ExecutionDigest,
		BaseTransactionID:     upgrade.BaseTransactionID,
		PredecessorWorkloadID: upgrade.PredecessorWorkloadID,
		ManifestPath:          intent.Backup.ManifestPath,
		ManifestDigest:        intent.Backup.ManifestDigest,
		CreatedUnix:           intent.Backup.CreatedUnix,
	}
}

func assertBackupCandidateIndexed(t *testing.T, state *Store, candidate BackupIndexCandidate) {
	t.Helper()

	want := BackupIndex{
		TransactionID:  candidate.TransactionID,
		Runtime:        candidate.Runtime,
		ManifestPath:   candidate.ManifestPath,
		ManifestDigest: candidate.ManifestDigest,
		CreatedUnix:    candidate.CreatedUnix,
	}
	got, found, err := state.BackupIndex(context.Background(), candidate.TransactionID)
	if err != nil || !found || got != want {
		t.Fatalf("BackupIndex() = %#v, %t, %v, want %#v", got, found, err, want)
	}
}
