package cli

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/internal/compose"
)

func generatedTUIArtifacts(generated generatedCompose) []generatedArtifact {
	artifacts := []generatedArtifact{{path: generated.absolutePath, content: generated.content}}
	if generated.preparationAbsolute != "" {
		artifacts = append(artifacts, generatedArtifact{
			path: generated.preparationAbsolute, content: generated.preparation,
		})
	}

	return artifacts
}

func generatedTUIDraftArtifacts(generated generatedCompose) []generatedArtifact {
	artifacts := generatedTUIArtifacts(generated)
	for index := range artifacts {
		artifacts[index].path = tuiDraftPath(artifacts[index].path)
	}

	return artifacts
}

func generatedTUIDraftRelativePaths(generated generatedCompose) []string {
	paths := generatedRelativePaths(generated)
	for index := range paths {
		paths[index] = tuiDraftPath(paths[index])
	}
	slices.Sort(paths)

	return paths
}

func tuiDraftPath(path string) string {
	return filepath.Join(filepath.Dir(path), "."+filepath.Base(path)+tuiDraftSuffix)
}

func writeTUIDraftFiles(generated generatedCompose) error {
	return writeGeneratedArtifacts(generatedTUIDraftArtifacts(generated), generatedComposeDefaultOperations())
}

func generatedTUIDraftFilesMatch(generated generatedCompose) bool {
	artifacts := generatedTUIDraftArtifacts(generated)
	for _, artifact := range artifacts {
		if !generatedArtifactMatches(artifact) {
			return false
		}
	}

	return true
}

func promoteTUIDraftFiles(generated generatedCompose) error {
	for _, artifact := range generatedTUIArtifacts(generated) {
		if err := moveTUIArtifact(tuiDraftPath(artifact.path), artifact.path, artifact.content); err != nil {
			return err
		}
	}

	return nil
}

func restoreTUIDraftFiles(generated generatedCompose) error {
	var result error
	for _, artifact := range generatedTUIArtifacts(generated) {
		draft := generatedArtifact{path: tuiDraftPath(artifact.path), content: artifact.content}
		finalMatches := generatedArtifactMatches(artifact)
		draftMatches := generatedArtifactMatches(draft)
		switch {
		case draftMatches && !finalMatches:
			continue
		case !draftMatches && finalMatches:
			result = errors.Join(result, moveTUIArtifact(artifact.path, draft.path, artifact.content))
		case draftMatches && finalMatches:
			if !tuiArtifactsShareIdentity(artifact.path, draft.path) {
				result = errors.Join(result, compose.ErrInvalidSource)

				continue
			}
			result = errors.Join(result, removeMatchingTUIArtifact(artifact))
		default:
			result = errors.Join(result, compose.ErrInvalidSource)
		}
	}

	return result
}

//nolint:cyclop // Recovery keeps draft presence, bytes, and inode ownership checks in one ordered proof.
func recoverPartialTUIDraftPublication(
	ctx context.Context,
	repository string,
	generated generatedCompose,
) error {
	status, err := runGit(
		ctx,
		repository,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none",
	)
	if err != nil {
		return compose.ErrInvalidSource
	}
	entries, valid := splitNullTerminated(status)
	if !valid {
		return compose.ErrInvalidSource
	}
	untracked := untrackedTUIPaths(entries)
	for _, artifact := range generatedTUIArtifacts(generated) {
		final, _ := filepath.Rel(repository, artifact.path)
		final = filepath.ToSlash(final)
		draft := filepath.ToSlash(tuiDraftPath(final))
		_, finalUntracked := untracked[final]
		_, draftUntracked := untracked[draft]
		if !finalUntracked || (!draftUntracked && !gitPathIgnored(ctx, repository, draft)) {
			continue
		}
		if !generatedArtifactMatches(artifact) ||
			!generatedArtifactMatches(generatedArtifact{path: tuiDraftPath(artifact.path), content: artifact.content}) ||
			!tuiArtifactsShareIdentity(artifact.path, tuiDraftPath(artifact.path)) {
			return compose.ErrInvalidSource
		}
		if err = removeMatchingTUIArtifact(artifact); err != nil {
			return err
		}
	}

	return nil
}

func untrackedTUIPaths(entries [][]byte) map[string]struct{} {
	const untrackedPrefix = "?? "

	untracked := make(map[string]struct{}, len(entries))
	for _, entry := range entries {
		if path, found := strings.CutPrefix(string(entry), untrackedPrefix); found {
			untracked[path] = struct{}{}
		}
	}

	return untracked
}

func moveTUIArtifact(from, to string, content []byte) (returnErr error) {
	return moveTUIArtifactWithOperations(from, to, content, generatedComposeDefaultOperations())
}

