//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"database/sql"
	"errors"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const testJournalActionKind = "workload.create"

var errJournalTest = errors.New("journal test failure")

func TestJournalContainsStorageAndOwnershipFailures(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	intent := testTransactionIntent(domain.RuntimeDocker)

	_, err := lock.beginTransaction(context.Background(), intent, failingReader{})
	assertErrorIs(t, err, ErrUnavailable)

	identifierBytes := bytes.Repeat([]byte{1}, transactionIDBytes)
	first, err := lock.beginTransaction(context.Background(), intent, bytes.NewReader(identifierBytes))
	requireNoError(t, err)
	_, err = lock.CommitAppliedService(context.Background(), first.ID, testAppliedServiceIntent())
	requireNoError(t, err)

	upgrade := intent
	upgrade.Kind = TransactionUpgrade
	upgrade.BaseTransactionID = first.ID
	upgrade.HasBaseTransaction = true
	upgrade.PredecessorWorkloadID = testAppliedServiceIntent().WorkloadID

	_, err = lock.beginTransaction(context.Background(), upgrade, bytes.NewReader(identifierBytes))
	assertErrorIs(t, err, ErrInvalidState)

	active, err := lock.BeginTransaction(context.Background(), upgrade)
	requireNoError(t, err)

	_, err = lock.MarkActionEffectOutcomeUnknown(context.Background(), active.ID, 1)
	assertErrorIs(t, err, ErrInvalidState)
	_, err = lock.action(context.Background(), TransactionID{2}, 1)
	assertErrorIs(t, err, ErrInvalidState)
	_, err = (*ServiceLock)(nil).action(context.Background(), active.ID, 1)
	assertErrorIs(t, err, ErrInvalidState)

	err = lock.updateAction(context.Background(), active.ID, 1, "invalid SQL ", nil)
	assertErrorIs(t, err, ErrInvalidState)

	_, err = state.database.ExecContext(
		context.Background(),
		"CREATE TRIGGER reject_journal_state BEFORE UPDATE ON journal_transactions "+
			"BEGIN SELECT RAISE(ABORT, 'rejected'); END",
	)
	requireNoError(t, err)

	_, err = lock.SetTransactionState(context.Background(), active.ID, TransactionFailed)
	assertErrorIs(t, err, ErrInvalidState)

	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())

	_, err = state.Actions(context.Background(), active.ID)
	assertErrorIs(t, err, ErrInvalidState)
}

func TestJournalReadersRejectUnavailableAndInvalidRequests(t *testing.T) {
	t.Parallel()

	var nilStore *Store

	_, err := nilStore.Transaction(context.Background(), TransactionID{})
	assertErrorIs(t, err, ErrInvalidState)
	_, _, err = nilStore.UnresolvedTransaction(context.Background(), "project", "api")
	assertErrorIs(t, err, ErrInvalidState)
	_, err = nilStore.Actions(context.Background(), TransactionID{})
	assertErrorIs(t, err, ErrInvalidState)

	state := openJournalStore(t, filepath.Join(privateTempDir(t), "state.db"))
	_, _, err = state.UnresolvedTransaction(context.Background(), "", "api")
	assertErrorIs(t, err, ErrInvalidState)

	requireNoError(t, state.Close())
	_, err = state.Actions(context.Background(), TransactionID{})
	assertErrorIs(t, err, ErrInvalidState)
}

func TestJournalDMLRejectsStaleWriterOwner(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)

	_, err = state.database.ExecContext(
		context.Background(),
		"UPDATE writer_leases SET owner = ? WHERE service_id = ?",
		[]byte("different-owner!"),
		lock.lease.serviceID[:],
	)
	requireNoError(t, err)

	_, err = lock.RecordActionIntent(
		context.Background(),
		transaction.ID,
		testActionIntent(1, testJournalActionKind),
	)
	assertErrorIs(t, err, ErrOwnershipLost)

	actions, err := state.Actions(context.Background(), transaction.ID)
	requireNoError(t, err)

	if len(actions) != 0 {
		t.Fatalf("stale writer actions = %#v", actions)
	}

	assertErrorIs(t, lock.Close(), ErrOwnershipLost)
	requireNoError(t, state.Close())
}

