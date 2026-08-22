//go:build linux || darwin

package store

import (
	"context"
	"crypto/rand"
	"database/sql"
	"io"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const maximumWorkloadIDBytes = 256

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
		if baselineErr := validateTransactionBaseline(ctx, transaction, lease.serviceID, intent); baselineErr != nil {
			return baselineErr
		}

		result, execErr := transaction.ExecContext(
			ctx,
			"INSERT INTO journal_transactions "+
				"(transaction_id, service_id, kind, state, runtime, source_digest, effective_digest, execution_digest, "+
				"base_transaction_id, predecessor_workload_id) "+
				"SELECT ?, ?, ?, 'active', ?, ?, ?, ?, ?, ? "+
				"WHERE EXISTS (SELECT 1 FROM writer_leases "+
				"WHERE service_id = ? AND epoch = ? AND owner = ?) "+
				"AND NOT EXISTS (SELECT 1 FROM journal_transactions "+
				"WHERE service_id = ? AND state IN ('active', 'degraded'))",
			record.ID[:],
			lease.serviceID[:],
			intent.Kind,
			intent.Runtime.String(),
			intent.SourceDigest[:],
			intent.EffectiveDigest[:],
			intent.ExecutionDigest[:],
			nullableTransactionID(intent.BaseTransactionID, intent.HasBaseTransaction),
			nullableWorkloadID(intent.PredecessorWorkloadID),
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

// SetTransactionState marks a transaction degraded or failed only after all
// external-effect outcomes have been resolved by typed probes. Successful
// transactions must atomically publish their applied workload baseline.
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
	if !validTransactionIdentity(intent) {
		return false
	}

	return validTransactionRelationship(intent)
}

func validTransactionIdentity(intent TransactionIntent) bool {
	return validTransactionKind(intent.Kind) && intent.Runtime.SupportsWorkloads() &&
		intent.HasBaseTransaction == (intent.BaseTransactionID != (TransactionID{})) &&
		validTransactionDigests(intent)
}

func validTransactionRelationship(intent TransactionIntent) bool {
	switch intent.Kind {
	case TransactionBootstrap:
		return !intent.HasBaseTransaction && intent.PredecessorWorkloadID == ""
	case TransactionAdopt:
		return !intent.HasBaseTransaction && validWorkloadID(intent.PredecessorWorkloadID)
	case TransactionUpgrade:
		return intent.HasBaseTransaction && validWorkloadID(intent.PredecessorWorkloadID)
	default:
		return false
	}
}

func validTransactionDigests(intent TransactionIntent) bool {
	empty := domain.Digest{}

	return intent.SourceDigest != empty && intent.EffectiveDigest != empty && intent.ExecutionDigest != empty
}

func validTransactionKind(kind TransactionKind) bool {
	return kind == TransactionBootstrap || kind == TransactionAdopt || kind == TransactionUpgrade
}

func validWorkloadID(identifier string) bool {
	return len(identifier) > 0 && len(identifier) <= maximumWorkloadIDBytes &&
		utf8.ValidString(identifier) && !strings.ContainsRune(identifier, 0)
}

func nullableTransactionID(identifier TransactionID, present bool) any {
	if !present {
		return nil
	}

	return identifier[:]
}

func nullableWorkloadID(identifier string) any {
	if identifier == "" {
		return nil
	}

	return identifier
}

func validTransactionState(state TransactionState) bool {
	return state == TransactionActive || state == TransactionDegraded ||
		state == TransactionFailed || state == TransactionSucceeded
}

func validTransactionTargetState(state TransactionState) bool {
	return state == TransactionDegraded || state == TransactionFailed
}

func validateTransactionBaseline(
	ctx context.Context,
	transaction *sql.Tx,
	serviceID [32]byte,
	intent TransactionIntent,
) error {
	applied, found, err := appliedService(ctx, transaction, serviceID)
	if err != nil {
		return err
	}

	switch intent.Kind {
	case TransactionBootstrap, TransactionAdopt:
		if found {
			return ErrInvalidState
		}
	case TransactionUpgrade:
		if !found || applied.TransactionID != intent.BaseTransactionID ||
			applied.WorkloadID != intent.PredecessorWorkloadID {
			return ErrInvalidState
		}
	default:
		return ErrInvalidState
	}

	return nil
}