//nolint:cyclop // Validation, hard-link identity, and durability form one no-clobber move.
func moveTUIArtifactWithOperations(
	from string,
	to string,
	content []byte,
	operations generatedComposeOperations,
) (returnErr error) {
	if filepath.Dir(from) != filepath.Dir(to) {
		return compose.ErrInvalidSource
	}
	root, err := operations.openRoot(filepath.Dir(from))
	if err != nil {
		return compose.ErrInvalidSource
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeRoot(root)) }()
	directory, err := operations.openDirectory(root)
	if err != nil {
		return compose.ErrInvalidSource
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeFile(directory)) }()
	fromName := filepath.Base(from)
	toName := filepath.Base(to)
	identity, retained, matches := matchingTUIArtifact(root, fromName, content, operations)
	if !matches {
		return compose.ErrInvalidSource
	}
	defer closeGeneratedFile(retained)
	if err = operations.link(root, fromName, toName); err != nil {
		return compose.ErrInvalidSource
	}
	currentFrom, fromErr := operations.lstat(root, fromName)
	currentTo, toErr := operations.lstat(root, toName)
	linked := fromErr == nil && toErr == nil && currentFrom.Mode().IsRegular() && currentTo.Mode().IsRegular() &&
		os.SameFile(currentFrom, currentTo)
	if !linked || !os.SameFile(identity, currentFrom) {
		syncErr := operations.syncDirectory(directory)

		return errors.Join(compose.ErrInvalidSource, fromErr, toErr, syncErr)
	}
	if err = operations.syncDirectory(directory); err != nil {
		return errors.Join(compose.ErrInvalidSource, err)
	}
	if !tuiArtifactHasIdentity(root, fromName, identity, operations) ||
		!tuiArtifactHasIdentity(root, toName, identity, operations) {
		return compose.ErrInvalidSource
	}
	if err = removeTUIArtifactWithIdentity(root, fromName, identity, operations); err != nil {
		syncErr := operations.syncDirectory(directory)

		return errors.Join(compose.ErrInvalidSource, err, syncErr)
	}
	if err = operations.syncDirectory(directory); err != nil {
		return errors.Join(compose.ErrInvalidSource, err)
	}
	if !tuiArtifactHasIdentity(root, toName, identity, operations) {
		return compose.ErrInvalidSource
	}

	return nil
}

func removeMatchingTUIArtifact(artifact generatedArtifact) (returnErr error) {
	return removeMatchingTUIArtifactWithOperations(artifact, generatedComposeDefaultOperations())
}

func removeMatchingTUIArtifactWithOperations(
	artifact generatedArtifact,
	operations generatedComposeOperations,
) (returnErr error) {
	root, err := operations.openRoot(filepath.Dir(artifact.path))
	if err != nil {
		return compose.ErrInvalidSource
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeRoot(root)) }()
	directory, err := operations.openDirectory(root)
	if err != nil {
		return compose.ErrInvalidSource
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeFile(directory)) }()
	name := filepath.Base(artifact.path)
	identity, retained, matches := matchingTUIArtifact(root, name, artifact.content, operations)
	if !matches {
		return compose.ErrInvalidSource
	}
	defer closeGeneratedFile(retained)
	if err := removeTUIArtifactWithIdentity(root, name, identity, operations); err != nil {
		return compose.ErrInvalidSource
	}
	if err := operations.syncDirectory(directory); err != nil {
		return errors.Join(compose.ErrInvalidSource, err)
	}

	return nil
}

func matchingTUIArtifact(
	root *os.Root,
	name string,
	content []byte,
	operations generatedComposeOperations,
) (os.FileInfo, *os.File, bool) {
	file, info, matched, err := openGeneratedFileMatching(root, name, content, operations.lstat)
	if err != nil || !matched {
		return nil, nil, false
	}

	return info, file, true
}

func tuiArtifactHasIdentity(
	root *os.Root,
	name string,
	identity os.FileInfo,
	operations generatedComposeOperations,
) bool {
	current, err := operations.lstat(root, name)

	return err == nil && current.Mode().IsRegular() && os.SameFile(identity, current)
}

func removeTUIArtifactWithIdentity(
	root *os.Root,
	name string,
	identity os.FileInfo,
	operations generatedComposeOperations,
) error {
	if !tuiArtifactHasIdentity(root, name, identity, operations) {
		return compose.ErrInvalidSource
	}

	return operations.remove(root, name)
}

func tuiArtifactsShareIdentity(first, second string) bool {
	if filepath.Dir(first) != filepath.Dir(second) {
		return false
	}
	root, err := os.OpenRoot(filepath.Dir(first))
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	firstInfo, firstErr := root.Lstat(filepath.Base(first))
	secondInfo, secondErr := root.Lstat(filepath.Base(second))

	return firstErr == nil && secondErr == nil && firstInfo != nil && secondInfo != nil && firstInfo.Mode().IsRegular() &&
		secondInfo.Mode().IsRegular() && os.SameFile(firstInfo, secondInfo)
}

func generatedArtifactMatches(artifact generatedArtifact) bool {
	root, err := os.OpenRoot(filepath.Dir(artifact.path))
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	name := filepath.Base(artifact.path)
	info, err := root.Lstat(name)
	if err != nil || !info.Mode().IsRegular() {
		return false
	}
	matched, err := generatedFileMatches(root, name, info, artifact.content)

	return err == nil && matched
}

func generatedFileMatches(root *os.Root, name string, info os.FileInfo, expected []byte) (bool, error) {
	file, current, matched, err := openGeneratedFileMatching(root, name, expected, (*os.Root).Lstat)
	matched = matched && os.SameFile(info, current)
	if file != nil {
		closeGeneratedFile(file)
	}

	return matched, err
}

func openGeneratedFileMatching(
	root *os.Root,
	name string,
	expected []byte,
	lstat func(*os.Root, string) (os.FileInfo, error),
) (*os.File, os.FileInfo, bool, error) {
	file, err := root.Open(name)
	if err != nil {
		return nil, nil, false, fmt.Errorf("open generated file: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
	current, statErr := file.Stat()
	visible, visibleErr := lstat(root, name)
	if readErr != nil || statErr != nil || visibleErr != nil ||
		!current.Mode().IsRegular() || !visible.Mode().IsRegular() || !os.SameFile(current, visible) {
		closeGeneratedFile(file)

		return nil, nil, false, errors.Join(readErr, statErr, visibleErr)
	}

	if !bytes.Equal(content, expected) {
		closeGeneratedFile(file)

		return nil, nil, false, nil
	}

	return file, current, true, nil
}

func closeGeneratedFile(file *os.File) {
	_ = file.Close()
}
