package containerd

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/archive"
	continuityfs "github.com/containerd/continuity/fs"
	containerdlog "github.com/containerd/log"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func prepareRuntimeMounts(
	options WorkloadOptions,
	identifier string,
	mounts []domain.Mount,
) ([]domain.RuntimeMount, []domain.RuntimeMount, error) {
	if len(mounts) == 0 {
		return nil, nil, nil
	}
	result := make([]domain.RuntimeMount, len(mounts))
	created := make([]domain.RuntimeMount, 0, len(mounts))
	complete := false
	defer func() {
		if complete {
			return
		}
		discardRuntimeMountSources(created)
	}()
	for index, selected := range mounts {
		switch selected.Kind {
		case domain.MountBind:
			if !validHostPersistentSource(selected.Source) {
				return nil, nil, ErrUnsupportedWorkload
			}
			result[index] = domain.RuntimeMount{
				Kind: selected.Kind, Name: "", Source: selected.Source,
				Target: selected.Target, ReadOnly: selected.ReadOnly,
			}
		case domain.MountVolume:
			name := workloadVolumeName(identifier, selected.Target)
			source := filepath.Join(options.StateRoot, "volumes", name)
			wasCreated, err := secureNewDirectory(source)
			if err != nil || !wasCreated {
				return nil, nil, ErrProtocol
			}
			result[index] = domain.RuntimeMount{
				Kind: selected.Kind, Name: name, Source: source,
				Target: selected.Target, ReadOnly: false,
			}
			created = append(created, result[index])
		default:
			return nil, nil, ErrUnsupportedWorkload
		}
	}
	complete = true

	return result, created, nil
}

func discardRuntimeMountSources(mounts []domain.RuntimeMount) {
	for _, selected := range mounts {
		_ = os.RemoveAll(selected.Source)
	}
}

func validRuntimeMountEvidence(
	options WorkloadOptions,
	identifier string,
	mounts []domain.RuntimeMount,
) bool {
	return !slices.ContainsFunc(mounts, func(selected domain.RuntimeMount) bool {
		switch selected.Kind {
		case domain.MountBind:
			return selected.Name != "" || !validHostPersistentSource(selected.Source)
		case domain.MountVolume:
			expectedName := workloadVolumeName(identifier, selected.Target)

			return selected.Name != expectedName || selected.ReadOnly ||
				selected.Source != filepath.Join(options.StateRoot, "volumes", expectedName) ||
				!validHostPersistentSource(selected.Source)
		default:
			return true
		}
	})
}

//nolint:cyclop // Mount comparison rejects every unsupported or mismatched source property.
func runtimeMountsMatchConfiguration(
	options WorkloadOptions,
	identifier string,
	desired []domain.Mount,
	observed []domain.RuntimeMount,
) bool {
	if len(desired) == 0 && observed != nil {
		return false
	}
	if len(desired) != len(observed) {
		return false
	}
	byTarget := make(map[string]domain.RuntimeMount, len(observed))
	for _, selected := range observed {
		if _, duplicate := byTarget[selected.Target]; duplicate {
			return false
		}
		byTarget[selected.Target] = selected
	}
	for _, selected := range desired {
		actual, found := byTarget[selected.Target]
		if !found || actual.Kind != selected.Kind || actual.ReadOnly != selected.ReadOnly {
			return false
		}
		switch selected.Kind {
		case domain.MountBind:
			if actual.Name != "" || actual.Source != selected.Source {
				return false
			}
		case domain.MountVolume:
			name := workloadVolumeName(identifier, selected.Target)
			if selected.Source != "" || selected.ReadOnly || actual.Name != name ||
				actual.Source != filepath.Join(options.StateRoot, "volumes", name) {
				return false
			}
		default:
			return false
		}
	}

	return true
}

func validHostPersistentSource(value string) bool {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	info, err := os.Lstat(value)
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeSymlink == 0 &&
		(info.IsDir() || info.Mode().IsRegular())
}

func appendRuntimeVolumeMounts(
	mounts []specs.Mount,
	runtimeMounts []domain.RuntimeMount,
) []specs.Mount {
	result := slices.Clone(mounts)
	for _, selected := range runtimeMounts {
		if selected.Kind != domain.MountVolume {
			continue
		}
		result = append(result, specs.Mount{
			Destination: selected.Target, Type: bindMountType, Source: selected.Source,
			Options: []string{"rbind", "rw", "rprivate"},
		})
	}

	return result
}

func copyVolumeInitialContents(
	ctx context.Context,
	mounts []mount.Mount,
	volumes []domain.RuntimeMount,
	withRootfs func(context.Context, []mount.Mount, func(string) error) error,
) error {
	return copyVolumeInitialContentsWith(
		ctx, mounts, volumes, withRootfs, os.Lstat, copyVolumeContents,
	)
}

func copyVolumeInitialContentsWith(
	ctx context.Context,
	mounts []mount.Mount,
	volumes []domain.RuntimeMount,
	withRootfs func(context.Context, []mount.Mount, func(string) error) error,
	lstat func(string) (os.FileInfo, error),
	copyContents func(context.Context, string, string) error,
) error {
	if len(mounts) == 0 || withRootfs == nil {
		return ErrProtocol
	}
	private := privateContainerdLogContext(ctx)
	err := withRootfs(private, mounts, func(root string) error {
		for _, volume := range volumes {
			source, err := continuityfs.RootPath(root, volume.Target)
			if err != nil {
				return ErrProtocol
			}
			if _, err = lstat(source); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return ErrUnavailable
			}
			if err = copyContents(private, source, volume.Source); err != nil {
				return ErrUnavailable
			}
		}

		return nil
	})
	if err != nil {
		return ErrUnavailable
	}

	return nil
}

func copyVolumeContents(ctx context.Context, source, destination string) error {
	return copyVolumeContentsWith(ctx, source, destination, archive.Diff, archive.Apply)
}

func copyVolumeContentsWith(
	ctx context.Context,
	source string,
	destination string,
	diff func(context.Context, string, string, ...archive.WriteDiffOpt) io.ReadCloser,
	apply func(context.Context, string, io.Reader, ...archive.ApplyOpt) (int64, error),
) error {
	reader := diff(ctx, "", source)
	_, applyErr := apply(ctx, destination, reader)
	closeErr := reader.Close()
	if applyErr != nil || closeErr != nil {
		return ErrUnavailable
	}

	return nil
}

func privateContainerdLogContext(ctx context.Context) context.Context {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	return containerdlog.WithLogger(ctx, logrus.NewEntry(logger))
}
