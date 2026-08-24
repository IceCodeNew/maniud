package cli

import (
	"bytes"
	"context"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/compose"
)

type composeSourceFilesystem struct {
	hasGitMetadata func(string) (bool, error)
	evalSymlinks   func(string) (string, error)
	stat           func(string) (os.FileInfo, error)
	readFile       func(string) ([]byte, error)
}

func loadComposeSource(
	ctx context.Context,
	path string,
	workingDirectory string,
	environment map[string]string,
	runtimeBase string,
) (compose.Source, error) {
	return loadComposeSourceWithFilesystem(
		ctx, path, workingDirectory, environment, runtimeBase,
		composeSourceFilesystem{
			hasGitMetadata: hasGitMetadata,
			evalSymlinks:   filepath.EvalSymlinks,
			stat:           os.Stat,
			readFile:       os.ReadFile,
		},
	)
}

func loadComposeSourceWithFilesystem(
	ctx context.Context,
	path string,
	workingDirectory string,
	environment map[string]string,
	runtimeBase string,
	filesystem composeSourceFilesystem,
) (compose.Source, error) {
	absolutePath, valid := normalizedComposeSourcePath(path, workingDirectory)
	if !valid {
		return compose.Source{}, compose.ErrInvalidSource
	}
	tracked, err := filesystem.hasGitMetadata(filepath.Dir(absolutePath))
	if err != nil {
		return compose.Source{}, compose.ErrInvalidSource
	}
	if tracked {
		return loadTrackedComposeSource(ctx, absolutePath, workingDirectory, environment, runtimeBase)
	}
	resolvedPath, err := filesystem.evalSymlinks(absolutePath)
	if err != nil {
		return compose.Source{}, compose.ErrInvalidSource
	}
	if resolvedPath != absolutePath {
		return compose.Source{}, compose.ErrInvalidSource
	}

	info, err := filesystem.stat(absolutePath)
	if err != nil || !info.Mode().IsRegular() {
		return compose.Source{}, compose.ErrInvalidSource
	}
	content, err := filesystem.readFile(absolutePath)
	if err != nil {
		return compose.Source{}, compose.ErrInvalidSource
	}

	return compose.Source{
		Content: content, WorkingDir: filepath.Dir(absolutePath), Environment: map[string]string{},
	}, nil
}

func hasGitMetadata(directory string) (bool, error) {
	return hasGitMetadataWith(directory, os.Lstat)
}

func hasGitMetadataWith(
	directory string,
	lstat func(string) (os.FileInfo, error),
) (bool, error) {
	for {
		_, err := lstat(filepath.Join(directory, ".git"))
		if err == nil {
			return true, nil
		}
		if !os.IsNotExist(err) {
			return false, err
		}
		parent := filepath.Dir(directory)
		if parent == directory {
			return false, nil
		}
		directory = parent
	}
}

func loadTrackedComposeSource(
	ctx context.Context,
	path string,
	workingDirectory string,
	environment map[string]string,
	runtimeBase string,
) (compose.Source, error) {
	return loadTrackedComposeSourceWithFinalCheck(
		ctx, path, workingDirectory, environment, runtimeBase, cleanGitTree,
	)
}

func loadTrackedComposeSourceWithFinalCheck(
	ctx context.Context,
	path string,
	workingDirectory string,
	environment map[string]string,
	runtimeBase string,
	finalState func(context.Context, string) (gitTreeState, error),
) (compose.Source, error) {
	root, entry, state, err := locateCleanGitSource(ctx, path, workingDirectory)
	if err != nil {
		return compose.Source{}, compose.ErrInvalidSource
	}

	source, err := compose.CaptureRepositorySource(
		root,
		entry,
		environment,
		func(name string) (compose.RepositoryFile, bool, error) {
			return readCommittedGitFile(ctx, root, state.tree, name)
		},
		func(name string) (compose.RepositoryPathSnapshot, error) {
			return readCommittedGitPath(ctx, root, state.tree, name)
		},
	)
	if err != nil {
		return compose.Source{}, fmt.Errorf("capture committed Compose source: %w", err)
	}
	after, err := finalState(ctx, root)
	if err != nil || state != after {
		return compose.Source{}, compose.ErrInvalidSource
	}

	pinned, err := compose.PinRepositoryRuntime(source, runtimeBase)
	if err != nil {
		return compose.Source{}, fmt.Errorf("pin committed Compose runtime source: %w", err)
	}

	return pinned, nil
}

