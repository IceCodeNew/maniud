package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
	"github.com/IceCodeNew/maniud/internal/tui"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const (
	tuiTestDigest        = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tuiTestServicePath   = "services/api.yaml"
	tuiTestCommitMessage = "Add api service"
	tuiCommand           = "maniud tui"
)

func TestTUIGenInvocationAcceptsOneNonExecutingServiceSource(t *testing.T) {
	t.Parallel()

	image := "registry.example/team/api@sha256:" + tuiTestDigest
	for _, input := range []string{
		"docker://" + image,
		"docker run --name api " + image,
		"podman create --name api " + image,
		"nerdctl run --name api " + image,
	} {
		invocation, err := tuiGenInvocation(input, t.TempDir())
		if err != nil || invocation.output != tuiTestServicePath {
			t.Fatalf("tuiGenInvocation(%q) = %#v, %v", input, invocation, err)
		}
	}
}

func TestTUIGenInvocationRejectsIncompleteOrAmbiguousInput(t *testing.T) {
	t.Parallel()

	image := "registry.example/team/api@sha256:" + tuiTestDigest
	for _, input := range []string{
		"", testDockerRuntime, "docker ps", "docker run", "docker compose up", "https://" + image,
		"docker run " + image + " && echo changed",
		"docker run " + image + "\npodman run " + image,
		"docker run \"unterminated",
		strings.Repeat("x", maximumTUIServiceInput+1),
	} {
		if _, err := tuiGenInvocation(input, t.TempDir()); !errors.Is(err, runtimeargv.ErrInvalid) {
			t.Fatalf("tuiGenInvocation(%q) error = %v", input, err)
		}
	}
}

func TestTUIWorkspaceStagesActualDiffAndDiscardsOwnedFiles(t *testing.T) {
	t.Parallel()

	draft := newTUIWorkspaceDraft(t)
	workspace := &tuiServiceWorkspace{draft: &draft}
	staged, err := workspace.Stage(t.Context())
	if err != nil || staged.ComposePath != tuiTestServicePath ||
		!strings.Contains(staged.Diff, "+    image: registry.example/team/api@sha256:"+tuiTestDigest) {
		t.Fatalf("Stage() = %#v, %v", staged, err)
	}
	if !verifyTUIStagedState(t.Context(), *workspace.staged) {
		t.Fatal("Stage() did not preserve its proven index state")
	}
	if err = workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	state, err := cleanGitTree(t.Context(), draft.repository)
	if err != nil || state != draft.base {
		t.Fatalf("discarded state = %#v, %v", state, err)
	}
	if _, err = os.Stat(draft.generated.absolutePath); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("discarded file error = %v", err)
	}
}

//nolint:cyclop // The assertions jointly prove commit parentage, identity, signature absence, and next steps.
func TestTUIWorkspaceCreatesProvenUnsignedCommit(t *testing.T) {
	t.Parallel()

	draft := newTUIWorkspaceDraft(t)
	workspace := &tuiServiceWorkspace{
		draft: &draft, runtimeBase: t.TempDir(),
	}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	result, err := workspace.Commit(t.Context(), "Add api service", true)
	if err != nil || !result.Committed || result.NeedsUnsignedApproval || result.ValidationUnavailable ||
		result.Request.Service != applyServiceValue || result.Request.Source.Repository == nil ||
		!result.Request.Repository.ValidFor(result.Request.Source.Repository.Digest) {
		t.Fatalf("Commit(unsigned) = %#v, %v", result, err)
	}
	head, err := resolveGitObject(t.Context(), draft.repository, "HEAD^{commit}")
	if err != nil || head == draft.base.head {
		t.Fatalf("committed HEAD = %q, %v", head, err)
	}
	metadata, err := runGit(t.Context(), draft.repository, "show", "-s", "--format=%P%x00%an%x00%ae%x00%G?", head)
	fields := bytes.Split(bytes.TrimSpace(metadata), []byte{0})
	if err != nil || len(fields) != 4 || string(fields[0]) != draft.base.head ||
		string(fields[1]) != "maniud" || string(fields[2]) != "maniud@localhost" || string(fields[3]) != "N" {
		t.Fatalf("unsigned commit metadata = %q, %v", metadata, err)
	}
	wantInstructions := []string{
		"git -C " + shellArgument(draft.repository) + " push origin '" + gitOpsTestBranch + "'",
		tuiCommand,
	}
	if instructions := workspace.Instructions(); !slices.Equal(instructions, wantInstructions) {
		t.Fatalf("Instructions() = %q, want %q", instructions, wantInstructions)
	}
}

