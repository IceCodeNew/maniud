//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"fmt"
)

func currentSchemaMigrations() []schemaMigration {
	return []schemaMigration{
		{
			source:         1,
			target:         currentSchemaVersion,
			apply:          addWriterLeaseTable,
			validateSource: validateSchemaVersion(1),
			validateTarget: validateSchemaVersion(currentSchemaVersion),
		},
	}
}

func addWriterLeaseTable(ctx context.Context, transaction *sql.Tx) error {
	_, err := transaction.ExecContext(ctx, writerLeaseTableSQL)
	if err != nil {
		return fmt.Errorf("create writer lease table: %w", err)
	}

	return nil
}

func validateSchemaVersion(version int) func(context.Context, *sql.DB) error {
	return func(ctx context.Context, database *sql.DB) error {
		return validateSchema(ctx, database, version)
	}
}

func reconcileSchema(
	ctx context.Context,
	database *sql.DB,
	anchor *stateAnchor,
	migrations []schemaMigration,
) error {
	objectCount, _, err := schemaObjectSummary(ctx, database)
	if err != nil {
		return err
	}

	if objectCount == 0 {
		err = initializeSchema(ctx, database)
		if err != nil {
			return err
		}
	}

	version, err := storedSchemaVersion(ctx, database)
	if err != nil {
		return err
	}

	for version < currentSchemaVersion {
		migration, found := schemaMigrationFrom(migrations, version)
		if !found {
			return ErrInvalidState
		}

		err = executeSchemaMigration(ctx, database, anchor, migration)
		if err != nil {
			return err
		}

		version = migration.target
	}

	if version != currentSchemaVersion {
		return ErrInvalidState
	}

	return validateSchema(ctx, database, currentSchemaVersion)
}

func schemaMigrationFrom(migrations []schemaMigration, source int) (schemaMigration, bool) {
	var match schemaMigration

	found := false

	for _, migration := range migrations {
		if !validSchemaMigrationDefinition(migration) {
			return match, false
		}

		if migration.source != source {
			continue
		}

		if found {
			return match, false
		}

		match = migration
		found = true
	}

	return match, found
}
