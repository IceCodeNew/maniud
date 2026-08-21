package compose

import (
	"bytes"
	"crypto/rand"
	"errors"
	"io/fs"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	repositoryRuntimeDirectory = "sources"
	runtimeDirectoryMode       = os.FileMode(0o700)
	runtimeFileMode            = os.FileMode(0o400)
	runtimeExecutableMode      = os.FileMode(0o500)
	runtimePrivateMode         = os.FileMode(0o700)
)

// PinRepositoryRuntime selects the content-addressed state directory used by
// Docker bind mounts without creating it. Dry-run therefore remains read-only.
func PinRepositoryRuntime(source Source, base string) (Source, error) {
	if source.Repository == nil || !filepath.IsAbs(base) || filepath.Clean(base) != base ||
		source.Repository.Digest == (domain.Digest{}) {
		return Source{}, ErrInvalidSource
	}

	source.runtimeBase = base

	return source, nil
}

// MaterializeRuntime atomically publishes the committed bind-mount files at
// the path already represented by the desired workload. Existing content must
// match the captured source exactly.
func (source Source) MaterializeRuntime() error {
	if source.Repository == nil || len(source.Repository.RuntimePaths) == 0 {
		return nil
	}
	if source.runtimeBase == "" || !validRepositorySnapshot(source) {
		return ErrInvalidSource
	}

	root, err := os.OpenRoot(source.runtimeBase)
	if err != nil {
		return ErrInvalidSource
	}
	defer func() {
		_ = root.Close()
	}()

	return source.materializeRuntime(root, root.Lstat)
}

func (source Source) materializeRuntime(
	root *os.Root,
	lstat func(string) (fs.FileInfo, error),
) error {
	if err := ensureRuntimeParent(root); err != nil {
		return err
	}
	final := filepath.Join(repositoryRuntimeDirectory, repositoryRuntimeName(source.Repository.Digest))
	if _, err := lstat(final); err == nil {
		if validMaterializedRuntime(root, final, source.Repository) {
			return nil
		}

		return ErrInvalidSource
	} else if !errors.Is(err, fs.ErrNotExist) {
		return ErrInvalidSource
	}

	temporary, err := prepareRuntimeSnapshot(root, source.Repository)
	if err != nil {
		return err
	}
	defer func() {
		_ = root.RemoveAll(temporary)
	}()

	return publishRuntimeSnapshot(root, temporary, final, source.Repository)
}

func publishRuntimeSnapshot(
	root *os.Root,
	temporary string,
	final string,
	snapshot *RepositorySnapshot,
) error {
	if err := root.Rename(temporary, final); err != nil {
		if validMaterializedRuntime(root, final, snapshot) {
			return nil
		}

		return ErrInvalidSource
	}

	return syncRootDirectory(root, repositoryRuntimeDirectory)
}

func repositoryRuntimeRoot(base string, digest domain.Digest) string {
	return filepath.Join(base, repositoryRuntimeDirectory, repositoryRuntimeName(digest))
}

func repositoryRuntimeName(digest domain.Digest) string {
	return strings.TrimPrefix(digest.String(), "sha256:")
}

func ensureRuntimeParent(root *os.Root) error {
	err := root.Mkdir(repositoryRuntimeDirectory, runtimePrivateMode)
	if err != nil && !errors.Is(err, fs.ErrExist) {
		return ErrInvalidSource
	}
	info, err := root.Lstat(repositoryRuntimeDirectory)
	if err != nil || !info.IsDir() || info.Mode().Perm() != runtimePrivateMode {
		return ErrInvalidSource
	}

	return nil
}

func createRuntimeTemporary(root *os.Root) (string, error) {
	name := filepath.Join(repositoryRuntimeDirectory, ".tmp-"+rand.Text())
	if err := root.Mkdir(name, runtimePrivateMode); err != nil {
		return "", ErrInvalidSource
	}

	return name, nil
}

func prepareRuntimeSnapshot(root *os.Root, snapshot *RepositorySnapshot) (string, error) {
	temporary, err := createRuntimeTemporary(root)
	if err != nil {
		return "", err
	}
	if err := writeRuntimeSnapshot(root, temporary, snapshot); err != nil {
		_ = root.RemoveAll(temporary)

		return "", err
	}

	return temporary, nil
}