//nolint:cyclop // Each assertion proves one step of the real signed-commit path.
func TestTUIWorkspaceCreatesProvenSignedCommit(t *testing.T) {
	home := t.TempDir()
	key := filepath.Join(home, "signing-key")
	//nolint:gosec // The executable and all options except the temporary output path are fixed.
	command := exec.CommandContext(
		t.Context(), "ssh-keygen", "-q", "-t", "ed25519", "-N", "", "-f", key,
	)
	if output, err := command.CombinedOutput(); err != nil {
		t.Fatalf("ssh-keygen error = %v: %s", err, output)
	}
	configuration := []byte(
		"[user]\n\tname = Maniud Tests\n\temail = maniud@example.invalid\n\tsigningKey = " + key +
			"\n[gpg]\n\tformat = ssh\n",
	)
	if err := os.WriteFile(filepath.Join(home, ".gitconfig"), configuration, 0o600); err != nil {
		t.Fatalf("WriteFile(.gitconfig) error = %v", err)
	}
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("SSH_AUTH_SOCK", "")

	draft := newTUIWorkspaceDraft(t)
	workspace := &tuiServiceWorkspace{draft: &draft, runtimeBase: t.TempDir()}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	result, err := workspace.Commit(t.Context(), "Add api service", false)
	if err != nil || !result.Committed || result.NeedsUnsignedApproval || result.ValidationUnavailable ||
		result.Request.Source.Repository == nil {
		t.Fatalf("Commit(signed) = %#v, %v", result, err)
	}
	head, err := resolveGitObject(t.Context(), draft.repository, "HEAD^{commit}")
	if err != nil || !gitCommitHasSignature(t.Context(), draft.repository, head) {
		t.Fatalf("signed HEAD = %q, %v", head, err)
	}
}

// This test isolates Git identity and signing configuration through process environment.
func TestTUIWorkspaceOffersUnsignedFallbackOnlyForUnchangedStage(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, "config"))
	t.Setenv("GNUPGHOME", filepath.Join(home, "gnupg"))
	t.Setenv("GPG_TTY", "")
	t.Setenv("SSH_AUTH_SOCK", "")

	draft := newTUIWorkspaceDraft(t)
	workspace := &tuiServiceWorkspace{draft: &draft}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	result, err := workspace.Commit(t.Context(), "Add api service", false)
	if err != nil || !result.NeedsUnsignedApproval || result.Committed ||
		!verifyTUIStagedState(t.Context(), *workspace.staged) {
		t.Fatalf("Commit(signed failure) = %#v, %v", result, err)
	}

	changed := append(slices.Clone(draft.generated.content), []byte("# drift\n")...)
	if err = os.WriteFile(draft.generated.absolutePath, changed, 0o600); err != nil {
		t.Fatalf("WriteFile(drift) error = %v", err)
	}
	if _, err = runGit(t.Context(), draft.repository, "add", "--", draft.generated.path); err != nil {
		t.Fatalf("git add drift error = %v", err)
	}
	if result, err = workspace.Commit(t.Context(), "Add api service", false); !errors.Is(
		err,
		compose.ErrInvalidSource,
	) || result.NeedsUnsignedApproval {
		t.Fatalf("Commit(drift) = %#v, %v", result, err)
	}
}

func TestTUIWorkspaceRejectsRepositorySignerOverride(t *testing.T) {
	t.Parallel()

	draft := newTUIWorkspaceDraft(t)
	workspace := &tuiServiceWorkspace{draft: &draft, runtimeBase: t.TempDir()}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if _, err := runGit(
		t.Context(), draft.repository, "config", "--local", "gpg.ssh.program", "hostile-signer",
	); err != nil {
		t.Fatalf("git config error = %v", err)
	}
	result, err := workspace.Commit(t.Context(), "Add api service", false)
	if !errors.Is(err, compose.ErrInvalidSource) || result.Committed || result.NeedsUnsignedApproval ||
		workspace.staged == nil {
		t.Fatalf("Commit(repository signer) = %#v, %v", result, err)
	}
	if err = commitTUIStaged(t.Context(), *workspace.staged, "Add api service", false); !errors.Is(
		err,
		compose.ErrInvalidSource,
	) {
		t.Fatalf("commitTUIStaged(repository signer) error = %v", err)
	}
}

