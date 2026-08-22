//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	maximumBackupIdentityBytes = 256
	rebuildBackupIndexSQL      = "INSERT INTO workload_backups " +
		"(transaction_id, service_id, manifest_path, manifest_digest, created_unix) " +
		"SELECT transaction_id, service_id, ?, ?, ? FROM journal_transactions " +
		"WHERE transaction_id = ? AND service_id = ? AND kind = 'upgrade' AND state = 'succeeded' " +
		"AND runtime = ? AND source_digest = ? AND effective_digest = ? AND execution_digest = ? " +
		"AND base_transaction_id = ? AND predecessor_workload_id = ?"
)

// BackupIndexLock excludes service writers while a maintenance caller scans
// complete manifests and atomically replaces the private backup index.
type BackupIndexLock struct {
	store     *Store
	operation *stateOperationLock
}

// LockBackupIndex waits for active service writers to finish and prevents new
// writers from starting until the returned lock is closed.
func (store *Store) LockBackupIndex(ctx context.Context) (*BackupIndexLock, error) {
	return store.lockBackupIndexWith(ctx, lockExclusiveStateOperation)
}

func (store *Store) lockBackupIndexWith(
	ctx context.Context,
	acquire func(context.Context, *stateAnchor) (*stateOperationLock, error),
) (*BackupIndexLock, error) {
	if store == nil || store.database == nil || store.anchor == nil || acquire == nil {
		return nil, ErrInvalidState
	}

	operation, err := acquire(ctx, store.anchor)
	if err != nil {
		return nil, err
	}

	lock := &BackupIndexLock{store: store, operation: operation}
	if !lock.valid() {
		_ = operation.close()

		return nil, ErrInvalidState
	}

	return lock, nil
}

// ReplaceBackupIndex replaces the complete private index in one SQLite
// transaction. It never creates or changes journal or applied-service rows.
func (lock *BackupIndexLock) ReplaceBackupIndex(
	ctx context.Context,
	candidates []BackupIndexCandidate,
) error {
	return lock.replaceBackupIndexWith(ctx, candidates, replaceBackupIndex)
}

// Close permits new service writers to start.
func (lock *BackupIndexLock) Close() error {
	if lock == nil || lock.operation == nil {
		return ErrUnavailable
	}

	valid := lock.valid()
	closeErr := lock.operation.close()
	if !valid {
		return errors.Join(ErrInvalidState, closeErr)
	}

	return closeErr
}

func (lock *BackupIndexLock) replaceBackupIndexWith(
	ctx context.Context,
	candidates []BackupIndexCandidate,
	replace func(context.Context, *sql.Tx, []BackupIndexCandidate) error,
) error {
	if !lock.valid() || !validBackupIndexCandidates(candidates) || replace == nil {
		return ErrInvalidState
	}
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	transaction, err := lock.store.database.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	err = replace(ctx, transaction, candidates)
	if err != nil {
		return err
	}
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}
	if !lock.valid() {
		return ErrInvalidState
	}

	return classifyWriterLeaseResult(ctx, transaction.Commit())
}

func (lock *BackupIndexLock) valid() bool {
	return lock != nil && lock.store != nil && lock.store.database != nil &&
		lock.operation != nil && lock.operation.valid()
}

func validBackupIndexCandidates(candidates []BackupIndexCandidate) bool {
	seen := make(map[TransactionID]struct{}, len(candidates))
	for _, candidate := range candidates {
		if !validBackupIndexCandidate(candidate) {
			return false
		}
		if _, exists := seen[candidate.TransactionID]; exists {
			return false
		}

		seen[candidate.TransactionID] = struct{}{}
	}

	return true
}

func validBackupIndexCandidate(candidate BackupIndexCandidate) bool {
	return validBackupIndexTransaction(candidate) && validBackupIndexManifest(candidate)
}

func validBackupIndexTransaction(candidate BackupIndexCandidate) bool {
	emptyDigest := domain.Digest{}
	_, serviceValid := serviceIdentity(candidate.Project, candidate.Service)

	return candidate.TransactionID != (TransactionID{}) && serviceValid &&
		validBackupIdentity(candidate.Project) && validBackupIdentity(candidate.Service) &&
		candidate.Runtime.SupportsWorkloads() && candidate.SourceDigest != emptyDigest &&
		candidate.EffectiveDigest != emptyDigest && candidate.ExecutionDigest != emptyDigest &&
		candidate.BaseTransactionID != (TransactionID{}) &&
		validWorkloadID(candidate.PredecessorWorkloadID)
}

func validBackupIndexManifest(candidate BackupIndexCandidate) bool {
	return candidate.ManifestPath == backupManifestPath(candidate.TransactionID) &&
		candidate.ManifestDigest != (domain.Digest{}) && candidate.CreatedUnix > 0
}

func validBackupIdentity(value string) bool {
	return len(value) > 0 && len(value) <= maximumBackupIdentityBytes &&
		utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func replaceBackupIndex(
	ctx context.Context,
	transaction *sql.Tx,
	candidates []BackupIndexCandidate,
) error {
	if transaction == nil {
		return ErrInvalidState
	}

	_, err := transaction.ExecContext(ctx, "DELETE FROM workload_backups")
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	for _, candidate := range candidates {
		serviceID, _ := serviceIdentity(candidate.Project, candidate.Service)
		result, execErr := transaction.ExecContext(
			ctx,
			rebuildBackupIndexSQL,
			candidate.ManifestPath,
			candidate.ManifestDigest[:],
			candidate.CreatedUnix,
			candidate.TransactionID[:],
			serviceID[:],
			candidate.Runtime.String(),
			candidate.SourceDigest[:],
			candidate.EffectiveDigest[:],
			candidate.ExecutionDigest[:],
			candidate.BaseTransactionID[:],
			candidate.PredecessorWorkloadID,
		)
		if execErr != nil {
			return classifySQLiteProbe(ctx, execErr)
		}
		if resultErr := requireJournalMutation(result); resultErr != nil {
			return resultErr
		}
	}

	return nil
}
