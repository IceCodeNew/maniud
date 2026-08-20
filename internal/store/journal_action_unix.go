//go:build linux || darwin

package store

import (
	"context"
	"database/sql"

	"github.com/IceCodeNew/maniud/internal/domain"
)

// RecordActionIntent persists or reuses one exact action plan. A transaction
// with any other unresolved action rejects the new mutation.
func (lock *ServiceLock) RecordActionIntent(
	ctx context.Context,
	identifier TransactionID,
	intent ActionIntent,
) (Action, error) {
	if !validActionIntent(intent) {
		return Action{}, ErrInvalidState
	}

	err := lock.withFencedWrite(ctx, func(
		ctx context.Context,
		transaction *sql.Tx,
		lease writerLease,
	) error {
		return recordActionIntent(ctx, transaction, lease, identifier, intent)
	})
	if err != nil {
		return Action{}, err
	}

	return lock.action(ctx, identifier, intent.Sequence)
}

// MarkActionEffectOutcomeUnknown durably closes the replay window before the
// caller performs the external effect. Recovery may only run its typed probe.
func (lock *ServiceLock) MarkActionEffectOutcomeUnknown(
	ctx context.Context,
	identifier TransactionID,
	sequence int64,
) (Action, error) {
	if sequence <= 0 {
		return Action{}, ErrInvalidState
	}

	err := lock.updateAction(
		ctx,
		identifier,
		sequence,
		"UPDATE journal_actions SET state = 'effect_outcome_unknown' "+
			"WHERE transaction_id = ? AND sequence = ? "+
			"AND state IN ('intent', 'effect_outcome_unknown') AND postcondition_digest IS NULL ",
		nil,
	)
	if err != nil {
		return Action{}, err
	}

	return lock.action(ctx, identifier, sequence)
}

// CompleteAction records the digest of a typed postcondition. It never treats
// an external API response or a completed row alone as effect proof.
func (lock *ServiceLock) CompleteAction(
	ctx context.Context,
	identifier TransactionID,
	sequence int64,
	postcondition domain.Digest,
) (Action, error) {
	if sequence <= 0 {
		return Action{}, ErrInvalidState
	}

	err := lock.updateAction(
		ctx,
		identifier,
		sequence,
		"UPDATE journal_actions SET state = 'completed', postcondition_digest = ? "+
			"WHERE transaction_id = ? AND sequence = ? AND "+
			"(state = 'effect_outcome_unknown' OR "+
			"(state = 'completed' AND postcondition_digest = ?)) ",
		postcondition[:],
	)
	if err != nil {
		return Action{}, err
	}

	return lock.action(ctx, identifier, sequence)
}

func recordActionIntent(
	ctx context.Context,
	transaction *sql.Tx,
	lease writerLease,
	identifier TransactionID,
	intent ActionIntent,
) error {
	current, found, err := actionInTransaction(ctx, transaction, identifier, intent.Sequence)
	if err != nil {
		return err
	}

	if found {
		if current.Kind != intent.Kind || current.IntentDigest != intent.IntentDigest {
			return ErrInvalidState
		}

		return reuseActionIntent(ctx, transaction, lease, identifier, intent.Sequence)
	}

	return insertActionIntent(ctx, transaction, lease, identifier, intent)
}

func reuseActionIntent(
	ctx context.Context,
	transaction *sql.Tx,
	lease writerLease,
	identifier TransactionID,
	sequence int64,
) error {
	result, err := transaction.ExecContext(
		ctx,
		"UPDATE journal_actions SET kind = kind "+
			"WHERE transaction_id = ? AND sequence = ? "+
			"AND EXISTS (SELECT 1 FROM journal_transactions "+
			"WHERE transaction_id = ? AND service_id = ? AND state IN ('active', 'degraded')) "+
			"AND EXISTS (SELECT 1 FROM writer_leases "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?)",
		identifier[:],
		sequence,
		identifier[:],
		lease.serviceID[:],
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	return requireJournalMutation(result)
}

func insertActionIntent(
	ctx context.Context,
	transaction *sql.Tx,
	lease writerLease,
	identifier TransactionID,
	intent ActionIntent,
) error {
	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO journal_actions "+
			"(transaction_id, sequence, kind, state, intent_digest, postcondition_digest) "+
			"SELECT ?, ?, ?, 'intent', ?, NULL "+
			"WHERE EXISTS (SELECT 1 FROM journal_transactions "+
			"WHERE transaction_id = ? AND service_id = ? AND state IN ('active', 'degraded')) "+
			"AND EXISTS (SELECT 1 FROM writer_leases "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?) "+
			"AND NOT EXISTS (SELECT 1 FROM journal_actions "+
			"WHERE transaction_id = ? AND state != 'completed')",
		identifier[:],
		intent.Sequence,
		intent.Kind,
		intent.IntentDigest[:],
		identifier[:],
		lease.serviceID[:],
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
		identifier[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	return requireJournalMutation(result)
}

func (lock *ServiceLock) updateAction(
	ctx context.Context,
	identifier TransactionID,
	sequence int64,
	statement string,
	postcondition []byte,
) error {
	return lock.withFencedWrite(ctx, func(
		ctx context.Context,
		transaction *sql.Tx,
		lease writerLease,
	) error {
		var arguments []any
		if postcondition != nil {
			arguments = append(arguments, postcondition)
		}

		statement += "AND EXISTS (SELECT 1 FROM journal_transactions " +
			"WHERE transaction_id = ? AND service_id = ? AND state IN ('active', 'degraded')) " +
			"AND EXISTS (SELECT 1 FROM writer_leases " +
			"WHERE service_id = ? AND epoch = ? AND owner = ?)"

		arguments = append(
			arguments,
			identifier[:],
			sequence,
		)
		if postcondition != nil {
			arguments = append(arguments, postcondition)
		}

		arguments = append(
			arguments,
			identifier[:],
			lease.serviceID[:],
			lease.serviceID[:],
			lease.epoch,
			lease.owner[:],
		)

		result, err := transaction.ExecContext(ctx, statement, arguments...)
		if err != nil {
			return classifySQLiteProbe(ctx, err)
		}

		return requireJournalMutation(result)
	})
}

func validActionIntent(intent ActionIntent) bool {
	return intent.Sequence > 0 && validActionKind(intent.Kind)
}

func validAction(action Action) bool {
	return action.Sequence > 0 && validActionKind(action.Kind) &&
		(action.State == ActionStateIntent || action.State == ActionStateEffectOutcomeUnknown ||
			action.State == ActionStateCompleted)
}

func validActionKind(kind string) bool {
	if len(kind) == 0 || len(kind) > 64 || !lowercaseAlphaNumeric(kind[0]) {
		return false
	}

	for index := 1; index < len(kind); index++ {
		if !lowercaseAlphaNumeric(kind[index]) && kind[index] != '.' && kind[index] != '_' && kind[index] != '-' {
			return false
		}
	}

	return true
}

func lowercaseAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}
