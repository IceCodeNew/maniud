//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const replacementWorkloadID = "replacement-workload"

func TestBackupIndexCommitsAtomicallyWithUpgrade(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	t.Cleanup(func() {
		requireNoError(t, lock.Close())
		requireNoError(t, state.Close())
	})

	first, upgrade := beginUpgradeForBackupTest(t, lock)
	intent := testAppliedServiceIntent()
	intent.WorkloadID = replacementWorkloadID
	intent.Backup = testBackupIndexIntent(upgrade.ID)

	applied, err := lock.CommitAppliedService(context.Background(), upgrade.ID, intent)
	requireNoError(t, err)
	if applied.TransactionID != upgrade.ID || applied.WorkloadID != intent.WorkloadID {
		t.Fatalf("CommitAppliedService() = %#v", applied)
	}

	want := BackupIndex{
		TransactionID:  upgrade.ID,
		Runtime:        upgrade.Runtime,
		ManifestPath:   intent.Backup.ManifestPath,
		ManifestDigest: intent.Backup.ManifestDigest,
		CreatedUnix:    intent.Backup.CreatedUnix,
	}
	got, found, err := state.BackupIndex(context.Background(), upgrade.ID)
	if err != nil || !found || got != want {
		t.Fatalf("BackupIndex() = %#v, %t, %v, want %#v", got, found, err, want)
	}

	reused, err := lock.CommitAppliedService(context.Background(), upgrade.ID, intent)
	requireNoError(t, err)
	if reused != applied {
		t.Fatalf("replayed CommitAppliedService() = %#v", reused)
	}

	withoutBackup := intent
	withoutBackup.Backup = nil
	_, err = lock.CommitAppliedService(context.Background(), upgrade.ID, withoutBackup)
	assertErrorIs(t, err, ErrInvalidState)

	conflicting := intent
	conflicting.Backup = testBackupIndexIntent(upgrade.ID)
	conflicting.Backup.ManifestDigest = domain.Hash([]byte("different manifest"))
	_, err = lock.CommitAppliedService(context.Background(), upgrade.ID, conflicting)
	assertErrorIs(t, err, ErrInvalidState)

	_, found, err = state.BackupIndex(context.Background(), first.TransactionID)
	if err != nil || found {
		t.Fatalf("bootstrap BackupIndex() = %t, %v", found, err)
	}
}

func TestBackupIndexRejectsInvalidAndNonUpgradeIntent(t *testing.T) {
	t.Parallel()

	var nilStore *Store
	_, _, err := nilStore.BackupIndex(context.Background(), TransactionID{1})
	assertErrorIs(t, err, ErrInvalidState)

	for _, test := range invalidBackupIndexIntentCases() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
			var (
				before      AppliedService
				transaction Transaction
			)
			if test.upgrade {
				before, transaction = beginUpgradeForBackupTest(t, lock)
			} else {
				var err error
				transaction, err = lock.BeginTransaction(
					context.Background(),
					testTransactionIntent(domain.RuntimeDocker),
				)
				requireNoError(t, err)
			}
			intent := testAppliedServiceIntent()
			test.mutate(transaction, &intent)

			_, err := lock.CommitAppliedService(context.Background(), transaction.ID, intent)
			assertErrorIs(t, err, ErrInvalidState)

			after, found, err := state.AppliedService(context.Background(), "project", "api")
			if err != nil || found != test.upgrade || after != before {
				t.Fatalf("AppliedService() after rejection = %#v, %t, %v", after, found, err)
			}

			requireNoError(t, lock.Close())
			requireNoError(t, state.Close())
		})
	}
}

type invalidBackupIndexIntentCase struct {
	name    string
	upgrade bool
	mutate  func(Transaction, *AppliedServiceIntent)
}

func invalidBackupIndexIntentCases() []invalidBackupIndexIntentCase {
	return []invalidBackupIndexIntentCase{
		{
			name: "bootstrap",
			mutate: func(transaction Transaction, intent *AppliedServiceIntent) {
				intent.Backup = testBackupIndexIntent(transaction.ID)
			},
		},
		{
			name:    "wrong manifest path",
			upgrade: true,
			mutate: func(transaction Transaction, intent *AppliedServiceIntent) {
				intent.Backup = testBackupIndexIntent(transaction.ID)
				intent.Backup.ManifestPath = "00000000000000000000000000000000/manifest.json"
			},
		},
		{
			name:    "missing manifest digest",
			upgrade: true,
			mutate: func(transaction Transaction, intent *AppliedServiceIntent) {
				intent.Backup = testBackupIndexIntent(transaction.ID)
				intent.Backup.ManifestDigest = domain.Digest{}
			},
		},
		{
			name:    "invalid creation time",
			upgrade: true,
			mutate: func(transaction Transaction, intent *AppliedServiceIntent) {
				intent.Backup = testBackupIndexIntent(transaction.ID)
				intent.Backup.CreatedUnix = 0
			},
		},
		{
			name:    "oversized manifest path",
			upgrade: true,
			mutate: func(transaction Transaction, intent *AppliedServiceIntent) {
				intent.Backup = testBackupIndexIntent(transaction.ID)
				intent.Backup.ManifestPath = string(make([]byte, 129))
			},
		},
	}
}