func TestJournalSQLHelpersContainSchemaFailures(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	transaction, err := state.database.BeginTx(context.Background(), nil)
	requireNoError(t, err)
	_, err = transaction.ExecContext(context.Background(), "DROP TABLE journal_actions")
	requireNoError(t, err)

	identifier := TransactionID{1}
	intent := testActionIntent(1, testJournalActionKind)

	err = recordActionIntent(context.Background(), transaction, lock.lease, identifier, intent)
	assertErrorIs(t, err, ErrInvalidState)
	err = reuseActionIntent(context.Background(), transaction, lock.lease, identifier, intent.Sequence)
	assertErrorIs(t, err, ErrInvalidState)
	err = insertActionIntent(context.Background(), transaction, lock.lease, identifier, intent)
	assertErrorIs(t, err, ErrInvalidState)

	requireNoError(t, transaction.Rollback())
}

func TestJournalScannersRejectMalformedRows(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})

	identifier := make([]byte, transactionIDBytes)
	digest := make([]byte, len(domain.Digest{}))

	transactionRows := [][]any{
		{
			identifier, string(TransactionBootstrap), "active", domain.RuntimeDocker.String(),
			digest, digest, []byte("short"), nil, nil,
		},
		{
			identifier, string(TransactionBootstrap), testUnknownValue, domain.RuntimeDocker.String(),
			digest, digest, digest, nil, nil,
		},
		{identifier, string(TransactionUpgrade), "active", domain.RuntimeDocker.String(), digest, digest, digest, nil, nil},
	}
	for _, values := range transactionRows {
		_, err := scanTransaction(context.Background(), queryValues(t, database, values...))
		assertErrorIs(t, err, ErrInvalidState)
	}

	_, err := scanTransaction(context.Background(), failingScanner{err: errJournalTest})
	assertErrorIs(t, err, ErrInvalidState)

	actionRows := [][]any{
		{[]byte("short"), int64(1), testJournalActionKind, "intent", digest, nil},
		{identifier, int64(1), testJournalActionKind, "completed", digest, []byte("short")},
		{identifier, int64(1), testJournalActionKind, "intent", digest, digest},
	}
	for _, values := range actionRows {
		_, err = scanAction(context.Background(), queryValues(t, database, values...))
		assertErrorIs(t, err, ErrInvalidState)
	}

	_, err = scanAction(context.Background(), failingScanner{err: errJournalTest})
	assertErrorIs(t, err, ErrInvalidState)
	assertErrorIs(t, requireJournalMutation(nil), ErrInvalidState)
}

func TestJournalTransactionIdentityHelpersRejectInvalidState(t *testing.T) {
	t.Parallel()

	digest := make([]byte, len(domain.Digest{}))
	identifier := make([]byte, transactionIDBytes)
	if populateTransactionIdentity(
		&Transaction{},
		testUnknownValue,
		identifier,
		digest,
		digest,
		digest,
	) {
		t.Fatal("populateTransactionIdentity() accepted an unknown runtime")
	}

	if populateTransactionRelationship(&Transaction{}, []byte("short"), sql.NullString{}) {
		t.Fatal("populateTransactionRelationship() accepted a short base transaction ID")
	}

	intent := testTransactionIntent(domain.RuntimeDocker)
	intent.Kind = TransactionKind(testUnknownValue)
	if validTransactionRelationship(intent) {
		t.Fatal("validTransactionRelationship() accepted an unknown kind")
	}
}

