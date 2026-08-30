package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const (
	tuiTestDigest      = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	tuiTestServicePath = "services/api.yaml"
	tuiCommand         = "maniud tui"
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
		"", "docker", "docker ps", "docker run", "docker compose up", "https://" + image,
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
		draft: &draft,
		loadSource: func(context.Context, string) (compose.Source, error) {
			return committedTUIComposeSource(t, draft.generated.content), nil
		},
	}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	result, err := workspace.Commit(t.Context(), "Add api service", true)
	if err != nil || !result.Committed || result.NeedsUnsignedApproval || result.Request.Service != "api" {
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
	path := filepath.Join(gitOpsServicesDirectory, "api.yaml")
	content := []byte("services:\n  api:\n    image: registry.example/team/api@sha256:" + tuiTestDigest + "\n")

	return tuiServiceDraft{
		generated: generatedCompose{
			content: content, path: path, absolutePath: filepath.Join(repository, path),
			runtime: domain.RuntimeDocker, image: "registry.example/team/api@sha256:" + tuiTestDigest,
			service: "api",
		},
		repository: repository,
		branch:     gitOpsTestBranch,
		base:       base,
	}
}
