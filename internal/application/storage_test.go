package application

import (
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testDataName      = "data"
	testVolumeTarget  = "/data"
	testVolumeName    = "vol"
	testVolumeSource  = "/var/lib/docker/volumes/vol/_data"
	testBindSourceOld = "/repo/data-v1"
	testBindSourceNew = "/repo/data-v2"
)

func TestBackedStorageSourcesSelectsWritableVolumes(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	workload.Mounts = []domain.Mount{{
		Kind: domain.MountVolume, Target: testVolumeTarget,
	}}
	observation := WorkloadObservation{
		State: WorkloadObservationPresent,
		RuntimeMounts: []domain.RuntimeMount{{
			Kind: domain.MountVolume, Name: testVolumeName, Source: testVolumeSource, Target: testVolumeTarget,
		}},
	}

	sources := backedStorageSources(observation, workload)
	if len(sources) != 1 || sources[0].Mount.Target != testVolumeTarget {
		t.Fatalf("backedStorageSources() = %#v", sources)
	}
}

func TestBackedStorageSourcesIgnoresReadOnlyBindsAndSameSource(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	workload.Mounts = []domain.Mount{
		{Kind: domain.MountBind, Source: "/repo/config", Target: "/config", ReadOnly: true},
		{Kind: domain.MountBind, Source: "/data/same", Target: "/same"},
	}
	observation := WorkloadObservation{
		State: WorkloadObservationPresent,
		RuntimeMounts: []domain.RuntimeMount{
			{Kind: domain.MountBind, Source: "/repo/config", Target: "/config", ReadOnly: true},
			{Kind: domain.MountBind, Source: "/data/same", Target: "/same"},
		},
	}

	if sources := backedStorageSources(observation, workload); sources != nil {
		t.Fatalf("backedStorageSources() = %#v", sources)
	}
}

func TestBackedStorageSourcesCopiesWritableBindOnProvenanceChange(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	workload.Mounts = []domain.Mount{{
		Kind: domain.MountBind, Source: testBindSourceNew, Target: testVolumeTarget,
	}}
	observation := WorkloadObservation{
		State: WorkloadObservationPresent,
		RuntimeMounts: []domain.RuntimeMount{{
			Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget,
		}},
	}

	sources := backedStorageSources(observation, workload)
	if len(sources) != 1 || sources[0].Mount.Source != testBindSourceOld {
		t.Fatalf("backedStorageSources(bind provenance) = %#v", sources)
	}
}

func TestUpgradeCreateOptionsDisablesImageCopyForVolumes(t *testing.T) {
	t.Parallel()

	if !upgradeCreateOptions(nil).CopyImageVolumes {
		t.Fatal("empty sources disabled image volume copy")
	}

	options := upgradeCreateOptions([]backedStorageSource{{
		Mount: domain.RuntimeMount{Kind: domain.MountVolume, Name: testVolumeName, Target: testVolumeTarget},
	}})
	if options.CopyImageVolumes {
		t.Fatal("volume replacement still copies image content")
	}
}

func TestReplacementBindSourcesSelectsProvenanceChange(t *testing.T) {
	t.Parallel()

	workload := testWorkloadEffect(t)
	workload.Mounts = []domain.Mount{{
		Kind: domain.MountBind, Source: testBindSourceNew, Target: testVolumeTarget,
	}}
	sources := []backedStorageSource{{
		Mount: domain.RuntimeMount{
			Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget,
		},
	}}

	got := replacementBindSources(sources, workload)
	if len(got) != 1 || got[0].Mount.Source != testBindSourceOld {
		t.Fatalf("replacementBindSources() = %#v", got)
	}

	same := workload
	same.Mounts = []domain.Mount{{
		Kind: domain.MountBind, Source: testBindSourceOld, Target: testVolumeTarget,
	}}
	if replacementBindSources(sources, same) != nil {
		t.Fatal("same bind source selected a replacement")
	}
}

func TestBackupRootPathJoinsStateDirectory(t *testing.T) {
	t.Parallel()

	got := backupRootPath(filepath.Join("/var", "lib", "maniud", "state.db"))
	want := filepath.Join("/var", "lib", "maniud", "backups")
	if got != want {
		t.Fatalf("backupRootPath() = %q, want %q", got, want)
	}
}

func TestEnsureEmptyReplacementBindCreatesPrivateDirectory(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), "replacements", "txn", "data")
	if err := ensureEmptyReplacementBind(path); err != nil {
		t.Fatalf("ensureEmptyReplacementBind() error = %v", err)
	}
	if err := ensureEmptyReplacementBind(path); err != nil {
		t.Fatalf("ensureEmptyReplacementBind(reuse) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(path, "stale"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := ensureEmptyReplacementBind(path); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("ensureEmptyReplacementBind(occupied) = %v", err)
	}
}
