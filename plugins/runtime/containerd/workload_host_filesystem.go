package containerd

import (
	"archive/tar"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strings"

	"github.com/containerd/containerd/v2/pkg/archive"
	continuityfs "github.com/containerd/continuity/fs"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func secureDirectory(path string) error {
	_, err := secureNewDirectory(path)

	return err
}

func secureNewDirectory(path string) (bool, error) {
	if !filepath.IsAbs(path) || filepath.Clean(path) != path {
		return false, ErrProtocol
	}
	_, statErr := os.Lstat(path)
	created := errors.Is(statErr, os.ErrNotExist)
	if statErr != nil && !created {
		return false, ErrUnavailable
	}
	if err := os.MkdirAll(path, privateDirectoryMode); err != nil {
		return false, ErrUnavailable
	}
	resolved, err := filepath.EvalSymlinks(path)
	if err != nil || resolved != path {
		return false, ErrProtocol
	}
	info, err := os.Lstat(path)

	return validateSecureDirectory(path, created, resolved, info, err, os.Chmod)
}

func validateSecureDirectory(
	path string,
	created bool,
	resolved string,
	info os.FileInfo,
	statErr error,
	chmod func(string, os.FileMode) error,
) (bool, error) {
	if statErr != nil || info == nil || resolved != path || !info.IsDir() || info.Mode()&os.ModeSymlink != 0 {
		return false, ErrProtocol
	}
	// Directories require owner execute permission for traversal.
	if created && chmod(path, privateDirectoryMode) != nil {
		return false, ErrUnavailable
	}

	return created, nil
}

func writePrivateFile(path string, content []byte) error {
	return writePrivateFileWith(path, content, os.CreateTemp)
}

func writePrivateFileWith(
	path string,
	content []byte,
	createTemp func(string, string) (*os.File, error),
) error {
	directory := filepath.Dir(path)
	if err := secureDirectory(directory); err != nil {
		return err
	}
	temporary, err := createTemp(directory, ".maniud-host-*")
	if err != nil {
		return ErrUnavailable
	}
	temporaryName := temporary.Name()
	defer func() { _ = os.Remove(temporaryName) }()

	return writePrivateFileContents(temporary, content, path)
}

func writePrivateFileContents(temporary *os.File, content []byte, path string) error {
	_, writeErr := temporary.Write(content)
	var syncErr error
	if writeErr == nil {
		syncErr = temporary.Sync()
	}
	closeErr := temporary.Close()
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return ErrUnavailable
	}
	if err := os.Rename(temporary.Name(), path); err != nil {
		return ErrUnavailable
	}

	return nil
}

func workloadStateDirectory(options WorkloadOptions, identifier string) string {
	return filepath.Join(options.StateRoot, "workloads", identifier)
}

func workloadNetworkNamespace(options WorkloadOptions, identifier string) string {
	return filepath.Join(options.NetworkNamespaceRoot, identifier)
}

func workloadVolumeName(identifier, target string) string {
	digest := domain.Hash([]byte(target))

	return identifier + "-" + hex.EncodeToString(digest[:8])
}

func removeWorkloadState(options WorkloadOptions, identifier string, mounts []domain.RuntimeMount) error {
	for _, selected := range mounts {
		if selected.Kind != domain.MountVolume {
			continue
		}
		if selected.Source != filepath.Join(options.StateRoot, "volumes", selected.Name) ||
			os.RemoveAll(selected.Source) != nil {
			return ErrUnavailable
		}
	}
	if os.RemoveAll(workloadStateDirectory(options, identifier)) != nil {
		return ErrUnavailable
	}

	return nil
}

func archiveStatPath(path string) (application.ArchivePathStat, error) {
	info, err := os.Lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return application.ArchivePathStat{}, application.ErrArchivePathMissing
	}
	if err != nil {
		return application.ArchivePathStat{}, ErrUnavailable
	}

	return archiveStatInfo(path, info)
}

func archiveStatInfo(path string, info os.FileInfo) (application.ArchivePathStat, error) {
	linkTarget := ""
	if info.Mode()&os.ModeSymlink != 0 {
		var err error
		linkTarget, err = os.Readlink(path)
		if err != nil {
			return application.ArchivePathStat{}, ErrUnavailable
		}
	}

	return archivePathStat(info, linkTarget), nil
}

func writePathArchive(
	ctx context.Context,
	path string,
	destination io.Writer,
	maximumBytes int64,
) error {
	writer := &boundedArchiveWriter{destination: destination, remaining: maximumBytes}
	changeWriter := archive.NewChangeWriter(writer, filepath.Dir(path))
	err := filepath.Walk(path, func(current string, info os.FileInfo, walkErr error) error {
		if err := ctx.Err(); err != nil {
			return fmt.Errorf("archive source walk: %w", err)
		}
		relative, err := filepath.Rel(filepath.Dir(path), current)
		if err != nil || relative == "." || !filepath.IsLocal(relative) {
			return ErrProtocol
		}

		return changeWriter.HandleChange(
			continuityfs.ChangeKindAdd, "/"+filepath.ToSlash(relative), info, walkErr,
		)
	})
	if closeErr := changeWriter.Close(); err == nil {
		err = closeErr
	}
	if writer.exceeded {
		return ErrProtocol
	}
	if err != nil {
		return ErrUnavailable
	}

	return nil
}

type boundedArchiveWriter struct {
	destination io.Writer
	remaining   int64
	exceeded    bool
}

func (writer *boundedArchiveWriter) Write(value []byte) (int, error) {
	if int64(len(value)) > writer.remaining {
		writer.exceeded = true

		return 0, ErrProtocol
	}
	written, err := writer.destination.Write(value)
	writer.remaining -= int64(written)
	if err != nil || written != len(value) {
		return written, ErrUnavailable
	}

	return written, nil
}

func applyPathArchive(ctx context.Context, path string, source io.Reader) error {
	filter := func(header *tar.Header) (bool, error) {
		if header == nil || !filepath.IsLocal(header.Name) ||
			strings.ContainsRune(header.Name, 0) {
			return false, ErrProtocol
		}

		return true, nil
	}
	if _, err := archive.Apply(
		privateContainerdLogContext(ctx), path, source, archive.WithFilter(filter),
	); err != nil {
		return ErrProtocol
	}

	return nil
}
