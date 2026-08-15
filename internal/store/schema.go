package store

import (
	"context"
	"database/sql"
)

const (
	writerLeaseSchemaVersion = 2
	currentSchemaVersion     = 3
	writerLeaseObjectCount   = 2
	journalObjectCount       = 5
	schemaTableName          = "schema_version"
	schemaTableSQL           = "CREATE TABLE schema_version (" +
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
		"state TEXT NOT NULL CHECK (state IN ('active', 'degraded', 'failed', 'succeeded')), " +
		"runtime TEXT NOT NULL CHECK (runtime IN ('docker', 'podman')), " +
		"source_digest BLOB NOT NULL CHECK (typeof(source_digest) = 'blob' AND length(source_digest) = 32), " +
		"effective_digest BLOB NOT NULL CHECK (typeof(effective_digest) = 'blob' AND length(effective_digest) = 32), " +
		"execution_digest BLOB NOT NULL CHECK (typeof(execution_digest) = 'blob' AND length(execution_digest) = 32), " +
		"FOREIGN KEY (service_id) REFERENCES writer_leases(service_id)) WITHOUT ROWID"
	journalUnresolvedIndexName = "journal_one_unresolved_transaction_per_service"
	journalUnresolvedIndexSQL  = "CREATE UNIQUE INDEX journal_one_unresolved_transaction_per_service " +
		"ON journal_transactions(service_id) WHERE state IN ('active', 'degraded')"
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
	initialSchemaSQL = schemaTableSQL + "; " +
		"INSERT INTO schema_version (singleton, version) VALUES (1, 1)"
)

func ensureInitialSchema(ctx context.Context, database *sql.DB) error {
	objectCount, _, err := schemaObjectSummary(ctx, database)
	if err != nil {
		return classifySchema(ctx)
	}

	if objectCount == 0 {
		return initializeSchema(ctx, database)
	}

	return validateSchema(ctx, database, 1)
}

func schemaObjectSummary(ctx context.Context, database *sql.DB) (int, string, error) {
	var (
		count int
		name  string
	)

	err := database.QueryRowContext(
		ctx,
		"SELECT count(*), coalesce(min(name), '') FROM sqlite_schema "+
			"WHERE type IN ('table', 'index', 'view', 'trigger') AND name NOT LIKE 'sqlite_%'",
	).Scan(&count, &name)
	if err != nil {
		return 0, "", classifySchema(ctx)
	}

	return count, name, nil
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
	objectCount           int
	schemaDefinition      string
	writerLeaseDefinition string
	transactionDefinition string
	unresolvedDefinition  string
	actionDefinition      string
	schemaRows            int
	invalidSchemaRows     int
}

func (facts schemaFacts) valid(version int) bool {
	expectedObjects := map[int]int{
		1:                        1,
		writerLeaseSchemaVersion: writerLeaseObjectCount,
		currentSchemaVersion:     journalObjectCount,
	}

	return facts.objectCount == expectedObjects[version] &&
		facts.schemaDefinition == schemaTableSQL && facts.schemaRows == 1 &&
		facts.invalidSchemaRows == 0 &&
		(version < writerLeaseSchemaVersion || facts.writerLeaseDefinition == writerLeaseTableSQL) &&
		(version != currentSchemaVersion ||
			(facts.transactionDefinition == journalTransactionTableSQL &&
				facts.unresolvedDefinition == journalUnresolvedIndexSQL &&
				facts.actionDefinition == journalActionTableSQL))
}

func validateSchema(ctx context.Context, database *sql.DB, version int) error {
	if version != 1 && version != writerLeaseSchemaVersion && version != currentSchemaVersion {
		return ErrInvalidState
	}

	facts, err := readSchemaFacts(ctx, database, version)
	if err != nil {
		return err
	}

	if !facts.valid(version) {
		return ErrInvalidState
	}

	if version >= writerLeaseSchemaVersion {
		err = validateWriterLeaseRows(ctx, database)
		if err != nil {
			return err
		}
	}

	if version == currentSchemaVersion {
		return validateJournalRows(ctx, database)
	}

	return nil
}

func readSchemaFacts(
	ctx context.Context,
	database *sql.DB,
	version int,
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
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'journal_actions'), ''), "+
			"(SELECT count(*) FROM schema_version), "+
			"(SELECT count(*) FROM schema_version "+
			" WHERE singleton != 1 OR version != ? OR typeof(version) != 'integer')",
		version,
	).Scan(
		&facts.objectCount,
		&facts.schemaDefinition,
		&facts.writerLeaseDefinition,
		&facts.transactionDefinition,
		&facts.unresolvedDefinition,
		&facts.actionDefinition,
		&facts.schemaRows,
		&facts.invalidSchemaRows,
	)
	if err != nil {
		return schemaFacts{}, classifySQLiteProbe(ctx, err)
	}

	return facts, nil
}

func validateWriterLeaseRows(ctx context.Context, database *sql.DB) error {
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

func validateJournalRows(ctx context.Context, database *sql.DB) error {
	var invalidRows int

	err := database.QueryRowContext(
		ctx,
		"SELECT "+
			"(SELECT count(*) FROM journal_transactions WHERE "+
			" typeof(transaction_id) != 'blob' OR length(transaction_id) != 16 OR "+
			" typeof(service_id) != 'blob' OR length(service_id) != 32 OR "+
			" typeof(state) != 'text' OR state NOT IN ('active', 'degraded', 'failed', 'succeeded') OR "+
			" typeof(runtime) != 'text' OR runtime NOT IN ('docker', 'podman') OR "+
			" typeof(source_digest) != 'blob' OR length(source_digest) != 32 OR "+
			" typeof(effective_digest) != 'blob' OR length(effective_digest) != 32 OR "+
			" typeof(execution_digest) != 'blob' OR length(execution_digest) != 32) + "+
			"(SELECT count(*) FROM journal_actions WHERE "+
			" typeof(transaction_id) != 'blob' OR length(transaction_id) != 16 OR "+
			" typeof(sequence) != 'integer' OR sequence <= 0 OR "+
			" typeof(kind) != 'text' OR length(kind) NOT BETWEEN 1 AND 64 OR "+
			" substr(kind, 1, 1) NOT GLOB '[a-z0-9]' OR kind GLOB '*[^a-z0-9._-]*' OR "+
			" typeof(state) != 'text' OR state NOT IN ('intent', 'effect_outcome_unknown', 'completed') OR "+
			" typeof(intent_digest) != 'blob' OR length(intent_digest) != 32 OR "+
			" (state = 'completed' AND (typeof(postcondition_digest) != 'blob' OR "+
			"  length(postcondition_digest) != 32)) OR "+
			" (state != 'completed' AND postcondition_digest IS NOT NULL)) + "+
			"(SELECT count(*) FROM (SELECT transaction_id FROM journal_actions "+
			" WHERE state != 'completed' GROUP BY transaction_id HAVING count(*) > 1)) + "+
			"(SELECT count(*) FROM journal_actions AS action "+
			" JOIN journal_transactions AS journal USING (transaction_id) "+
			" WHERE action.state != 'completed' AND journal.state NOT IN ('active', 'degraded')) + "+
			"(SELECT count(*) FROM pragma_foreign_key_check)",
	).Scan(&invalidRows)
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
