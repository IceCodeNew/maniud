package containerd

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"testing"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	api "github.com/containerd/containerd/api/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

//nolint:cyclop // The test exercises stat, get, and put against one proven runtime volume.
func TestNativeArchiveUsesRuntimeVolumeEvidence(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	desired.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: testDataMount}}
	desired.EffectiveDigest = domain.ComputeEffectiveDigest(desired)
	options := testHostWorkloadOptions(t)
	identifier := workloadIdentifier(desired.ContainerName, testWorkloadTransaction)
	name := workloadVolumeName(identifier, testDataMount)
	source := filepath.Join(options.StateRoot, "volumes", name)
	if err := os.MkdirAll(source, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	file := filepath.Join(source, testFileName)
	if err := os.WriteFile(file, []byte("value"), privateFileMode); err != nil {
		t.Fatal(err)
	}
	runtimeMounts := []domain.RuntimeMount{{
		Kind: domain.MountVolume, Name: name, Source: source, Target: testDataMount,
	}}
	fixture := testNativeManagedBackendFor(t, desired, options, runtimeMounts)

	stat, err := fixture.backend.ArchiveStat(
		context.Background(), fixture.container.GetID(), testDataMount+"/file",
	)
	if err != nil || stat.Name != testFileName || stat.Size != 5 {
		t.Fatalf("ArchiveStat() = %#v, %v", stat, err)
	}
	var archive bytes.Buffer
	stat, err = fixture.backend.ArchiveGet(
		context.Background(), fixture.container.GetID(), testDataMount+"/file", &archive, 1<<20,
	)
	if err != nil || stat.Name != testFileName || archive.Len() == 0 {
		t.Fatalf("ArchiveGet() = %#v, %v, bytes %d", stat, err, archive.Len())
	}

	newSource := filepath.Join(t.TempDir(), "new")
	if err = os.WriteFile(newSource, []byte("new"), privateFileMode); err != nil {
		t.Fatal(err)
	}
	archive.Reset()
	if err = writePathArchive(context.Background(), newSource, &archive, 1<<20); err != nil {
		t.Fatal(err)
	}
	if err = fixture.backend.ArchivePut(
		context.Background(), fixture.container.GetID(), testDataMount, bytes.NewReader(archive.Bytes()),
	); err != nil {
		t.Fatalf("ArchivePut() = %v", err)
	}
	//nolint:gosec // The path is derived entirely from this test's temporary directory.
	if value, readErr := os.ReadFile(filepath.Join(source, "new")); readErr != nil || string(value) != "new" {
		t.Fatalf("ArchivePut() content = %q, %v", value, readErr)
	}
}

func TestNativeArchiveUsesSnapshotRootfs(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	rootfs := t.TempDir()
	fixture.host.rootfs = rootfs
	fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
		return &snapshotsapi.MountsResponse{Mounts: []*api.Mount{{
			Type: bindMountType, Source: rootfs,
		}}}, nil
	}
	directory := filepath.Join(rootfs, "data")
	if err := os.Mkdir(directory, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(directory, testFileName), []byte(testChangedValue), privateFileMode,
	); err != nil {
		t.Fatal(err)
	}
	identifier := fixture.container.GetID()
	stat, err := fixture.backend.ArchiveStat(context.Background(), identifier, testDataMount+"/file")
	if err != nil || stat.Name != testFileName {
		t.Fatalf("ArchiveStat(rootfs) = %#v, %v", stat, err)
	}
	var output bytes.Buffer
	stat, err = fixture.backend.ArchiveGet(
		context.Background(), identifier, testDataMount+"/file", &output, 1<<20,
	)
	if err != nil || stat.Name != testFileName || output.Len() == 0 {
		t.Fatalf("ArchiveGet(rootfs) = %#v, %v, bytes %d", stat, err, output.Len())
	}
	if err = fixture.backend.ArchivePut(
		context.Background(), identifier, testDataMount, bytes.NewReader(output.Bytes()),
	); err != nil {
		t.Fatalf("ArchivePut(rootfs) = %v", err)
	}
}

func TestNativeArchiveRejectsInvalidOrMissingWorkload(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	if _, err := fixture.backend.ArchiveStat(
		context.Background(), fixture.container.GetID(), testRelativePath,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ArchiveStat(relative) = %v", err)
	}
	fixture.containers.get = func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}
	if _, err := fixture.backend.ArchiveStat(
		context.Background(), fixture.container.GetID(), testDataMount,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ArchiveStat(missing workload) = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if _, err := archiveStatPath(missing); !errors.Is(err, application.ErrArchivePathMissing) {
		t.Fatalf("archiveStatPath(missing) = %v", err)
	}
}