func locateCleanGitSource(
	ctx context.Context,
	path string,
	workingDirectory string,
) (string, string, gitTreeState, error) {
	absolutePath, valid := normalizedComposeSourcePath(path, workingDirectory)
	if !valid {
		return "", "", gitTreeState{}, compose.ErrInvalidSource
	}
	absolutePath, err := filepath.EvalSymlinks(absolutePath)
	if err != nil || !filepath.IsAbs(absolutePath) {
		return "", "", gitTreeState{}, compose.ErrInvalidSource
	}
	rootOutput, err := runGit(ctx, filepath.Dir(absolutePath), "rev-parse", "--show-toplevel")
	if err != nil {
		return "", "", gitTreeState{}, compose.ErrInvalidSource
	}
	root := filepath.Clean(strings.TrimSpace(string(rootOutput)))
	if !filepath.IsAbs(root) {
		return "", "", gitTreeState{}, compose.ErrInvalidSource
	}
	if err = validateGitProcessConfiguration(ctx, root); err != nil {
		return "", "", gitTreeState{}, compose.ErrInvalidSource
	}
	// rev-parse ran in the source file's parent and returned its containing worktree.
	// On the supported Unix platforms, filepath.Rel cannot fail for these absolute paths.
	relative, _ := filepath.Rel(root, absolutePath)
	state, err := cleanGitTree(ctx, root)
	if err != nil {
		return "", "", gitTreeState{}, compose.ErrInvalidSource
	}

	return root, filepath.ToSlash(relative), state, nil
}

type gitTreeState struct {
	head string
	tree string
}

func cleanGitTree(ctx context.Context, root string) (gitTreeState, error) {
	status, err := runGit(
		ctx,
		root,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none",
	)
	if err != nil || len(status) != 0 {
		return gitTreeState{}, compose.ErrInvalidSource
	}
	head, err := resolveGitObject(ctx, root, "HEAD^{commit}")
	if err != nil {
		return gitTreeState{}, compose.ErrInvalidSource
	}
	tree, err := resolveGitObject(ctx, root, head+"^{tree}")
	if err != nil {
		return gitTreeState{}, compose.ErrInvalidSource
	}

	return gitTreeState{head: head, tree: tree}, nil
}

func resolveGitObject(ctx context.Context, root, revision string) (string, error) {
	output, err := runGit(ctx, root, "rev-parse", "--verify", revision)
	value := strings.TrimSpace(string(output))
	if err != nil || !validGitObjectID(value) {
		return "", compose.ErrInvalidSource
	}

	return value, nil
}

func readCommittedGitFile(
	ctx context.Context,
	root string,
	tree string,
	name string,
) (compose.RepositoryFile, bool, error) {
	entry, found, err := readGitTreeEntry(ctx, root, tree, name)
	if err != nil || found && !entry.regularFile() {
		return compose.RepositoryFile{}, false, compose.ErrInvalidSource
	}
	if !found {
		return compose.RepositoryFile{}, false, nil
	}
	content, err := readGitBlob(ctx, root, entry.object)

	return compose.RepositoryFile{
		Content: content, Executable: entry.mode == executableGitMode,
	}, true, err
}

func readCommittedGitPath(
	ctx context.Context,
	root string,
	tree string,
	name string,
) (compose.RepositoryPathSnapshot, error) {
	entry, found, err := readGitTreeEntry(ctx, root, tree, name)
	if err != nil || !found {
		return compose.RepositoryPathSnapshot{}, compose.ErrInvalidSource
	}
	if entry.regularFile() {
		return readCommittedGitRegularPath(ctx, root, name, entry)
	}
	if !entry.directory() {
		return compose.RepositoryPathSnapshot{}, compose.ErrInvalidSource
	}

	return readCommittedGitDirectory(ctx, root, tree, name)
}

