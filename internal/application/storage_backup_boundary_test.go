package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestStorageBackupRejectsInvalidRequestsAndActions(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	_, _, err := settleStorageBackup(
		context.Background(), nil, store.Action{}, 1, fixture.mutation.backupRoot,
		fixture.manifest, [][]byte{fixture.archive}, backup.PublicationCapacity{},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("settleStorageBackup(nil mutation) error = %v", err)
	}
	_, _, err = settleStorageBackup(
		context.Background(), fixture.mutation, store.Action{}, 1, fixture.mutation.backupRoot,
		fixture.manifest, [][]byte{fixture.archive, fixture.archive}, backup.PublicationCapacity{},
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("settleStorageBackup(mismatched archives) error = %v", err)
	}

	intent := storageBackupIntent(1, fixture.manifest)
	conflicting := storageCompletedAction(fixture.mutation, intent, domain.Hash([]byte("postcondition")))
	conflicting.Kind = testOtherValue
	_, _, err = settleStorageBackup(
		context.Background(), fixture.mutation, conflicting, 1, fixture.mutation.backupRoot,
		fixture.manifest, [][]byte{fixture.archive}, backup.PublicationCapacity{},
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleStorageBackup(conflicting action) error = %v", err)
	}
}

//nolint:cyclop // The cases share publication fixtures and exercise one effect boundary.
func TestStorageBackupEffectPublicationBoundaries(t *testing.T) {
	t.Parallel()

	missing := newStorageTestFixture(t, false)
	effect := &storageBackupEffect{
		root: missing.mutation.backupRoot, manifest: missing.manifest, archives: [][]byte{missing.archive},
	}
	postcondition, err := effect.Probe(context.Background())
	if err != nil || postcondition.Satisfied {
		t.Fatalf("storageBackupEffect.Probe(missing) = %#v, %v", postcondition, err)
	}

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if err = effect.Apply(cancelled); err == nil {
		t.Fatal("storageBackupEffect.Apply(cancelled) succeeded")
	}
	if _, err = effect.Probe(cancelled); err == nil {
		t.Fatal("storageBackupEffect.Probe(cancelled) succeeded")
	}

	invalid := *effect
	invalid.root = "relative"
	if err = invalid.Apply(context.Background()); err == nil {
		t.Fatal("storageBackupEffect.Apply(invalid root) succeeded")
	}

	published := newStorageTestFixture(t, true)
	effect = &storageBackupEffect{
		root: published.mutation.backupRoot, manifest: published.manifest, archives: [][]byte{published.archive},
	}
	if err = effect.Apply(context.Background()); err != nil ||
		effect.publication.ManifestDigest != published.publication.ManifestDigest {
		t.Fatalf("storageBackupEffect.Apply(existing) = %#v, %v", effect.publication, err)
	}
	postcondition, err = effect.Probe(context.Background())
	if err != nil || !postcondition.Satisfied {
		t.Fatalf("storageBackupEffect.Probe(existing) = %#v, %v", postcondition, err)
	}

	conflicting := *effect
	conflicting.manifest.BaseTransactionID = backup.Identifier{1}
	if err = conflicting.Apply(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("storageBackupEffect.Apply(conflicting) error = %v", err)
	}
	if _, err = conflicting.Probe(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("storageBackupEffect.Probe(conflicting) error = %v", err)
	}
}

func TestCompletedStorageBackupRequiresExactEvidence(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, true)
	newEffect := func() *storageBackupEffect {
		return &storageBackupEffect{
			root: fixture.mutation.backupRoot, manifest: fixture.manifest, archives: [][]byte{fixture.archive},
		}
	}
	if _, err := completedStorageBackup(
		context.Background(),
		store.Action{},
		newEffect(),
	); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completedStorageBackup(missing digest) error = %v", err)
	}

	intent := storageBackupIntent(1, fixture.manifest)
	action := storageCompletedAction(fixture.mutation, intent, domain.Hash([]byte("wrong backup")))
	if _, err := completedStorageBackup(context.Background(), action, newEffect()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completedStorageBackup(wrong digest) error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := completedStorageBackup(cancelled, action, newEffect()); err == nil {
		t.Fatal("completedStorageBackup(cancelled) succeeded")
	}
	postcondition, err := newEffect().Probe(context.Background())
	if err != nil {
		t.Fatalf("storageBackupEffect.Probe() error = %v", err)
	}
	action = storageCompletedAction(fixture.mutation, intent, postcondition.Digest)
	postcondition, err = completedStorageBackup(context.Background(), action, newEffect())
	if err != nil || !postcondition.Satisfied {
		t.Fatalf("completedStorageBackup() = %#v, %v", postcondition, err)
	}
}
