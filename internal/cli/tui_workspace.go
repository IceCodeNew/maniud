package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
	"github.com/IceCodeNew/maniud/internal/tui"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const (
	maximumTUICommitMessage = 200
	maximumTUIServiceInput  = 64 << 10
	minimumRuntimeArguments = 3
	tuiDraftSuffix          = ".swp"
)

type tuiServiceDraft struct {
	generated       generatedCompose
	repository      string
	repositoryScope compose.RepositoryScope
	branch          string
	base            gitTreeState
	recovered       bool
}

type tuiStagedService struct {
	draft        tuiServiceDraft
	paths        []string
	expectedTree string
}

type tuiStagedProof struct {
	repository      string
	base            gitTreeState
	paths           []string
	indexStatus     string
	expectedTree    string
	attributeSource string
}

type tuiServiceWorkspace struct {
	mu               sync.Mutex
	registrationPath string
	environment      map[string]string
	runtimes         runtimeplugin.Set
	runtimeBase      string
	dependencies     func(map[string]string, io.Writer, func() (string, error), runtimeplugin.Set) (genDependencies, error)
	render           func(context.Context, genInvocation, genDependencies) (generatedCompose, error)
	draft            *tuiServiceDraft
	staged           *tuiStagedService
	instructions     []string
}

func defaultTUIServiceWorkspace(
	environment map[string]string,
	runtimes runtimeplugin.Set,
) *tuiServiceWorkspace {
	statePath, err := defaultStatePath(environment)
	if err != nil {
		return &tuiServiceWorkspace{
			environment: environment, runtimes: runtimes,
			dependencies: defaultGenDependencies, render: renderGen,
		}
	}

	return &tuiServiceWorkspace{
		registrationPath: gitOpsRegistrationPath(statePath),
		environment:      environment,
		runtimes:         runtimes,
		runtimeBase:      filepath.Dir(statePath),
		dependencies:     defaultGenDependencies,
		render:           renderGen,
	}
}

func (workspace *tuiServiceWorkspace) Preview(ctx context.Context, input string) (tui.ServiceDraft, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged != nil {
		return tui.ServiceDraft{}, runtimeargv.ErrInvalid
	}
	draft, err := prepareTUIServiceDraft(ctx, input, workspace)
	if err != nil {
		return tui.ServiceDraft{}, err
	}
	workspace.draft = &draft
	workspace.staged = nil
	workspace.instructions = nil

	return publicServiceDraft(draft), nil
}

func (workspace *tuiServiceWorkspace) Stage(ctx context.Context) (tui.StagedService, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	if workspace.draft == nil || workspace.staged != nil {
		return tui.StagedService{}, runtimeargv.ErrInvalid
	}
	draft := *workspace.draft
	paths, diff, expectedTree, err := stageTUIService(ctx, draft)
	if err != nil {
		if verifyTUIDraftState(context.WithoutCancel(ctx), draft) {
			draft.recovered = true
			workspace.draft = &draft
		}

		return tui.StagedService{}, err
	}
	staged := tuiStagedService{draft: draft, paths: paths, expectedTree: expectedTree}
	workspace.staged = &staged

	return tui.StagedService{
		Diff:          string(diff),
		ComposePath:   filepath.ToSlash(draft.generated.path),
		Preparation:   filepath.ToSlash(draft.generated.preparationPath),
		CommitMessage: "Add " + draft.generated.service + " service",
	}, nil
}

func stageTUIService(ctx context.Context, draft tuiServiceDraft) ([]string, []byte, string, error) {
	return stageTUIServiceWith(ctx, draft, stagedTUIDiff, writeGitTree)
}

