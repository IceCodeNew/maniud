package cli

import (
	"bytes"
	"context"
	"crypto/sha256"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/internal/compose"
)

const (
	deploymentGitConfigurationFields = 2
	deploymentGitFileMode            = os.FileMode(0o600)
	deploymentGitDirectoryMode       = os.FileMode(0o700)
)

type deploymentGitTransform struct {
	infoAttributes  []byte
	infoPresent     bool
	infoIdentity    os.FileInfo
	configArguments []string
	objectFormat    string
}

type tuiDeploymentConfirmation struct {
	transform    deploymentGitTransform
	content      []byte
	diff         []byte
	expectedTree string
}

type deploymentGitIsolation struct {
	repository       string
	root             string
	configuration    []string
	extraEnvironment []string
	removeAll        func(string) error
}

type deploymentGitFileOperations struct {
	mkdirTemp func(string, string) (string, error)
	writeFile func(string, []byte, os.FileMode) error
	mkdir     func(string, os.FileMode) error
	removeAll func(string) error
	openFile  func(string, int, os.FileMode) (*os.File, error)
	lstat     func(string) (os.FileInfo, error)
	open      func(string) (*os.File, error)
	read      func(io.Reader) ([]byte, error)
	copy      func(io.Writer, io.Reader) (int64, error)
	stat      func(*os.File) (os.FileInfo, error)
	sync      func(*os.File) error
	close     func(*os.File) error
	remove    func(string) error
	rename    func(string, string) error
	sameFile  func(os.FileInfo, os.FileInfo) bool
}

func defaultDeploymentGitFileOperations() deploymentGitFileOperations {
	return deploymentGitFileOperations{
		mkdirTemp: os.MkdirTemp,
		writeFile: os.WriteFile,
		mkdir:     os.Mkdir,
		removeAll: os.RemoveAll,
		openFile:  os.OpenFile,
		lstat:     os.Lstat,
		open:      os.Open,
		read: func(reader io.Reader) ([]byte, error) {
			return io.ReadAll(io.LimitReader(reader, maximumComposeSourceBytes+1))
		},
		copy:     io.Copy,
		stat:     (*os.File).Stat,
		sync:     (*os.File).Sync,
		close:    (*os.File).Close,
		remove:   os.Remove,
		rename:   os.Rename,
		sameFile: os.SameFile,
	}
}

func captureDeploymentGitTransform(
	ctx context.Context,
	repository string,
) (deploymentGitTransform, error) {
	path, err := absoluteGitPath(ctx, repository, "info/attributes")
	if err != nil {
		return deploymentGitTransform{}, err
	}
	attributes, identity, present, err := readOptionalGitFile(path)
	if err != nil {
		return deploymentGitTransform{}, err
	}
	configuration, err := deploymentGitConfiguration(ctx, repository)
	if err != nil {
		return deploymentGitTransform{}, err
	}
	objectFormat, err := deploymentGitObjectFormat(ctx, repository)
	if err != nil {
		return deploymentGitTransform{}, err
	}

	return deploymentGitTransform{
		infoAttributes: attributes, infoPresent: present, infoIdentity: identity,
		configArguments: configuration, objectFormat: objectFormat,
	}, nil
}

func sameDeploymentGitTransform(left, right deploymentGitTransform) bool {
	infoIdentityMatches := !left.infoPresent || left.infoIdentity != nil && right.infoIdentity != nil &&
		os.SameFile(left.infoIdentity, right.infoIdentity)

	return left.infoPresent == right.infoPresent && infoIdentityMatches &&
		bytes.Equal(left.infoAttributes, right.infoAttributes) &&
		slices.Equal(left.configArguments, right.configArguments) &&
		left.objectFormat == right.objectFormat
}

func deploymentGitObjectFormat(ctx context.Context, repository string) (string, error) {
	output, err := runGit(ctx, repository, "rev-parse", "--show-object-format")
	format := strings.TrimSpace(string(output))
	if err != nil || format != "sha1" && format != "sha256" {
		return "", errDeploymentEditInvalid
	}

	return format, nil
}

