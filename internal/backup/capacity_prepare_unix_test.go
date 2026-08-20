//go:build linux || darwin

package backup

import (
	"errors"
	"math"
	"os"
	"path/filepath"
	"testing"
)

func TestPreparePublicationCapacityBindsDestinationAndManifest(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	root := privatePublicationRoot(t)
	read := fixedCapacityReader(ampleFilesystemCapacity(), nil)

	missing, err := preparePublicationCapacity(root, manifest, read)
	if err != nil {
		t.Fatalf("preparePublicationCapacity(missing root) error = %v", err)
	}
	if missing.requiredInodes != uint64(len(manifest.Artifacts)+3) {
		t.Fatalf("missing-root required inodes = %d", missing.requiredInodes)
	}
	if missing.manifest == ([32]byte{}) || missing.filesystem != ampleFilesystemCapacity().identity {
		t.Fatalf("prepared capacity = %#v", missing)
	}

	if err = os.Mkdir(root, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(backup root): %v", err)
	}
	existing, err := preparePublicationCapacity(root, manifest, read)
	if err != nil {
		t.Fatalf("preparePublicationCapacity(existing root) error = %v", err)
	}
	if existing.requiredInodes != uint64(len(manifest.Artifacts)+2) {
		t.Fatalf("existing-root required inodes = %d", existing.requiredInodes)
	}
}

func TestPreparePublicationCapacityRejectsUnprovenDestination(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	ample := ampleFilesystemCapacity()
	root := privatePublicationRoot(t)

	if _, err := preparePublicationCapacity(
		"relative", manifest, fixedCapacityReader(ample, nil),
	); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("relative root error = %v", err)
	}
	if _, err := preparePublicationCapacity(root, manifest, nil); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("nil reader error = %v", err)
	}
	if _, err := preparePublicationCapacity(
		root, Manifest{}, fixedCapacityReader(ample, nil),
	); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("invalid manifest error = %v", err)
	}
	overflow := cloneManifest(manifest)
	maximumArchiveBytes := int64(math.MaxInt64 / tarBlockBytes * tarBlockBytes)
	for index := range overflow.Artifacts {
		overflow.Artifacts[index].Inventory.ArchiveBytes = maximumArchiveBytes
	}
	if _, err := preparePublicationCapacity(
		root, overflow, fixedCapacityReader(ample, nil),
	); !errors.Is(err, ErrInvalidManifest) {
		t.Fatalf("overflowing manifest error = %v", err)
	}

	missingParent := filepath.Join(t.TempDir(), "missing", "backups")
	if _, err := preparePublicationCapacity(
		missingParent, manifest, fixedCapacityReader(ample, nil),
	); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("missing parent error = %v", err)
	}

	if _, err := preparePublicationCapacity(
		root, manifest, fixedCapacityReader(filesystemCapacity{}, errCapacityProbeTest),
	); !errors.Is(err, errCapacityProbeTest) {
		t.Fatalf("capacity probe error = %v", err)
	}

	insufficient := ample
	insufficient.availableBytes = 0
	if _, err := preparePublicationCapacity(
		root, manifest, fixedCapacityReader(insufficient, nil),
	); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("insufficient capacity error = %v", err)
	}
}

func TestOpenCapacityTargetRejectsUnsafeRoot(t *testing.T) {
	t.Parallel()

	parent := t.TempDir()
	if err := os.Chmod(parent, privateDirectoryMode); err != nil {
		t.Fatalf("Chmod(parent): %v", err)
	}

	plain := filepath.Join(parent, "plain")
	if err := os.WriteFile(plain, []byte("not a directory"), privateFileMode); err != nil {
		t.Fatalf("WriteFile(root): %v", err)
	}

	public := filepath.Join(parent, "public")
	if err := os.Mkdir(public, 0o755); err != nil { //nolint:gosec // Unsafe mode is the fixture.
		t.Fatalf("Mkdir(public root): %v", err)
	}

	symlink := filepath.Join(parent, "symlink")
	if err := os.Symlink(public, symlink); err != nil {
		t.Fatalf("Symlink(root): %v", err)
	}

	for _, root := range []string{plain, public, symlink} {
		descriptor, _, err := openCapacityTarget(root)
		if !errors.Is(err, ErrInvalidBackupRoot) || descriptor != -1 {
			t.Fatalf("openCapacityTarget(%q) = %d, %v", root, descriptor, err)
		}
	}
}

func TestPublicationBytesRejectsInvalidBounds(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	want := uint64(publicationWorkspaceBytes + 123)
	manifest.Artifacts = manifest.Artifacts[:1]
	manifest.Artifacts[0].Inventory.ArchiveBytes = 100
	if got, valid := publicationBytes(manifest, 23, 1); !valid || got != want {
		t.Fatalf("publicationBytes(valid) = %d, %t", got, valid)
	}
	if got, valid := publicationBytes(manifest, 23, 4096); !valid || got != 1056768 {
		t.Fatalf("publicationBytes(allocated) = %d, %t", got, valid)
	}

	manifest.Artifacts[0].Inventory.ArchiveBytes = -1
	if _, valid := publicationBytes(manifest, 1, 1); valid {
		t.Fatal("negative archive size accepted")
	}

	manifest.Artifacts = []Artifact{
		{Inventory: Inventory{ArchiveBytes: math.MaxInt64}},
		{Inventory: Inventory{ArchiveBytes: math.MaxInt64}},
	}
	if _, valid := publicationBytes(manifest, 1, 1); valid {
		t.Fatal("archive size overflow accepted")
	}

	manifest.Artifacts = manifest.Artifacts[:1]
	if _, valid := publicationBytes(manifest, -1, 1); valid {
		t.Fatal("negative manifest size accepted")
	}
	if _, valid := publicationBytes(manifest, math.MaxInt64, 1); valid {
		t.Fatal("manifest size overflow accepted")
	}
	if _, valid := publicationBytes(manifest, 1, 0); valid {
		t.Fatal("zero fragment size accepted")
	}
}
