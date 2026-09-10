package store

import (
	"context"
	"database/sql"
	"strings"
)

// Version 1 predates repository identity. Its missing identity remains unknown;
// neither migration nor read-only inspection associates it with a repository.
const version1TransactionTableSQL = "CREATE TABLE journal_transactions (" +
	"transaction_id BLOB PRIMARY KEY CHECK (typeof(transaction_id) = 'blob' AND length(transaction_id) = 16), " +
	"service_id BLOB NOT NULL CHECK (typeof(service_id) = 'blob' AND length(service_id) = 32), " +
	"kind TEXT NOT NULL CHECK (kind IN ('bootstrap', 'adopt', 'upgrade')), " +
	"state TEXT NOT NULL CHECK (state IN ('active', 'degraded', 'failed', 'succeeded')), " +
	"runtime TEXT NOT NULL CHECK (runtime IN ('docker', 'podman', 'containerd')), " +
	"source_digest BLOB NOT NULL CHECK " +
	"(typeof(source_digest) = 'blob' AND length(source_digest) = 32 AND source_digest != zeroblob(32)), " +
	"effective_digest BLOB NOT NULL CHECK " +
	"(typeof(effective_digest) = 'blob' AND length(effective_digest) = 32 AND effective_digest != zeroblob(32)), " +
	"execution_digest BLOB NOT NULL CHECK " +
	"(typeof(execution_digest) = 'blob' AND length(execution_digest) = 32 AND execution_digest != zeroblob(32)), " +
	"base_transaction_id BLOB CHECK (base_transaction_id IS NULL OR " +
	"(typeof(base_transaction_id) = 'blob' AND length(base_transaction_id) = 16)), " +
	"predecessor_workload_id TEXT CHECK (predecessor_workload_id IS NULL OR " +
	"(typeof(predecessor_workload_id) = 'text' AND " +
	"length(CAST(predecessor_workload_id AS BLOB)) BETWEEN 1 AND 256 " +
	"AND instr(predecessor_workload_id, char(0)) = 0)), " +
	"UNIQUE (transaction_id, service_id), " +
	"FOREIGN KEY (service_id) REFERENCES writer_leases(service_id), " +
	"FOREIGN KEY (base_transaction_id, service_id) " +
	"REFERENCES journal_transactions(transaction_id, service_id), " +
	"CHECK ((kind = 'bootstrap' AND base_transaction_id IS NULL AND predecessor_workload_id IS NULL) OR " +
	"(kind = 'adopt' AND base_transaction_id IS NULL AND predecessor_workload_id IS NOT NULL) OR " +
	"(kind = 'upgrade' AND base_transaction_id IS NOT NULL AND predecessor_workload_id IS NOT NULL))) " +
	"WITHOUT ROWID"

const version1TransactionColumns = "transaction_id, service_id, kind, state, runtime, " +
	"source_digest, effective_digest, execution_digest, base_transaction_id, predecessor_workload_id"

const version1ReadProjection = "WITH journal_transactions AS (SELECT " + version1TransactionColumns + ", " +
	"NULL AS repository_version, NULL AS repository_scope_digest, NULL AS repository_location_digest " +
	"FROM main.journal_transactions) "

// version1Query projects only the fields missing from the validated released
// schema. It never writes to the reader's snapshot or creates temporary views.
type version1Query struct {
	database journalQueryer
	prefix   string
}

func (query version1Query) QueryRowContext(ctx context.Context, statement string, arguments ...any) *sql.Row {
	return query.database.QueryRowContext(ctx, query.prefix+statement, arguments...)
}

func (query version1Query) QueryContext(ctx context.Context, statement string, arguments ...any) (*sql.Rows, error) {
	//nolint:wrapcheck // Shared row readers classify SQLite errors before exposing them.
	return query.database.QueryContext(ctx, query.prefix+statement, arguments...)
}

