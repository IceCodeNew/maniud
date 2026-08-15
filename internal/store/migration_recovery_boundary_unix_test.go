//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestRecoverDiscoveredSchemaMigrationRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	_, _, pair := testRecoveryPair(t)

	err := recoverDiscoveredSchemaMigration(context.Background(), nil, pair, nil)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("recoverDiscoveredSchemaMigration(nil database) = %v", err)
	}

	requireNoError(t, pair.Close())

	_, database, pair := testRecoveryPair(t)

	err = recoverDiscoveredSchemaMigration(context.Background(), database, pair, nil)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("recoverDiscoveredSchemaMigration(missing migration) = %v", err)
	}

	_, database, pair = testRecoveryPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = recoverDiscoveredSchemaMigration(ctx, database, pair, []schemaMigration{testSchemaMigration()})
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("recoverDiscoveredSchemaMigration(cancelled) = %v", err)
	}
}

func TestRecoverSchemaMigrationVersionRejectsUnexpectedVersion(t *testing.T) {
	t.Parallel()

	_, database, pair := testRecoveryPair(t)

	err := recoverSchemaMigrationVersion(
		context.Background(),
		database,
		pair,
		testSchemaMigration(),
		99,
	)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("recoverSchemaMigrationVersion() = %v", err)
	}

	requireNoError(t, pair.Close())
}

func TestRecoverSchemaMigrationVersionPreservesUnavailableValidation(t *testing.T) {
	t.Parallel()

	_, database, pair := testRecoveryPair(t)
	migration := testSchemaMigration()
	migration.validateSource = func(context.Context, *sql.DB) error { return ErrUnavailable }

	err := recoverSchemaMigrationVersion(
		context.Background(),
		database,
		pair,
		migration,
		migration.source,
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("recoverSchemaMigrationVersion(source unavailable) = %v", err)
	}

	requireNoError(t, pair.Close())

	_, database, pair = testRecoveryPair(t)
	migration = testSchemaMigration()
	requireNoError(t, applySchemaMigration(context.Background(), database, migration))

	migration.validateTarget = func(context.Context, *sql.DB) error { return ErrUnavailable }

	err = recoverSchemaMigrationVersion(
		context.Background(),
		database,
		pair,
		migration,
		migration.target,
	)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("recoverSchemaMigrationVersion(target unavailable) = %v", err)
	}

	version, versionErr := storedSchemaVersion(context.Background(), database)
	if versionErr != nil || version != migration.target {
		t.Fatalf("schema version after unavailable target validation = %d, %v", version, versionErr)
	}

	requireNoError(t, pair.Close())
}

func TestRecoverDiscoveredSchemaMigrationRejectsWrongArtifactSchema(t *testing.T) {
	t.Parallel()

	anchor, database := testMigrationDatabase(t)
	snapshot := newMigrationSnapshot(t, database, anchor)

	plan, valid := planMigrationBackup(anchor.databaseName, 2, 3, snapshot.size, snapshot.digest)
	if !valid {
		t.Fatal("planMigrationBackup() rejected recovery fixture")
	}

	established, err := publishMigrationArtifact(snapshot, plan)
	if err != nil || !established {
		t.Fatalf("publishMigrationArtifact() = %t, %v", established, err)
	}

	requireNoError(t, releaseMigrationSnapshot(snapshot))

	established, err = publishMigrationManifest(anchor, plan)
	if err != nil || !established {
		t.Fatalf("publishMigrationManifest() = %t, %v", established, err)
	}

	pair, found, err := discoverMigrationBackup(anchor)
	if err != nil || !found {
		t.Fatalf("discoverMigrationBackup() = %#v, %t, %v", pair, found, err)
	}

	migration := testSchemaMigration()
	migration.source = 2
	migration.target = 3

	err = recoverDiscoveredSchemaMigration(
		context.Background(),
		database,
		pair,
		[]schemaMigration{migration},
	)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("recoverDiscoveredSchemaMigration(wrong artifact schema) = %v", err)
	}

	assertMigrationBackupExists(t, anchor.directoryPath)
}

