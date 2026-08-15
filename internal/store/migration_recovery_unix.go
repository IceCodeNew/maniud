//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
)

func recoverDiscoveredSchemaMigration(
	ctx context.Context,
	database *sql.DB,
	pair *migrationBackupPair,
	migrations []schemaMigration,
) (resultErr error) {
	if database == nil || pair == nil || !pair.Valid() {
		return ErrInvalidState
	}

	defer func() {
		resultErr = errors.Join(resultErr, pair.Close())
	}()

	migration, valid := schemaMigrationForBackup(migrations, pair.plan.manifest)
	if !valid {
		return classifySchema(ctx)
	}

	version, err := storedSchemaVersion(ctx, database)
	if err != nil && errors.Is(err, ErrUnavailable) {
		return err
	}

	if !validRecoveryArtifact(ctx, pair) {
		return classifySchema(ctx)
	}

	if err != nil {
		return restoreAndRetrySchemaMigration(ctx, database, pair, migration)
	}

	return recoverSchemaMigrationVersion(ctx, database, pair, migration, version)
}

func recoverSchemaMigrationVersion(
	ctx context.Context,
	database *sql.DB,
	pair *migrationBackupPair,
	migration schemaMigration,
	version int,
) error {
	switch version {
	case migration.source:
		validationErr := migration.validateSource(ctx, database)
		if validationErr == nil {
			return retrySchemaMigration(ctx, database, pair, migration)
		}

		if errors.Is(validationErr, ErrInvalidState) {
			return restoreAndRetrySchemaMigration(ctx, database, pair, migration)
		}

		return validationErr
	case migration.target:
		validationErr := migration.validateTarget(ctx, database)
		if validationErr == nil {
			return pair.remove()
		}

		if errors.Is(validationErr, ErrInvalidState) {
			return restoreAndRetrySchemaMigration(ctx, database, pair, migration)
		}

		return validationErr
	default:
		return ErrInvalidState
	}
}

func restoreAndRetrySchemaMigration(
	ctx context.Context,
	database *sql.DB,
	pair *migrationBackupPair,
	migration schemaMigration,
) error {
	if !pair.Valid() {
		return ErrInvalidState
	}

	source := sqliteReadOnlyURI(platformDescriptorPath(int(pair.artifact.Fd())))

	err := restoreSQLite(ctx, database, source)
	if err != nil {
		return err
	}

	err = validateRestoredSchemaMigration(ctx, database, pair, migration)
	if err != nil {
		return err
	}

	return retrySchemaMigration(ctx, database, pair, migration)
}

func validateRestoredSchemaMigration(
	ctx context.Context,
	database *sql.DB,
	pair *migrationBackupPair,
	migration schemaMigration,
) error {
	if !pair.anchor.refreshSQLiteSidecars() || !pair.Valid() {
		return classifySchema(ctx)
	}

	return migration.validateSource(ctx, database)
}

func retrySchemaMigration(
	ctx context.Context,
	database *sql.DB,
	pair *migrationBackupPair,
	migration schemaMigration,
) error {
	err := applySchemaMigration(ctx, database, migration)
	if err != nil {
		return err
	}

	err = migration.validateTarget(ctx, database)
	if err != nil {
		return err
	}

	return pair.remove()
}

func validRecoveryArtifact(ctx context.Context, pair *migrationBackupPair) bool {
	return pair.Valid() && validMigrationSnapshot(
		ctx,
		platformDescriptorPath(int(pair.artifact.Fd())),
		pair.plan.manifest.SourceSchema,
	) && pair.Valid()
}

func schemaMigrationForBackup(
	migrations []schemaMigration,
	manifest migrationBackupManifest,
) (schemaMigration, bool) {
	var empty schemaMigration

	var match schemaMigration

	found := false

	for _, migration := range migrations {
		if !validSchemaMigrationDefinition(migration) {
			return empty, false
		}

		if migration.source != manifest.SourceSchema || migration.target != manifest.TargetSchema {
			continue
		}

		if found {
			return empty, false
		}

		match = migration
		found = true
	}

	return match, found
}