func deploymentGitConfiguration(ctx context.Context, repository string) ([]string, error) {
	output, err := runGit(ctx, repository, "config", "--no-includes", "--null", "--list")
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	records, valid := splitNullTerminated(output)
	if !valid {
		return nil, errDeploymentEditInvalid
	}
	arguments := make([]string, 0, len(records)*deploymentGitConfigurationFields)
	for _, record := range records {
		key, value, found := bytes.Cut(record, []byte{'\n'})
		if !found || !deploymentGitConfigurationKey(string(key)) {
			continue
		}
		arguments = append(arguments, "-c", strings.ToLower(string(key))+"="+string(value))
	}

	return arguments, nil
}

func deploymentGitConfigurationKey(value string) bool {
	switch strings.ToLower(value) {
	case "core.autocrlf", "core.eol", "core.safecrlf", "core.checkroundtripencoding":
		return true
	default:
		return false
	}
}

func absoluteGitPath(ctx context.Context, repository, name string) (string, error) {
	output, err := runGit(
		ctx, repository, "rev-parse", "--path-format=absolute", "--git-path", name,
	)
	path := filepath.Clean(strings.TrimSpace(string(output)))
	if err != nil || !filepath.IsAbs(path) {
		return "", errDeploymentEditInvalid
	}

	return path, nil
}

func readOptionalGitFile(path string) ([]byte, os.FileInfo, bool, error) {
	return readOptionalGitFileWithOperations(path, defaultDeploymentGitFileOperations())
}

//nolint:cyclop // Stable path, descriptor, size, and identity checks form one file-read proof.
func readOptionalGitFileWithOperations(
	path string,
	operations deploymentGitFileOperations,
) ([]byte, os.FileInfo, bool, error) {
	before, err := operations.lstat(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil, false, nil
	}
	if err != nil || !before.Mode().IsRegular() || before.Size() > maximumComposeSourceBytes {
		return nil, nil, false, errDeploymentEditInvalid
	}
	file, err := operations.open(path)
	if err != nil {
		return nil, nil, false, errDeploymentEditInvalid
	}
	content, readErr := operations.read(file)
	opened, statErr := operations.stat(file)
	closeErr := operations.close(file)
	after, pathErr := operations.lstat(path)
	if readErr != nil || statErr != nil || closeErr != nil || pathErr != nil ||
		len(content) > maximumComposeSourceBytes || !opened.Mode().IsRegular() ||
		!operations.sameFile(before, opened) || !operations.sameFile(opened, after) {
		return nil, nil, false, errors.Join(
			errDeploymentEditInvalid, readErr, statErr, closeErr, pathErr,
		)
	}

	return content, opened, true, nil
}

func newDeploymentGitIsolation(
	ctx context.Context,
	repository string,
	transform deploymentGitTransform,
	useRepositoryIndex bool,
) (deploymentGitIsolation, error) {
	return newDeploymentGitIsolationWithOperations(
		ctx, repository, transform, useRepositoryIndex, "", defaultDeploymentGitFileOperations(),
	)
}

//nolint:cyclop,funlen // Filesystem setup and cleanup must stay in one owned isolation lifecycle.
func newDeploymentGitIsolationWithOperations(
	ctx context.Context,
	repository string,
	transform deploymentGitTransform,
	useRepositoryIndex bool,
	repositoryIndexPath string,
	operations deploymentGitFileOperations,
) (deploymentGitIsolation, error) {
	root, err := operations.mkdirTemp("", "maniud-deployment-git-")
	if err != nil {
		return deploymentGitIsolation{}, err
	}
	isolation := deploymentGitIsolation{
		repository: repository,
		root:       root,
		configuration: append(
			[]string{"-c", "core.attributesFile=" + os.DevNull}, transform.configArguments...,
		),
		removeAll: operations.removeAll,
	}
	fail := func(cause error) (deploymentGitIsolation, error) {
		return deploymentGitIsolation{}, errors.Join(cause, isolation.Close())
	}
	gitDirectory := filepath.Join(root, "git")
	if _, err = runGit(
		ctx, root, "init", "--bare", "--quiet", "--object-format="+transform.objectFormat, gitDirectory,
	); err != nil {
		return fail(err)
	}
	if transform.infoPresent {
		if err = operations.writeFile(
			filepath.Join(gitDirectory, "info", "attributes"), transform.infoAttributes, deploymentGitFileMode,
		); err != nil {
			return fail(err)
		}
	}
	objectDirectory, err := absoluteGitPath(ctx, repository, "objects")
	if err != nil {
		return fail(err)
	}
	indexPath := filepath.Join(root, "index")
	objectPath := filepath.Join(root, "objects")
	if useRepositoryIndex {
		indexPath = repositoryIndexPath
		if indexPath == "" {
			indexPath, err = absoluteGitPath(ctx, repository, "index")
			if err != nil {
				return fail(err)
			}
		}
		objectPath = objectDirectory
	} else if err = operations.mkdir(objectPath, deploymentGitDirectoryMode); err != nil {
		return fail(err)
	}
	isolation.extraEnvironment = []string{
		"GIT_DIR=" + gitDirectory,
		"GIT_WORK_TREE=" + repository,
		"GIT_INDEX_FILE=" + indexPath,
		"GIT_OBJECT_DIRECTORY=" + objectPath,
		"GIT_ATTR_NOSYSTEM=1",
	}
	if !useRepositoryIndex {
		isolation.extraEnvironment = append(
			isolation.extraEnvironment, "GIT_ALTERNATE_OBJECT_DIRECTORIES="+objectDirectory,
		)
	}

	return isolation, nil
}

