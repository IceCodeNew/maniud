package cli

import (
	"bytes"
	"context"
	"io"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

const (
	handoffBranch        = "review/it's-ready"
	handoffReloadFailure = "reload failure"
	handoffUnregistered  = "unregistered"
)

func TestDeploymentCommitEmitsPostTerminalHandoff(t *testing.T) {
	t.Parallel()
	for _, mode := range []string{"edit", "restore", handoffReloadFailure, handoffUnregistered} {
		t.Run(mode, func(t *testing.T) {
			t.Parallel()
			assertDeploymentPostTerminalHandoff(t, mode)
		})
	}
}

//nolint:cyclop,funlen // The CLI callback proves terminal restoration, export, and commit handoff ordering together.
func assertDeploymentPostTerminalHandoff(t *testing.T, mode string) {
	t.Helper()
	fixture, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := runGit(t.Context(), repository, "checkout", "-b", handoffBranch); err != nil {
		t.Fatal(err)
	}
	if _, err := runGit(t.Context(), repository, "remote", "add", gitOpsRemoteName, repository); err != nil {
		t.Fatal(err)
	}
	head, err := resolveGitObject(t.Context(), repository, "HEAD")
	if err != nil {
		t.Fatal(err)
	}
	working := t.TempDir()
	environment := map[string]string{
		homeKey: working, xdgStateHomeKey: filepath.Join(working, "state"),
		testTermEnvironment: testTerminalName, languageEnvironment: tuiTestLocale,
	}
	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatal(err)
	}
	if mode != handoffUnregistered {
		request = registerDeploymentHandoff(t, gitOpsRegistrationPath(statePath), request, repository, head)
	}
	output := new(bytes.Buffer)
	var notifications processNotifications
	err = executeProductionTUIWith(t.Context(),
		terminalFixture{Reader: bytes.NewReader(nil), descriptor: 1},
		terminalFixture{Writer: output, descriptor: 2}, io.Discard, environment,
		func() (string, error) { return working, nil }, testRuntimePlugins(t), &notifications,
		func(uintptr) bool { return true },
		func(ctx context.Context, _ io.Reader, screen io.Writer, _ tui.Catalog, _ tui.ServiceWorkspace,
			deployments tui.DeploymentWorkspace, _ tui.Operations, _ *tui.EventStream, _ tui.Options,
		) (tui.Result, error) {
			workspace, ok := deployments.(*tuiDeploymentWorkspace)
			if !ok {
				t.Fatal("unexpected deployment workspace")
			}
			workspace.environment = fixture.environment
			workspace.runtimeBase = fixture.runtimeBase
			if _, previewErr := workspace.Preview(ctx, request, application.DeploymentCPUs.ID(), "2", false); previewErr != nil {
				t.Fatal(previewErr)
			}
			staged, stageErr := workspace.Stage(ctx)
			if stageErr != nil {
				t.Fatal(stageErr)
			}
			result, commitErr := workspace.commitWith(ctx, staged.CommitMessage, true,
				func(commitCtx context.Context, proof tuiStagedProof, message string, unsigned bool) error {
					if mode == handoffReloadFailure {
						workspace.runtimeBase = testRelativePath
					}

					return commitTUIStagedProof(commitCtx, proof, message, unsigned)
				})
			if commitErr != nil || !result.Committed || result.ValidationUnavailable != (mode == handoffReloadFailure) {
				t.Fatalf("commit = %#v, %v", result, commitErr)
			}
			if mode == "restore" {
				commitDeploymentHandoffRestore(ctx, t, workspace, result.Request, head)
			}
			if strings.Contains(output.String(), "Next steps:") {
				t.Fatal("instructions appeared before terminal restoration")
			}
			_, _ = io.WriteString(screen, "terminal restored\n")

			return tui.Result{Export: "session export\n"}, nil
		})
	notifications.Close()
	want := "terminal restored\nsession export\n"
	if mode != handoffUnregistered {
		want += "Next steps:\n$ git -C " + shellArgument(repository) +
			" push origin 'review/it'\"'\"'s-ready'\n$ maniud tui\n"
	}
	if err != nil || output.String() != want {
		t.Fatalf("post-terminal handoff = %q, %v; want %q", output.String(), err, want)
	}
}

func registerDeploymentHandoff(
	t *testing.T, path string, request application.Request, repository, head string,
) application.Request {
	t.Helper()
	if err := writeGitOpsRegistration(path, gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: repository, Branch: handoffBranch,
		Remote: gitOpsRemoteName, BaselineCommit: head,
	}); err != nil {
		t.Fatal(err)
	}
	scope, err := compose.NewRepositoryScope(repository, repository, handoffBranch)
	if err != nil {
		t.Fatal(err)
	}
	request.Repository, err = scope.Bind(deploymentComposeEntry, request.Source.Repository.Digest)
	if err != nil {
		t.Fatal(err)
	}

	return request
}

func commitDeploymentHandoffRestore(
	ctx context.Context, t *testing.T, workspace *tuiDeploymentWorkspace, request application.Request, head string,
) {
	t.Helper()
	if _, err := workspace.PreviewRestore(ctx, request, head); err != nil {
		t.Fatal(err)
	}
	staged, err := workspace.Stage(ctx)
	if err != nil {
		t.Fatal(err)
	}
	result, err := workspace.Commit(ctx, staged.CommitMessage, true)
	if err != nil || !result.Committed {
		t.Fatalf("restore commit = %#v, %v", result, err)
	}
}
