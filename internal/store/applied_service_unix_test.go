//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestAppliedServiceCommitsBootstrapGeneration(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	_, found, err := state.AppliedService(context.Background(), "project", "api")
	requireNoError(t, err)
	if found {
		t.Fatal("empty service has an applied baseline")
	}

	bootstrapIntent := testTransactionIntent(domain.RuntimeDocker)
	bootstrap, err := lock.BeginTransaction(context.Background(), bootstrapIntent)
	requireNoError(t, err)

	_, err = lock.SetTransactionState(context.Background(), bootstrap.ID, TransactionSucceeded)
	assertErrorIs(t, err, ErrInvalidState)

	firstIntent := testAppliedServiceIntent()
	first, err := lock.CommitAppliedService(context.Background(), bootstrap.ID, firstIntent)
	requireNoError(t, err)
	assertAppliedService(t, first, bootstrap, bootstrapIntent, firstIntent)

	read, found, err := state.AppliedService(context.Background(), "project", "api")
	requireNoError(t, err)
	if !found || read != first {
		t.Fatalf("AppliedService() = %#v, %t", read, found)
	}

	conflicting := firstIntent
	conflicting.ConfigurationDigest = domain.Hash([]byte("conflicting configuration"))
	_, err = lock.CommitAppliedService(context.Background(), bootstrap.ID, conflicting)
	assertErrorIs(t, err, ErrInvalidState)

	_, found, err = state.UnresolvedTransaction(context.Background(), "project", "api")
	requireNoError(t, err)
	if found {
		t.Fatal("committed bootstrap remained unresolved")
	}
}

func TestAppliedServiceCommitsUpgradeGeneration(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	bootstrapIntent := testTransactionIntent(domain.RuntimeDocker)
	bootstrap, err := lock.BeginTransaction(context.Background(), bootstrapIntent)
	requireNoError(t, err)
	first, err := lock.CommitAppliedService(context.Background(), bootstrap.ID, testAppliedServiceIntent())
	requireNoError(t, err)

	upgradeIntent := testTransactionIntent(domain.RuntimeDocker)
	upgradeIntent.Kind = TransactionUpgrade
	upgradeIntent.BaseTransactionID = bootstrap.ID
	upgradeIntent.HasBaseTransaction = true
	upgradeIntent.PredecessorWorkloadID = first.WorkloadID
	upgradeIntent.EffectiveDigest = domain.Hash([]byte("upgraded normalized Compose"))
	upgradeIntent.ExecutionDigest = domain.Hash([]byte("upgraded runtime execution binding"))

	upgrade, err := lock.BeginTransaction(context.Background(), upgradeIntent)
	requireNoError(t, err)
	assertTransaction(t, upgrade, TransactionActive, upgradeIntent)

	secondIntent := testAppliedServiceIntent()
	secondIntent.WorkloadID = "maniud-workload-upgraded"
	secondIntent.ConfigurationDigest = domain.Hash([]byte("upgraded runtime configuration"))

	second, err := lock.CommitAppliedService(context.Background(), upgrade.ID, secondIntent)
	requireNoError(t, err)
	assertAppliedService(t, second, upgrade, upgradeIntent, secondIntent)

	read, found, err := state.AppliedService(context.Background(), "project", "api")
	requireNoError(t, err)
	if !found || read != second || read == first {
		t.Fatalf("upgraded AppliedService() = %#v, %t", read, found)
	}

	transaction, err := state.Transaction(context.Background(), bootstrap.ID)
	requireNoError(t, err)
	if transaction.State != TransactionSucceeded {
		t.Fatalf("bootstrap transaction = %#v", transaction)
	}

	_, found, err = state.UnresolvedTransaction(context.Background(), "project", "api")
	requireNoError(t, err)
	if found {
		t.Fatal("committed upgrade remained unresolved")
	}
}

func TestAppliedServiceCommitsExactAdoptedWorkload(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	intent := testTransactionIntent(domain.RuntimePodman)
	intent.Kind = TransactionAdopt
	intent.PredecessorWorkloadID = "unmanaged-workload"

	transaction, err := lock.BeginTransaction(context.Background(), intent)
	requireNoError(t, err)

	appliedIntent := testAppliedServiceIntent()
	appliedIntent.WorkloadID = "different-workload"

	_, err = lock.CommitAppliedService(context.Background(), transaction.ID, appliedIntent)
	assertErrorIs(t, err, ErrInvalidState)

	_, found, err := state.AppliedService(context.Background(), "project", "api")
	requireNoError(t, err)
	if found {
		t.Fatal("failed adoption published an applied baseline")
	}

	appliedIntent.WorkloadID = intent.PredecessorWorkloadID
	applied, err := lock.CommitAppliedService(context.Background(), transaction.ID, appliedIntent)
	requireNoError(t, err)
	assertAppliedService(t, applied, transaction, intent, appliedIntent)
}