//nolint:cyclop,funlen,gocognit // Each case rejects one independent archive routing or filesystem boundary.
func TestNativeArchiveFailureMatrix(t *testing.T) {
	t.Parallel()

	t.Run("runtime mount operations", func(t *testing.T) {
		t.Parallel()
		desired := testContainerdDesiredWorkload(t)
		desired.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: testDataMount}}
		desired.EffectiveDigest = domain.ComputeEffectiveDigest(desired)
		options := testHostWorkloadOptions(t)
		identifier := workloadIdentifier(desired.ContainerName, testWorkloadTransaction)
		name := workloadVolumeName(identifier, testDataMount)
		source := filepath.Join(options.StateRoot, "volumes", name)
		if err := os.MkdirAll(source, privateDirectoryMode); err != nil {
			t.Fatal(err)
		}
		fixture := testNativeManagedBackendFor(t, desired, options, []domain.RuntimeMount{{
			Kind: domain.MountVolume, Name: name, Source: source, Target: testDataMount,
		}})
		missingPath := testDataMount + "/missing"
		if _, err := fixture.backend.ArchiveGet(
			context.Background(), identifier, missingPath, &bytes.Buffer{}, 1<<20,
		); !errors.Is(err, application.ErrArchivePathMissing) {
			t.Fatalf("ArchiveGet(missing) = %v", err)
		}
		if err := fixture.backend.ArchivePut(
			context.Background(), identifier, missingPath, bytes.NewReader(nil),
		); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("ArchivePut(missing) = %v", err)
		}
		file := filepath.Join(source, testFileName)
		if err := os.WriteFile(file, nil, privateFileMode); err != nil {
			t.Fatal(err)
		}
		if err := fixture.backend.ArchivePut(
			context.Background(), identifier, testDataMount+"/"+testFileName, bytes.NewReader(nil),
		); !errors.Is(err, ErrUnsupportedWorkload) {
			t.Fatalf("ArchivePut(file) = %v", err)
		}
		if err := os.RemoveAll(source); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.backend.ArchiveStat(
			context.Background(), identifier, testDataMount+"/child",
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ArchiveStat(stale mount) = %v", err)
		}
	})

	t.Run("snapshot operations", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		identifier := fixture.container.GetID()
		fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
			return nil, errContainerdTest
		}
		if _, err := fixture.backend.ArchiveStat(
			context.Background(), identifier, testDataMount,
		); err == nil {
			t.Fatal("ArchiveStat(snapshot error) succeeded")
		}
		fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
			return &snapshotsapi.MountsResponse{Mounts: []*api.Mount{nil}}, nil
		}
		if _, err := fixture.backend.ArchiveStat(
			context.Background(), identifier, testDataMount,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ArchiveStat(snapshot protocol) = %v", err)
		}
		fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
			return &snapshotsapi.MountsResponse{}, nil
		}
		fixture.host.rootfsErr = errContainerdTest
		if _, err := fixture.backend.ArchiveStat(
			context.Background(), identifier, testDataMount,
		); err == nil {
			t.Fatal("ArchiveStat(rootfs error) succeeded")
		}
		fixture.host.rootfsErr = nil
		fixture.host.rootfs = t.TempDir()
		if err := os.Symlink("loop", filepath.Join(fixture.host.rootfs, "loop")); err != nil {
			t.Fatal(err)
		}
		if _, err := fixture.backend.ArchiveStat(
			context.Background(), identifier, "/loop/file",
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ArchiveStat(unsafe rootfs path) = %v", err)
		}
	})

	t.Run("runtime mount disappears after workload proof", func(t *testing.T) {
		t.Parallel()
		desired := testContainerdDesiredWorkload(t)
		desired.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: testDataMount}}
		desired.EffectiveDigest = domain.ComputeEffectiveDigest(desired)
		options := testHostWorkloadOptions(t)
		identifier := workloadIdentifier(desired.ContainerName, testWorkloadTransaction)
		name := workloadVolumeName(identifier, testDataMount)
		source := filepath.Join(options.StateRoot, "volumes", name)
		if err := os.MkdirAll(source, privateDirectoryMode); err != nil {
			t.Fatal(err)
		}
		fixture := testNativeManagedBackendFor(t, desired, options, []domain.RuntimeMount{{
			Kind: domain.MountVolume, Name: name, Source: source, Target: testDataMount,
		}})
		fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			if err := os.RemoveAll(source); err != nil {
				return nil, fmt.Errorf("remove runtime mount: %w", err)
			}

			return nil, status.Error(codes.NotFound, "missing")
		}
		if _, err := fixture.backend.ArchiveStat(
			context.Background(), identifier, testDataMount+"/child",
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("ArchiveStat(disappeared runtime mount) = %v", err)
		}
	})

	root := t.TempDir()
	if _, found, err := runtimeArchivePath(
		[]domain.RuntimeMount{{Source: root, Target: testDataMount}}, testDataMount+"/bad\x00path",
	); !found || !errors.Is(err, ErrProtocol) {
		t.Fatalf("runtimeArchivePath(unsafe) = %v, %v", found, err)
	}
}
