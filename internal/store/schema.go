package store

import (
	"context"
	"database/sql"
)

type rowQueryer interface {
	QueryRowContext(ctx context.Context, query string, arguments ...any) *sql.Row
}

const (
	currentSchemaVersion = 1
	currentObjectCount   = 8
	schemaTableName      = "schema_version"
	schemaTableSQL       = "CREATE TABLE schema_version (" +
		"singleton INTEGER PRIMARY KEY CHECK (singleton = 1), " +
		"version INTEGER NOT NULL CHECK (version > 0))"
	writerLeaseTableName = "writer_leases"
	writerLeaseTableSQL  = "CREATE TABLE writer_leases (" +
		"service_id BLOB PRIMARY KEY CHECK (typeof(service_id) = 'blob' AND length(service_id) = 32), " +
		"epoch INTEGER NOT NULL CHECK (epoch > 0), " +
		"owner BLOB CHECK (owner IS NULL OR (typeof(owner) = 'blob' AND length(owner) = 16))) WITHOUT ROWID"
	journalTransactionTableName = "journal_transactions"
	journalTransactionTableSQL  = "CREATE TABLE journal_transactions (" +
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
		"repository_version INTEGER, " +
		"repository_scope_digest BLOB, " +
		"repository_location_digest BLOB, " +
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
		"CHECK ((repository_version IS NULL AND repository_scope_digest IS NULL " +
		"AND repository_location_digest IS NULL) OR " +
		"(repository_version IS NOT NULL AND repository_scope_digest IS NOT NULL " +
		"AND repository_location_digest IS NOT NULL " +
		"AND repository_version = 1 AND typeof(repository_version) = 'integer' " +
		"AND typeof(repository_scope_digest) = 'blob' AND length(repository_scope_digest) = 32 " +
		"AND repository_scope_digest != zeroblob(32) " +
		"AND typeof(repository_location_digest) = 'blob' AND length(repository_location_digest) = 32 " +
		"AND repository_location_digest != zeroblob(32))), " +
		"CHECK ((kind = 'bootstrap' AND base_transaction_id IS NULL AND predecessor_workload_id IS NULL) OR " +
		"(kind = 'adopt' AND base_transaction_id IS NULL AND predecessor_workload_id IS NOT NULL) OR " +
		"(kind = 'upgrade' AND base_transaction_id IS NOT NULL AND predecessor_workload_id IS NOT NULL))) " +
		"WITHOUT ROWID"
	journalUnresolvedIndexName = "journal_one_unresolved_transaction_per_service"
	journalUnresolvedIndexSQL  = "CREATE UNIQUE INDEX journal_one_unresolved_transaction_per_service " +
		"ON journal_transactions(service_id) WHERE state IN ('active', 'degraded')"
	journalRepositoryInventoryIndexName = "journal_unresolved_repository_inventory"
	journalRepositoryInventoryIndexSQL  = "CREATE INDEX journal_unresolved_repository_inventory " +
		"ON journal_transactions(repository_scope_digest, repository_location_digest, transaction_id) " +
		"WHERE state IN ('active', 'degraded')"
	journalActionTableName = "journal_actions"
	journalActionTableSQL  = "CREATE TABLE journal_actions (" +
		"transaction_id BLOB NOT NULL CHECK (typeof(transaction_id) = 'blob' AND length(transaction_id) = 16), " +
		"sequence INTEGER NOT NULL CHECK (typeof(sequence) = 'integer' AND sequence > 0), " +
		"kind TEXT NOT NULL CHECK (typeof(kind) = 'text' AND length(kind) BETWEEN 1 AND 64 " +
		"AND substr(kind, 1, 1) GLOB '[a-z0-9]' AND kind NOT GLOB '*[^a-z0-9._-]*'), " +
		"state TEXT NOT NULL CHECK (state IN ('intent', 'effect_outcome_unknown', 'completed')), " +
		"intent_digest BLOB NOT NULL CHECK (typeof(intent_digest) = 'blob' AND length(intent_digest) = 32), " +
		"postcondition_digest BLOB, " +
		"PRIMARY KEY (transaction_id, sequence), " +
		"FOREIGN KEY (transaction_id) REFERENCES journal_transactions(transaction_id) ON DELETE CASCADE, " +
		"CHECK ((state = 'completed' AND typeof(postcondition_digest) = 'blob' " +
		"AND length(postcondition_digest) = 32) OR " +
		"(state != 'completed' AND postcondition_digest IS NULL))) WITHOUT ROWID"
	appliedServiceTableName = "applied_services"
	appliedServiceTableSQL  = "CREATE TABLE applied_services (" +
		"service_id BLOB PRIMARY KEY CHECK (typeof(service_id) = 'blob' AND length(service_id) = 32), " +
		"transaction_id BLOB NOT NULL UNIQUE CHECK " +
		"(typeof(transaction_id) = 'blob' AND length(transaction_id) = 16), " +
		"workload_id TEXT NOT NULL CHECK (typeof(workload_id) = 'text' AND " +
		"length(CAST(workload_id AS BLOB)) BETWEEN 1 AND 256 " +
		"AND instr(workload_id, char(0)) = 0), " +
		"configuration_digest BLOB NOT NULL CHECK " +
		"(typeof(configuration_digest) = 'blob' AND length(configuration_digest) = 32 " +
		"AND configuration_digest != zeroblob(32)), " +
		"storage_digest BLOB NOT NULL CHECK " +
		"(typeof(storage_digest) = 'blob' AND length(storage_digest) = 32 " +
		"AND storage_digest != zeroblob(32)), " +
		"reference_digest BLOB NOT NULL CHECK (typeof(reference_digest) = 'blob' " +
		"AND length(reference_digest) = 32 AND reference_digest != zeroblob(32)), " +
		"platform_manifest_digest BLOB NOT NULL CHECK " +
		"(typeof(platform_manifest_digest) = 'blob' AND length(platform_manifest_digest) = 32 " +
		"AND platform_manifest_digest != zeroblob(32)), " +
		"image_config_digest BLOB NOT NULL CHECK " +
		"(typeof(image_config_digest) = 'blob' AND length(image_config_digest) = 32 " +
		"AND image_config_digest != zeroblob(32)), " +
		"FOREIGN KEY (service_id) REFERENCES writer_leases(service_id), " +
		"FOREIGN KEY (transaction_id, service_id) " +
		"REFERENCES journal_transactions(transaction_id, service_id)) WITHOUT ROWID"
	backupIndexTableName = "workload_backups"
	backupIndexTableSQL  = "CREATE TABLE workload_backups (" +
		"transaction_id BLOB PRIMARY KEY CHECK (typeof(transaction_id) = 'blob' AND length(transaction_id) = 16), " +
		"service_id BLOB NOT NULL CHECK (typeof(service_id) = 'blob' AND length(service_id) = 32), " +
		"manifest_path TEXT NOT NULL CHECK (typeof(manifest_path) = 'text' AND " +
		"manifest_path = lower(hex(transaction_id)) || '/manifest.json'), " +
		"manifest_digest BLOB NOT NULL CHECK (typeof(manifest_digest) = 'blob' " +
		"AND length(manifest_digest) = 32 AND manifest_digest != zeroblob(32)), " +
		"created_unix INTEGER NOT NULL CHECK (typeof(created_unix) = 'integer' AND created_unix > 0), " +
		"FOREIGN KEY (transaction_id, service_id) " +
		"REFERENCES journal_transactions(transaction_id, service_id)) WITHOUT ROWID"
	initialSchemaSQL = schemaTableSQL + "; " +
		"INSERT INTO schema_version (singleton, version) VALUES (1, 1); " +
		writerLeaseTableSQL + "; " + journalTransactionTableSQL + "; " +
		journalUnresolvedIndexSQL + "; " + journalRepositoryInventoryIndexSQL + "; " +
		journalActionTableSQL + "; " + appliedServiceTableSQL + "; " + backupIndexTableSQL
)