func TestTUIWorkspaceProvesCommitAfterCallerCancellation(t *testing.T) {
	t.Parallel()

	draft := newTUIWorkspaceDraft(t)
	workspace := &tuiServiceWorkspace{draft: &draft, runtimeBase: t.TempDir()}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	ctx, cancel := context.WithCancel(t.Context())
	result, err := workspace.commitWith(
		ctx,
		"Add api service",
		true,
		func(ctx context.Context, staged tuiStagedService, message string, unsigned bool) error {
			commitErr := commitTUIStaged(ctx, staged, message, unsigned)
			cancel()

			return commitErr
		},
	)
	if err != nil || ctx.Err() == nil || !result.Committed || result.ValidationUnavailable ||
		result.Request.Source.Repository == nil {
		t.Fatalf("Commit(cancelled after commit) = %#v, %v, context %v", result, err, ctx.Err())
	}
}

func TestTUIWorkspaceRefusesToDiscardChangedGeneratedFile(t *testing.T) {
	t.Parallel()

	draft := newTUIWorkspaceDraft(t)
	workspace := &tuiServiceWorkspace{draft: &draft}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	changed := []byte("owned by another process\n")
	if err := os.WriteFile(draft.generated.absolutePath, changed, 0o600); err != nil {
		t.Fatalf("WriteFile(changed) error = %v", err)
	}
	if err := workspace.Discard(t.Context()); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("Discard(changed) error = %v", err)
	}
	content, err := os.ReadFile(draft.generated.absolutePath)
	if err != nil || !bytes.Equal(content, changed) {
		t.Fatalf("changed file = %q, %v", content, err)
	}
}

func TestRemoveGeneratedFileRequiresMatchingRegularFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	path := filepath.Join(root, "compose.yaml")
	if err := os.WriteFile(path, []byte("actual\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := removeGeneratedFile(generatedArtifact{path: path, content: []byte("expected\n")}); !errors.Is(
		err,
		compose.ErrInvalidSource,
	) {
		t.Fatalf("removeGeneratedFile(mismatch) error = %v", err)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("mismatched file was removed: %v", err)
	}
}

func TestTUIWorkspacePreviewsRegisteredRepositoryWithoutExecutingInput(t *testing.T) {
	t.Parallel()

	workspace, repository := newRegisteredTUIWorkspace(t)
	image := "registry.example/team/api@sha256:" + tuiTestDigest
	rendered := false
	workspace.render = func(
		_ context.Context,
		invocation genInvocation,
		dependencies genDependencies,
	) (generatedCompose, error) {
		rendered = true
		if dependencies.workingDirectory != repository || invocation.output != tuiTestServicePath {
			t.Fatalf("render input = %#v, working directory %q", invocation, dependencies.workingDirectory)
		}

		return generatedCompose{
			content: []byte("services:\n  api:\n    image: " + image + "\n"),
			path:    tuiTestServicePath, absolutePath: filepath.Join(repository, tuiTestServicePath),
			runtime: domain.RuntimeDocker, image: image, service: applyServiceValue,
		}, nil
	}
	draft, err := workspace.Preview(t.Context(), "docker://"+image)
	if err != nil || !rendered || draft != (tui.ServiceDraft{
		Runtime: testDockerRuntime, Image: image, Service: applyServiceValue, ComposePath: tuiTestServicePath,
	}) {
		t.Fatalf("Preview() = %#v, rendered %t, %v", draft, rendered, err)
	}
	if workspace.draft == nil || workspace.staged != nil || workspace.instructions != nil {
		t.Fatalf("Preview() workspace = %#v", workspace)
	}

	workspace.staged = &tuiStagedService{}
	if _, err = workspace.Preview(t.Context(), "docker://"+image); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("Preview(staged) error = %v", err)
	}
}

func TestTUIWorkspaceContainsPreviewPreparationFailures(t *testing.T) {
	t.Parallel()

	invalidEnvironment := map[string]string{homeKey: testRelativePath}
	invalidWorkspace := defaultTUIServiceWorkspace(invalidEnvironment, testRuntimePlugins(t))
	if invalidWorkspace.registrationPath != "" || invalidWorkspace.render == nil {
		t.Fatalf("invalid default workspace = %#v", invalidWorkspace)
	}
	if _, err := invalidWorkspace.Preview(t.Context(), "docker://example.invalid/image"); !errors.Is(
		err,
		compose.ErrInvalidSource,
	) {
		t.Fatalf("Preview(missing registration) error = %v", err)
	}

	workspace, _ := newRegisteredTUIWorkspace(t)
	if _, err := workspace.Preview(t.Context(), "docker ps"); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("Preview(invalid input) error = %v", err)
	}
	workspace.render = func(
		context.Context,
		genInvocation,
		genDependencies,
	) (generatedCompose, error) {
		return generatedCompose{}, io.ErrClosedPipe
	}
	if _, err := workspace.Preview(
		t.Context(), "docker://registry.example/team/api@sha256:"+tuiTestDigest,
	); !errors.Is(err, io.ErrClosedPipe) || !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("Preview(render failure) error = %v", err)
	}
	workspace.render = func(
		context.Context,
		genInvocation,
		genDependencies,
	) (generatedCompose, error) {
		return generatedCompose{service: applyServiceValue}, nil
	}
	if _, err := workspace.Preview(
		t.Context(), "docker://registry.example/team/api@sha256:"+tuiTestDigest,
	); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("Preview(invalid generation) error = %v", err)
	}
}

