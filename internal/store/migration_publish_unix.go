//go:build linux || darwin

package store

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"

	"golang.org/x/sys/unix"
)

const (
	migrationManifestTempPrefix = ".maniud-migration-manifest-"
	migrationManifestTempSuffix = ".json.partial"
)

type migrationPublishOps struct {
	publishArtifact func(*migrationSnapshot, migrationBackupPlan) (bool, error)
	publishManifest func(*stateAnchor, migrationBackupPlan) (bool, error)
}

type migrationArtifactPublishOps struct {
	rename        func(int, string, string) error
	syncDirectory func(int) error
}

type migrationManifestPublishOps struct {
	random        io.Reader
	write         func(*migrationManifestTemp, []byte) bool
	rename        func(int, string, string) error
	syncDirectory func(int) error
}

type migrationManifestTemp struct {
	anchor   *stateAnchor
	file     *os.File
	name     string
	identity fileIdentity
}

func publishMigrationBackup(
	ctx context.Context,
	snapshot *migrationSnapshot,
) (migrationBackupManifest, error) {
	return publishMigrationBackupWithOps(ctx, snapshot, migrationPublishOps{
		publishArtifact: publishMigrationArtifact,
		publishManifest: publishMigrationManifest,
	})
}

func publishMigrationBackupWithOps(
	ctx context.Context,
	snapshot *migrationSnapshot,
	operations migrationPublishOps,
) (_ migrationBackupManifest, resultErr error) {
	if snapshot == nil || !snapshot.Valid() {
		return migrationBackupManifest{}, ErrInvalidState
	}

	removeSnapshot := true
	defer func() {
		if removeSnapshot {
			resultErr = errors.Join(resultErr, snapshot.Close())
		}
	}()

	if ctx.Err() != nil {
		return migrationBackupManifest{}, classifyContext(ctx)
	}

	plan, valid := planMigrationBackup(
		snapshot.anchor.databaseName,
		snapshot.sourceSchema,
		snapshot.targetSchema,
		snapshot.size,
		snapshot.digest,
	)
	if !valid {
		return migrationBackupManifest{}, ErrInvalidState
	}

	established, err := operations.publishArtifact(snapshot, plan)
	if err != nil {
		if established {
			removeSnapshot = false

			return migrationBackupManifest{}, errors.Join(err, releaseMigrationSnapshot(snapshot))
		}

		return migrationBackupManifest{}, err
	}

	removeSnapshot = false
	releaseErr := releaseMigrationSnapshot(snapshot)

	_, manifestErr := operations.publishManifest(snapshot.anchor, plan)
	if manifestErr != nil || releaseErr != nil {
		return migrationBackupManifest{}, errors.Join(manifestErr, releaseErr)
	}

	return plan.manifest, nil
}

func publishMigrationArtifact(
	snapshot *migrationSnapshot,
	plan migrationBackupPlan,
) (bool, error) {
	return publishMigrationArtifactWithOps(snapshot, plan, migrationArtifactPublishOps{
		rename:        renameNoReplace,
		syncDirectory: syncMigrationSnapshotDirectory,
	})
}

func publishMigrationArtifactWithOps(
	snapshot *migrationSnapshot,
	plan migrationBackupPlan,
	operations migrationArtifactPublishOps,
) (bool, error) {
	err := operations.rename(snapshot.anchor.directory, snapshot.name, plan.artifactName)
	if err == nil {
		snapshot.name = plan.artifactName

		if !snapshot.Valid() {
			return true, ErrInvalidState
		}

		syncErr := operations.syncDirectory(snapshot.anchor.directory)
		if syncErr != nil {
			return true, ErrUnavailable
		}

		return true, nil
	}

	if !errors.Is(err, unix.EEXIST) {
		return false, ErrUnavailable
	}

	if !existingMigrationArtifactMatches(snapshot.anchor, plan) {
		return false, ErrInvalidState
	}

	closeErr := snapshot.Close()
	if closeErr != nil {
		return true, closeErr
	}

	return true, nil
}

func releaseMigrationSnapshot(snapshot *migrationSnapshot) error {
	if snapshot == nil || snapshot.file == nil {
		return nil
	}

	err := snapshot.file.Close()
	snapshot.file = nil

	if err != nil {
		return ErrUnavailable
	}

	return nil
}

