package store

import (
	"context"
	"database/sql"
)

const (
	currentSchemaVersion = 1
	schemaTableName      = "schema_version"
	schemaTableSQL       = "CREATE TABLE schema_version (" +
		"singleton INTEGER PRIMARY KEY CHECK (singleton = 1), " +
		"version INTEGER NOT NULL CHECK (version > 0))"
	initialSchemaSQL = schemaTableSQL + "; " +
		"INSERT INTO schema_version (singleton, version) VALUES (1, 1)"
)

func ensureSchema(ctx context.Context, database *sql.DB) error {
	objectCount, objectName, err := schemaObjectSummary(ctx, database)
	if err != nil {
		return classifySchema(ctx)
	}

	if objectCount == 0 {
		return initializeSchema(ctx, database)
	}

	if objectCount != 1 || objectName != schemaTableName {
		return ErrInvalidState
	}

	var (
		definition string
		rows       int
		version    int
	)

	err = database.QueryRowContext(
		ctx,
		"SELECT sql FROM sqlite_schema WHERE type = 'table' AND name = ?",
		schemaTableName,
	).Scan(&definition)
	if err != nil || definition != schemaTableSQL {
		return classifySchema(ctx)
	}

	err = database.QueryRowContext(
		ctx,
		"SELECT count(*), coalesce(max(version), 0) FROM schema_version WHERE singleton = 1",
	).Scan(&rows, &version)
	if err != nil || rows != 1 || version != currentSchemaVersion {
		return classifySchema(ctx)
	}

	return nil
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