func (isolation deploymentGitIsolation) Run(
	ctx context.Context,
	input []byte,
	arguments ...string,
) ([]byte, error) {
	commandArguments := slices.Concat(isolation.configuration, arguments)

	return runGitProcess(
		ctx, isolation.repository, false, input, isolation.extraEnvironment, commandArguments...,
	)
}

func (isolation deploymentGitIsolation) Close() error {
	if isolation.root == "" {
		return nil
	}

	return isolation.removeAll(isolation.root)
}

//nolint:cyclop // Every branch belongs to the one standard Git index-lock acquisition proof.
func createDeploymentIndexLockWithOperations(
	indexPath string,
	operations deploymentGitFileOperations,
) (_ [sha256.Size]byte, returnErr error) {
	source, err := operations.open(indexPath)
	if err != nil {
		return [sha256.Size]byte{}, errDeploymentEditInvalid
	}
	lockPath := indexPath + ".lock"
	lock, err := operations.openFile(
		lockPath, os.O_WRONLY|os.O_CREATE|os.O_EXCL, deploymentGitFileMode,
	)
	if err != nil {
		_ = source.Close()

		return [sha256.Size]byte{}, errDeploymentEditInvalid
	}
	keepLock := false
	defer func() {
		if keepLock {
			return
		}
		if removeErr := operations.remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, removeErr)
		}
	}()

	before, statErr := operations.stat(source)
	digest := sha256.New()
	_, copyErr := operations.copy(io.MultiWriter(lock, digest), source)
	after, afterStatErr := operations.stat(source)
	pathInfo, pathErr := operations.lstat(indexPath)
	syncErr := operations.sync(lock)
	closeLockErr := operations.close(lock)
	closeSourceErr := operations.close(source)
	if statErr != nil || copyErr != nil || afterStatErr != nil || pathErr != nil ||
		syncErr != nil || closeLockErr != nil || closeSourceErr != nil || before == nil || after == nil ||
		pathInfo == nil || !before.Mode().IsRegular() || !os.SameFile(before, after) ||
		!os.SameFile(after, pathInfo) || before.Size() != after.Size() ||
		!before.ModTime().Equal(after.ModTime()) {
		return [sha256.Size]byte{}, errors.Join(
			errDeploymentEditInvalid, statErr, copyErr, afterStatErr, pathErr,
			syncErr, closeLockErr, closeSourceErr,
		)
	}

	keepLock = true

	return [sha256.Size]byte(digest.Sum(nil)), nil
}

func deploymentIndexDigestWithOperations(
	indexPath string,
	operations deploymentGitFileOperations,
) (_ [sha256.Size]byte, returnErr error) {
	file, err := operations.open(indexPath)
	if err != nil {
		return [sha256.Size]byte{}, errDeploymentEditInvalid
	}
	defer func() { returnErr = errors.Join(returnErr, operations.close(file)) }()
	digest := sha256.New()
	if _, err = operations.copy(digest, file); err != nil {
		return [sha256.Size]byte{}, errors.Join(errDeploymentEditInvalid, err)
	}

	return [sha256.Size]byte(digest.Sum(nil)), nil
}