func existingMigrationArtifactMatches(anchor *stateAnchor, plan migrationBackupPlan) bool {
	file, identity, valid := openAnchoredPrivateFile(anchor, plan.artifactName)
	if !valid {
		return false
	}

	matches := migrationArtifactFileMatches(anchor, plan, file, identity)

	closeErr := file.Close()

	return matches && closeErr == nil
}

func migrationArtifactFileMatches(
	anchor *stateAnchor,
	plan migrationBackupPlan,
	file *os.File,
	identity fileIdentity,
) bool {
	metadata, err := file.Stat()
	if err != nil || metadata.Size() != plan.manifest.Size {
		return false
	}

	digester := sha256.New()
	reader := io.NewSectionReader(file, 0, metadata.Size())
	size, err := io.Copy(digester, reader)

	return err == nil && size == metadata.Size() &&
		hex.EncodeToString(digester.Sum(nil)) == plan.manifest.SHA256 &&
		anchoredPrivateFileMatches(anchor, plan.artifactName, file, identity)
}

func publishMigrationManifest(
	anchor *stateAnchor,
	plan migrationBackupPlan,
) (bool, error) {
	return publishMigrationManifestWithOps(anchor, plan, migrationManifestPublishOps{
		random:        rand.Reader,
		write:         (*migrationManifestTemp).write,
		rename:        renameNoReplace,
		syncDirectory: syncMigrationSnapshotDirectory,
	})
}

func publishMigrationManifestWithOps(
	anchor *stateAnchor,
	plan migrationBackupPlan,
	operations migrationManifestPublishOps,
) (bool, error) {
	temporary, err := openMigrationManifestTemp(anchor, operations.random)
	if err != nil {
		return false, err
	}

	if !operations.write(temporary, plan.content) {
		return false, errors.Join(ErrUnavailable, temporary.Close())
	}

	err = operations.rename(anchor.directory, temporary.name, plan.manifestName)
	if errors.Is(err, unix.EEXIST) {
		return acceptExistingMigrationManifest(temporary, anchor, plan)
	}

	if err != nil {
		return false, errors.Join(ErrUnavailable, temporary.Close())
	}

	temporary.name = plan.manifestName

	return true, finishPublishedMigrationManifest(temporary, operations.syncDirectory)
}

func acceptExistingMigrationManifest(
	temporary *migrationManifestTemp,
	anchor *stateAnchor,
	plan migrationBackupPlan,
) (bool, error) {
	if !existingMigrationManifestMatches(anchor, plan) || !existingMigrationArtifactMatches(anchor, plan) {
		return false, errors.Join(ErrInvalidState, temporary.Close())
	}

	return true, temporary.Close()
}

func finishPublishedMigrationManifest(
	temporary *migrationManifestTemp,
	syncDirectory func(int) error,
) error {
	if !temporary.Valid() {
		return errors.Join(ErrInvalidState, temporary.release())
	}

	syncErr := syncDirectory(temporary.anchor.directory)
	if syncErr != nil {
		return errors.Join(ErrUnavailable, temporary.release())
	}

	releaseErr := temporary.release()
	if releaseErr != nil {
		return releaseErr
	}

	return nil
}