func readCommittedGitRegularPath(
	ctx context.Context,
	root string,
	name string,
	entry gitTreeEntry,
) (compose.RepositoryPathSnapshot, error) {
	content, err := readGitBlob(ctx, root, entry.object)
	if err != nil {
		return compose.RepositoryPathSnapshot{}, err
	}

	return compose.RepositoryPathSnapshot{Files: map[string]compose.RepositoryFile{
		name: {Content: content, Executable: entry.mode == executableGitMode},
	}}, nil
}

func readCommittedGitDirectory(
	ctx context.Context,
	root string,
	tree string,
	name string,
) (compose.RepositoryPathSnapshot, error) {
	metadata, err := runGit(ctx, root, "ls-tree", "-r", "-z", tree, "--", name)
	entries, valid := parseGitTreeEntries(metadata)
	if err != nil || !valid || len(entries) == 0 {
		return compose.RepositoryPathSnapshot{}, compose.ErrInvalidSource
	}
	files := make(map[string]compose.RepositoryFile, len(entries))
	for _, child := range entries {
		if !child.regularFile() || !strings.HasPrefix(child.path, name+"/") {
			return compose.RepositoryPathSnapshot{}, compose.ErrInvalidSource
		}
		content, readErr := readGitBlob(ctx, root, child.object)
		if readErr != nil {
			return compose.RepositoryPathSnapshot{}, readErr
		}
		files[child.path] = compose.RepositoryFile{
			Content: content, Executable: child.mode == executableGitMode,
		}
	}

	return compose.RepositoryPathSnapshot{Directory: true, Files: files}, nil
}

func readGitTreeEntry(ctx context.Context, root, tree, name string) (gitTreeEntry, bool, error) {
	metadata, err := runGit(ctx, root, "ls-tree", "-z", tree, "--", name)
	if err != nil {
		return gitTreeEntry{}, false, compose.ErrInvalidSource
	}
	if len(metadata) == 0 {
		return gitTreeEntry{}, false, nil
	}
	entries, valid := parseGitTreeEntries(metadata)
	if !valid || len(entries) != 1 || entries[0].path != name {
		return gitTreeEntry{}, false, compose.ErrInvalidSource
	}

	return entries[0], true, nil
}

func readGitBlob(ctx context.Context, root, object string) ([]byte, error) {
	content, err := runGit(ctx, root, "cat-file", "blob", object)
	if err != nil {
		return nil, compose.ErrInvalidSource
	}

	return content, nil
}

type gitTreeEntry struct {
	mode   string
	kind   string
	object string
	path   string
}

const executableGitMode = "100755"

func (entry gitTreeEntry) regularFile() bool {
	return (entry.mode == "100644" || entry.mode == "100755") && entry.kind == "blob"
}

func (entry gitTreeEntry) directory() bool {
	return entry.mode == "040000" && entry.kind == "tree"
}

func parseGitTreeEntries(value []byte) ([]gitTreeEntry, bool) {
	if len(value) == 0 || value[len(value)-1] != 0 {
		return nil, false
	}
	records := bytes.Split(value[:len(value)-1], []byte{0})
	entries := make([]gitTreeEntry, len(records))
	for index, record := range records {
		metadata, path, found := bytes.Cut(record, []byte{'\t'})
		fields := bytes.Fields(metadata)
		if !found || len(fields) != 3 || !validGitObjectID(string(fields[2])) ||
			!validGitTreePath(string(path)) {
			return nil, false
		}
		entries[index] = gitTreeEntry{
			mode: string(fields[0]), kind: string(fields[1]), object: string(fields[2]), path: string(path),
		}
	}

	return entries, true
}

func validGitTreePath(value string) bool {
	return value != "" && value != "." && filepath.IsLocal(filepath.FromSlash(value)) &&
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) == value
}

func validGitObjectID(value string) bool {
	if len(value) != 40 && len(value) != 64 {
		return false
	}
	for _, character := range value {
		if character < '0' || character > '9' && character < 'a' || character > 'f' {
			return false
		}
	}

	return true
}
