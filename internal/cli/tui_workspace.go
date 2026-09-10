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
	"sync"

	"github.com/mattn/go-shellwords"

	"github.com/IceCodeNew/maniud/internal/application"
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

//nolint:cyclop // Preview keeps repository proof, generation, and draft recovery in one ordered transaction.
func prepareTUIServiceDraft(
	ctx context.Context,
	input string,
	workspace *tuiServiceWorkspace,
) (tuiServiceDraft, error) {
	registration, repository, base, err := registeredTUIWorkspace(ctx, workspace.registrationPath)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	repositoryScope, err := gitOpsRepositoryScope(ctx, registration, repository)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	arguments, err := tuiGenInvocation(input, repository)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	dependencies, err := workspace.dependencies(
		workspace.environment,
		io.Discard,
		func() (string, error) { return repository, nil },
		workspace.runtimes,
	)
	if err != nil {
		return tuiServiceDraft{}, err
	}
	generated, err := workspace.render(ctx, arguments, dependencies)
	if err != nil || !validGeneratedTUIService(generated) {
		return tuiServiceDraft{}, errors.Join(err, runtimeargv.ErrInvalid)
	}
	draft := tuiServiceDraft{
		generated: generated, repository: repository, repositoryScope: repositoryScope,
		branch: registration.Branch, base: base,
	}
	if err = recoverPartialTUIDraftPublication(ctx, repository, generated); err != nil {
		return tuiServiceDraft{}, err
	}
	state, err := gitTreeAllowingTUIDrafts(ctx, repository)
	if err != nil || state != base {
		return tuiServiceDraft{}, compose.ErrInvalidSource
	}
	if verifyTUIDraftState(ctx, draft) {
		draft.recovered = true

		return draft, nil
	}
	if err = writeTUIDraftFiles(generated); err != nil {
		return tuiServiceDraft{}, err
	}
	if !verifyTUIDraftState(context.WithoutCancel(ctx), draft) {
		return tuiServiceDraft{}, compose.ErrInvalidSource
	}

	return draft, nil
}

func registeredTUIWorkspace(
	ctx context.Context,
	registrationPath string,
) (gitOpsRegistration, string, gitTreeState, error) {
	registration, err := readGitOpsRegistration(registrationPath)
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, compose.ErrInvalidSource
	}
	repository, err := resolveGitOpsRepository(ctx, registration.Repository)
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, err
	}
	branch, err := currentGitBranch(ctx, repository)
	if err != nil || branch != registration.Branch {
		return gitOpsRegistration{}, "", gitTreeState{}, errGitOpsRepositoryInvalid
	}
	head, err := resolveGitObject(ctx, repository, "HEAD^{commit}")
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, errGitOpsRepositoryInvalid
	}
	tree, err := resolveGitObject(ctx, repository, "HEAD^{tree}")
	if err != nil {
		return gitOpsRegistration{}, "", gitTreeState{}, errGitOpsRepositoryInvalid
	}

	return registration, repository, gitTreeState{head: head, tree: tree}, nil
}

func validGeneratedTUIService(generated generatedCompose) bool {
	return generated.service != "" && generated.image != "" && generated.runtime != "" &&
		validTUIArtifactPath(generated.path, generated.service) &&
		(generated.preparationPath == "" || validTUIPreparationPath(generated))
}

func tuiGenInvocation(input, workingDirectory string) (genInvocation, error) {
	invocation, err := parseTUIServiceInput(input)
	if err != nil {
		return genInvocation{}, err
	}
	projection, err := parseGenProjection(invocation, workingDirectory)
	if err != nil {
		return genInvocation{}, err
	}
	invocation.output = filepath.Join(gitOpsServicesDirectory, projection.Name()+".yaml")

	return invocation, nil
}

func parseTUIServiceInput(input string) (genInvocation, error) {
	arguments, err := tokenizeTUIServiceInput(input)
	if err != nil {
		return genInvocation{}, err
	}

	invocation := genInvocation{}
	switch {
	case len(arguments) == 1 && strings.Contains(arguments[0], "://"):
		invocation.source = arguments[0]
	case tuiRuntimeCommand(arguments):
		invocation.runtimeArgs = arguments
	default:
		return genInvocation{}, runtimeargv.ErrInvalid
	}

	return invocation, nil
}

