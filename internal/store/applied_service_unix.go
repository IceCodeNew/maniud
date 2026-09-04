//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
)

// AppliedService returns the latest committed workload generation for a
// project and service without acquiring a writer lease.
func (store *Store) AppliedService(
	ctx context.Context,
	projectName string,
	serviceName string,
) (AppliedService, bool, error) {
	if store == nil || store.database == nil {
		return AppliedService{}, false, ErrInvalidState
	}

	serviceID, valid := serviceIdentity(projectName, serviceName)
	if !valid {
		return AppliedService{}, false, ErrInvalidState
	}

	return appliedService(ctx, store.database, serviceID)
}

// CommitAppliedService atomically publishes the latest opaque workload
// baseline and marks its transaction succeeded under the retained writer
// fence. Bootstrap and adoption insert the first baseline; upgrade replaces
// only the exact predecessor generation bound into its transaction.
func (lock *ServiceLock) CommitAppliedService(
	ctx context.Context,
	identifier TransactionID,
	intent AppliedServiceIntent,
) (AppliedService, error) {
	if !validAppliedServiceIntent(intent) {
		return AppliedService{}, ErrInvalidState
	}

	var committed Transaction

	err := lock.withFencedWrite(ctx, func(
		ctx context.Context,
		transaction *sql.Tx,
		lease writerLease,
	) error {
		var commitErr error

		committed, commitErr = commitAppliedService(ctx, transaction, lease, identifier, intent)

		return commitErr
	})
	if err != nil {
		return AppliedService{}, err
	}

	return appliedServiceRecord(committed, intent), nil
}

func commitAppliedService(
	ctx context.Context,
	transaction *sql.Tx,
	lease writerLease,
	identifier TransactionID,
	intent AppliedServiceIntent,
) (Transaction, error) {
	record, err := transactionForService(ctx, transaction, identifier, lease.serviceID)
	if err != nil {
		return Transaction{}, err
	}

	current, found, err := appliedService(ctx, transaction, lease.serviceID)
	if err != nil {
		return Transaction{}, err
	}

	if record.State == TransactionSucceeded {
		return replayAppliedService(ctx, transaction, record, current, found, intent)
	}

	if record.State != TransactionActive && record.State != TransactionHealthDegraded {
		return Transaction{}, ErrInvalidState
	}

	result, err := publishAppliedService(ctx, transaction, lease.serviceID, record, current, found, intent)
	if err != nil {
		return Transaction{}, err
	}

	if err = requireJournalMutation(result); err != nil {
		return Transaction{}, err
	}

	if err = publishBackupIndex(ctx, transaction, lease.serviceID, record, intent.Backup); err != nil {
		return Transaction{}, err
	}

	if err = finishAppliedTransaction(ctx, transaction, lease, identifier); err != nil {
		return Transaction{}, err
	}

	record.State = TransactionSucceeded

	return record, nil
}

func replayAppliedService(
	ctx context.Context,
	database rowQueryer,
	record Transaction,
	current AppliedService,
	found bool,
	intent AppliedServiceIntent,
) (Transaction, error) {
	backupMatches, err := backupIndexMatchesIntent(ctx, database, record, intent.Backup)
	if err != nil {
		return Transaction{}, err
	}

	if !found || !appliedServiceMatches(current, record, intent) || !backupMatches {
		return Transaction{}, ErrInvalidState
	}

	return record, nil
}

func publishAppliedService(
	ctx context.Context,
	transaction *sql.Tx,
	serviceID [32]byte,
	record Transaction,
	current AppliedService,
	found bool,
	intent AppliedServiceIntent,
) (sql.Result, error) {
	switch record.Kind {
	case TransactionBootstrap:
		if found {
			return nil, ErrInvalidState
		}

		return insertAppliedService(ctx, transaction, serviceID, record.ID, intent)
	case TransactionAdopt:
		if found || intent.WorkloadID != record.PredecessorWorkloadID {
			return nil, ErrInvalidState
		}

		return insertAppliedService(ctx, transaction, serviceID, record.ID, intent)
	case TransactionUpgrade:
		return replaceAppliedService(ctx, transaction, serviceID, record, current, found, intent)
	default:
		return nil, ErrInvalidState
	}
}

