//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
)

type schemaMigration struct {
	source   int
	target   int
	apply    func(context.Context, *sql.Tx) error
	validate func(context.Context, *sql.DB) bool
}

type schemaMigrationOps struct {
	createSnapshot func(context.Context, *sql.DB, *stateAnchor, int, int) (*migrationSnapshot, error)
	publishBackup  func(context.Context, *migrationSnapshot) (migrationBackupManifest, error)
}

func executeSchemaMigration(
	ctx context.Context,
	database *sql.DB,
	anchor *stateAnchor,
	migration schemaMigration,
) error {
	return executeSchemaMigrationWithOps(ctx, database, anchor, migration, standardSchemaMigrationOps())
}

func standardSchemaMigrationOps() schemaMigrationOps {
	return schemaMigrationOps{
		createSnapshot: createMigrationSnapshot,
		publishBackup:  publishMigrationBackup,
	}
}

func executeSchemaMigrationWithOps(
	ctx context.Context,
	database *sql.DB,
	anchor *stateAnchor,
	migration schemaMigration,
	operations schemaMigrationOps,
) error {
	if !validSchemaMigration(database, anchor, migration) || !validSchemaMigrationOps(operations) {
		return ErrInvalidState
	}

	version, err := storedSchemaVersion(ctx, database)
	if err != nil || version != migration.source {
		return classifySchema(ctx)
	}

	snapshot, err := operations.createSnapshot(ctx, database, anchor, migration.source, migration.target)
	if err != nil {
		return err
	}

	manifest, err := operations.publishBackup(ctx, snapshot)
	if err != nil {
		return err
	}

	err = applySchemaMigration(ctx, database, migration)
	if err != nil {
		return err
	}

	if !migration.validate(ctx, database) {
		return classifySchema(ctx)
	}

	return removeMigrationBackup(ctx, anchor, manifest)
}

func validSchemaMigration(
	database *sql.DB,
	anchor *stateAnchor,
	migration schemaMigration,
) bool {
	return database != nil && anchor != nil && anchor.locked && anchor.valid() && migration.source > 0 &&
		migration.target > migration.source && migration.target-migration.source == 1 &&
		migration.apply != nil && migration.validate != nil
}

func validSchemaMigrationOps(operations schemaMigrationOps) bool {
	return operations.createSnapshot != nil && operations.publishBackup != nil
}

func storedSchemaVersion(ctx context.Context, database *sql.DB) (int, error) {
	var version int

	err := database.QueryRowContext(
		ctx,
		"SELECT version FROM schema_version WHERE singleton = 1",
	).Scan(&version)
	if err != nil {
		return 0, classifySchema(ctx)
	}

	return version, nil
}

func applySchemaMigration(
	ctx context.Context,
	database *sql.DB,
	migration schemaMigration,
) error {
	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return classifySchema(ctx)
	}

	err = migration.apply(ctx, transaction)
	if err != nil {
		_ = transaction.Rollback()

		return classifySchema(ctx)
	}

	result, err := transaction.ExecContext(
		ctx,
		"UPDATE schema_version SET version = ? WHERE singleton = 1 AND version = ?",
		migration.target,
		migration.source,
	)
	if err != nil {
		_ = transaction.Rollback()

		return classifySchema(ctx)
	}

	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		_ = transaction.Rollback()

		return classifySchema(ctx)
	}

	err = transaction.Commit()

	return classifySchemaResult(ctx, err)
}