func TestValidateTransactionBaselineContainsInvalidSchema(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	transaction, err := state.database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	_, err = transaction.ExecContext(context.Background(), "DROP TABLE applied_services")
	requireNoError(t, err)

	err = validateTransactionBaseline(
		context.Background(),
		transaction,
		lock.lease.serviceID,
		testTransactionIntent(domain.RuntimeDocker),
	)
	assertErrorIs(t, err, ErrInvalidState)
	requireNoError(t, transaction.Rollback())

	transaction, err = state.database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	intent := testTransactionIntent(domain.RuntimeDocker)
	intent.Kind = TransactionKind(testUnknownValue)
	err = validateTransactionBaseline(context.Background(), transaction, lock.lease.serviceID, intent)
	assertErrorIs(t, err, ErrInvalidState)
	requireNoError(t, transaction.Rollback())
	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestReadActionsContainsIteratorFailures(t *testing.T) {
	t.Parallel()

	_, err := readActions(
		context.Background(),
		&failingActionRows{next: true, scanErr: errJournalTest, rowsErr: nil},
	)
	assertErrorIs(t, err, ErrInvalidState)

	_, err = readActions(
		context.Background(),
		&failingActionRows{next: false, scanErr: nil, rowsErr: errJournalTest},
	)
	assertErrorIs(t, err, ErrInvalidState)
}

func TestRepositoryInventoryReaderContainsInvalidState(t *testing.T) {
	t.Parallel()

	scope := domain.Hash([]byte("repository scope"))
	var nilStore *Store
	_, err := nilStore.UnresolvedRepositoryTransactions(t.Context(), scope)
	assertErrorIs(t, err, ErrInvalidState)
	_, err = (&Store{}).UnresolvedRepositoryTransactions(t.Context(), scope)
	assertErrorIs(t, err, ErrInvalidState)

	database := testDatabase(t, "file::memory:")
	store := &Store{database: database}
	_, err = store.UnresolvedRepositoryTransactions(t.Context(), domain.Digest{})
	assertErrorIs(t, err, ErrInvalidState)
	_, err = unresolvedRepositoryTransactions(t.Context(), database, domain.Digest{})
	assertErrorIs(t, err, ErrInvalidState)
	requireNoError(t, database.Close())
	_, err = unresolvedRepositoryTransactions(t.Context(), database, scope)
	assertErrorIs(t, err, ErrInvalidState)

	_, err = readTransactions(t.Context(), &failingActionRows{next: true, scanErr: errJournalTest})
	assertErrorIs(t, err, ErrInvalidState)
	_, err = readTransactions(t.Context(), &failingActionRows{rowsErr: errJournalTest})
	assertErrorIs(t, err, ErrInvalidState)
}

func TestTransactionScannerContainsRepositoryAssociationFailures(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() { requireNoError(t, database.Close()) })
	identifier := make([]byte, transactionIDBytes)
	digest := make([]byte, len(domain.Digest{}))
	values := []any{
		identifier, string(TransactionBootstrap), string(TransactionActive), domain.RuntimeDocker.String(),
		digest, digest, []byte("short"), nil, nil, nil, nil, nil,
	}
	_, err := scanTransaction(t.Context(), queryValues(t, database, values...))
	assertErrorIs(t, err, ErrInvalidState)

	values[6] = digest
	values[7] = int64(2)
	values[8] = digest
	values[9] = digest
	_, err = scanTransaction(t.Context(), queryValues(t, database, values...))
	assertErrorIs(t, err, ErrInvalidState)

	if populateTransactionRepository(&Transaction{}, sql.NullInt64{Int64: 2, Valid: true}, digest, digest) {
		t.Fatal("populateTransactionRepository() accepted an unsupported version")
	}
}

type failingReader struct{}

func (failingReader) Read(_ []byte) (int, error) {
	return 0, io.ErrUnexpectedEOF
}

type failingScanner struct {
	err error
}

func (scanner failingScanner) Scan(_ ...any) error {
	return scanner.err
}

type failingActionRows struct {
	next    bool
	scanErr error
	rowsErr error
}

func (rows *failingActionRows) Next() bool {
	next := rows.next
	rows.next = false

	return next
}

func (rows *failingActionRows) Scan(_ ...any) error {
	return rows.scanErr
}

func (rows *failingActionRows) Err() error {
	return rows.rowsErr
}

func queryValues(t *testing.T, database *sql.DB, values ...any) *sql.Row {
	t.Helper()

	placeholders := "?" + strings.Repeat(", ?", len(values)-1)

	return database.QueryRowContext(context.Background(), "SELECT "+placeholders, values...)
}