func TestTUIWorkspaceGeneratedServiceValidationAndProjection(t *testing.T) {
	t.Parallel()

	generated := generatedCompose{
		path: tuiTestServicePath, preparationPath: "services/api.prepare.sh",
		runtime: domain.RuntimeDocker, image: imageValue, service: applyServiceValue,
		warnings: []runtimeargv.Warning{{}},
	}
	if !validGeneratedTUIService(generated) || !validTUIArtifactPath(generated.path, generated.service) ||
		!validTUIPreparationPath(generated) {
		t.Fatalf("valid generated service rejected: %#v", generated)
	}
	if draft := publicServiceDraft(tuiServiceDraft{generated: generated}); draft != (tui.ServiceDraft{
		Runtime: testDockerRuntime, Image: imageValue, Service: applyServiceValue, ComposePath: tuiTestServicePath,
		Preparation: "services/api.prepare.sh", WarningCount: 1,
	}) {
		t.Fatalf("publicServiceDraft() = %#v", draft)
	}

	for _, invalid := range []generatedCompose{
		{},
		{path: tuiTestServicePath, runtime: domain.RuntimeDocker, image: imageValue, service: applyServiceValue,
			preparationPath: "services/wrong.prepare.sh"},
		{path: "services/wrong.yaml", runtime: domain.RuntimeDocker, image: imageValue, service: applyServiceValue},
	} {
		if validGeneratedTUIService(invalid) {
			t.Fatalf("invalid generated service accepted: %#v", invalid)
		}
	}
	if tuiRuntimeCommand([]string{"unknown", "run", imageValue}) ||
		tuiRuntimeCommand([]string{testDockerRuntime, "inspect", imageValue}) {
		t.Fatal("unsupported runtime command accepted")
	}
}

//nolint:cyclop,funlen // Each assertion proves a distinct Git transaction rejection or cleanup boundary.
func TestTUIWorkspaceContainsGitTransactionFailures(t *testing.T) {
	t.Parallel()

	workspace := &tuiServiceWorkspace{}
	if _, err := workspace.Stage(t.Context()); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("Stage(no draft) error = %v", err)
	}
	draft := newTUIWorkspaceDraft(t)
	workspace.draft = &draft
	workspace.staged = &tuiStagedService{}
	if _, err := workspace.Stage(t.Context()); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("Stage(already staged) error = %v", err)
	}
	workspace.staged = nil
	drift := append(slices.Clone(draft.generated.content), []byte("# drift\n")...)
	if err := os.WriteFile(filepath.Join(draft.repository, "dirty"), drift, 0o600); err != nil {
		t.Fatalf("WriteFile(dirty) error = %v", err)
	}
	if _, err := workspace.Stage(t.Context()); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("Stage(dirty tree) error = %v", err)
	}

	workspace = &tuiServiceWorkspace{draft: &draft}
	if _, err := workspace.Commit(t.Context(), "", false); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("Commit(no stage) error = %v", err)
	}
	if err := workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard(no stage) error = %v", err)
	}

	if exactStagedPaths(t.Context(), draft.repository, []string{testMissingName}) {
		t.Fatal("exactStagedPaths(missing) succeeded")
	}
	if _, err := stagedTUIDiff(t.Context(), draft.repository, []string{testMissingName}); !errors.Is(
		err,
		compose.ErrInvalidSource,
	) {
		t.Fatalf("stagedTUIDiff(empty) error = %v", err)
	}
	if _, err := writeGitTree(t.Context(), filepath.Join(draft.repository, testMissingName)); !errors.Is(
		err,
		compose.ErrInvalidSource,
	) {
		t.Fatalf("writeGitTree(missing) error = %v", err)
	}

	staged := tuiStagedService{draft: draft, paths: []string{draft.generated.path}}
	committed, unchanged := proveTUICommit(t.Context(), staged, tuiTestCommitMessage, false)
	if committed != "" || unchanged {
		t.Fatalf("proveTUICommit(invalid stage) = %q, %t", committed, unchanged)
	}
	if proveNewTUICommit(t.Context(), staged, draft.base.head, tuiTestCommitMessage, false) ||
		matchesTUICommit(t.Context(), staged, draft.base.head, tuiTestCommitMessage, false) {
		t.Fatal("invalid new commit proof succeeded")
	}
	if gitCommitHasSignature(t.Context(), draft.repository, draft.base.head) {
		t.Fatal("unsigned fixture commit reported a signature")
	}

	if _, err := committedTUIRequest(
		t.Context(), staged, gitOpsTestCommit, nil, t.TempDir(),
	); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("committedTUIRequest(invalid source) error = %v", err)
	}
}