//nolint:cyclop // Exact historical DDL variants and their projections form one compatibility proof.
func readVersion1Query(ctx context.Context, database journalQueryer) (version1Query, error) {
	const repositorySchemaVersion = 2
	facts, err := readSchemaFactsAtVersion(ctx, database, 1)
	if err != nil {
		return version1Query{}, err
	}
	if facts.invalidSchemaRows != 0 {
		facts, err = readSchemaFactsAtVersion(ctx, database, repositorySchemaVersion)
		if err != nil {
			return version1Query{}, err
		}
	}

	query := version1Query{database: database, prefix: ""}
	if facts.objectCount == 7 && facts.transactionDefinition == version1TransactionTableSQL &&
		facts.repositoryInventoryDefinition == "" {
		facts.objectCount = currentObjectCount
		facts.transactionDefinition = journalTransactionTableSQL
		facts.repositoryInventoryDefinition = journalRepositoryInventoryIndexSQL
		query.prefix = version1ReadProjection
	}
	// Accept only the exact earlier definitions. Unknown health metadata stays
	// NULL; later restore proof must inspect the predecessor's actual health.
	facts.transactionDefinition = upgradeHealthDefinition(facts.transactionDefinition, journalTransactionTableSQL)
	facts.unresolvedDefinition = upgradeHealthDefinition(facts.unresolvedDefinition, journalUnresolvedIndexSQL)
	facts.repositoryInventoryDefinition = upgradeHealthDefinition(
		facts.repositoryInventoryDefinition, journalRepositoryInventoryIndexSQL)
	const healthColumn = "healthcheck INTEGER CHECK (healthcheck IS NULL OR " +
		"(typeof(healthcheck) = 'integer' AND healthcheck IN (0, 1))), "
	switch facts.appliedDefinition {
	case strings.Replace(appliedServiceTableSQL, healthColumn, "", 1):
		projection := "applied_services AS (SELECT " + version1AppliedColumns +
			", NULL AS healthcheck FROM main.applied_services) "
		if query.prefix == "" {
			query.prefix = "WITH " + projection
		} else {
			query.prefix = strings.TrimSpace(query.prefix) + ", " + projection
		}
		facts.appliedDefinition = appliedServiceTableSQL
	case strings.Replace(appliedServiceTableSQL, healthColumn,
		"healthcheck INTEGER NOT NULL CHECK (typeof(healthcheck) = 'integer' AND healthcheck IN (0, 1)), ", 1):
		facts.appliedDefinition = appliedServiceTableSQL
	}
	if !facts.valid() {
		return version1Query{}, ErrInvalidState
	}
	if err = validateWriterLeaseRows(ctx, query); err != nil {
		return version1Query{}, err
	}
	if err = validateJournalRows(ctx, query); err != nil {
		return version1Query{}, err
	}

	return query, nil
}

func upgradeHealthDefinition(definition, current string) string {
	if definition == strings.ReplaceAll(current, ", 'health_degraded'", "") {
		return current
	}

	return definition
}

const version1ActionColumns = "transaction_id, sequence, kind, state, intent_digest, postcondition_digest"
const version1AppliedColumns = "service_id, transaction_id, workload_id, configuration_digest, " +
	"storage_digest, reference_digest, platform_manifest_digest, image_config_digest"
const version1BackupColumns = "transaction_id, service_id, manifest_path, manifest_digest, created_unix"
const migrationTransactionColumns = version1TransactionColumns +
	", repository_version, repository_scope_digest, repository_location_digest"
const migrationAppliedColumns = version1AppliedColumns + ", healthcheck"

// Save dependent rows before replacing their parent, including self-references.
// Recreate the canonical CREATE statements rather than accepting altered DDL.
const migrateVersion1SQL = `
PRAGMA defer_foreign_keys = ON;
CREATE TEMP TABLE migration_actions AS SELECT ` + version1ActionColumns + ` FROM journal_actions;
CREATE TEMP TABLE migration_backups AS SELECT ` + version1BackupColumns + ` FROM workload_backups;
DROP TABLE workload_backups;
DROP TABLE applied_services;
DROP TABLE journal_actions;
DROP TABLE journal_transactions;
` + journalTransactionTableSQL + "; " + journalUnresolvedIndexSQL + "; " + journalRepositoryInventoryIndexSQL + "; " +
	journalActionTableSQL + "; " + appliedServiceTableSQL + "; " + backupIndexTableSQL + `;
INSERT INTO journal_transactions (` + migrationTransactionColumns + `)
  SELECT ` + migrationTransactionColumns + ` FROM migration_transactions;
INSERT INTO journal_actions (` + version1ActionColumns + `) SELECT ` + version1ActionColumns + ` FROM migration_actions;
INSERT INTO applied_services (` + migrationAppliedColumns + `)
  SELECT ` + migrationAppliedColumns + ` FROM migration_applied;
INSERT INTO workload_backups (` + version1BackupColumns + `)
  SELECT ` + version1BackupColumns + ` FROM migration_backups;
DROP TABLE migration_backups;
DROP TABLE migration_applied;
DROP TABLE migration_actions;
DROP TABLE migration_transactions;
`

func migrateVersion1(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}
	defer func() { _ = transaction.Rollback() }()

	query, err := readVersion1Query(ctx, transaction)
	if err != nil {
		return err
	}
	copySQL := "CREATE TEMP TABLE migration_transactions AS " + query.prefix +
		"SELECT " + migrationTransactionColumns + " FROM journal_transactions; " +
		"CREATE TEMP TABLE migration_applied AS " + query.prefix +
		"SELECT " + migrationAppliedColumns + " FROM applied_services; "
	if _, err = transaction.ExecContext(ctx, copySQL+migrateVersion1SQL); err != nil {
		return classifySQLiteProbe(ctx, err)
	}
	if _, err = transaction.ExecContext(ctx, "UPDATE schema_version SET version = ?", currentSchemaVersion); err != nil {
		return classifySQLiteProbe(ctx, err)
	}
	if err = validateSchema(ctx, transaction); err != nil {
		return err
	}

	return classifySchemaResult(ctx, transaction.Commit())
}
