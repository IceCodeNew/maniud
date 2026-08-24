//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const backupManifestName = "manifest.json"

// BackupIndex returns the complete-manifest locator recorded for one
// successful upgrade transaction.
func (store *Store) BackupIndex(ctx context.Context, identifier TransactionID) (BackupIndex, bool, error) {
	if store == nil || store.database == nil || identifier == (TransactionID{}) {
		return BackupIndex{}, false, ErrInvalidState
	}

	return backupIndex(ctx, store.database, identifier)
}

func publishBackupIndex(
	ctx context.Context,
	transaction *sql.Tx,
	serviceID [32]byte,
	record Transaction,
	intent *BackupIndexIntent,
) error {
	if intent == nil {
		return nil
	}
	if record.Kind != TransactionUpgrade || !validBackupIndexIntent(intent) ||
		intent.ManifestPath != backupManifestPath(record.ID) {
		return ErrInvalidState
	}

	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO workload_backups "+
			"(transaction_id, service_id, manifest_path, manifest_digest, created_unix) "+
			"VALUES (?, ?, ?, ?, ?)",
		record.ID[:],
		serviceID[:],
		intent.ManifestPath,
		intent.ManifestDigest[:],
		intent.CreatedUnix,
	)
	if err != nil {
		return fmt.Errorf("insert workload backup index: %w", err)
	}

	return requireJournalMutation(result)
}

func backupIndexMatchesIntent(
	ctx context.Context,
	database rowQueryer,
	record Transaction,
	intent *BackupIndexIntent,
) (bool, error) {
	index, found, err := backupIndex(ctx, database, record.ID)
	if err != nil {
		return false, err
	}
	if intent == nil {
		return !found, nil
	}

	return found && index.Runtime == record.Runtime &&
		index.ManifestPath == intent.ManifestPath &&
		index.ManifestDigest == intent.ManifestDigest &&
		index.CreatedUnix == intent.CreatedUnix, nil
}

func backupIndex(
	ctx context.Context,
	database rowQueryer,
	identifier TransactionID,
) (BackupIndex, bool, error) {
	row := database.QueryRowContext(
		ctx,
		"SELECT backup.transaction_id, journal.runtime, backup.manifest_path, "+
			"backup.manifest_digest, backup.created_unix FROM workload_backups AS backup "+
			"JOIN journal_transactions AS journal USING (transaction_id) WHERE backup.transaction_id = ?",
		identifier[:],
	)

	record, err := scanBackupIndex(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		return BackupIndex{}, false, nil
	}

	return record, err == nil, err
}

func scanBackupIndex(ctx context.Context, row rowScanner) (BackupIndex, error) {
	var (
		record     BackupIndex
		identifier []byte
		runtime    string
		digest     []byte
	)

	err := row.Scan(&identifier, &runtime, &record.ManifestPath, &digest, &record.CreatedUnix)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return BackupIndex{}, sql.ErrNoRows
		}

		return BackupIndex{}, classifySQLiteProbe(ctx, err)
	}

	parsedRuntime, valid := domain.ParseRuntimeKind(runtime)
	if !valid || !parsedRuntime.SupportsWorkloads() ||
		!copyExact(record.TransactionID[:], identifier) ||
		!copyExact(record.ManifestDigest[:], digest) ||
		record.ManifestDigest == (domain.Digest{}) || record.CreatedUnix <= 0 ||
		record.ManifestPath != backupManifestPath(record.TransactionID) {
		return BackupIndex{}, ErrInvalidState
	}
	record.Runtime = parsedRuntime

	return record, nil
}

func validBackupIndexIntent(intent *BackupIndexIntent) bool {
	if intent == nil {
		return true
	}

	return intent.ManifestDigest != (domain.Digest{}) && intent.CreatedUnix > 0 &&
		len(intent.ManifestPath) > len(backupManifestName) && len(intent.ManifestPath) <= 128
}

func backupManifestPath(identifier TransactionID) string {
	return identifier.String() + "/" + backupManifestName
}