//nolint:cyclop // Staging keeps publication, index ownership, and rollback proof in one ordered transaction.
func stageTUIServiceWith(
	ctx context.Context,
	draft tuiServiceDraft,
	diffStaged func(context.Context, string, []string) ([]byte, error),
	writeTree func(context.Context, string) (string, error),
) ([]string, []byte, string, error) {
	if !verifyTUIBaseState(ctx, draft) {
		return nil, nil, "", compose.ErrInvalidSource
	}
	paths := generatedRelativePaths(draft.generated)
	if !verifyTUIDraftState(ctx, draft) {
		if draft.recovered {
			return nil, nil, "", compose.ErrInvalidSource
		}
		state, err := gitTreeAllowingTUIDrafts(ctx, draft.repository)
		if err != nil || state != draft.base {
			return nil, nil, "", compose.ErrInvalidSource
		}
		if err = writeTUIDraftFiles(draft.generated); err != nil {
			return nil, nil, "", err
		}
	}
	if err := promoteTUIDraftFiles(draft.generated); err != nil {
		return nil, nil, "", errors.Join(err, restoreTUIDraftFiles(draft.generated))
	}
	if _, err := runGit(ctx, draft.repository, append([]string{"add", "--"}, paths...)...); err != nil {
		return nil, nil, "", errors.Join(err, suspendTUIStaged(ctx, draft, paths))
	}
	if !exactStagedPaths(ctx, draft.repository, paths) {
		return nil, nil, "", errors.Join(compose.ErrInvalidSource, suspendTUIStaged(ctx, draft, paths))
	}
	diff, err := diffStaged(ctx, draft.repository, paths)
	if err != nil {
		return nil, nil, "", errors.Join(err, suspendTUIStaged(ctx, draft, paths))
	}
	expectedTree, err := writeTree(ctx, draft.repository)
	if err != nil {
		return nil, nil, "", errors.Join(err, suspendTUIStaged(ctx, draft, paths))
	}
	staged := tuiStagedService{draft: draft, paths: paths, expectedTree: expectedTree}
	if !verifyTUIStagedState(ctx, staged) {
		return nil, nil, "", errors.Join(compose.ErrInvalidSource, suspendTUIStaged(ctx, draft, paths))
	}

	return paths, diff, expectedTree, nil
}

func stagedTUIDiff(ctx context.Context, repository string, paths []string) ([]byte, error) {
	diff, err := runGit(
		ctx,
		repository,
		append(
			[]string{"diff", "--cached", "--no-ext-diff", "--no-textconv", "--no-renames", "--binary", "--"},
			paths...,
		)...,
	)
	if err != nil || len(diff) == 0 {
		return nil, compose.ErrInvalidSource
	}

	return diff, nil
}

func generatedRelativePaths(generated generatedCompose) []string {
	paths := []string{generated.path}
	if generated.preparationPath != "" {
		paths = append(paths, generated.preparationPath)
	}
	slices.Sort(paths)

	return paths
}

func exactStagedPaths(ctx context.Context, repository string, paths []string) bool {
	expected := make([]string, len(paths))
	for index, path := range paths {
		expected[index] = "A  " + filepath.ToSlash(path)
	}

	return gitStatusMatchesWithTUIDrafts(ctx, repository, expected)
}

func exactStagedPathStatusWithAttributes(
	ctx context.Context,
	repository string,
	paths []string,
	indexStatus string,
	attributeSource string,
) bool {
	arguments := []string{}
	if attributeSource != "" {
		arguments = append(arguments, "--attr-source="+attributeSource)
	}
	arguments = append(
		arguments,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none",
	)
	status, err := runGit(ctx, repository, arguments...)
	if err != nil {
		return false
	}
	entries, valid := splitNullTerminated(status)
	if !valid || len(entries) != len(paths) {
		return false
	}
	expected := make([]string, len(paths))
	for index, path := range paths {
		expected[index] = indexStatus + "  " + filepath.ToSlash(path)
	}
	actual := make([]string, len(entries))
	for index, entry := range entries {
		actual[index] = string(entry)
	}
	slices.Sort(actual)

	return slices.Equal(actual, expected)
}

func verifyTUIBaseState(ctx context.Context, draft tuiServiceDraft) bool {
	branch, err := currentGitBranch(ctx, draft.repository)
	if err != nil || branch != draft.branch {
		return false
	}
	head, err := resolveGitObject(ctx, draft.repository, "HEAD^{commit}")
	if err != nil || head != draft.base.head {
		return false
	}
	tree, err := resolveGitObject(ctx, draft.repository, "HEAD^{tree}")

	return err == nil && tree == draft.base.tree
}