//nolint:cyclop // Assertions cover the owned-file removal contract and its resulting instructions.
func TestTUIWorkspaceRemovesOwnedArtifactsAndBuildsInstructions(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	missing := filepath.Join(root, testMissingName)
	if err := removeGeneratedFile(generatedArtifact{path: missing, content: []byte("expected")}); err != nil {
		t.Fatalf("removeGeneratedFile(missing) error = %v", err)
	}
	directory := filepath.Join(root, "directory")
	if err := os.Mkdir(directory, 0o700); err != nil {
		t.Fatalf("Mkdir() error = %v", err)
	}
	if err := removeGeneratedFile(generatedArtifact{path: directory}); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("removeGeneratedFile(directory) error = %v", err)
	}
	path := filepath.Join(root, "owned")
	content := []byte("owned\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile(owned) error = %v", err)
	}
	if err := removeGeneratedFile(generatedArtifact{path: path, content: content}); err != nil {
		t.Fatalf("removeGeneratedFile(owned) error = %v", err)
	}

	generated := generatedCompose{
		absolutePath: path, content: content,
		preparationAbsolute: filepath.Join(root, "prepare.sh"), preparation: []byte("prepare\n"),
	}
	if err := os.WriteFile(generated.absolutePath, generated.content, 0o600); err != nil {
		t.Fatalf("WriteFile(compose) error = %v", err)
	}
	if err := os.WriteFile(generated.preparationAbsolute, generated.preparation, 0o600); err != nil {
		t.Fatalf("WriteFile(preparation) error = %v", err)
	}
	if err := removeGeneratedFiles(generated); err != nil {
		t.Fatalf("removeGeneratedFiles() error = %v", err)
	}

	draft := tuiServiceDraft{generated: generated, repository: root, branch: "feature's"}
	instructions := commitInstructions(draft)
	if len(instructions) != 3 || !strings.HasPrefix(instructions[0], "sudo sh '") ||
		!strings.Contains(instructions[1], "feature'\"'\"'s") || instructions[2] != tuiCommand {
		t.Fatalf("commitInstructions() = %q", instructions)
	}
}

func TestTUIWorkspaceContainsDependencyAndRepositoryInspectionFailures(t *testing.T) {
	t.Parallel()

	workspace, repository := newRegisteredTUIWorkspace(t)
	workspace.dependencies = func(
		map[string]string,
		io.Writer,
		func() (string, error),
		runtimeplugin.Set,
	) (genDependencies, error) {
		return genDependencies{}, io.ErrClosedPipe
	}
	if _, err := workspace.Preview(t.Context(), "docker run registry.example/team/api:latest"); !errors.Is(
		err,
		io.ErrClosedPipe,
	) {
		t.Fatalf("Preview(dependency failure) error = %v", err)
	}

	registration, err := readGitOpsRegistration(workspace.registrationPath)
	if err != nil {
		t.Fatalf("readGitOpsRegistration() error = %v", err)
	}
	registration.Repository = filepath.Join(repository, testMissingName)
	if err := os.Remove(workspace.registrationPath); err != nil {
		t.Fatalf("Remove(registration) error = %v", err)
	}
	if err := writeGitOpsRegistration(workspace.registrationPath, registration); err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}
	if _, _, _, err := registeredTUIWorkspace(t.Context(), workspace.registrationPath); err == nil {
		t.Fatal("registeredTUIWorkspace(missing repository) succeeded")
	}

	workspace, repository = newRegisteredTUIWorkspace(t)
	if _, err := runGit(t.Context(), repository, "remote", "remove", gitOpsRemoteName); err != nil {
		t.Fatalf("git remote remove error = %v", err)
	}
	if _, err := workspace.Preview(t.Context(), "docker run registry.example/team/api:latest"); !errors.Is(
		err,
		errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("Preview(missing remote) error = %v", err)
	}
}