func tokenizeTUIServiceInput(input string) ([]string, error) {
	if input == "" || len(input) > maximumTUIServiceInput || strings.ContainsAny(input, "\x00\r\n") {
		return nil, runtimeargv.ErrInvalid
	}
	parser := shellwords.NewParser()
	arguments, err := parser.Parse(input)
	if err != nil || len(arguments) == 0 || parser.Position >= 0 {
		return nil, runtimeargv.ErrInvalid
	}

	return arguments, nil
}

func tuiRuntimeCommand(arguments []string) bool {
	if len(arguments) < minimumRuntimeArguments {
		return false
	}
	switch arguments[0] {
	case "docker", "podman", nerdctlRuntimeCommand:
		return arguments[1] == "create" || arguments[1] == "run"
	default:
		return false
	}
}

func validTUIArtifactPath(path, service string) bool {
	return path == filepath.Join(gitOpsServicesDirectory, service+".yaml")
}

func validTUIPreparationPath(generated generatedCompose) bool {
	want := strings.TrimSuffix(generated.path, filepath.Ext(generated.path)) + ".prepare.sh"

	return generated.preparationPath == want
}

func publicServiceDraft(draft tuiServiceDraft) tui.ServiceDraft {
	return tui.ServiceDraft{
		Runtime:      draft.generated.runtime.String(),
		Image:        draft.generated.image,
		Service:      draft.generated.service,
		ComposePath:  filepath.ToSlash(draft.generated.path),
		Preparation:  filepath.ToSlash(draft.generated.preparationPath),
		WarningCount: len(draft.generated.warnings),
		Recovered:    draft.recovered,
	}
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
		append([]string{"diff", "--cached", "--no-ext-diff", "--no-renames", "--binary", "--"}, paths...)...,
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
		if _, duplicate := remaining[entry]; duplicate {
			return false
		}
		remaining[entry] = struct{}{}
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

	return len(remaining) == 0
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
		if !generatedArtifactMatches(generatedArtifact{
			path: tuiDraftPath(artifact.path), content: artifact.content,
		}) {
			return compose.ErrInvalidSource
		}
		if err := moveTUIArtifact(tuiDraftPath(artifact.path), artifact.path); err != nil {
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
			result = errors.Join(result, moveTUIArtifact(artifact.path, draft.path))
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
		if !finalUntracked || !draftUntracked {
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

func moveTUIArtifact(from, to string) (returnErr error) {
	return moveTUIArtifactWithOperations(from, to, generatedComposeDefaultOperations())
}

func moveTUIArtifactWithOperations(
	from string,
	to string,
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
	if err = operations.link(root, fromName, toName); err != nil {
		return compose.ErrInvalidSource
	}
	if err = operations.syncDirectory(directory); err != nil {
		return errors.Join(compose.ErrInvalidSource, err)
	}
	if err = operations.remove(root, fromName); err != nil {
		removeErr := operations.remove(root, toName)
		syncErr := operations.syncDirectory(directory)

		return errors.Join(compose.ErrInvalidSource, err, removeErr, syncErr)
	}
	if err = operations.syncDirectory(directory); err != nil {
		return errors.Join(compose.ErrInvalidSource, err)
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
	if !generatedArtifactMatches(artifact) {
		return compose.ErrInvalidSource
	}
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
	if err := operations.remove(root, filepath.Base(artifact.path)); err != nil {
		return compose.ErrInvalidSource
	}
	if err := operations.syncDirectory(directory); err != nil {
		return errors.Join(compose.ErrInvalidSource, err)
	}

	return nil
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

func exactStagedPaths(ctx context.Context, repository string, paths []string) bool {
	expected := make([]string, len(paths))
	for index, path := range paths {
		expected[index] = "A  " + filepath.ToSlash(path)
	}

	return gitStatusMatchesWithTUIDrafts(ctx, repository, expected)
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
) (tui.ServiceCommitResult, error) {
	return workspace.commitWith(ctx, message, unsigned, commitTUIStaged)
}

//nolint:funcorder // Commit delegates to this injected command boundary while retaining the transaction lock.
func (workspace *tuiServiceWorkspace) commitWith(
	ctx context.Context,
	message string,
	unsigned bool,
	commit func(context.Context, tuiStagedService, string, bool) error,
) (tui.ServiceCommitResult, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	if workspace.staged == nil || !validTUICommitMessage(message) {
		return tui.ServiceCommitResult{}, runtimeargv.ErrInvalid
	}
	staged := *workspace.staged
	if !verifyTUIStagedState(ctx, staged) {
		return tui.ServiceCommitResult{}, compose.ErrInvalidSource
	}
	if !unsigned {
		if err := validateGitProcessConfiguration(ctx, staged.draft.repository); err != nil {
			return tui.ServiceCommitResult{}, err
		}
	}

	commitErr := commit(ctx, staged, message, unsigned)

	return workspace.settleCommit(ctx, staged, message, unsigned, commitErr)
}

//nolint:funcorder // settleCommit completes commitWith while the transaction lock is held.
func (workspace *tuiServiceWorkspace) settleCommit(
	ctx context.Context,
	staged tuiStagedService,
	message string,
	unsigned bool,
	commitErr error,
) (tui.ServiceCommitResult, error) {
	proofCtx := context.WithoutCancel(ctx)
	committedHead, unchanged := proveTUICommit(proofCtx, staged, message, !unsigned)
	if committedHead != "" {
		workspace.staged = nil
		workspace.draft = nil
		workspace.instructions = commitInstructions(staged.draft)
		if staged.draft.generated.preparationAbsolute != "" {
			return tui.ServiceCommitResult{Committed: true, PreparationRequired: true}, nil
		}
		request, requestErr := committedTUIRequest(
			proofCtx, staged, committedHead, workspace.environment, workspace.runtimeBase,
		)
		if requestErr != nil {
			//nolint:nilerr // Commit proof succeeded; only immutable validation is unavailable.
			return tui.ServiceCommitResult{Committed: true, ValidationUnavailable: true}, nil
		}

		return tui.ServiceCommitResult{Request: request, Committed: true}, nil
	}
	if err := ctx.Err(); err != nil {
		return tui.ServiceCommitResult{}, fmt.Errorf("commit cancelled: %w", err)
	}
	if !unsigned && unchanged {
		return tui.ServiceCommitResult{NeedsUnsignedApproval: true}, nil
	}
	if commitErr != nil {
		return tui.ServiceCommitResult{}, commitErr
	}

	return tui.ServiceCommitResult{}, compose.ErrInvalidSource
}

func commitTUIStaged(ctx context.Context, staged tuiStagedService, message string, unsigned bool) error {
	if unsigned {
		_, err := runGit(
			ctx,
			staged.draft.repository,
			"-c", "user.name=maniud",
			"-c", "user.email=maniud@localhost",
			"-c", "commit.gpgsign=false",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", message,
		)

		return err
	}
	if err := validateGitProcessConfiguration(ctx, staged.draft.repository); err != nil {
		return err
	}
	_, err := runGitWithUserConfig(
		ctx, staged.draft.repository, "commit", "--quiet", "-S", "--no-verify", "-m", message,
	)

	return err
}

func validTUICommitMessage(message string) bool {
	return message != "" && len(message) <= maximumTUICommitMessage &&
		strings.TrimSpace(message) == message && !strings.ContainsAny(message, "\x00\r\n")
}

func verifyTUIStagedState(ctx context.Context, staged tuiStagedService) bool {
	if !verifyTUIBaseState(ctx, staged.draft) ||
		!exactStagedPaths(ctx, staged.draft.repository, staged.paths) {
		return false
	}
	tree, err := writeGitTree(ctx, staged.draft.repository)

	return err == nil && tree == staged.expectedTree
}

func proveTUICommit(
	ctx context.Context,
	staged tuiStagedService,
	message string,
	requireSignature bool,
) (string, bool) {
	head, err := resolveGitObject(ctx, staged.draft.repository, "HEAD^{commit}")
	if err != nil {
		return "", false
	}
	if head == staged.draft.base.head {
		return "", verifyTUIStagedState(ctx, staged)
	}
	if !proveNewTUICommit(ctx, staged, head, message, requireSignature) {
		return "", false
	}

	return head, false
}

func proveNewTUICommit(
	ctx context.Context,
	staged tuiStagedService,
	head string,
	message string,
	requireSignature bool,
) bool {
	if !matchesTUICommit(ctx, staged, head, message, requireSignature) {
		return false
	}
	branch, err := currentGitBranch(ctx, staged.draft.repository)
	if err != nil || branch != staged.draft.branch {
		return false
	}
	state, err := gitTreeAllowingTUIDrafts(ctx, staged.draft.repository)

	return err == nil && state.head == head && state.tree == staged.expectedTree
}

func matchesTUICommit(
	ctx context.Context,
	staged tuiStagedService,
	head string,
	message string,
	requireSignature bool,
) bool {
	parents, err := runGit(ctx, staged.draft.repository, "rev-list", "--parents", "-n", "1", head)
	fields := strings.Fields(string(parents))
	if err != nil || len(fields) != 2 || fields[0] != head || fields[1] != staged.draft.base.head {
		return false
	}
	tree, err := resolveGitObject(ctx, staged.draft.repository, head+"^{tree}")
	if err != nil || tree != staged.expectedTree {
		return false
	}
	if !gitCommitMatches(ctx, staged.draft.repository, head, message, requireSignature) {
		return false
	}

	return true
}

func gitCommitMatches(ctx context.Context, repository, head, message string, requireSignature bool) bool {
	commit, err := runGit(ctx, repository, "cat-file", "commit", head)
	header, body, found := bytes.Cut(commit, []byte("\n\n"))
	if err != nil || !found || string(body) != message+"\n" {
		return false
	}

	return !requireSignature || bytes.Contains(header, []byte("\ngpgsig "))
}

func gitCommitHasSignature(ctx context.Context, repository, head string) bool {
	commit, err := runGit(ctx, repository, "cat-file", "commit", head)

	return err == nil && bytes.Contains(commit, []byte("\ngpgsig "))
}

//nolint:cyclop // Readback keeps immutable source, checkout, runtime, and provenance proofs ordered.
func committedTUIRequest(
	ctx context.Context,
	staged tuiStagedService,
	head string,
	environment map[string]string,
	runtimeBase string,
) (application.Request, error) {
	entry := filepath.ToSlash(staged.draft.generated.path)
	if !validGitObjectID(head) || !validGitObjectID(staged.expectedTree) ||
		!staged.draft.repositoryScope.Valid() || !onTUIBranch(ctx, staged.draft) {
		return application.Request{}, compose.ErrInvalidSource
	}
	source, err := compose.CaptureRepositorySource(
		staged.draft.repository,
		entry,
		environment,
		func(name string) (compose.RepositoryFile, bool, error) {
			return readCommittedGitFile(ctx, staged.draft.repository, staged.expectedTree, name)
		},
		func(name string) (compose.RepositoryPathSnapshot, error) {
			return readCommittedGitPath(ctx, staged.draft.repository, staged.expectedTree, name)
		},
	)
	if err != nil {
		return application.Request{}, compose.ErrInvalidSource
	}
	after, err := gitTreeAllowingTUIDrafts(ctx, staged.draft.repository)
	if err != nil || after != (gitTreeState{head: head, tree: staged.expectedTree}) ||
		!onTUIBranch(ctx, staged.draft) {
		return application.Request{}, compose.ErrInvalidSource
	}
	source, err = compose.PinRepositoryRuntime(source, runtimeBase)
	if err != nil {
		return application.Request{}, compose.ErrInvalidSource
	}
	provenance, err := bindApplyRepositorySource(
		staged.draft.repository,
		staged.draft.generated.absolutePath,
		staged.draft.repositoryScope,
		source,
	)
	if err != nil {
		return application.Request{}, compose.ErrInvalidSource
	}

	return application.Request{
		Source: source, Service: staged.draft.generated.service, Repository: provenance,
	}, nil
}

func onTUIBranch(ctx context.Context, draft tuiServiceDraft) bool {
	branch, err := currentGitBranch(ctx, draft.repository)

	return err == nil && branch == draft.branch
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

func generatedFileMatches(root *os.Root, name string, info os.FileInfo, expected []byte) (bool, error) {
	file, err := root.Open(name)
	if err != nil {
		return false, fmt.Errorf("open generated file: %w", err)
	}
	content, readErr := io.ReadAll(io.LimitReader(file, int64(len(expected)+1)))
	current, statErr := file.Stat()
	closeErr := file.Close()
	after, lstatErr := root.Lstat(name)
	if readErr != nil || statErr != nil || closeErr != nil || lstatErr != nil ||
		!os.SameFile(info, current) || !os.SameFile(info, after) {
		return false, errors.Join(readErr, statErr, closeErr, lstatErr)
	}

	return bytes.Equal(content, expected), nil
}

func commitInstructions(draft tuiServiceDraft) []string {
	var instructions []string
	if draft.generated.preparationAbsolute != "" {
		instructions = append(instructions, "sudo sh "+shellArgument(draft.generated.preparationAbsolute))
	}
	instructions = append(
		instructions,
		"git -C "+shellArgument(draft.repository)+" push origin "+shellArgument(draft.branch),
		"maniud tui",
	)

	return instructions
}

func shellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}

func (workspace *tuiServiceWorkspace) Instructions() []string {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	return slices.Clone(workspace.instructions)
}