func writeRuntimeSnapshot(root *os.Root, temporary string, snapshot *RepositorySnapshot) error {
	files := runtimeSnapshotFiles(snapshot)
	directories := runtimeSnapshotDirectories(files)
	for _, directory := range directories {
		if directory == "." {
			continue
		}
		if err := root.MkdirAll(filepath.Join(temporary, filepath.FromSlash(directory)), runtimePrivateMode); err != nil {
			return ErrInvalidSource
		}
	}
	fileNames := mapsKeys(files)
	slices.Sort(fileNames)
	for _, name := range fileNames {
		if err := writeRuntimeFile(root, filepath.Join(temporary, filepath.FromSlash(name)), files[name]); err != nil {
			return err
		}
	}
	for _, directory := range slices.Backward(directories) {
		name := temporary
		if directory != "." {
			name = filepath.Join(temporary, filepath.FromSlash(directory))
		}
		if err := root.Chmod(name, runtimeDirectoryMode); err != nil || syncRootDirectory(root, name) != nil {
			return ErrInvalidSource
		}
	}

	return nil
}

func writeRuntimeFile(root *os.Root, name string, file RepositoryFile) error {
	descriptor, err := root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, runtimeFileMode)
	if err != nil {
		return ErrInvalidSource
	}

	return writeRuntimeDescriptor(descriptor, file)
}

func writeRuntimeDescriptor(descriptor *os.File, file RepositoryFile) error {
	mode := runtimeFileMode
	if file.Executable {
		mode = runtimeExecutableMode
	}
	_, writeErr := descriptor.Write(file.Content)
	chmodErr := descriptor.Chmod(mode)
	syncErr := descriptor.Sync()
	closeErr := descriptor.Close()
	if writeErr != nil || chmodErr != nil || syncErr != nil || closeErr != nil {
		return ErrInvalidSource
	}

	return nil
}

//nolint:cyclop // The walk rejects every extra type, path, mode, and byte sequence in one audit surface.
func validMaterializedRuntime(root *os.Root, final string, snapshot *RepositorySnapshot) bool {
	files := runtimeSnapshotFiles(snapshot)
	directories := runtimeSnapshotDirectories(files)
	wantedDirectories := make(map[string]struct{}, len(directories))
	for _, name := range directories {
		path := final
		if name != "." {
			path = filepath.Join(final, filepath.FromSlash(name))
		}
		wantedDirectories[filepath.ToSlash(path)] = struct{}{}
	}
	seen := make(map[string]struct{}, len(files))
	err := fs.WalkDir(root.FS(), final, func(current string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			info, err := entry.Info()
			if _, found := wantedDirectories[filepath.ToSlash(current)]; !found || err != nil ||
				info.Mode().Perm() != runtimeDirectoryMode {
				return ErrInvalidSource
			}

			return nil
		}
		relative, _ := filepath.Rel(final, current)
		name := filepath.ToSlash(relative)
		file, found := files[name]
		info, infoErr := entry.Info()
		content, readErr := root.ReadFile(current)
		mode := runtimeFileMode
		if file.Executable {
			mode = runtimeExecutableMode
		}
		if !found || infoErr != nil || readErr != nil || !info.Mode().IsRegular() ||
			info.Mode().Perm() != mode || !bytes.Equal(content, file.Content) {
			return ErrInvalidSource
		}
		seen[name] = struct{}{}

		return nil
	})

	return err == nil && len(seen) == len(files)
}

func runtimeSnapshotFiles(snapshot *RepositorySnapshot) map[string]RepositoryFile {
	files := make(map[string]RepositoryFile)
	for _, source := range snapshot.RuntimePaths {
		for name, file := range snapshot.Files {
			if repositoryPathContains(source.Path, name, source.Directory) {
				files[name] = file
			}
		}
	}

	return files
}

func runtimeSnapshotDirectories(files map[string]RepositoryFile) []string {
	directories := map[string]struct{}{".": {}}
	for name := range files {
		for directory := filepath.ToSlash(filepath.Dir(filepath.FromSlash(name))); directory != "."; {
			directories[directory] = struct{}{}
			directory = filepath.ToSlash(filepath.Dir(filepath.FromSlash(directory)))
		}
	}
	result := mapsKeys(directories)
	slices.SortFunc(result, func(first, second string) int {
		firstDepth := repositoryPathDepth(first)
		secondDepth := repositoryPathDepth(second)
		if firstDepth != secondDepth {
			return firstDepth - secondDepth
		}

		return strings.Compare(first, second)
	})

	return result
}

func repositoryPathDepth(value string) int {
	if value == "." {
		return 0
	}

	return strings.Count(value, "/") + 1
}

func mapsKeys[Value any](values map[string]Value) []string {
	result := make([]string, 0, len(values))
	for name := range values {
		result = append(result, name)
	}

	return result
}

func syncRootDirectory(root *os.Root, name string) error {
	directory, err := root.Open(name)
	if err != nil {
		return ErrInvalidSource
	}

	return syncRuntimeDirectory(directory)
}

func syncRuntimeDirectory(directory *os.File) error {
	syncErr := directory.Sync()
	closeErr := directory.Close()
	if syncErr != nil || closeErr != nil {
		return ErrInvalidSource
	}

	return nil
}
