package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/tui"
)

const (
	gitAutocrlfKey = "autocrlf"
	gitSafecrlfKey = "safecrlf"
)

func TestDeploymentGitConfigurationPreservesValues(t *testing.T) {
	t.Parallel()
	for _, key := range []string{gitAutocrlfKey, gitSafecrlfKey, "eol", "checkRoundtripEncoding"} {
		for _, suffix := range []string{"", " =", " = true", " = false"} {
			t.Run(key+suffix, func(t *testing.T) {
				t.Parallel()
				_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
				appendDeploymentGitConfiguration(t, repository, "config", "[core]\n"+key+suffix+"\n")
				arguments, err := deploymentGitConfiguration(t.Context(), repository)
				if err != nil {
					t.Fatal(err)
				}
				want := "core." + strings.ToLower(key)
				if suffix != "" {
					want += "=" + strings.TrimSpace(strings.TrimPrefix(suffix, " ="))
				} else if key == gitAutocrlfKey || key == gitSafecrlfKey {
					want += "=true"
				}
				if !slices.Equal(arguments, []string{"-c", want}) {
					t.Fatalf("configuration = %q, want %q", arguments, want)
				}
				assertDeploymentGitHashReplay(t, repository, arguments)
			})
		}
	}
}

func assertDeploymentGitHashReplay(t *testing.T, repository string, arguments []string) {
	t.Helper()
	other := t.TempDir()
	if _, err := runGit(t.Context(), other, "init", "--quiet"); err != nil {
		t.Fatal(err)
	}
	command := []string{"hash-object", "--stdin", "--path", "sample.txt"}
	content := []byte("one\r\ntwo\r\n")
	native, nativeErr := runGitProcess(t.Context(), repository, false, content, nil, command...)
	replayed, replayErr := runGitProcess(t.Context(), other, false, content, nil, append(arguments, command...)...)
	if (nativeErr != nil) != (replayErr != nil) || !bytes.Equal(native, replayed) {
		t.Fatalf("native = %q, %v; replay = %q, %v", native, nativeErr, replayed, replayErr)
	}
}

func TestDeploymentGitConfigurationPreservesPrecedence(t *testing.T) {
	t.Parallel()
	_, _, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := runGit(t.Context(), repository, "config", "extensions.worktreeConfig", "true"); err != nil {
		t.Fatal(err)
	}
	appendDeploymentGitConfiguration(t, repository, "config", "[core]\nautocrlf = false\nautocrlf\n")
	appendDeploymentGitConfiguration(t, repository, "config.worktree", "[core]\nautocrlf = true\nautocrlf =\n")
	arguments, err := deploymentGitConfiguration(t.Context(), repository)
	if err != nil {
		t.Fatal(err)
	}
	want := []string{"-c", "core.autocrlf=false", "-c", "core.autocrlf=true",
		"-c", "core.autocrlf=true", "-c", "core.autocrlf="}
	if !slices.Equal(arguments, want) {
		t.Fatalf("scope and duplicate order = %q, want %q", arguments, want)
	}
	assertDeploymentGitHashReplay(t, repository, arguments)
	actual, err := runGit(t.Context(), repository, "config", "--bool", "core.autocrlf")
	if err != nil || string(actual) != "false\n" {
		t.Fatalf("worktree precedence = %q, %v", actual, err)
	}
}

func TestDeploymentRejectsValuelessConfigurationDrift(t *testing.T) {
	t.Parallel()
	for _, key := range []string{gitAutocrlfKey, gitSafecrlfKey} {
		t.Run(key, func(t *testing.T) {
			t.Parallel()
			workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
			if _, err := workspace.Preview(t.Context(), request, application.DeploymentCPUs.ID(), "2", false); err != nil {
				t.Fatal(err)
			}
			appendDeploymentGitConfiguration(t, repository, "config", "[core]\n"+key+"\n")
			if _, err := workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
				t.Fatalf("Stage accepted valueless configuration drift: %v", err)
			}
			assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
		})
	}
}

//nolint:cyclop // The preview, stage, commit, and exact blob assertions share one real CRLF repository.
func TestDeploymentValuelessAutocrlfCommitsPreviewedBlob(t *testing.T) {
	t.Parallel()
	crlf := bytes.ReplaceAll(deploymentComposeFixture(), []byte("\n"), []byte("\r\n"))
	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, crlf)
	appendDeploymentGitConfiguration(t, repository, "config", "[core]\nautocrlf\n")
	preview, err := workspace.Preview(t.Context(), request,
		application.DeploymentCPUs.ID(), "2", false)
	if err != nil {
		t.Fatal(err)
	}
	expectedTree := workspace.draft.confirmation.expectedTree
	staged, err := workspace.Stage(t.Context())
	if err != nil || staged.Diff != preview.Diff {
		t.Fatalf("Stage = %#v, %v; preview = %#v", staged, err, preview)
	}
	result, err := workspace.Commit(t.Context(), staged.CommitMessage, true)
	if err != nil || result.Outcome != tui.CommitSucceeded {
		t.Fatalf("Commit = %#v, %v", result, err)
	}
	actualTree, err := resolveGitObject(t.Context(), repository, "HEAD^{tree}")
	if err != nil || actualTree != expectedTree {
		t.Fatalf("committed tree = %q, %v; previewed %q", actualTree, err, expectedTree)
	}
	blob, err := runGit(t.Context(), repository, "show", "HEAD:"+deploymentComposeEntry)
	if err != nil || bytes.Contains(blob, []byte("\r")) || !bytes.Contains(blob, []byte("cpus: 2 # CPU budget\n")) {
		t.Fatalf("committed blob = %q, %v", blob, err)
	}
}

//nolint:gosec // The caller supplies a test-owned repository and a literal Git configuration filename.
func appendDeploymentGitConfiguration(t *testing.T, repository, name, configuration string) {
	t.Helper()
	path := filepath.Join(repository, ".git", name)
	content, err := os.ReadFile(path)
	if err != nil && !errors.Is(err, os.ErrNotExist) {
		t.Fatal(err)
	}
	if err = os.WriteFile(path, append(content, []byte("\n"+configuration)...), 0o600); err != nil {
		t.Fatal(err)
	}
}
