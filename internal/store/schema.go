package store

import (
	"context"
	"database/sql"
)

const (
	currentSchemaVersion = 2
	schemaTableName      = "schema_version"
	schemaTableSQL       = "CREATE TABLE schema_version (" +
		"singleton INTEGER PRIMARY KEY CHECK (singleton = 1), " +
		"version INTEGER NOT NULL CHECK (version > 0))"
	writerLeaseTableName = "writer_leases"
	writerLeaseTableSQL  = "CREATE TABLE writer_leases (" +
		"service_id BLOB PRIMARY KEY CHECK (typeof(service_id) = 'blob' AND length(service_id) = 32), " +
		"epoch INTEGER NOT NULL CHECK (epoch > 0), " +
		"owner BLOB CHECK (owner IS NULL OR (typeof(owner) = 'blob' AND length(owner) = 16))) WITHOUT ROWID"
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
	objectName            string
	schemaDefinition      string
	writerLeaseDefinition string
	schemaRows            int
	invalidSchemaRows     int
}

func (facts schemaFacts) valid(version int) bool {
	expectedObjects := 1
	if version == currentSchemaVersion {
		expectedObjects = 2
	}

	return facts.objectCount == expectedObjects && facts.objectName == schemaTableName &&
		facts.schemaDefinition == schemaTableSQL && facts.schemaRows == 1 &&
		facts.invalidSchemaRows == 0 &&
		(version != currentSchemaVersion || facts.writerLeaseDefinition == writerLeaseTableSQL)
}

func validateSchema(ctx context.Context, database *sql.DB, version int) error {
	if version != 1 && version != currentSchemaVersion {
		return ErrInvalidState
	}

	facts, err := readSchemaFacts(ctx, database, version)
	if err != nil {
		return err
	}

	if !facts.valid(version) {
		return ErrInvalidState
	}

	if version == currentSchemaVersion {
		return validateWriterLeaseRows(ctx, database)
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
			"(SELECT coalesce(min(name), '') FROM sqlite_schema "+
			" WHERE type IN ('table', 'index', 'view', 'trigger') AND name NOT LIKE 'sqlite_%'), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'schema_version'), ''), "+
			"coalesce((SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = 'writer_leases'), ''), "+
			"(SELECT count(*) FROM schema_version), "+
			"(SELECT count(*) FROM schema_version "+
			" WHERE singleton != 1 OR version != ? OR typeof(version) != 'integer')",
		version,
	).Scan(
		&facts.objectCount,
		&facts.objectName,
		&facts.schemaDefinition,
		&facts.writerLeaseDefinition,
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