func TestBackupIndexReplayContainsCorruptIndex(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	_, upgrade := beginUpgradeForBackupTest(t, lock)
	intent := testAppliedServiceIntent()
	intent.WorkloadID = replacementWorkloadID
	intent.Backup = testBackupIndexIntent(upgrade.ID)
	_, err := lock.CommitAppliedService(context.Background(), upgrade.ID, intent)
	requireNoError(t, err)
	_, err = state.database.ExecContext(context.Background(), "DROP TABLE workload_backups")
	requireNoError(t, err)

	_, err = lock.CommitAppliedService(context.Background(), upgrade.ID, intent)
	assertErrorIs(t, err, ErrInvalidState)

	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestBackupIndexContainsInsertFailure(t *testing.T) {
	t.Parallel()

	state, lock := openJournalTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
	_, upgrade := beginUpgradeForBackupTest(t, lock)
	_, err := state.database.ExecContext(
		context.Background(),
		"CREATE TRIGGER reject_backup_insert BEFORE INSERT ON workload_backups "+
			"BEGIN SELECT RAISE(ABORT, 'rejected'); END",
	)
	requireNoError(t, err)

	intent := testAppliedServiceIntent()
	intent.WorkloadID = replacementWorkloadID
	intent.Backup = testBackupIndexIntent(upgrade.ID)
	_, err = lock.CommitAppliedService(context.Background(), upgrade.ID, intent)
	assertErrorIs(t, err, ErrInvalidState)

	transaction, err := state.Transaction(context.Background(), upgrade.ID)
	requireNoError(t, err)
	if transaction.State != TransactionActive {
		t.Fatalf("upgrade state = %q", transaction.State)
	}
	_, found, err := state.BackupIndex(context.Background(), upgrade.ID)
	if err != nil || found {
		t.Fatalf("BackupIndex() after rollback = %t, %v", found, err)
	}

	requireNoError(t, lock.Close())
	requireNoError(t, state.Close())
}

func TestBackupIndexRejectsMalformedAndUnavailableRows(t *testing.T) {
	t.Parallel()

	database := testDatabase(t, "file::memory:")
	t.Cleanup(func() { requireNoError(t, database.Close()) })

	identifier := TransactionID{1}
	digest := domain.Hash([]byte("manifest"))
	valid := []any{
		identifier[:],
		domain.RuntimeDocker.String(),
		backupManifestPath(identifier),
		digest[:],
		int64(1),
	}
	rows := [][]any{
		append([]any(nil), valid...),
		append([]any(nil), valid...),
		append([]any(nil), valid...),
		append([]any(nil), valid...),
		append([]any(nil), valid...),
	}
	rows[0][0] = []byte("short")
	rows[1][1] = testUnknownValue
	rows[2][2] = "wrong/manifest.json"
	rows[3][3] = make([]byte, len(domain.Digest{}))
	rows[4][4] = int64(0)

	for _, values := range rows {
		_, err := scanBackupIndex(context.Background(), queryValues(t, database, values...))
		assertErrorIs(t, err, ErrInvalidState)
	}

	_, err := scanBackupIndex(context.Background(), failingScanner{err: errJournalTest})
	assertErrorIs(t, err, ErrInvalidState)

	_, err = scanBackupIndex(
		context.Background(),
		database.QueryRowContext(context.Background(), "SELECT 1 WHERE 0"),
	)
	if !errors.Is(err, sql.ErrNoRows) {
		t.Fatalf("scanBackupIndex(no row) = %v", err)
	}
}

func beginUpgradeForBackupTest(t *testing.T, lock *ServiceLock) (AppliedService, Transaction) {
	t.Helper()

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
	upgradeIntent.EffectiveDigest = domain.Hash([]byte("replacement Compose"))
	upgradeIntent.ExecutionDigest = domain.Hash([]byte("replacement runtime"))
	upgrade, err := lock.BeginTransaction(context.Background(), upgradeIntent)
	requireNoError(t, err)

	return first, upgrade
}

func testBackupIndexIntent(identifier TransactionID) *BackupIndexIntent {
	return &BackupIndexIntent{
		ManifestPath:   backupManifestPath(identifier),
		ManifestDigest: domain.Hash([]byte("backup manifest")),
		CreatedUnix:    1,
	}
}