func deploymentIndexTree(ctx context.Context, repository, indexPath string) (string, error) {
	output, err := runGitProcess(
		ctx, repository, false, nil, []string{"GIT_INDEX_FILE=" + indexPath}, "write-tree",
	)
	tree := strings.TrimSpace(string(output))
	if err != nil || !validGitObjectID(tree) {
		return "", errDeploymentEditInvalid
	}

	return tree, nil
}

//nolint:cyclop,funlen // Blob transformation, validation, tree construction, and diff proof are one transaction.
func prepareDeploymentConfirmation(
	ctx context.Context,
	draft tuiDeploymentDraft,
) (_ tuiDeploymentConfirmation, noChanges bool, returnErr error) {
	transform, err := captureDeploymentGitTransform(ctx, draft.repository)
	if err != nil {
		return tuiDeploymentConfirmation{}, false, err
	}
	isolation, err := newDeploymentGitIsolation(ctx, draft.repository, transform, false)
	if err != nil {
		return tuiDeploymentConfirmation{}, false, err
	}
	defer func() { returnErr = errors.Join(returnErr, isolation.Close()) }()
	attributeSource := "--attr-source=" + draft.base.tree
	attributes, err := isolation.Run(
		ctx, nil, attributeSource, "check-attr", "-z", "--all", "--", draft.entry,
	)
	if err != nil || !deploymentAttributeOutputSafe(attributes, draft.entry) {
		return tuiDeploymentConfirmation{}, false, errDeploymentEditInvalid
	}
	if _, err = isolation.Run(ctx, nil, "read-tree", draft.base.tree); err != nil {
		return tuiDeploymentConfirmation{}, false, errDeploymentEditInvalid
	}
	object, err := isolation.Run(
		ctx, draft.candidate.Content,
		attributeSource, "hash-object", "-w", "--path="+draft.entry, "--stdin",
	)
	objectID := strings.TrimSpace(string(object))
	if err != nil || !validGitObjectID(objectID) {
		return tuiDeploymentConfirmation{}, false, errDeploymentEditInvalid
	}
	content, err := isolation.Run(ctx, nil, "cat-file", "blob", objectID)
	if err != nil {
		return tuiDeploymentConfirmation{}, false, errDeploymentEditInvalid
	}
	if err = validateDeploymentContent(ctx, draft, content); err != nil {
		return tuiDeploymentConfirmation{}, false, err
	}
	entry, found, err := readGitTreeEntry(ctx, draft.repository, draft.base.tree, draft.entry)
	if err != nil || !found || !entry.regularFile() {
		return tuiDeploymentConfirmation{}, false, errDeploymentEditInvalid
	}
	if _, err = isolation.Run(
		ctx, nil, "update-index", "--add", "--cacheinfo", entry.mode, objectID, draft.entry,
	); err != nil {
		return tuiDeploymentConfirmation{}, false, errDeploymentEditInvalid
	}
	tree, err := isolation.Run(ctx, nil, "write-tree")
	expectedTree := strings.TrimSpace(string(tree))
	if err != nil || !validGitObjectID(expectedTree) {
		return tuiDeploymentConfirmation{}, false, errDeploymentEditInvalid
	}
	confirmation := tuiDeploymentConfirmation{
		transform: transform, content: content, expectedTree: expectedTree,
	}
	if expectedTree == draft.base.tree {
		return confirmation, true, nil
	}
	diff, err := isolation.Run(
		ctx, nil, "diff", "--cached", draft.base.tree,
		"--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--", draft.entry,
	)
	if err != nil || len(diff) == 0 {
		return tuiDeploymentConfirmation{}, false, compose.ErrInvalidSource
	}
	confirmation.diff = diff

	return confirmation, false, nil
}

