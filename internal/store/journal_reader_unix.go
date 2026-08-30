//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	unresolvedRepositoryTransactionQueryLimit = 257
	transactionSelectColumns                  = "transaction_id, kind, state, runtime, source_digest, " +
		"effective_digest, execution_digest, repository_version, repository_scope_digest, " +
		"repository_location_digest, base_transaction_id, predecessor_workload_id"
	unresolvedRepositoryTransactionsSQL = "SELECT " + transactionSelectColumns + " FROM journal_transactions " +
		"WHERE state IN ('active', 'degraded') AND repository_scope_digest = ? " +
		"ORDER BY repository_location_digest, transaction_id LIMIT ?"
)

// Transaction loads one durable transaction by its runtime ownership ID.
func (store *Store) Transaction(ctx context.Context, identifier TransactionID) (Transaction, error) {
	if store == nil || store.database == nil {
		return Transaction{}, ErrInvalidState
	}

	return transaction(ctx, store.database, identifier)
}

func transaction(ctx context.Context, database journalQueryer, identifier TransactionID) (Transaction, error) {
	row := database.QueryRowContext(
		ctx,
		"SELECT "+transactionSelectColumns+" FROM journal_transactions WHERE transaction_id = ?",
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

	return unresolvedTransaction(ctx, store.database, projectName, serviceName)
}

func unresolvedTransaction(
	ctx context.Context,
	database journalQueryer,
	projectName string,
	serviceName string,
) (Transaction, bool, error) {
	serviceID, valid := serviceIdentity(projectName, serviceName)
	if !valid {
		return Transaction{}, false, ErrInvalidState
	}

	row := database.QueryRowContext(
		ctx,
		"SELECT "+transactionSelectColumns+" FROM journal_transactions "+
			"WHERE service_id = ? AND state IN ('active', 'degraded')",
		serviceID[:],
	)

	record, err := scanTransaction(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		var empty Transaction

		return empty, false, nil
	}

	return record, err == nil, err
}

// UnresolvedRepositoryTransactions returns a bounded inventory for one opaque
// repository scope. Callers detect overflow when all 257 records are present.
func (store *Store) UnresolvedRepositoryTransactions(
	ctx context.Context,
	scope domain.Digest,
) ([]Transaction, error) {
	if store == nil || store.database == nil || scope == (domain.Digest{}) {
		return nil, ErrInvalidState
	}

	return unresolvedRepositoryTransactions(ctx, store.database, scope)
}

func unresolvedRepositoryTransactions(
	ctx context.Context,
	database journalQueryer,
	scope domain.Digest,
) ([]Transaction, error) {
	if scope == (domain.Digest{}) {
		return nil, ErrInvalidState
	}

	//nolint:rowserrcheck // readTransactions checks the iterator before this function closes it.
	rows, err := database.QueryContext(
		ctx,
		unresolvedRepositoryTransactionsSQL,
		scope[:],
		unresolvedRepositoryTransactionQueryLimit,
	)
	if err != nil {
		return nil, classifySQLiteProbe(ctx, err)
	}
	defer func() {
		_ = rows.Close()
	}()

	return readTransactions(ctx, rows)
}

// Actions returns every action for a transaction in sequence order.
func (store *Store) Actions(ctx context.Context, identifier TransactionID) ([]Action, error) {
	if store == nil || store.database == nil {
		return nil, ErrInvalidState
	}

	return actions(ctx, store.database, identifier)
}

type journalQueryer interface {
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
	QueryContext(ctx context.Context, query string, arguments ...any) (*sql.Rows, error)
}

func actions(ctx context.Context, database journalQueryer, identifier TransactionID) ([]Action, error) {
	//nolint:rowserrcheck // readActions checks the iterator before this function closes it.
	rows, err := database.QueryContext(
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

	_, err = transaction(ctx, database, identifier)
	if err != nil {
		return nil, err
	}

	return actions, nil
}

type journalRowIterator interface {
	rowScanner
	Next() bool
	Err() error
}

func readActions(ctx context.Context, rows journalRowIterator) ([]Action, error) {
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

func readTransactions(ctx context.Context, rows journalRowIterator) ([]Transaction, error) {
	records := make([]Transaction, 0)

	for rows.Next() {
		record, err := scanTransaction(ctx, rows)
		if err != nil {
			return nil, err
		}

		records = append(records, record)
	}

	if rows.Err() != nil {
		return nil, classifySQLiteProbe(ctx, rows.Err())
	}

	return records, nil
}

func scanTransaction(ctx context.Context, row rowScanner) (Transaction, error) {
	var (
		record             Transaction
		identifier         []byte
		kind               string
		state              string
		runtime            string
		source             []byte
		effective          []byte
		execution          []byte
		repositoryVersion  sql.NullInt64
		repositoryScope    []byte
		repositoryLocation []byte
		base               []byte
		predecessor        sql.NullString
	)

	err := row.Scan(
		&identifier,
		&kind,
		&state,
		&runtime,
		&source,
		&effective,
		&execution,
		&repositoryVersion,
		&repositoryScope,
		&repositoryLocation,
		&base,
		&predecessor,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return Transaction{}, sql.ErrNoRows
		}

		return Transaction{}, classifySQLiteProbe(ctx, err)
	}

	if !populateTransactionIdentity(&record, runtime, identifier, source, effective, execution) {
		return Transaction{}, ErrInvalidState
	}

	record.Kind = TransactionKind(kind)
	record.State = TransactionState(state)
	if !populateTransactionRepository(&record, repositoryVersion, repositoryScope, repositoryLocation) ||
		!populateTransactionRelationship(&record, base, predecessor) ||
		!validTransactionState(record.State) || !validTransactionRecord(record) {
		return Transaction{}, ErrInvalidState
	}

	return record, nil
}

func populateTransactionRepository(
	record *Transaction,
	version sql.NullInt64,
	scope []byte,
	location []byte,
) bool {
	if !version.Valid {
		return scope == nil && location == nil
	}
	if version.Int64 != 1 || !copyExact(record.RepositoryScopeDigest[:], scope) ||
		!copyExact(record.RepositoryLocationDigest[:], location) {
		return false
	}

	record.RepositoryVersion = int(version.Int64)
	record.HasRepository = true

	return true
}

func populateTransactionIdentity(
	record *Transaction,
	runtime string,
	identifier []byte,
	source []byte,
	effective []byte,
	execution []byte,
) bool {
	parsedRuntime, valid := domain.ParseRuntimeKind(runtime)
	if !valid || !parsedRuntime.SupportsWorkloads() {
		return false
	}

	record.Runtime = parsedRuntime

	return copyExact(record.ID[:], identifier) &&
		copyExact(record.SourceDigest[:], source) &&
		copyExact(record.EffectiveDigest[:], effective) &&
		copyExact(record.ExecutionDigest[:], execution)
}

func populateTransactionRelationship(
	record *Transaction,
	base []byte,
	predecessor sql.NullString,
) bool {
	if base != nil {
		record.HasBaseTransaction = copyExact(record.BaseTransactionID[:], base)
		if !record.HasBaseTransaction {
			return false
		}
	}

	if predecessor.Valid {
		record.PredecessorWorkloadID = predecessor.String
	}

	return true
}

func validTransactionRecord(record Transaction) bool {
	return validTransactionIntent(TransactionIntent{
		Kind:                     record.Kind,
		Runtime:                  record.Runtime,
		SourceDigest:             record.SourceDigest,
		EffectiveDigest:          record.EffectiveDigest,
		ExecutionDigest:          record.ExecutionDigest,
		RepositoryVersion:        record.RepositoryVersion,
		RepositoryScopeDigest:    record.RepositoryScopeDigest,
		RepositoryLocationDigest: record.RepositoryLocationDigest,
		HasRepository:            record.HasRepository,
		BaseTransactionID:        record.BaseTransactionID,
		HasBaseTransaction:       record.HasBaseTransaction,
		PredecessorWorkloadID:    record.PredecessorWorkloadID,
	})
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