func TestRestoreAndRetrySchemaMigrationContainsFailures(t *testing.T) {
	t.Parallel()

	_, database, pair := testRecoveryPair(t)
	requireNoError(t, pair.Close())

	err := restoreAndRetrySchemaMigration(
		context.Background(),
		database,
		pair,
		testSchemaMigration(),
	)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("restoreAndRetrySchemaMigration(closed pair) = %v", err)
	}

	_, database, pair = testRecoveryPair(t)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err = restoreAndRetrySchemaMigration(ctx, database, pair, testSchemaMigration())
	if !errors.Is(err, context.Canceled) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("restoreAndRetrySchemaMigration(cancelled) = %v", err)
	}

	requireNoError(t, pair.Close())

	_, database, pair = testRecoveryPair(t)
	migration := testSchemaMigration()
	migration.validateSource = func(context.Context, *sql.DB) error { return ErrInvalidState }

	err = restoreAndRetrySchemaMigration(context.Background(), database, pair, migration)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("restoreAndRetrySchemaMigration(invalid source) = %v", err)
	}

	requireNoError(t, pair.Close())
}

func TestValidateRestoredSchemaMigrationRejectsChangedAnchor(t *testing.T) {
	t.Parallel()

	anchor, database, pair := testRecoveryPair(t)
	databaseID := anchor.databaseID
	anchor.databaseID = fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}

	err := validateRestoredSchemaMigration(
		context.Background(),
		database,
		pair,
		testSchemaMigration(),
	)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("validateRestoredSchemaMigration(changed anchor) = %v", err)
	}

	anchor.databaseID = databaseID

	requireNoError(t, pair.Close())
}

func TestRetrySchemaMigrationContainsFailures(t *testing.T) {
	t.Parallel()

	_, database, pair := testRecoveryPair(t)
	migration := testSchemaMigration()
	migration.apply = func(context.Context, *sql.Tx) error { return ErrUnavailable }

	err := retrySchemaMigration(context.Background(), database, pair, migration)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("retrySchemaMigration(apply failure) = %v", err)
	}

	requireNoError(t, pair.Close())

	_, database, pair = testRecoveryPair(t)
	migration = testSchemaMigration()
	migration.validateTarget = func(context.Context, *sql.DB) error { return ErrInvalidState }

	err = retrySchemaMigration(context.Background(), database, pair, migration)
	if !errors.Is(err, ErrInvalidState) {
		t.Fatalf("retrySchemaMigration(invalid target) = %v", err)
	}

	requireNoError(t, pair.Close())
}

func TestSchemaMigrationForBackupRejectsInvalidRegistry(t *testing.T) {
	t.Parallel()

	_, _, pair := testRecoveryPair(t)
	manifest := pair.plan.manifest
	requireNoError(t, pair.Close())

	var invalid schemaMigration
	if _, valid := schemaMigrationForBackup([]schemaMigration{invalid}, manifest); valid {
		t.Fatal("schemaMigrationForBackup() accepted an invalid migration")
	}

	unrelated := testSchemaMigration()
	unrelated.source++
	unrelated.target++

	if _, valid := schemaMigrationForBackup([]schemaMigration{unrelated}, manifest); valid {
		t.Fatal("schemaMigrationForBackup() accepted an unrelated migration")
	}

	migration := testSchemaMigration()
	if _, valid := schemaMigrationForBackup([]schemaMigration{migration, migration}, manifest); valid {
		t.Fatal("schemaMigrationForBackup() accepted duplicate migrations")
	}
}

func TestRefreshSQLiteSidecarsRejectsChangedEntries(t *testing.T) {
	t.Parallel()

	anchor, database := testAnchoredDatabase(t)
	t.Cleanup(func() {
		requireNoError(t, database.Close())
		requireNoError(t, anchor.close())
	})

	databaseID := anchor.databaseID
	anchor.databaseID = fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}

	if anchor.refreshSQLiteSidecars() {
		t.Fatal("refreshSQLiteSidecars() accepted a changed database")
	}

	anchor.databaseID = databaseID

	walPath := filepath.Join(anchor.directoryPath, anchor.databaseName+"-wal")
	requireNoError(t, os.Remove(walPath))
	requireNoError(t, os.Mkdir(walPath, 0o700))

	if anchor.refreshSQLiteSidecars() {
		t.Fatal("refreshSQLiteSidecars() accepted a directory sidecar")
	}

	requireNoError(t, os.Remove(walPath))
	requireNoError(t, os.WriteFile(walPath, nil, privateFileMode))

	walID, valid := anchor.openRegular(anchor.databaseName + "-wal")
	if !valid {
		t.Fatal("failed to restore WAL fixture")
	}

	anchor.walID = walID
}