func ensureInitialSchema(ctx context.Context, database *sql.DB) error {
	objectCount, err := schemaObjectSummary(ctx, database)
	if err != nil {
		return classifySchema(ctx)
	}

	if objectCount == 0 {
		return initializeSchema(ctx, database)
	}

	return validateSchema(ctx, database)
}

func schemaObjectSummary(ctx context.Context, database *sql.DB) (int, error) {
	var count int

	err := database.QueryRowContext(
		ctx,
		"SELECT count(*) FROM sqlite_schema "+
			"WHERE type IN ('table', 'index', 'view', 'trigger') AND name NOT LIKE 'sqlite_%'",
	).Scan(&count)
	if err != nil {
		return 0, classifySchema(ctx)
	}

	return count, nil
}

func initializeSchema(ctx context.Context, database *sql.DB) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return classifySchema(ctx)
	}

	_, err = transaction.ExecContext(ctx, initialSchemaSQL)
	if err != nil {
		_ = transaction.Rollback()

		return classifySchema(ctx)
	}

	err = transaction.Commit()

	return classifySchemaResult(ctx, err)
}

type schemaFacts struct {
	objectCount                   int
	schemaDefinition              string
	writerLeaseDefinition         string
	transactionDefinition         string
	unresolvedDefinition          string
	repositoryInventoryDefinition string
	actionDefinition              string
	appliedDefinition             string
	backupDefinition              string
	schemaRows                    int
	invalidSchemaRows             int
}