func verifyTUIDraftState(ctx context.Context, draft tuiServiceDraft) bool {
	paths := generatedTUIDraftRelativePaths(draft.generated)

	return verifyTUIBaseState(ctx, draft) &&
		exactTUIDraftPaths(ctx, draft.repository, paths) &&
		generatedTUIDraftFilesMatch(draft.generated)
}

func exactTUIDraftPaths(ctx context.Context, repository string, paths []string) bool {
	expected := make([]string, len(paths))
	for index, path := range paths {
		expected[index] = "?? " + filepath.ToSlash(path)
	}

	return gitStatusMatchesWithTUIDrafts(ctx, repository, expected)
}

func gitTreeAllowingTUIDrafts(ctx context.Context, repository string) (gitTreeState, error) {
	if !gitStatusMatchesWithTUIDrafts(ctx, repository, nil) {
		return gitTreeState{}, compose.ErrInvalidSource
	}
	head, err := resolveGitObject(ctx, repository, "HEAD^{commit}")
	if err != nil {
		return gitTreeState{}, compose.ErrInvalidSource
	}
	tree, err := resolveGitObject(ctx, repository, head+"^{tree}")
	if err != nil {
		return gitTreeState{}, compose.ErrInvalidSource
	}

	return gitTreeState{head: head, tree: tree}, nil
}

func gitStatusMatchesWithTUIDrafts(ctx context.Context, repository string, expected []string) bool {
	status, err := runGit(
		ctx,
		repository,
		"status", "--porcelain=v1", "-z", "--untracked-files=all", "--ignore-submodules=none",
	)
	if err != nil {
		return false
	}
	entries, valid := splitNullTerminated(status)
	if !valid {
		return false
	}
	remaining := make(map[string]struct{}, len(expected))
	for _, entry := range expected {
		remaining[entry] = struct{}{}
	}
	if len(remaining) != len(expected) {
		return false
	}
	for _, raw := range entries {
		entry := string(raw)
		if _, found := remaining[entry]; found {
			delete(remaining, entry)

			continue
		}
		if !validTUIDraftStatusEntry(repository, entry) {
			return false
		}
	}

	return missingTUIStatusEntriesAreIgnored(ctx, repository, remaining)
}

func missingTUIStatusEntriesAreIgnored(
	ctx context.Context,
	repository string,
	remaining map[string]struct{},
) bool {
	for entry := range remaining {
		path, found := strings.CutPrefix(entry, "?? ")
		if !found || !gitPathIgnored(ctx, repository, path) {
			return false
		}
	}

	return true
}

func gitPathIgnored(ctx context.Context, repository, path string) bool {
	_, err := runGit(ctx, repository, "check-ignore", "--quiet", "--", filepath.FromSlash(path))

	return err == nil
}

func validTUIDraftStatusEntry(repository, entry string) bool {
	const untrackedPrefix = "?? "
	path, found := strings.CutPrefix(entry, untrackedPrefix)
	if !found {
		return false
	}
	path = filepath.FromSlash(path)
	if filepath.Dir(path) != gitOpsServicesDirectory || !validTUIDraftName(filepath.Base(path)) {
		return false
	}
	root, err := os.OpenRoot(filepath.Join(repository, gitOpsServicesDirectory))
	if err != nil {
		return false
	}
	defer func() { _ = root.Close() }()
	info, err := root.Lstat(filepath.Base(path))

	return err == nil && info != nil && info.Mode().IsRegular()
}

func validTUIDraftName(name string) bool {
	if !strings.HasPrefix(name, ".") || !strings.HasSuffix(name, tuiDraftSuffix) {
		return false
	}
	final := strings.TrimSuffix(strings.TrimPrefix(name, "."), tuiDraftSuffix)
	service := strings.TrimSuffix(final, ".yaml")
	if service == final {
		service = strings.TrimSuffix(final, ".prepare.sh")
	}

	return service != "" && service != final && !strings.ContainsAny(service, "\x00\r\n")
}

func writeGitTree(ctx context.Context, repository string) (string, error) {
	output, err := runGit(ctx, repository, "write-tree")
	value := strings.TrimSpace(string(output))
	if err != nil || !validGitObjectID(value) {
		return "", compose.ErrInvalidSource
	}

	return value, nil
}

