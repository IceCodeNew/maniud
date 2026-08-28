package containerd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"strings"

	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	continuityfs "github.com/containerd/continuity/fs"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func (backend *nativeWorkloadBackendV1) ArchiveStat(
	ctx context.Context,
	identifier string,
	archivePath string,
) (application.ArchivePathStat, error) {
	var result application.ArchivePathStat
	err := backend.withArchivePath(ctx, identifier, archivePath, func(hostPath string) error {
		var err error
		result, err = archiveStatPath(hostPath)

		return err
	})

	return result, err
}

func (backend *nativeWorkloadBackendV1) ArchiveGet(
	ctx context.Context,
	identifier string,
	archivePath string,
	destination io.Writer,
	maximumBytes int64,
) (application.ArchivePathStat, error) {
	var result application.ArchivePathStat
	err := backend.withArchivePath(ctx, identifier, archivePath, func(hostPath string) error {
		var err error
		result, err = archiveStatPath(hostPath)
		if err != nil {
			return err
		}

		return writePathArchive(ctx, hostPath, destination, maximumBytes)
	})

	return result, err
}

func (backend *nativeWorkloadBackendV1) ArchivePut(
	ctx context.Context,
	identifier string,
	archivePath string,
	source io.Reader,
) error {
	return backend.withArchivePath(ctx, identifier, archivePath, func(hostPath string) error {
		info, err := os.Lstat(hostPath)
		if err != nil {
			return ErrUnavailable
		}
		if !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsupportedWorkload
		}

		return applyPathArchive(ctx, hostPath, source)
	})
}

//nolint:cyclop // Archive routing validates runtime mounts and snapshot paths before access.
func (backend *nativeWorkloadBackendV1) withArchivePath(
	ctx context.Context,
	identifier string,
	archivePath string,
	operation func(string) error,
) error {
	if operation == nil || !validArchivePath(archivePath) {
		return ErrProtocol
	}
	workload, err := backend.Workload(ctx, identifier)
	if err != nil || workload == nil || !workload.ConfigurationMatches {
		return ErrProtocol
	}
	if hostPath, found, resolveErr := runtimeArchivePath(workload.RuntimeMounts, archivePath); found {
		if resolveErr != nil {
			return resolveErr
		}

		return operation(hostPath)
	}
	response, err := backend.snapshots.Mounts(ctx, &snapshotsapi.MountsRequest{
		Snapshotter: backend.options.Snapshotter, Key: workloadSnapshotKey(identifier),
	})
	if err != nil {
		return classifyRPCError(err)
	}
	mounts, err := apiMounts(response.GetMounts())
	if err != nil {
		return err
	}
	err = backend.host.WithRootfs(ctx, mounts, func(root string) error {
		hostPath, pathErr := continuityfs.RootPath(root, archivePath)
		if pathErr != nil {
			return ErrProtocol
		}

		return operation(hostPath)
	})
	if err != nil {
		return workloadError(err)
	}

	return nil
}

//nolint:cyclop // Longest-target resolution rejects unsafe source and relative path shapes.
func runtimeArchivePath(
	mounts []domain.RuntimeMount,
	archivePath string,
) (string, bool, error) {
	var selected *domain.RuntimeMount
	for index := range mounts {
		candidate := &mounts[index]
		if archivePath != candidate.Target &&
			!strings.HasPrefix(archivePath, candidate.Target+"/") {
			continue
		}
		if selected == nil || len(candidate.Target) > len(selected.Target) {
			selected = candidate
		}
	}
	if selected == nil {
		return "", false, nil
	}
	if archivePath == selected.Target {
		return selected.Source, true, nil
	}
	info, err := os.Lstat(selected.Source)
	if err != nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return "", true, ErrProtocol
	}
	relative := strings.TrimPrefix(archivePath, selected.Target)
	hostPath, err := continuityfs.RootPath(selected.Source, filepath.ToSlash(relative))
	if err != nil {
		return "", true, ErrProtocol
	}

	return hostPath, true, nil
}
