//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
)

type migrationBackupPair struct {
	anchor           *stateAnchor
	plan             migrationBackupPlan
	artifact         *os.File
	artifactIdentity fileIdentity
	manifest         *os.File
	manifestIdentity fileIdentity
}

type migrationBackupCleanupOps struct {
	unlink        func(int, string) error
	syncDirectory func(int) error
}

func removeMigrationBackup(
	ctx context.Context,
	anchor *stateAnchor,
	manifest migrationBackupManifest,
) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	pair, err := openMigrationBackupPair(anchor, manifest)
	if err != nil {
		return err
	}

	return pair.remove()
}

func openMigrationBackupPair(
	anchor *stateAnchor,
	manifest migrationBackupManifest,
) (*migrationBackupPair, error) {
	if anchor == nil || !anchor.locked || !anchor.valid() {
		return nil, ErrInvalidState
	}

	plan, valid := planExistingMigrationBackup(anchor.databaseName, manifest)
	if !valid {
		return nil, ErrInvalidState
	}

	artifact, artifactIdentity, valid := openAnchoredPrivateFile(anchor, plan.artifactName)
	if !valid {
		return nil, ErrInvalidState
	}

	manifestFile, manifestIdentity, valid := openAnchoredPrivateFile(anchor, plan.manifestName)
	if !valid {
		_ = artifact.Close()

		return nil, ErrInvalidState
	}

	pair := &migrationBackupPair{
		anchor:           anchor,
		plan:             plan,
		artifact:         artifact,
		artifactIdentity: artifactIdentity,
		manifest:         manifestFile,
		manifestIdentity: manifestIdentity,
	}
	if !pair.Valid() {
		_ = pair.Close()

		return nil, ErrInvalidState
	}

	return pair, nil
}

func (pair *migrationBackupPair) Valid() bool {
	return pair != nil && pair.anchor != nil && pair.artifact != nil && pair.manifest != nil &&
		pair.anchor.locked && pair.anchor.valid() &&
		migrationArtifactFileMatches(pair.anchor, pair.plan, pair.artifact, pair.artifactIdentity) &&
		migrationManifestFileMatches(pair.anchor, pair.plan, pair.manifest, pair.manifestIdentity)
}

func (pair *migrationBackupPair) Close() error {
	if pair == nil {
		return nil
	}

	artifactErr := error(nil)
	if pair.artifact != nil {
		artifactErr = pair.artifact.Close()
		pair.artifact = nil
	}

	manifestErr := error(nil)
	if pair.manifest != nil {
		manifestErr = pair.manifest.Close()
		pair.manifest = nil
	}

	if artifactErr != nil || manifestErr != nil {
		return errors.Join(ErrUnavailable, artifactErr, manifestErr)
	}

	return nil
}

func (pair *migrationBackupPair) remove() error {
	return pair.removeWithOps(migrationBackupCleanupOps{
		unlink:        unlinkMigrationSnapshot,
		syncDirectory: syncMigrationSnapshotDirectory,
	})
}

func (pair *migrationBackupPair) removeWithOps(operations migrationBackupCleanupOps) error {
	if !pair.Valid() {
		return errors.Join(ErrInvalidState, pair.Close())
	}

	err := operations.unlink(pair.anchor.directory, pair.plan.manifestName)
	if err != nil {
		return errors.Join(ErrUnavailable, err, pair.Close())
	}

	err = operations.syncDirectory(pair.anchor.directory)
	if err != nil {
		return errors.Join(ErrUnavailable, err, pair.Close())
	}

	if !migrationArtifactFileMatches(pair.anchor, pair.plan, pair.artifact, pair.artifactIdentity) {
		return errors.Join(ErrInvalidState, pair.Close())
	}

	err = operations.unlink(pair.anchor.directory, pair.plan.artifactName)
	if err != nil {
		return errors.Join(ErrUnavailable, err, pair.Close())
	}

	syncErr := operations.syncDirectory(pair.anchor.directory)

	closeErr := pair.Close()
	if syncErr != nil || closeErr != nil {
		return errors.Join(ErrUnavailable, syncErr, closeErr)
	}

	return nil
}