func TestTUIWorkspaceContainsStableStageFailures(t *testing.T) {
	t.Parallel()

	t.Run("write generated file", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		draft.generated.path = "missing/api.yaml"
		draft.generated.absolutePath = filepath.Join(draft.repository, draft.generated.path)
		if _, _, _, err := stageTUIService(t.Context(), draft); err == nil {
			t.Fatal("stageTUIService(write failure) succeeded")
		}
	})

	t.Run("git add", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		draft.generated.path = "missing.yaml"
		if _, _, _, err := stageTUIService(t.Context(), draft); err == nil {
			t.Fatal("stageTUIService(git add failure) succeeded")
		}
	})

	t.Run("exact staged paths", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		draft.generated.preparationPath = draft.generated.path
		if _, _, _, err := stageTUIService(t.Context(), draft); !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("stageTUIService(duplicate path) error = %v", err)
		}
	})

	t.Run("staged diff", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		if _, _, _, err := stageTUIServiceWith(
			t.Context(), draft,
			func(context.Context, string, []string) ([]byte, error) { return nil, io.ErrClosedPipe },
			writeGitTree,
		); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("stageTUIServiceWith(diff failure) error = %v", err)
		}
	})

	t.Run("write tree", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		if _, _, _, err := stageTUIServiceWith(
			t.Context(), draft, stagedTUIDiff,
			func(context.Context, string) (string, error) { return "", io.ErrClosedPipe },
		); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("stageTUIServiceWith(write-tree failure) error = %v", err)
		}
	})
}

//nolint:cyclop,funlen // Subtests exercise independent Git commit and discard failure boundaries.
func TestTUIWorkspaceContainsCommitAndDiscardBoundaryFailures(t *testing.T) {
	t.Parallel()

	t.Run("committed source", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		workspace := &tuiServiceWorkspace{draft: &draft}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		result, err := workspace.Commit(t.Context(), "Add api service", true)
		if err != nil || !result.Committed || !result.ValidationUnavailable {
			t.Fatalf("Commit(missing runtime base) = %#v, %v", result, err)
		}
	})

	t.Run("commit command", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		workspace := &tuiServiceWorkspace{draft: &draft}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		result, err := workspace.commitWith(
			t.Context(), "Add api service", true,
			func(context.Context, tuiStagedService, string, bool) error { return io.ErrClosedPipe },
		)
		if !errors.Is(err, io.ErrClosedPipe) || result.NeedsUnsignedApproval || result.Committed {
			t.Fatalf("Commit(command failure) = %#v, %v", result, err)
		}
	})

	t.Run("unproven commit", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		workspace := &tuiServiceWorkspace{draft: &draft}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		if _, err := workspace.commitWith(
			t.Context(), "Add api service", true,
			func(context.Context, tuiStagedService, string, bool) error { return nil },
		); !errors.Is(
			err,
			compose.ErrInvalidSource,
		) {
			t.Fatalf("Commit(unproven commit) error = %v", err)
		}
	})

	t.Run("cancelled commit command", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		workspace := &tuiServiceWorkspace{draft: &draft}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		ctx, cancel := context.WithCancel(t.Context())
		result, err := workspace.commitWith(
			ctx, "Add api service", true,
			func(context.Context, tuiStagedService, string, bool) error {
				cancel()

				return nil
			},
		)
		if !errors.Is(err, context.Canceled) || result.Committed || result.NeedsUnsignedApproval {
			t.Fatalf("Commit(cancelled command) = %#v, %v", result, err)
		}
	})

	t.Run("discard removal", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		workspace := &tuiServiceWorkspace{draft: &draft}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		services := filepath.Join(draft.repository, gitOpsServicesDirectory)
		if err := os.Chmod(services, 0o500); err != nil { //nolint:gosec // The test removes parent write access.
			t.Fatalf("Chmod(services) error = %v", err)
		}
		t.Cleanup(func() {
			_ = os.Chmod(services, 0o700) //nolint:gosec // Cleanup restores private owner access.
		})
		if err := workspace.Discard(t.Context()); err == nil {
			t.Fatal("Discard(read-only generated file) succeeded")
		}
	})

	t.Run("discard checkout drift", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		writeGitOpsTestCommit(t, draft.repository, "drift", "drift\n", "advance checkout")
		if err := discardTUIStaged(t.Context(), draft, nil); !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("discardTUIStaged(checkout drift) error = %v", err)
		}
	})
}

