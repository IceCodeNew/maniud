//go:build linux || darwin

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
)

// BeginTransaction creates the sole unresolved transaction for the locked
// service and returns its random runtime ownership identifier.
func (lock *ServiceLock) BeginTransaction(
	ctx context.Context,
	intent TransactionIntent,
) (Transaction, error) {
	return lock.beginTransaction(ctx, intent, rand.Reader)
}

func (lock *ServiceLock) beginTransaction(
	ctx context.Context,
	intent TransactionIntent,
	random io.Reader,
) (Transaction, error) {
	var record Transaction

	if !validTransactionIntent(intent) || random == nil {
		return record, ErrInvalidState
	}

	_, err := io.ReadFull(random, record.ID[:])
	if err != nil {
		return Transaction{}, ErrUnavailable
	}

	err = lock.withFencedWrite(ctx, func(
		ctx context.Context,
		transaction *sql.Tx,
		lease writerLease,
	) error {
		result, execErr := transaction.ExecContext(
			ctx,
			"INSERT INTO journal_transactions "+
				"(transaction_id, service_id, state, runtime, source_digest, effective_digest, execution_digest) "+
				"SELECT ?, ?, 'active', ?, ?, ?, ? "+
				"WHERE EXISTS (SELECT 1 FROM writer_leases "+
				"WHERE service_id = ? AND epoch = ? AND owner = ?) "+
				"AND NOT EXISTS (SELECT 1 FROM journal_transactions "+
				"WHERE service_id = ? AND state IN ('active', 'degraded'))",
			record.ID[:],
			lease.serviceID[:],
			intent.Runtime.String(),
			intent.SourceDigest[:],
			intent.EffectiveDigest[:],
			intent.ExecutionDigest[:],
			lease.serviceID[:],
			lease.epoch,
			lease.owner[:],
			lease.serviceID[:],
		)
		if execErr != nil {
			return classifySQLiteProbe(ctx, execErr)
		}

		return requireJournalMutation(result)
	})
	if err != nil {
		return Transaction{}, err
	}

	return lock.store.Transaction(ctx, record.ID)
}

// SetTransactionState marks a transaction degraded or terminal only after all
// external-effect outcomes have been resolved by typed probes.
func (lock *ServiceLock) SetTransactionState(
	ctx context.Context,
	identifier TransactionID,
	state TransactionState,
) (Transaction, error) {
	if !validTransactionTargetState(state) {
		return Transaction{}, ErrInvalidState
	}

	err := lock.withFencedWrite(ctx, func(
		ctx context.Context,
		transaction *sql.Tx,
		lease writerLease,
	) error {
		result, execErr := transaction.ExecContext(
			ctx,
			"UPDATE journal_transactions SET state = ? "+
				"WHERE transaction_id = ? AND service_id = ? AND state IN ('active', 'degraded', ?) "+
				"AND NOT EXISTS (SELECT 1 FROM journal_actions "+
				"WHERE transaction_id = ? AND state != 'completed') "+
				"AND EXISTS (SELECT 1 FROM writer_leases "+
				"WHERE service_id = ? AND epoch = ? AND owner = ?)",
			state,
			identifier[:],
			lease.serviceID[:],
			state,
			identifier[:],
			lease.serviceID[:],
			lease.epoch,
			lease.owner[:],
		)
		if execErr != nil {
			return classifySQLiteProbe(ctx, execErr)
		}

		return requireJournalMutation(result)
	})
	if err != nil {
		return Transaction{}, err
	}

	return lock.store.Transaction(ctx, identifier)
}

func requireJournalMutation(result sql.Result) error {
	if result == nil {
		return ErrInvalidState
	}

	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrInvalidState
	}

	return nil
}

func validTransactionIntent(intent TransactionIntent) bool {
	return intent.Runtime.SupportsWorkloads()
}

func validTransactionState(state TransactionState) bool {
	return state == TransactionActive || state == TransactionDegraded ||
		state == TransactionFailed || state == TransactionSucceeded
}

func validTransactionTargetState(state TransactionState) bool {
	return state == TransactionDegraded || state == TransactionFailed || state == TransactionSucceeded
}