//nolint:cyclop,funlen,gocognit // This function owns one frozen Git transaction and its rollback.
func stageConfirmedDeploymentWith(
	ctx context.Context,
	draft tuiDeploymentDraft,
	replace func(string, string, []byte, []byte) (bool, error),
	operations deploymentGitFileOperations,
) (returnErr error) {
	transform, err := captureDeploymentGitTransform(ctx, draft.repository)
	if err != nil || !sameDeploymentGitTransform(transform, draft.confirmation.transform) {
		return errDeploymentEditInvalid
	}
	indexPath, err := absoluteGitPath(ctx, draft.repository, "index")
	if err != nil {
		return errDeploymentEditInvalid
	}
	originalDigest, err := createDeploymentIndexLockWithOperations(indexPath, operations)
	if err != nil {
		return err
	}
	lockPath := indexPath + ".lock"
	lockOwned := true
	worktreePublished := false
	isolationOpen := false
	var isolation deploymentGitIsolation
	defer func() {
		if isolationOpen {
			returnErr = errors.Join(returnErr, isolation.Close())
		}
		if worktreePublished && returnErr != nil {
			_, restoreErr := replace(
				draft.repository, draft.entry, draft.candidate.Content, draft.source.Content,
			)
			if restoreErr == nil {
				returnErr = errors.Join(errDeploymentPublishFailed, returnErr)
			} else {
				returnErr = errors.Join(errDeploymentWorktreeUnknown, returnErr, restoreErr)
			}
		}
		if lockOwned {
			if removeErr := operations.remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
				returnErr = errors.Join(returnErr, removeErr)
			}
		}
	}()
	head, err := resolveGitObject(ctx, draft.repository, "HEAD^{commit}")
	indexTree, treeErr := deploymentIndexTree(ctx, draft.repository, lockPath)
	if err != nil || treeErr != nil || head != draft.base.head || indexTree != draft.base.tree {
		return errors.Join(errDeploymentEditInvalid, err, treeErr)
	}
	isolation, err = newDeploymentGitIsolationWithOperations(
		ctx, draft.repository, transform, true, lockPath, operations,
	)
	if err != nil {
		return err
	}
	isolationOpen = true
	attributes, err := isolation.Run(
		ctx, nil, "--attr-source="+draft.base.tree,
		"check-attr", "-z", "--all", "--", draft.entry,
	)
	if err != nil || !deploymentAttributeOutputSafe(attributes, draft.entry) {
		return errDeploymentEditInvalid
	}
	worktreePublished, err = replace(
		draft.repository, draft.entry, draft.source.Content, draft.candidate.Content,
	)
	if err != nil {
		return err
	}
	if _, err = isolation.Run(
		ctx, nil, "--attr-source="+draft.base.tree, "add", "--", draft.entry,
	); err != nil {
		return err
	}
	transform, err = captureDeploymentGitTransform(ctx, draft.repository)
	if err != nil || !sameDeploymentGitTransform(transform, draft.confirmation.transform) {
		return errDeploymentEditInvalid
	}
	indexTree, err = deploymentIndexTree(ctx, draft.repository, lockPath)
	if err != nil || indexTree != draft.confirmation.expectedTree {
		return errors.Join(errDeploymentEditInvalid, err)
	}
	closeErr := isolation.Close()
	isolationOpen = false
	if closeErr != nil {
		return closeErr
	}
	if err = publishDeploymentIndex(
		ctx, draft, indexPath, lockPath, originalDigest, operations,
	); err != nil {
		return err
	}
	lockOwned = false

	return nil
}

func publishDeploymentIndex(
	ctx context.Context,
	draft tuiDeploymentDraft,
	indexPath string,
	lockPath string,
	originalDigest [sha256.Size]byte,
	operations deploymentGitFileOperations,
) error {
	currentDigest, digestErr := deploymentIndexDigestWithOperations(indexPath, operations)
	head, headErr := resolveGitObject(ctx, draft.repository, "HEAD^{commit}")
	if digestErr != nil || currentDigest != originalDigest || headErr != nil || head != draft.base.head {
		return errors.Join(errDeploymentEditInvalid, digestErr, headErr)
	}
	if err := operations.rename(lockPath, indexPath); err != nil {
		return fmt.Errorf("publish deployment index: %w", err)
	}

	return nil
}

func validateDeploymentContent(
	ctx context.Context,
	draft tuiDeploymentDraft,
	content []byte,
) error {
	source, err := draft.candidate.WithSemanticallyEquivalentEntryContent(content)
	if err != nil {
		return errDeploymentEditInvalid
	}
	project, err := compose.Load(ctx, source)
	if err != nil {
		return errDeploymentEditInvalid
	}
	if _, err = project.ServiceSpec(draft.request.Service); err != nil {
		return errDeploymentEditInvalid
	}

	return nil
}

func deploymentConfirmationMatches(
	confirmation tuiDeploymentConfirmation,
	content []byte,
	diff []byte,
	tree string,
) bool {
	return bytes.Equal(content, confirmation.content) &&
		bytes.Equal(diff, confirmation.diff) && tree == confirmation.expectedTree
}
