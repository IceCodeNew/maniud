//go:build linux || darwin

package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"errors"
	"fmt"
	"io"
	"os"

	modernsqlite "modernc.org/sqlite"
)

const (
	migrationSnapshotPrefix     = ".maniud-migration-"
	migrationSnapshotSuffix     = ".sqlite.partial"
	migrationSnapshotNonceBytes = 16
)

type migrationSnapshot struct {
	anchor       *stateAnchor
	file         *os.File
	name         string
	identity     fileIdentity
	sourceSchema int
	targetSchema int
	size         int64
	digest       [sha256.Size]byte
}

type migrationBackuper func(context.Context, *sql.DB, string) error

type migrationSnapshotOps struct {
	random   io.Reader
	backup   migrationBackuper
	validate func(context.Context, string, int) bool
	sync     func(*os.File) error
}

func createMigrationSnapshot(
	ctx context.Context,
	database *sql.DB,
	anchor *stateAnchor,
	sourceSchema int,
	targetSchema int,
) (*migrationSnapshot, error) {
	return createMigrationSnapshotWithOps(
		ctx,
		database,
		anchor,
		sourceSchema,
		targetSchema,
		standardMigrationSnapshotOps(),
	)
}

func createMigrationSnapshotWithBackup(
	ctx context.Context,
	database *sql.DB,
	anchor *stateAnchor,
	sourceSchema int,
	targetSchema int,
	backup migrationBackuper,
) (*migrationSnapshot, error) {
	operations := standardMigrationSnapshotOps()
	operations.backup = backup

	return createMigrationSnapshotWithOps(ctx, database, anchor, sourceSchema, targetSchema, operations)
}

func standardMigrationSnapshotOps() migrationSnapshotOps {
	return migrationSnapshotOps{
		random:   rand.Reader,
		backup:   backupSQLite,
		validate: validMigrationSnapshot,
		sync:     syncMigrationSnapshot,
	}
}

func syncMigrationSnapshot(file *os.File) error {
	err := file.Sync()
	if err != nil {
		return fmt.Errorf("sync migration snapshot: %w", err)
	}

	return nil
}

func createMigrationSnapshotWithOps(
	ctx context.Context,
	database *sql.DB,
	anchor *stateAnchor,
	sourceSchema int,
	targetSchema int,
	operations migrationSnapshotOps,
) (_ *migrationSnapshot, resultErr error) {
	if ctx.Err() != nil {
		return nil, classifyContext(ctx)
	}

	if database == nil || anchor == nil || !validMigrationSnapshotRequest(anchor, sourceSchema, targetSchema) {
		return nil, ErrInvalidState
	}

	snapshot, err := openMigrationSnapshot(anchor, operations.random, sourceSchema, targetSchema)
	if err != nil {
		return nil, err
	}

	keep := false
	defer func() {
		if !keep {
			resultErr = errors.Join(resultErr, snapshot.Close())
		}
	}()

	err = operations.backup(ctx, database, platformEntryPath(anchor, snapshot.name))
	if err != nil {
		return nil, err
	}

	err = finishMigrationSnapshot(ctx, snapshot, operations)
	if err != nil {
		return nil, err
	}

	keep = true

	return snapshot, nil
}

func validMigrationSnapshotRequest(
	anchor *stateAnchor,
	sourceSchema int,
	targetSchema int,
) bool {
	return anchor.locked && sourceSchema > 0 &&
		targetSchema > sourceSchema && targetSchema-sourceSchema == 1 && anchor.valid()
}

func finishMigrationSnapshot(
	ctx context.Context,
	snapshot *migrationSnapshot,
	operations migrationSnapshotOps,
) error {
	if !snapshot.Valid() ||
		!operations.validate(ctx, platformEntryPath(snapshot.anchor, snapshot.name), snapshot.sourceSchema) {
		return classifyMigrationSnapshot(ctx)
	}

	err := operations.sync(snapshot.file)
	if err != nil {
		return ErrUnavailable
	}

	size, digest, valid := snapshot.measure()
	if !valid {
		return ErrInvalidState
	}

	snapshot.size = size
	snapshot.digest = digest

	return nil
}

func validMigrationSnapshot(ctx context.Context, path string, sourceSchema int) bool {
	connector, _ := modernsqlite.NewConnector(sqliteReadOnlyURI(path))
	database := sql.OpenDB(connector)
	database.SetMaxOpenConns(1)
	database.SetMaxIdleConns(1)

	valid := validMigrationSnapshotDatabase(ctx, database, sourceSchema)
	closeErr := database.Close()

	return valid && closeErr == nil
}

func validMigrationSnapshotDatabase(ctx context.Context, database *sql.DB, sourceSchema int) bool {
	var (
		integrityOK   bool
		foreignKeysOK bool
		schemaOK      bool
	)

	err := database.QueryRowContext(
		ctx,
		"SELECT "+
			"(SELECT count(*) = 1 AND min(integrity_check) = 'ok' FROM pragma_integrity_check), "+
			"(SELECT count(*) = 0 FROM pragma_foreign_key_check), "+
			"(SELECT count(*) = 1 FROM schema_version WHERE singleton = 1 AND version = ?)",
		sourceSchema,
	).Scan(&integrityOK, &foreignKeysOK, &schemaOK)

	return err == nil && integrityOK && foreignKeysOK && schemaOK
}

func classifyMigrationSnapshot(ctx context.Context) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	return ErrInvalidState
}