func replaceAppliedService(
	ctx context.Context,
	transaction *sql.Tx,
	serviceID [32]byte,
	record Transaction,
	current AppliedService,
	found bool,
	intent AppliedServiceIntent,
) (sql.Result, error) {
	if !found || current.TransactionID != record.BaseTransactionID ||
		current.WorkloadID != record.PredecessorWorkloadID {
		return nil, ErrInvalidState
	}

	result, err := transaction.ExecContext(
		ctx,
		"UPDATE applied_services SET transaction_id = ?, workload_id = ?, configuration_digest = ?, "+
			"storage_digest = ?, reference_digest = ?, platform_manifest_digest = ?, image_config_digest = ?, healthcheck = ? "+
			"WHERE service_id = ? AND transaction_id = ? AND workload_id = ?",
		record.ID[:],
		intent.WorkloadID,
		intent.ConfigurationDigest[:],
		intent.StorageDigest[:],
		intent.ReferenceDigest[:],
		intent.PlatformManifestDigest[:],
		intent.ImageConfigDigest[:],
		intent.Healthcheck,
		serviceID[:],
		record.BaseTransactionID[:],
		record.PredecessorWorkloadID,
	)
	if err != nil {
		return nil, fmt.Errorf("replace applied service: %w", err)
	}

	return result, nil
}

func finishAppliedTransaction(
	ctx context.Context,
	transaction *sql.Tx,
	lease writerLease,
	identifier TransactionID,
) error {
	result, err := transaction.ExecContext(
		ctx,
		"UPDATE journal_transactions SET state = 'succeeded' "+
			"WHERE transaction_id = ? AND service_id = ? AND state IN ('active', 'health_degraded') "+
			"AND NOT EXISTS (SELECT 1 FROM journal_actions "+
			"WHERE transaction_id = ? AND state != 'completed') "+
			"AND EXISTS (SELECT 1 FROM writer_leases "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?)",
		identifier[:],
		lease.serviceID[:],
		identifier[:],
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	return requireJournalMutation(result)
}

func appliedServiceRecord(transaction Transaction, intent AppliedServiceIntent) AppliedService {
	return AppliedService{
		TransactionID:          transaction.ID,
		Kind:                   transaction.Kind,
		Runtime:                transaction.Runtime,
		SourceDigest:           transaction.SourceDigest,
		EffectiveDigest:        transaction.EffectiveDigest,
		ExecutionDigest:        transaction.ExecutionDigest,
		WorkloadID:             intent.WorkloadID,
		ConfigurationDigest:    intent.ConfigurationDigest,
		StorageDigest:          intent.StorageDigest,
		ReferenceDigest:        intent.ReferenceDigest,
		PlatformManifestDigest: intent.PlatformManifestDigest,
		ImageConfigDigest:      intent.ImageConfigDigest,
		Healthcheck:            intent.Healthcheck,
	}
}

func insertAppliedService(
	ctx context.Context,
	transaction *sql.Tx,
	serviceID [32]byte,
	identifier TransactionID,
	intent AppliedServiceIntent,
) (sql.Result, error) {
	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO applied_services "+
			"(service_id, transaction_id, workload_id, configuration_digest, storage_digest, reference_digest, "+
			"platform_manifest_digest, image_config_digest, healthcheck) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)",
		serviceID[:],
		identifier[:],
		intent.WorkloadID,
		intent.ConfigurationDigest[:],
		intent.StorageDigest[:],
		intent.ReferenceDigest[:],
		intent.PlatformManifestDigest[:],
		intent.ImageConfigDigest[:],
		intent.Healthcheck,
	)
	if err != nil {
		return nil, fmt.Errorf("insert applied service: %w", err)
	}

	return result, nil
}