func (facts schemaFacts) valid() bool {
	return facts.objectCount == currentObjectCount &&
		facts.schemaDefinition == schemaTableSQL &&
		facts.writerLeaseDefinition == writerLeaseTableSQL &&
		facts.transactionDefinition == journalTransactionTableSQL &&
		facts.unresolvedDefinition == journalUnresolvedIndexSQL &&
		facts.repositoryInventoryDefinition == journalRepositoryInventoryIndexSQL &&
		facts.actionDefinition == journalActionTableSQL &&
		facts.appliedDefinition == appliedServiceTableSQL &&
		facts.backupDefinition == backupIndexTableSQL &&
		facts.schemaRows == 1 && facts.invalidSchemaRows == 0
}

func validateSchema(ctx context.Context, database rowQueryer) error {
	facts, err := readSchemaFacts(ctx, database)
	if err != nil {
		return err
	}

	if !facts.valid() {
		return ErrInvalidState
	}

	err = validateWriterLeaseRows(ctx, database)
	if err != nil {
		return err
	}

	return validateJournalRows(ctx, database)
}

func readSchemaFacts(
	ctx context.Context,
	database rowQueryer,
) (schemaFacts, error) {
	var facts schemaFacts

	err := database.QueryRowContext(
		ctx,
		"SELECT "+
			"(SELECT count(*) FROM sqlite_schema "+
			" WHERE type IN ('table', 'index', 'view', 'trigger') AND name NOT LIKE 'sqlite_%'), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'schema_version'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'writer_leases'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'journal_transactions'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'index' "+
			" AND name = 'journal_one_unresolved_transaction_per_service'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'index' "+
			" AND name = 'journal_unresolved_repository_inventory'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'journal_actions'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'applied_services'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'workload_backups'), ''), "+
			"(SELECT count(*) FROM schema_version), "+
			"(SELECT count(*) FROM schema_version "+
			" WHERE singleton != 1 OR version != ? OR typeof(version) != 'integer')",
		currentSchemaVersion,
	).Scan(
		&facts.objectCount,
		&facts.schemaDefinition,
		&facts.writerLeaseDefinition,
		&facts.transactionDefinition,
		&facts.unresolvedDefinition,
		&facts.repositoryInventoryDefinition,
		&facts.actionDefinition,
		&facts.appliedDefinition,
		&facts.backupDefinition,
		&facts.schemaRows,
		&facts.invalidSchemaRows,
	)
	if err != nil {
		return schemaFacts{}, classifySQLiteProbe(ctx, err)
	}

	return facts, nil
}

func validateWriterLeaseRows(ctx context.Context, database rowQueryer) error {
	var invalidRows int

	err := database.QueryRowContext(
		ctx,
		"SELECT count(*) FROM writer_leases WHERE "+
			"typeof(service_id) != 'blob' OR length(service_id) != 32 OR "+
			"typeof(epoch) != 'integer' OR epoch <= 0 OR "+
			"(owner IS NOT NULL AND (typeof(owner) != 'blob' OR length(owner) != 16))",
	).Scan(&invalidRows)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	if invalidRows != 0 {
		return ErrInvalidState
	}

	return nil
}