func TestAppliedServiceRejectsBaselineDriftAndPendingActions(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)

	actionIntent := testActionIntent(1, "workload.create")
	_, err = lock.RecordActionIntent(context.Background(), transaction.ID, actionIntent)
	requireNoError(t, err)

	_, err = lock.CommitAppliedService(context.Background(), transaction.ID, testAppliedServiceIntent())
	assertErrorIs(t, err, ErrInvalidState)

	_, found, err := state.AppliedService(context.Background(), "project", "api")
	requireNoError(t, err)
	if found {
		t.Fatal("pending action published an applied baseline")
	}

	_, err = lock.MarkActionEffectOutcomeUnknown(context.Background(), transaction.ID, actionIntent.Sequence)
	requireNoError(t, err)
	_, err = lock.CompleteAction(
		context.Background(),
		transaction.ID,
		actionIntent.Sequence,
		domain.Hash([]byte("workload exists")),
	)
	requireNoError(t, err)

	applied, err := lock.CommitAppliedService(context.Background(), transaction.ID, testAppliedServiceIntent())
	requireNoError(t, err)

	assertBaselineDriftRejected(t, lock, applied)
}

func assertBaselineDriftRejected(t *testing.T, lock *ServiceLock, applied AppliedService) {
	t.Helper()

	for _, intent := range []TransactionIntent{
		testTransactionIntent(domain.RuntimeDocker),
		{
			Kind:                  TransactionAdopt,
			Runtime:               domain.RuntimeDocker,
			SourceDigest:          domain.Hash([]byte("source")),
			EffectiveDigest:       domain.Hash([]byte("effective")),
			ExecutionDigest:       domain.Hash([]byte("execution")),
			PredecessorWorkloadID: applied.WorkloadID,
		},
		{
			Kind:                  TransactionUpgrade,
			Runtime:               domain.RuntimeDocker,
			SourceDigest:          domain.Hash([]byte("source")),
			EffectiveDigest:       domain.Hash([]byte("effective")),
			ExecutionDigest:       domain.Hash([]byte("execution")),
			BaseTransactionID:     TransactionID{1},
			HasBaseTransaction:    true,
			PredecessorWorkloadID: applied.WorkloadID,
		},
	} {
		_, err := lock.BeginTransaction(context.Background(), intent)
		assertErrorIs(t, err, ErrInvalidState)
	}
}

func TestAppliedServiceRejectsInvalidRequestsAndLostOwnership(t *testing.T) {
	t.Parallel()

	var nilStore *Store
	_, _, err := nilStore.AppliedService(context.Background(), "project", "api")
	assertErrorIs(t, err, ErrInvalidState)

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	_, _, err = state.AppliedService(context.Background(), "", "api")
	assertErrorIs(t, err, ErrInvalidState)

	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)
	_, err = lock.CommitAppliedService(context.Background(), TransactionID{1}, testAppliedServiceIntent())
	assertErrorIs(t, err, ErrInvalidState)

	invalidIntents := []AppliedServiceIntent{
		{},
		testAppliedServiceIntent(),
		testAppliedServiceIntent(),
	}
	invalidIntents[1].WorkloadID = strings.Repeat("\u754c", maximumWorkloadIDBytes)
	invalidIntents[2].ConfigurationDigest = domain.Digest{}

	for _, intent := range invalidIntents {
		_, err = lock.CommitAppliedService(context.Background(), transaction.ID, intent)
		assertErrorIs(t, err, ErrInvalidState)
	}

	_, err = state.database.ExecContext(
		context.Background(),
		"UPDATE writer_leases SET owner = ? WHERE service_id = ?",
		[]byte("different-owner!"),
		lock.lease.serviceID[:],
	)
	requireNoError(t, err)

	_, err = lock.CommitAppliedService(context.Background(), transaction.ID, testAppliedServiceIntent())
	assertErrorIs(t, err, ErrOwnershipLost)
	assertErrorIs(t, lock.Close(), ErrOwnershipLost)
	requireNoError(t, state.Close())

	_, _, err = state.AppliedService(context.Background(), "project", "api")
	assertErrorIs(t, err, ErrInvalidState)
}

func TestAppliedServiceRejectsTerminalTransaction(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	transaction, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)
	_, err = lock.SetTransactionState(context.Background(), transaction.ID, TransactionFailed)
	requireNoError(t, err)

	_, err = lock.CommitAppliedService(context.Background(), transaction.ID, testAppliedServiceIntent())
	assertErrorIs(t, err, ErrInvalidState)
}

func TestAppliedServiceContainsMutationFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name    string
		trigger string
	}{
		{
			name: "ignored insert",
			trigger: "CREATE TRIGGER reject_applied_insert BEFORE INSERT ON applied_services " +
				"BEGIN SELECT RAISE(IGNORE); END",
		},
		{
			name: "failed insert",
			trigger: "CREATE TRIGGER reject_applied_insert BEFORE INSERT ON applied_services " +
				"BEGIN SELECT RAISE(ABORT, 'rejected'); END",
		},
		{
			name: "failed transaction finish",
			trigger: "CREATE TRIGGER reject_applied_finish BEFORE UPDATE ON journal_transactions " +
				"BEGIN SELECT RAISE(ABORT, 'rejected'); END",
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
			transaction, err := lock.BeginTransaction(
				context.Background(),
				testTransactionIntent(domain.RuntimeDocker),
			)
			requireNoError(t, err)
			_, err = state.database.ExecContext(context.Background(), test.trigger)
			requireNoError(t, err)

			_, err = lock.CommitAppliedService(context.Background(), transaction.ID, testAppliedServiceIntent())
			assertErrorIs(t, err, ErrInvalidState)

			_, found, err := state.AppliedService(context.Background(), "project", "api")
			requireNoError(t, err)
			if found {
				t.Fatal("failed commit published an applied baseline")
			}

			requireNoError(t, lock.Close())
			requireNoError(t, state.Close())
		})
	}
}

func TestAppliedServiceInternalGuards(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	transaction, err := state.database.BeginTx(context.Background(), nil)
	requireNoError(t, err)

	result, err := publishAppliedService(
		context.Background(),
		transaction,
		lock.lease.serviceID,
		Transaction{Kind: TransactionBootstrap},
		AppliedService{},
		true,
		testAppliedServiceIntent(),
	)
	if result != nil {
		t.Fatalf("publishAppliedService(found) = %#v", result)
	}
	assertErrorIs(t, err, ErrInvalidState)

	_, err = publishAppliedService(
		context.Background(),
		transaction,
		lock.lease.serviceID,
		Transaction{Kind: TransactionKind(testUnknownValue)},
		AppliedService{},
		false,
		testAppliedServiceIntent(),
	)
	assertErrorIs(t, err, ErrInvalidState)

	_, err = replaceAppliedService(
		context.Background(),
		transaction,
		lock.lease.serviceID,
		Transaction{Kind: TransactionUpgrade, BaseTransactionID: TransactionID{1}},
		AppliedService{},
		false,
		testAppliedServiceIntent(),
	)
	assertErrorIs(t, err, ErrInvalidState)

	requireNoError(t, transaction.Rollback())
	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestAppliedServiceInternalSQLiteFailures(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	journal, err := lock.BeginTransaction(context.Background(), testTransactionIntent(domain.RuntimeDocker))
	requireNoError(t, err)

	transaction, err := state.database.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatalf("BeginTx() error = %v", err)
	}
	_, err = transaction.ExecContext(context.Background(), "DROP TABLE applied_services")
	requireNoError(t, err)

	_, err = commitAppliedService(
		context.Background(),
		transaction,
		lock.lease,
		journal.ID,
		testAppliedServiceIntent(),
	)
	assertErrorIs(t, err, ErrInvalidState)

	upgrade := Transaction{
		ID:                    TransactionID{2},
		Kind:                  TransactionUpgrade,
		BaseTransactionID:     TransactionID{1},
		PredecessorWorkloadID: "old-workload",
	}
	current := AppliedService{TransactionID: upgrade.BaseTransactionID, WorkloadID: upgrade.PredecessorWorkloadID}
	_, err = replaceAppliedService(
		context.Background(),
		transaction,
		lock.lease.serviceID,
		upgrade,
		current,
		true,
		testAppliedServiceIntent(),
	)
	if err == nil {
		t.Fatal("replaceAppliedService() accepted a missing applied table")
	}

	requireNoError(t, transaction.Rollback())
	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestAppliedServiceScannersRejectMalformedAndUnavailableRows(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() {
		requireNoError(t, database.Close())
	})

	identifier := make([]byte, transactionIDBytes)
	digest := make([]byte, len(domain.Digest{}))
	valid := []any{
		identifier,
		string(TransactionBootstrap),
		domain.RuntimeDocker.String(),
		digest,
		digest,
		digest,
		"workload",
		digest,
		digest,
		digest,
		digest,
		digest,
	}

	rows := [][]any{
		append([]any(nil), valid...),
		append([]any(nil), valid...),
		append([]any(nil), valid...),
		append([]any(nil), valid...),
	}
	rows[0][0] = []byte("short")
	rows[1][1] = testUnknownValue
	rows[2][2] = "containerd"
	rows[3][6] = ""

	for _, values := range rows {
		_, err := scanAppliedService(context.Background(), queryValues(t, database, values...))
		assertErrorIs(t, err, ErrInvalidState)
	}

	_, err := scanAppliedService(context.Background(), failingScanner{err: errJournalTest})
	assertErrorIs(t, err, ErrInvalidState)

	_, err = scanAppliedService(
		context.Background(),
		database.QueryRowContext(context.Background(), "SELECT 1 WHERE 0"),
	)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("scanAppliedService(no row) = %v", err)
	}
}

func assertAppliedService(
	t *testing.T,
	applied AppliedService,
	transaction Transaction,
	transactionIntent TransactionIntent,
	appliedIntent AppliedServiceIntent,
) {
	t.Helper()

	assertTransaction(t, transaction, TransactionActive, transactionIntent)
	if applied != appliedServiceRecord(transaction, appliedIntent) {
		t.Fatalf("applied service = %#v", applied)
	}
}
