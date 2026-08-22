package application

import (
	"bytes"
	"context"
	"fmt"
	"io"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
)

type storageArchiveRuntime struct {
	archive []byte
	getErr  error
	putErr  error
}

func (*storageArchiveRuntime) ProbeWorkloadArchivePath(
	context.Context,
	domain.DesiredWorkload,
	string,
	string,
) (ArchivePathStat, error) {
	return ArchivePathStat{}, nil
}

func (runtime *storageArchiveRuntime) GetWorkloadArchive(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
	_ string,
	destination io.Writer,
	_ int64,
) (ArchivePathStat, error) {
	if runtime.getErr != nil {
		return ArchivePathStat{}, runtime.getErr
	}
	if _, err := destination.Write(runtime.archive); err != nil {
		return ArchivePathStat{}, fmt.Errorf("write test archive: %w", err)
	}

	return ArchivePathStat{Size: int64(len(runtime.archive))}, nil
}

func (runtime *storageArchiveRuntime) PutWorkloadArchive(
	_ context.Context,
	_ domain.DesiredWorkload,
	_ string,
	_ string,
	source io.Reader,
) error {
	if runtime.putErr != nil {
		return runtime.putErr
	}
	raw, err := io.ReadAll(source)
	if err != nil {
		return fmt.Errorf("read test archive: %w", err)
	}
	runtime.archive = raw

	return nil
}

type storageTestFixture struct {
	mutation       *boundMutation
	runtime        *storageArchiveRuntime
	upgradeRuntime *upgradeRuntimeFixture
	source         backedStorageSource
	archive        []byte
	inventory      backup.Inventory
	manifest       backup.Manifest
	publication    backup.Publication
}

func newStorageTestFixture(t *testing.T, publish bool) storageTestFixture {
	t.Helper()

	state, mutation, upgradeRuntime := newUpgradeMutation(t)
	t.Cleanup(func() { closeBootstrapMutation(t, state, mutation) })

	archive := upgradeTestArchive(t, "storage-boundary")
	inventory, err := backup.Analyze(context.Background(), bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("Analyze(storage fixture) error = %v", err)
	}
	source := backedStorageSource{Mount: domain.RuntimeMount{
		Kind: domain.MountVolume, Name: testVolumeName, Source: testVolumeSource, Target: testVolumeTarget,
	}}
	manifest, err := newUpgradeBackupManifest(
		mutation.preparation,
		[]backedStorageSource{source},
		[]backup.Inventory{inventory},
	)
	if err != nil {
		t.Fatalf("newUpgradeBackupManifest() error = %v", err)
	}

	fixture := storageTestFixture{
		mutation:       mutation,
		runtime:        &storageArchiveRuntime{archive: bytes.Clone(archive)},
		upgradeRuntime: upgradeRuntime,
		source:         source,
		archive:        archive,
		inventory:      inventory,
		manifest:       manifest,
	}
	if publish {
		capacity, capacityErr := backup.PreparePublicationCapacity(mutation.backupRoot, manifest)
		if capacityErr != nil {
			t.Fatalf("PreparePublicationCapacity() error = %v", capacityErr)
		}
		fixture.publication, err = backup.Publish(
			context.Background(),
			mutation.backupRoot,
			manifest,
			archiveInputs(manifest, [][]byte{archive}),
			capacity,
		)
		if err != nil {
			t.Fatalf("Publish(storage fixture) error = %v", err)
		}
	}

	return fixture
}