func appliedService(
	ctx context.Context,
	database rowQueryer,
	serviceID [32]byte,
) (AppliedService, bool, error) {
	row := database.QueryRowContext(
		ctx,
		"SELECT applied.transaction_id, journal.kind, journal.runtime, journal.source_digest, "+
			"journal.effective_digest, journal.execution_digest, applied.workload_id, "+
			"applied.configuration_digest, applied.storage_digest, applied.reference_digest, applied.platform_manifest_digest, "+
			"applied.image_config_digest, applied.healthcheck FROM applied_services AS applied "+
			"JOIN journal_transactions AS journal USING (transaction_id) WHERE applied.service_id = ?",
		serviceID[:],
	)

	record, err := scanAppliedService(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		return AppliedService{}, false, nil
	}

	return record, err == nil, err
}

//nolint:funlen // The scanner keeps one SQL row and its complete integrity validation together.
func scanAppliedService(ctx context.Context, row rowScanner) (AppliedService, error) {
	var (
		record        AppliedService
		identifier    []byte
		kind          string
		runtime       string
		source        []byte
		effective     []byte
		execution     []byte
		configuration []byte
		storage       []byte
		reference     []byte
		manifest      []byte
		imageConfig   []byte
		healthcheck   int
	)

	err := row.Scan(
		&identifier,
		&kind,
		&runtime,
		&source,
		&effective,
		&execution,
		&record.WorkloadID,
		&configuration,
		&storage,
		&reference,
		&manifest,
		&imageConfig,
		&healthcheck,
	)
	if err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return AppliedService{}, sql.ErrNoRows
		}

		return AppliedService{}, classifySQLiteProbe(ctx, err)
	}

	parsedRuntime, valid := domain.ParseRuntimeKind(runtime)
	if !valid || !parsedRuntime.SupportsWorkloads() || !validWorkloadID(record.WorkloadID) {
		return AppliedService{}, ErrInvalidState
	}

	record.Kind = TransactionKind(kind)
	record.Runtime = parsedRuntime
	if healthcheck != 0 && healthcheck != 1 {
		return AppliedService{}, ErrInvalidState
	}
	record.Healthcheck = healthcheck == 1
	if !validTransactionKind(record.Kind) || !copyAppliedServiceIdentity(
		&record,
		identifier,
		source,
		effective,
		execution,
		configuration,
		storage,
		reference,
		manifest,
		imageConfig,
	) {
		return AppliedService{}, ErrInvalidState
	}

	return record, nil
}

func copyAppliedServiceIdentity(
	record *AppliedService,
	identifier []byte,
	source []byte,
	effective []byte,
	execution []byte,
	configuration []byte,
	storage []byte,
	reference []byte,
	manifest []byte,
	imageConfig []byte,
) bool {
	return copyExact(record.TransactionID[:], identifier) &&
		copyExact(record.SourceDigest[:], source) &&
		copyExact(record.EffectiveDigest[:], effective) &&
		copyExact(record.ExecutionDigest[:], execution) &&
		copyExact(record.ConfigurationDigest[:], configuration) &&
		copyExact(record.StorageDigest[:], storage) &&
		copyExact(record.ReferenceDigest[:], reference) &&
		copyExact(record.PlatformManifestDigest[:], manifest) &&
		copyExact(record.ImageConfigDigest[:], imageConfig)
}

func transactionForService(
	ctx context.Context,
	transaction *sql.Tx,
	identifier TransactionID,
	serviceID [32]byte,
) (Transaction, error) {
	row := transaction.QueryRowContext(
		ctx,
		"SELECT "+transactionSelectColumns+" FROM journal_transactions "+
			"WHERE transaction_id = ? AND service_id = ?",
		identifier[:],
		serviceID[:],
	)

	record, err := scanTransaction(ctx, row)
	if errors.Is(err, sql.ErrNoRows) {
		return Transaction{}, ErrInvalidState
	}

	return record, err
}

func validAppliedServiceIntent(intent AppliedServiceIntent) bool {
	empty := domain.Digest{}

	return validWorkloadID(intent.WorkloadID) && intent.ConfigurationDigest != empty &&
		intent.StorageDigest != empty && intent.ReferenceDigest != empty &&
		intent.PlatformManifestDigest != empty && intent.ImageConfigDigest != empty &&
		validBackupIndexIntent(intent.Backup)
}

func appliedServiceMatches(
	applied AppliedService,
	transaction Transaction,
	intent AppliedServiceIntent,
) bool {
	return applied == appliedServiceRecord(transaction, intent)
}