const journalRowValidationSQL = `
SELECT
  (SELECT count(*) FROM journal_transactions WHERE
    typeof(transaction_id) != 'blob' OR length(transaction_id) != 16 OR
    typeof(service_id) != 'blob' OR length(service_id) != 32 OR
    typeof(kind) != 'text' OR kind NOT IN ('bootstrap', 'adopt', 'upgrade') OR
    typeof(state) != 'text' OR state NOT IN ('active', 'degraded', 'failed', 'succeeded') OR
    typeof(runtime) != 'text' OR runtime NOT IN ('docker', 'podman', 'containerd') OR
    typeof(source_digest) != 'blob' OR length(source_digest) != 32 OR source_digest = zeroblob(32) OR
    typeof(effective_digest) != 'blob' OR length(effective_digest) != 32 OR effective_digest = zeroblob(32) OR
    typeof(execution_digest) != 'blob' OR length(execution_digest) != 32 OR execution_digest = zeroblob(32) OR
    ((repository_version IS NULL OR repository_scope_digest IS NULL OR repository_location_digest IS NULL) AND
      (repository_version IS NOT NULL OR repository_scope_digest IS NOT NULL OR
        repository_location_digest IS NOT NULL)) OR
    (repository_version IS NOT NULL AND
      (typeof(repository_version) != 'integer' OR repository_version != 1 OR
        typeof(repository_scope_digest) != 'blob' OR length(repository_scope_digest) != 32 OR
          repository_scope_digest = zeroblob(32) OR
        typeof(repository_location_digest) != 'blob' OR length(repository_location_digest) != 32 OR
          repository_location_digest = zeroblob(32))) OR
    (base_transaction_id IS NOT NULL AND
      (typeof(base_transaction_id) != 'blob' OR length(base_transaction_id) != 16)) OR
    (predecessor_workload_id IS NOT NULL AND
      (typeof(predecessor_workload_id) != 'text' OR
        length(CAST(predecessor_workload_id AS BLOB)) NOT BETWEEN 1 AND 256 OR
        instr(predecessor_workload_id, char(0)) != 0)) OR
    (kind = 'bootstrap' AND (base_transaction_id IS NOT NULL OR predecessor_workload_id IS NOT NULL)) OR
    (kind = 'adopt' AND (base_transaction_id IS NOT NULL OR predecessor_workload_id IS NULL)) OR
    (kind = 'upgrade' AND (base_transaction_id IS NULL OR predecessor_workload_id IS NULL))) +
  (SELECT count(*) FROM applied_services WHERE
    typeof(service_id) != 'blob' OR length(service_id) != 32 OR
    typeof(transaction_id) != 'blob' OR length(transaction_id) != 16 OR
    typeof(workload_id) != 'text' OR length(CAST(workload_id AS BLOB)) NOT BETWEEN 1 AND 256 OR
    instr(workload_id, char(0)) != 0 OR
    typeof(configuration_digest) != 'blob' OR length(configuration_digest) != 32 OR
      configuration_digest = zeroblob(32) OR
    typeof(storage_digest) != 'blob' OR length(storage_digest) != 32 OR storage_digest = zeroblob(32) OR
    typeof(reference_digest) != 'blob' OR length(reference_digest) != 32 OR reference_digest = zeroblob(32) OR
    typeof(platform_manifest_digest) != 'blob' OR length(platform_manifest_digest) != 32 OR
      platform_manifest_digest = zeroblob(32) OR
    typeof(image_config_digest) != 'blob' OR length(image_config_digest) != 32 OR
      image_config_digest = zeroblob(32)) +
  (SELECT count(*) FROM workload_backups WHERE
    typeof(transaction_id) != 'blob' OR length(transaction_id) != 16 OR
    typeof(service_id) != 'blob' OR length(service_id) != 32 OR
    typeof(manifest_path) != 'text' OR length(CAST(manifest_path AS BLOB)) NOT BETWEEN 1 AND 128 OR
    instr(manifest_path, char(0)) != 0 OR
    typeof(manifest_digest) != 'blob' OR length(manifest_digest) != 32 OR
      manifest_digest = zeroblob(32) OR
    typeof(created_unix) != 'integer' OR created_unix <= 0) +
  (SELECT count(*) FROM journal_transactions AS child
    JOIN journal_transactions AS base ON base.transaction_id = child.base_transaction_id
    WHERE base.state != 'succeeded' OR base.service_id != child.service_id) +
  (SELECT count(*) FROM applied_services AS applied
    JOIN journal_transactions AS journal USING (transaction_id)
    WHERE journal.state != 'succeeded' OR journal.service_id != applied.service_id) +
  (SELECT count(*) FROM workload_backups AS backup
    JOIN journal_transactions AS journal USING (transaction_id)
    WHERE journal.kind != 'upgrade' OR journal.state != 'succeeded' OR journal.service_id != backup.service_id OR
      backup.manifest_path != lower(hex(backup.transaction_id)) || '/manifest.json') +
  (SELECT count(*) FROM (SELECT journal.service_id FROM journal_transactions AS journal
    LEFT JOIN applied_services AS applied USING (service_id)
    GROUP BY journal.service_id HAVING
      sum(journal.state = 'succeeded') > 0 AND count(applied.service_id) = 0)) +
  (SELECT count(*) FROM journal_transactions AS journal
    LEFT JOIN applied_services AS applied USING (service_id)
    WHERE journal.state IN ('active', 'degraded') AND
      ((journal.kind IN ('bootstrap', 'adopt') AND applied.service_id IS NOT NULL) OR
      (journal.kind = 'upgrade' AND (applied.service_id IS NULL OR
        applied.transaction_id != journal.base_transaction_id OR
        applied.workload_id != journal.predecessor_workload_id)))) +
  (SELECT count(*) FROM journal_transactions AS journal
    WHERE journal.state = 'succeeded' AND NOT EXISTS
      (SELECT 1 FROM applied_services AS applied
        WHERE applied.transaction_id = journal.transaction_id) AND NOT EXISTS
      (SELECT 1 FROM journal_transactions AS successor
        WHERE successor.base_transaction_id = journal.transaction_id
          AND successor.service_id = journal.service_id)) +
  (SELECT count(*) FROM journal_actions WHERE
    typeof(transaction_id) != 'blob' OR length(transaction_id) != 16 OR
    typeof(sequence) != 'integer' OR sequence <= 0 OR
    typeof(kind) != 'text' OR length(kind) NOT BETWEEN 1 AND 64 OR
    substr(kind, 1, 1) NOT GLOB '[a-z0-9]' OR kind GLOB '*[^a-z0-9._-]*' OR
    typeof(state) != 'text' OR state NOT IN ('intent', 'effect_outcome_unknown', 'completed') OR
    typeof(intent_digest) != 'blob' OR length(intent_digest) != 32 OR
    (state = 'completed' AND
      (typeof(postcondition_digest) != 'blob' OR length(postcondition_digest) != 32)) OR
    (state != 'completed' AND postcondition_digest IS NOT NULL)) +
  (SELECT count(*) FROM (SELECT transaction_id FROM journal_actions
    WHERE state != 'completed' GROUP BY transaction_id HAVING count(*) > 1)) +
  (SELECT count(*) FROM journal_actions AS action
    JOIN journal_transactions AS journal USING (transaction_id)
    WHERE action.state != 'completed' AND journal.state NOT IN ('active', 'degraded')) +
  (SELECT count(*) FROM pragma_foreign_key_check)`

func validateJournalRows(ctx context.Context, database rowQueryer) error {
	return validateJournalRowsWithQuery(ctx, database, journalRowValidationSQL)
}

func validateJournalRowsWithQuery(ctx context.Context, database rowQueryer, query string) error {
	var invalidRows int

	err := database.QueryRowContext(ctx, query).Scan(&invalidRows)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	if invalidRows != 0 {
		return ErrInvalidState
	}

	return nil
}

func classifySchemaResult(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	return classifySchema(ctx)
}

func classifySchema(ctx context.Context) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	return ErrInvalidState
}
