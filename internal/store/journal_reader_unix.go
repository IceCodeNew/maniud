//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/IceCodeNew/maniud/internal/domain"
)

// Transaction loads one durable transaction by its runtime ownership ID.
func (store *Store) Transaction(ctx context.Context, identifier TransactionID) (Transaction, error) {
	if store == nil || store.database == nil {
		return Transaction{}, ErrInvalidState
	}

	row := store.database.QueryRowContext(
		ctx,
		"SELECT transaction_id, state, runtime, source_digest, effective_digest, execution_digest "+
			"FROM journal_transactions WHERE transaction_id = ?",
		identifier[:],
	)

	record, err := scanTransaction(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, ErrInvalidState
	}

	return record, err
}

// UnresolvedTransaction returns the sole active or degraded transaction for a
// project and service without acquiring a writer lease or changing state.
func (store *Store) UnresolvedTransaction(
	ctx context.Context,
	projectName string,
	serviceName string,
) (Transaction, bool, error) {
	if store == nil || store.database == nil {
		return Transaction{}, false, ErrInvalidState
	}

	serviceID, valid := serviceIdentity(projectName, serviceName)
	if !valid {
		return Transaction{}, false, ErrInvalidState
	}

	row := store.database.QueryRowContext(
		ctx,
		"SELECT transaction_id, state, runtime, source_digest, effective_digest, execution_digest "+
			"FROM journal_transactions WHERE service_id = ? AND state IN ('active', 'degraded')",
		serviceID[:],
	)

	record, err := scanTransaction(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		var empty Transaction

		return empty, false, nil
	}

	return record, err == nil, err
}

// Actions returns every action for a transaction in sequence order.
func (store *Store) Actions(ctx context.Context, identifier TransactionID) ([]Action, error) {
	if store == nil || store.database == nil {
		return nil, ErrInvalidState
	}

	//nolint:rowserrcheck // readActions checks the iterator before this function closes it.
	rows, err := store.database.QueryContext(
		ctx,
		"SELECT transaction_id, sequence, kind, state, intent_digest, postcondition_digest "+
			"FROM journal_actions WHERE transaction_id = ? ORDER BY sequence",
		identifier[:],
	)
	if err != nil {
		return nil, classifySQLiteProbe(ctx, err)
	}
	defer func() {
		_ = rows.Close()
	}()

	actions, err := readActions(ctx, rows)
	if err != nil || len(actions) != 0 {
		return actions, err
	}

	_, err = store.Transaction(ctx, identifier)
	if err != nil {
		return nil, err
	}

	return actions, nil
}

type actionRowIterator interface {
	rowScanner
	Next() bool
	Err() error
}

func readActions(ctx context.Context, rows actionRowIterator) ([]Action, error) {
	actions := make([]Action, 0)

	for rows.Next() {
		action, scanErr := scanAction(ctx, rows)
		if scanErr != nil {
			return nil, scanErr
		}

		actions = append(actions, action)
	}

	if rows.Err() != nil {
		return nil, classifySQLiteProbe(ctx, rows.Err())
	}

	return actions, nil
}

func (lock *ServiceLock) action(
	ctx context.Context,
	identifier TransactionID,
	sequence int64,
) (Action, error) {
	if lock == nil || lock.store == nil {
		return Action{}, ErrInvalidState
	}

	row := lock.store.database.QueryRowContext(
		ctx,
		"SELECT transaction_id, sequence, kind, state, intent_digest, postcondition_digest "+
			"FROM journal_actions WHERE transaction_id = ? AND sequence = ?",
		identifier[:],
		sequence,
	)

	action, err := scanAction(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		return Action{}, ErrInvalidState
	}

	return action, err
}

func actionInTransaction(
	ctx context.Context,
	transaction *sql.Tx,
	identifier TransactionID,
	sequence int64,
) (Action, bool, error) {
	row := transaction.QueryRowContext(
		ctx,
		"SELECT transaction_id, sequence, kind, state, intent_digest, postcondition_digest "+
			"FROM journal_actions WHERE transaction_id = ? AND sequence = ?",
		identifier[:],
		sequence,
	)

	action, err := scanAction(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		var empty Action

		return empty, false, nil
	}

	return action, err == nil, err
}

type rowScanner interface {
	Scan(destination ...any) error
}

func scanTransaction(ctx context.Context, row rowScanner) (Transaction, error) {
	var (
		record     Transaction
		identifier []byte
		state      string
		runtime    string
		source     []byte
		effective  []byte
		execution  []byte
	)

	err := row.Scan(&identifier, &state, &runtime, &source, &effective, &execution)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Transaction{}, sql.ErrNoRows
		}

		return Transaction{}, classifySQLiteProbe(ctx, err)
	}

	parsedRuntime, valid := domain.ParseRuntimeKind(runtime)
	if !valid || !parsedRuntime.SupportsWorkloads() ||
		!copyExact(record.ID[:], identifier) ||
		!copyExact(record.SourceDigest[:], source) ||
		!copyExact(record.EffectiveDigest[:], effective) ||
		!copyExact(record.ExecutionDigest[:], execution) {
		return Transaction{}, ErrInvalidState
	}

	record.State = TransactionState(state)
	record.Runtime = parsedRuntime

	if !validTransactionState(record.State) {
		return Transaction{}, ErrInvalidState
	}

	return record, nil
}

func scanAction(ctx context.Context, row rowScanner) (Action, error) {
	var (
		action        Action
		identifier    []byte
		state         string
		intent        []byte
		postcondition []byte
	)

	err := row.Scan(
		&identifier,
		&action.Sequence,
		&action.Kind,
		&state,
		&intent,
		&postcondition,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Action{}, sql.ErrNoRows
		}

		return Action{}, classifySQLiteProbe(ctx, err)
	}

	action.State = ActionState(state)
	if !copyExact(action.TransactionID[:], identifier) ||
		!copyExact(action.IntentDigest[:], intent) ||
		!validAction(action) {
		return Action{}, ErrInvalidState
	}

	if action.State == ActionStateCompleted {
		var digest domain.Digest
		if !copyExact(digest[:], postcondition) {
			return Action{}, ErrInvalidState
		}

		action.PostconditionDigest = &digest
	} else if postcondition != nil {
		return Action{}, ErrInvalidState
	}

	return action, nil
}

func copyExact(destination, source []byte) bool {
	return len(destination) == len(source) && copy(destination, source) == len(destination)
}