func (workspace *tuiServiceWorkspace) Commit(
	ctx context.Context,
	message string,
	unsigned bool,
) (tui.CommitResult, error) {
	return workspace.commitWith(ctx, message, unsigned, commitTUIStagedProof)
}

//nolint:funcorder // Commit delegates to this injected command boundary while retaining the transaction lock.
func (workspace *tuiServiceWorkspace) commitWith(
	ctx context.Context,
	message string,
	unsigned bool,
	commit func(context.Context, tuiStagedProof, string, bool) error,
) (tui.CommitResult, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	if workspace.staged == nil || !validTUICommitMessage(message) {
		return tui.CommitResult{}, runtimeargv.ErrInvalid
	}
	staged := *workspace.staged
	if !verifyTUIStagedState(ctx, staged) {
		return tui.CommitResult{}, compose.ErrInvalidSource
	}
	if !unsigned {
		if err := validateGitProcessConfiguration(ctx, staged.draft.repository); err != nil {
			return tui.CommitResult{}, err
		}
	}

	commitErr := commit(ctx, staged.proof(), message, unsigned)

	return workspace.settleCommit(ctx, staged, message, unsigned, commitErr)
}

//nolint:funcorder // settleCommit completes commitWith while the transaction lock is held.
func (workspace *tuiServiceWorkspace) settleCommit(
	ctx context.Context,
	staged tuiStagedService,
	message string,
	unsigned bool,
	commitErr error,
) (tui.CommitResult, error) {
	proofCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
	defer cancel()
	committedHead, unchanged := proveTUICommit(proofCtx, staged, message, !unsigned)
	if committedHead != "" {
		workspace.staged = nil
		workspace.draft = nil
		workspace.instructions = commitInstructions(staged.draft)
		if staged.draft.generated.preparationAbsolute != "" {
			return tui.CommitResult{Outcome: tui.CommitPreparationRequired}, nil
		}
		request, requestErr := committedTUIRequest(
			proofCtx, staged, committedHead, workspace.environment, workspace.runtimeBase,
		)
		if requestErr != nil {
			return tui.CommitResult{Outcome: tui.CommitValidationUnavailable}, nil
		}

		return tui.CommitResult{Request: request, Outcome: tui.CommitSucceeded}, nil
	}
	cancelErr := ctx.Err()
	needsUnsigned, outcomeErr := resolveUnsettledTUICommit(cancelErr, commitErr, unsigned, unchanged)
	if outcomeErr != nil {
		if cancelErr != nil {
			return tui.CommitResult{}, fmt.Errorf("commit cancelled: %w", outcomeErr)
		}

		return tui.CommitResult{}, outcomeErr
	}
	if needsUnsigned {
		return tui.CommitResult{Outcome: tui.CommitNeedsUnsignedApproval}, nil
	}

	return tui.CommitResult{}, compose.ErrInvalidSource
}

func (workspace *tuiServiceWorkspace) Suspend(ctx context.Context) error {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	if workspace.staged == nil {
		return nil
	}
	staged := *workspace.staged
	if !verifyTUIStagedState(ctx, staged) {
		return compose.ErrInvalidSource
	}
	if err := suspendTUIStaged(ctx, staged.draft, staged.paths); err != nil {
		return err
	}
	staged.draft.recovered = true
	workspace.draft = &staged.draft
	workspace.staged = nil

	return nil
}

func suspendTUIStaged(
	ctx context.Context,
	draft tuiServiceDraft,
	paths []string,
) error {
	cleanupCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
	defer cancel()
	if !verifyTUIBaseState(cleanupCtx, draft) {
		return compose.ErrInvalidSource
	}
	_, resetErr := runGit(
		cleanupCtx,
		draft.repository,
		append([]string{"reset", "--quiet", "HEAD", "--"}, paths...)...,
	)
	if resetErr != nil {
		return resetErr
	}
	if err := restoreTUIDraftFiles(draft.generated); err != nil {
		return err
	}
	if !verifyTUIDraftState(cleanupCtx, draft) {
		return compose.ErrInvalidSource
	}

	return nil
}

func (workspace *tuiServiceWorkspace) Instructions() []string {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	return slices.Clone(workspace.instructions)
}
