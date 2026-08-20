package application

import (
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestStorageSelectionBoundaryMatrix(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	if backedStorageSources(WorkloadObservation{}, workload) != nil {
		t.Fatal("absent workload exposed storage sources")
	}
	if isBackedStorageSource(domain.RuntimeMount{Kind: domain.MountKind(99), Source: "x", Target: "/x"}, workload) {
		t.Fatal("tmpfs selected for persistent backup")
	}
	if writableBindNeedsCopy(domain.RuntimeMount{Target: "/missing"}, workload) {
		t.Fatal("missing desired bind selected for backup")
	}

	workload.Mounts = []domain.Mount{
		{Kind: domain.MountVolume, Target: "/skip"},
		{Kind: domain.MountBind, Source: "/new", Target: "/readonly", ReadOnly: true},
		{Kind: domain.MountBind, Source: "/new", Target: "/data"},
	}
	sources := []backedStorageSource{
		{Mount: domain.RuntimeMount{Kind: domain.MountVolume, Target: "/volume"}},
		{Mount: domain.RuntimeMount{Kind: domain.MountBind, Source: "/old", Target: "/missing"}},
		{Mount: domain.RuntimeMount{Kind: domain.MountBind, Source: "/old", Target: "/data"}},
	}
	replacements := replacementBindIndexes(sources, workload)
	if len(replacements) != 1 || replacements[0] != 2 {
		t.Fatalf("replacementBindIndexes() = %#v", replacements)
	}
}

func TestStorageDigestBoundaryMatrix(t *testing.T) {
	t.Parallel()

	readonly := appendRuntimeMount(nil, domain.RuntimeMount{ReadOnly: true})
	writable := appendRuntimeMount(nil, domain.RuntimeMount{})
	if len(readonly) == 0 || readonly[len(readonly)-1] != 1 || writable[len(writable)-1] != 0 {
		t.Fatalf("mount digest flags = %v, %v", readonly, writable)
	}
	if got := appendUnsignedCount(nil, -1); len(got) != 1 || got[0] != 0 {
		t.Fatalf("appendUnsignedCount(-1) = %v", got)
	}

	manifest := backup.Manifest{Artifacts: []backup.Artifact{{}}}
	if storageBackupPostcondition(manifest, backup.Publication{}, false).Satisfied ||
		storageInventoryPostcondition("selector", []backedStorageSource{{}}, nil, false).Satisfied ||
		storageRestorePostcondition("selector", manifest, false).Satisfied {
		t.Fatal("missing storage evidence was marked satisfied")
	}
	if backupIndexIntent(backup.Publication{}) != nil {
		t.Fatal("empty backup publication produced index intent")
	}
}

type failingRandomReader struct{}

func (failingRandomReader) Read([]byte) (int, error) {
	return 0, errors.New("random source failed")
}