//nolint:cyclop,funlen // Assertions jointly prove path identity, parentage, tree identity, and signature policy.
func TestTUIWorkspaceChecksPathsAndCommitProof(t *testing.T) {
	t.Parallel()

	draft := newTUIWorkspaceDraft(t)
	paths := generatedRelativePaths(draft.generated)
	if len(paths) != 1 || paths[0] != draft.generated.path {
		t.Fatalf("generatedRelativePaths(compose) = %q", paths)
	}
	draft.generated.preparationPath = "scripts/prepare-api.sh"
	paths = generatedRelativePaths(draft.generated)
	if !slices.Equal(paths, []string{draft.generated.preparationPath, draft.generated.path}) {
		t.Fatalf("generatedRelativePaths(preparation) = %q", paths)
	}
	if exact := exactStagedPaths(t.Context(), filepath.Join(draft.repository, testMissingName), paths); exact {
		t.Fatal("exactStagedPaths(missing repository) succeeded")
	}
	if exact := exactStagedPaths(t.Context(), draft.repository, paths); exact {
		t.Fatal("exactStagedPaths(clean repository) succeeded")
	}
	if committed, unchanged := proveTUICommit(t.Context(), tuiStagedService{draft: tuiServiceDraft{
		repository: filepath.Join(draft.repository, testMissingName),
	}}, tuiTestCommitMessage, false); committed != "" || unchanged {
		t.Fatalf("proveTUICommit(missing repository) = %q, %t", committed, unchanged)
	}

	draft.generated.preparationPath = ""
	stagedPaths, _, expectedTree, err := stageTUIService(t.Context(), draft)
	if err != nil {
		t.Fatalf("stageTUIService() error = %v", err)
	}
	staged := tuiStagedService{draft: draft, paths: stagedPaths, expectedTree: expectedTree}
	if _, err := runGit(
		t.Context(),
		draft.repository,
		"-c", "user.name=Maniud Tests",
		"-c", "user.email=maniud@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--no-gpg-sign", "-m", "add api",
	); err != nil {
		t.Fatalf("git commit error = %v", err)
	}
	head, err := resolveGitObject(t.Context(), draft.repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve HEAD error = %v", err)
	}
	if matchesTUICommit(t.Context(), staged, draft.base.head, "add api", false) {
		t.Fatal("matchesTUICommit(previous commit) succeeded")
	}
	mismatchedTree := staged
	mismatchedTree.expectedTree = draft.base.tree
	if matchesTUICommit(t.Context(), mismatchedTree, head, "add api", false) {
		t.Fatal("matchesTUICommit(mismatched tree) succeeded")
	}
	committed, unchanged := proveTUICommit(t.Context(), mismatchedTree, "add api", false)
	if committed != "" || unchanged {
		t.Fatalf("proveTUICommit(mismatched tree) = %q, %t", committed, unchanged)
	}
	if !matchesTUICommit(t.Context(), staged, head, "add api", false) {
		t.Fatal("matchesTUICommit(exact unsigned commit) failed")
	}
	if matchesTUICommit(t.Context(), staged, head, "different message", false) {
		t.Fatal("matchesTUICommit(mismatched message) succeeded")
	}
	if matchesTUICommit(t.Context(), staged, head, "add api", true) {
		t.Fatal("matchesTUICommit(unsigned commit) succeeded with required signature")
	}
	if _, err := committedTUIRequest(
		t.Context(), staged, head, nil, testRelativePath,
	); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("committedTUIRequest(relative runtime base) error = %v", err)
	}
}

func TestCommittedTUIRequestRejectsUnstableSources(t *testing.T) {
	t.Parallel()

	t.Run("missing bind source", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		draft.generated.content = append(
			draft.generated.content,
			[]byte("    volumes:\n      - ./missing:/data:ro\n")...,
		)
		staged, head := committedTUIStagedFixture(t, draft)
		if _, err := committedTUIRequest(
			t.Context(), staged, head, nil, t.TempDir(),
		); !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("committedTUIRequest(missing bind) error = %v", err)
		}
	})

	t.Run("checkout drift", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		staged, _ := committedTUIStagedFixture(t, draft)
		if _, err := committedTUIRequest(
			t.Context(), staged, draft.base.head, nil, t.TempDir(),
		); !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("committedTUIRequest(checkout drift) error = %v", err)
		}
	})

	t.Run("mismatched source path", func(t *testing.T) {
		t.Parallel()

		draft := newTUIWorkspaceDraft(t)
		staged, head := committedTUIStagedFixture(t, draft)
		staged.draft.generated.absolutePath = filepath.Join(draft.repository, "services", "other.yaml")
		if _, err := committedTUIRequest(
			t.Context(), staged, head, nil, t.TempDir(),
		); !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("committedTUIRequest(mismatched path) error = %v", err)
		}
	})
}