func openMigrationManifestTemp(anchor *stateAnchor, random io.Reader) (*migrationManifestTemp, error) {
	if anchor == nil || !anchor.locked || !anchor.valid() {
		return nil, ErrInvalidState
	}

	nonce := make([]byte, migrationSnapshotNonceBytes)

	_, err := io.ReadFull(random, nonce)
	if err != nil {
		return nil, ErrUnavailable
	}

	name := migrationManifestTempPrefix + hex.EncodeToString(nonce) + migrationManifestTempSuffix

	descriptor, err := unix.Openat(
		anchor.directory,
		name,
		unix.O_WRONLY|unix.O_CREAT|unix.O_EXCL|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err != nil {
		return nil, ErrInvalidState
	}

	return adoptMigrationManifestTemp(anchor, name, descriptor)
}

func adoptMigrationManifestTemp(
	anchor *stateAnchor,
	name string,
	descriptor int,
) (*migrationManifestTemp, error) {
	identity, valid := descriptorIdentity(descriptor)

	temporary := &migrationManifestTemp{
		anchor:   anchor,
		file:     os.NewFile(uintptr(descriptor), name),
		name:     name,
		identity: identity,
	}
	if !valid || !privateRegular(identity) || !temporary.Valid() {
		_ = temporary.Close()

		return nil, ErrInvalidState
	}

	return temporary, nil
}

func (temporary *migrationManifestTemp) Valid() bool {
	if temporary == nil || temporary.anchor == nil || temporary.file == nil ||
		!temporary.anchor.locked || !temporary.anchor.valid() {
		return false
	}

	return anchoredPrivateFileMatches(
		temporary.anchor,
		temporary.name,
		temporary.file,
		temporary.identity,
	)
}

func (temporary *migrationManifestTemp) Close() error {
	return temporary.closeWith(unlinkMigrationSnapshot, syncMigrationSnapshotDirectory)
}

func (temporary *migrationManifestTemp) closeWith(
	unlink func(int, string) error,
	syncDirectory func(int) error,
) error {
	if temporary == nil || temporary.file == nil {
		return ErrUnavailable
	}

	if !temporary.Valid() {
		return errors.Join(ErrInvalidState, temporary.release())
	}

	unlinkErr := unlink(temporary.anchor.directory, temporary.name)

	fsyncErr := error(nil)
	if unlinkErr == nil {
		fsyncErr = syncDirectory(temporary.anchor.directory)
	}

	releaseErr := temporary.release()
	if unlinkErr != nil || fsyncErr != nil || releaseErr != nil {
		return errors.Join(ErrUnavailable, unlinkErr, fsyncErr, releaseErr)
	}

	return nil
}

func (temporary *migrationManifestTemp) write(content []byte) bool {
	if !temporary.Valid() {
		return false
	}

	written, err := io.Copy(temporary.file, bytes.NewReader(content))
	if err != nil || written != int64(len(content)) || temporary.file.Sync() != nil {
		return false
	}

	return temporary.Valid()
}

func (temporary *migrationManifestTemp) release() error {
	if temporary == nil || temporary.file == nil {
		return nil
	}

	err := temporary.file.Close()
	temporary.file = nil

	if err != nil {
		return fmt.Errorf("close migration manifest: %w", err)
	}

	return nil
}

func existingMigrationManifestMatches(anchor *stateAnchor, plan migrationBackupPlan) bool {
	file, identity, valid := openAnchoredPrivateFile(anchor, plan.manifestName)
	if !valid {
		return false
	}

	matches := migrationManifestFileMatches(anchor, plan, file, identity)
	closeErr := file.Close()

	return matches && closeErr == nil
}

func migrationManifestFileMatches(
	anchor *stateAnchor,
	plan migrationBackupPlan,
	file *os.File,
	identity fileIdentity,
) bool {
	reader := io.NewSectionReader(file, 0, migrationManifestMaxBytes+1)

	content, err := io.ReadAll(reader)
	if err != nil || !bytes.Equal(content, plan.content) ||
		!anchoredPrivateFileMatches(anchor, plan.manifestName, file, identity) {
		return false
	}

	manifest, valid := parseMigrationBackupManifest(bytes.NewReader(content), anchor.databaseName)

	return valid && manifest == plan.manifest
}

func openAnchoredPrivateFile(anchor *stateAnchor, name string) (*os.File, fileIdentity, bool) {
	if anchor == nil || !anchor.locked || !anchor.valid() {
		return nil, fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	descriptor, err := unix.Openat(anchor.directory, name, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW, 0)
	if err != nil {
		return nil, fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	file := os.NewFile(uintptr(descriptor), name)

	identity, valid := descriptorIdentity(descriptor)
	if !valid || !privateRegular(identity) || !anchor.validEntry(name, identity) {
		_ = file.Close()

		return nil, fileIdentity{device: 0, inode: 0, mode: 0, owner: 0, links: 0}, false
	}

	return file, identity, true
}

func anchoredPrivateFileMatches(
	anchor *stateAnchor,
	name string,
	file *os.File,
	identity fileIdentity,
) bool {
	current, valid := descriptorIdentity(int(file.Fd()))

	return valid && privateRegular(current) && current == identity && anchor.validEntry(name, identity)
}
