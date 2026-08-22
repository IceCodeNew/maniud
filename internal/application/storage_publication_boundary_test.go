package application

import (
	"context"
	crand "crypto/rand"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestStoragePublicationMatchingBoundaries(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, true)
	if err := matchingBackupPublication(fixture.manifest, fixture.publication); err != nil {
		t.Fatalf("matchingBackupPublication() error = %v", err)
	}
	different := fixture.publication
	different.Manifest.BaseTransactionID = backup.Identifier{1}
	if !errors.Is(matchingBackupPublication(fixture.manifest, different), ErrConflictingState) {
		t.Fatal("matchingBackupPublication() accepted another base transaction")
	}

	sources := []backedStorageSource{fixture.source}
	inventories := []backup.Inventory{fixture.inventory}
	if err := matchingBackupArtifacts(fixture.manifest, sources, inventories); err != nil {
		t.Fatalf("matchingBackupArtifacts() error = %v", err)
	}
	if !errors.Is(matchingBackupArtifacts(fixture.manifest, nil, nil), ErrConflictingState) {
		t.Fatal("matchingBackupArtifacts() accepted missing evidence")
	}
	inventories[0].SemanticDigest = domain.Hash([]byte("different content"))
	if !errors.Is(matchingBackupArtifacts(fixture.manifest, sources, inventories), ErrConflictingState) {
		t.Fatal("matchingBackupArtifacts() accepted different content")
	}
}

//nolint:cyclop // The cases share expensive publication fixtures and one cache boundary.
func TestOpenPublishedBackupBoundaries(t *testing.T) {
	t.Parallel()

	var execution *upgradeExecution
	if _, _, err := execution.openPublishedBackup(context.Background()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("openPublishedBackup(nil) error = %v", err)
	}

	missing := newStorageTestFixture(t, false)
	execution = &upgradeExecution{mutation: missing.mutation}
	publication, found, err := execution.openPublishedBackup(context.Background())
	if err != nil || found || publication.ManifestDigest != (domain.Digest{}) {
		t.Fatalf("openPublishedBackup(missing) = %#v, %t, %v", publication, found, err)
	}
	if _, err := execution.publishedBackup(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("publishedBackup(missing) error = %v", err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := execution.openPublishedBackup(cancelled); err == nil {
		t.Fatal("openPublishedBackup(cancelled) succeeded")
	}

	published := newStorageTestFixture(t, true)
	execution = &upgradeExecution{
		mutation:    published.mutation,
		sources:     []backedStorageSource{published.source},
		inventories: []backup.Inventory{published.inventory},
	}
	publication, err = execution.publishedBackup(context.Background())
	if err != nil || publication.ManifestDigest != published.publication.ManifestDigest {
		t.Fatalf("publishedBackup() = %#v, %v", publication, err)
	}
	publication, err = execution.publishedBackup(context.Background())
	if err != nil || publication.ManifestDigest != published.publication.ManifestDigest {
		t.Fatalf("publishedBackup(cached) = %#v, %v", publication, err)
	}

	execution.publication = backup.Publication{}
	execution.inventories[0].SemanticDigest = domain.Hash([]byte("drift"))
	if _, err = execution.publishedBackup(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("publishedBackup(drift) error = %v", err)
	}
}

func TestBackupManifestReusesOrCreatesExactPublication(t *testing.T) {
	t.Parallel()

	missing := newStorageTestFixture(t, false)
	execution := &upgradeExecution{
		mutation:    missing.mutation,
		sources:     []backedStorageSource{missing.source},
		inventories: []backup.Inventory{missing.inventory},
	}
	manifest, err := execution.backupManifest(context.Background())
	if err != nil || manifest.TransactionID != missing.manifest.TransactionID {
		t.Fatalf("backupManifest(new) = %#v, %v", manifest, err)
	}

	published := newStorageTestFixture(t, true)
	execution = &upgradeExecution{
		mutation:    published.mutation,
		sources:     []backedStorageSource{published.source},
		inventories: []backup.Inventory{published.inventory},
	}
	manifest, err = execution.backupManifest(context.Background())
	if err != nil || manifest.OperationToken != published.manifest.OperationToken ||
		execution.publication.ManifestDigest != published.publication.ManifestDigest {
		t.Fatalf("backupManifest(existing) = %#v, %v", manifest, err)
	}
	execution.inventories[0].EntryCount++
	if _, err = execution.backupManifest(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("backupManifest(drift) error = %v", err)
	}
}

//nolint:paralleltest // The test temporarily replaces the package-level crypto/rand reader.
func TestBackupManifestConstructionBoundaries(t *testing.T) {
	fixture := newStorageTestFixture(t, false)
	if _, err := newUpgradeBackupManifest(fixture.mutation.preparation, nil, nil); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("newUpgradeBackupManifest(empty) error = %v", err)
	}

	bind := fixture.source
	bind.Mount.Kind = domain.MountBind
	manifest, err := newUpgradeBackupManifest(
		fixture.mutation.preparation,
		[]backedStorageSource{bind},
		[]backup.Inventory{fixture.inventory},
	)
	if err != nil || manifest.Artifacts[0].ProvenanceDigest != fixture.mutation.preparation.Applied.SourceDigest {
		t.Fatalf("newUpgradeBackupManifest(bind) = %#v, %v", manifest, err)
	}
	if backupIndexIntent(backup.Publication{Manifest: manifest, ManifestDigest: domain.Hash([]byte("manifest"))}) == nil {
		t.Fatal("complete publication produced no backup index intent")
	}

	original := crand.Reader
	crand.Reader = failingRandomReader{}
	t.Cleanup(func() { crand.Reader = original })
	if _, err = newUpgradeBackupManifest(
		fixture.mutation.preparation,
		[]backedStorageSource{fixture.source},
		[]backup.Inventory{fixture.inventory},
	); err == nil {
		t.Fatal("newUpgradeBackupManifest() ignored random source failure")
	}
}