func committedTUIStagedFixture(t *testing.T, draft tuiServiceDraft) (tuiStagedService, string) {
	t.Helper()

	paths, _, expectedTree, err := stageTUIService(t.Context(), draft)
	if err != nil {
		t.Fatalf("stageTUIService() error = %v", err)
	}
	staged := tuiStagedService{draft: draft, paths: paths, expectedTree: expectedTree}
	if err = commitTUIStaged(t.Context(), staged, "Add api service", true); err != nil {
		t.Fatalf("commitTUIStaged() error = %v", err)
	}
	head, err := resolveGitObject(t.Context(), draft.repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve HEAD error = %v", err)
	}

	return staged, head
}

//nolint:cyclop // Assertions exercise each generated-file identity and removal boundary.
func TestTUIWorkspaceContainsGeneratedFileRemovalFailures(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := removeGeneratedFile(generatedArtifact{
		path: filepath.Join(root, testMissingName, "api.yaml"), content: []byte(applyServiceValue),
	}); err == nil {
		t.Fatal("removeGeneratedFile(missing parent) succeeded")
	}

	locked := filepath.Join(root, "locked")
	if err := os.Mkdir(locked, 0o700); err != nil {
		t.Fatalf("Mkdir(locked) error = %v", err)
	}
	lockedPath := filepath.Join(locked, "api.yaml")
	lockedContent := []byte("api\n")
	if err := os.WriteFile(lockedPath, lockedContent, 0o600); err != nil {
		t.Fatalf("WriteFile(locked) error = %v", err)
	}
	if err := os.Chmod(locked, 0o500); err != nil { //nolint:gosec // The test removes parent write access.
		t.Fatalf("Chmod(locked) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(locked, 0o700) //nolint:gosec // Cleanup restores private owner access.
	})
	if err := removeGeneratedFile(generatedArtifact{path: lockedPath, content: lockedContent}); err == nil {
		t.Fatal("removeGeneratedFile(read-only parent) succeeded")
	}

	first := filepath.Join(root, "first")
	second := filepath.Join(root, "second")
	if err := os.WriteFile(first, []byte("same"), 0o600); err != nil {
		t.Fatalf("WriteFile(first) error = %v", err)
	}
	if err := os.WriteFile(second, []byte("same"), 0o600); err != nil {
		t.Fatalf("WriteFile(second) error = %v", err)
	}
	info, err := os.Lstat(first)
	if err != nil {
		t.Fatalf("Lstat(first) error = %v", err)
	}
	opened, err := os.OpenRoot(root)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	defer func() { _ = opened.Close() }()
	if matches, err := generatedFileMatches(opened, testMissingName, info, []byte("same")); err == nil || matches {
		t.Fatalf("generatedFileMatches(missing) = %t, %v", matches, err)
	}
	if matches, err := generatedFileMatches(opened, "second", info, []byte("same")); err != nil || matches {
		t.Fatalf("generatedFileMatches(identity mismatch) = %t, %v", matches, err)
	}
}

func newRegisteredTUIWorkspace(t *testing.T) (*tuiServiceWorkspace, string) {
	t.Helper()

	repository := initGitOpsTestRepository(t)
	if err := os.Mkdir(filepath.Join(repository, gitOpsServicesDirectory), 0o700); err != nil {
		t.Fatalf("Mkdir(services) error = %v", err)
	}
	head, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve HEAD error = %v", err)
	}
	home := t.TempDir()
	environment := map[string]string{homeKey: home, xdgStateHomeKey: filepath.Join(home, "state")}
	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	if err := writeGitOpsRegistration(gitOpsRegistrationPath(statePath), gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: repository, Branch: gitOpsTestBranch,
		Remote: gitOpsRemoteName, BaselineCommit: head,
	}); err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}

	return defaultTUIServiceWorkspace(environment, testRuntimePlugins(t)), repository
}

func newTUIWorkspaceDraft(t *testing.T) tuiServiceDraft {
	t.Helper()

	repository := initGitOpsTestRepository(t)
	if err := os.Mkdir(filepath.Join(repository, gitOpsServicesDirectory), 0o700); err != nil {
		t.Fatalf("Mkdir(services) error = %v", err)
	}
	base, err := cleanGitTree(t.Context(), repository)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	repositoryScope, err := compose.NewRepositoryScope(repository, repository, gitOpsTestBranch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	path := filepath.Join(gitOpsServicesDirectory, "api.yaml")
	content := []byte("services:\n  api:\n    image: registry.example/team/api@sha256:" + tuiTestDigest + "\n")

	return tuiServiceDraft{
		generated: generatedCompose{
			content: content, path: path, absolutePath: filepath.Join(repository, path),
			runtime: domain.RuntimeDocker, image: "registry.example/team/api@sha256:" + tuiTestDigest,
			service: applyServiceValue,
		},
		repository: repository, repositoryScope: repositoryScope,
		branch: gitOpsTestBranch, base: base,
	}
}
