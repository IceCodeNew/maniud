package cli

import (
	"bytes"
	"errors"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
)

//nolint:cyclop,funlen,gocyclo // The test keeps the edit, commit, history, and restore proof sequence contiguous.
func TestTUIDeploymentWorkspaceCommitsAndRestoresTrackedEdit(t *testing.T) {
	t.Parallel()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	initial, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve initial HEAD error = %v", err)
	}
	fields, err := workspace.Fields(t.Context(), request)
	if err != nil || len(fields) != len(application.DeploymentFields()) ||
		fields[0].ID != application.DeploymentCPUs.ID() || fields[0].Value != "1" || !fields[0].Present {
		t.Fatalf("Fields() = %#v, %v", fields, err)
	}
	preview, err := workspace.Preview(
		t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
	)
	if err != nil || preview.ComposePath != deploymentComposeEntry ||
		len(preview.FieldIDs) != 1 || preview.FieldIDs[0] != application.DeploymentCPUs.ID() {
		t.Fatalf("Preview() = %#v, %v", preview, err)
	}
	assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
	if _, err = cleanGitTree(t.Context(), repository); err != nil {
		t.Fatalf("Preview() changed repository: %v", err)
	}

	staged, err := workspace.Stage(t.Context())
	if err != nil || staged.ComposePath != deploymentComposeEntry ||
		!strings.Contains(staged.Diff, "+    cpus: 2.5") {
		t.Fatalf("Stage() = %#v, %v", staged, err)
	}
	result, err := workspace.Commit(t.Context(), staged.CommitMessage, true)
	if err != nil || !result.Committed || result.ValidationUnavailable || result.NeedsUnsignedApproval {
		t.Fatalf("Commit() = %#v, %v", result, err)
	}
	project, err := compose.Load(t.Context(), result.Request.Source)
	if err != nil {
		t.Fatalf("Load(committed request) error = %v", err)
	}
	spec, err := project.ServiceSpec("api")
	if err != nil || spec.CPUs != testDeploymentCPUValue {
		t.Fatalf("committed CPUs = %q, %v", spec.CPUs, err)
	}

	history, err := workspace.History(t.Context(), result.Request)
	if err != nil || len(history) != 2 || history[0].Subject != staged.CommitMessage ||
		history[0].SignaturePresent {
		t.Fatalf("History() = %#v, %v", history, err)
	}
	restore, err := workspace.PreviewRestore(t.Context(), result.Request, initial)
	if err != nil || restore.Restore != initial || restore.ComposePath != deploymentComposeEntry {
		t.Fatalf("PreviewRestore() = %#v, %v", restore, err)
	}
	restoredStage, err := workspace.Stage(t.Context())
	if err != nil || !strings.Contains(restoredStage.Diff, "+    cpus: 1") {
		t.Fatalf("Stage(restore) = %#v, %v", restoredStage, err)
	}
	restored, err := workspace.Commit(t.Context(), restoredStage.CommitMessage, true)
	if err != nil || !restored.Committed || restored.ValidationUnavailable {
		t.Fatalf("Commit(restore) = %#v, %v", restored, err)
	}
	project, err = compose.Load(t.Context(), restored.Request.Source)
	if err != nil {
		t.Fatalf("Load(restored request) error = %v", err)
	}
	spec, err = project.ServiceSpec("api")
	if err != nil || spec.CPUs != "1" {
		t.Fatalf("restored CPUs = %q, %v", spec.CPUs, err)
	}
}

func TestTUIDeploymentWorkspaceFreezesGitAttributes(t *testing.T) {
	t.Parallel()

	t.Run("built-in normalization", func(t *testing.T) {
		t.Parallel()

		workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
		commitDeploymentAttributes(t, repository, deploymentComposeEntry+" text eol=lf\n")
		if _, err := workspace.Preview(
			t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
		); err != nil {
			t.Fatalf("Preview() error = %v", err)
		}
		if _, err := workspace.Stage(t.Context()); err != nil {
			t.Fatalf("Stage() error = %v", err)
		}
		if err := workspace.Discard(t.Context()); err != nil {
			t.Fatalf("Discard() error = %v", err)
		}
	})

	t.Run("external filter", func(t *testing.T) {
		t.Parallel()

		workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
		commitDeploymentAttributes(t, repository, deploymentComposeEntry+" filter=hostile\n")
		if _, err := workspace.Preview(
			t.Context(), request, application.DeploymentCPUs.ID(), testDeploymentCPUValue, false,
		); err != nil {
			t.Fatalf("Preview() error = %v", err)
		}
		sentinel := filepath.Join(t.TempDir(), "filter-ran")
		script := filepath.Join(t.TempDir(), "filter")
		if err := os.WriteFile(
			script, []byte("#!/bin/sh\ntouch \"$1\"\ncat\n"), 0o600,
		); err != nil {
			t.Fatalf("WriteFile(filter) error = %v", err)
		}
		if err := os.Chmod(script, 0o700); err != nil { //nolint:gosec // The test filter must be executable.
			t.Fatalf("Chmod(filter) error = %v", err)
		}
		if _, err := runGit(
			t.Context(), repository, "config", "--local", "filter.hostile.clean", script+" "+sentinel,
		); err != nil {
			t.Fatalf("git config filter error = %v", err)
		}
		if _, err := workspace.Stage(t.Context()); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("Stage(external filter) error = %v", err)
		}
		if _, err := os.Stat(sentinel); !errors.Is(err, os.ErrNotExist) {
			t.Fatalf("external filter executed: %v", err)
		}
		assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
	})
}

func commitDeploymentAttributes(t *testing.T, repository, content string) {
	t.Helper()

	if err := os.WriteFile(filepath.Join(repository, ".gitattributes"), []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile(.gitattributes) error = %v", err)
	}
	if _, err := runGit(t.Context(), repository, "add", "--", ".gitattributes"); err != nil {
		t.Fatalf("git add .gitattributes error = %v", err)
	}
	commitDeploymentIndex(t, repository, "Add deployment attributes")
}

func TestTUIDeploymentWorkspaceDiscardsExactStagedEdit(t *testing.T) {
	t.Parallel()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	if _, err := workspace.Preview(
		t.Context(), request, application.DeploymentRestart.ID(), "unless-stopped", false,
	); err != nil {
		t.Fatalf("Preview() error = %v", err)
	}
	if _, err := workspace.Stage(t.Context()); err != nil {
		t.Fatalf("Stage() error = %v", err)
	}
	if err := workspace.Discard(t.Context()); err != nil {
		t.Fatalf("Discard() error = %v", err)
	}
	assertTUIDeploymentContent(t, repository, deploymentComposeEntry, request.Source.Content)
	if _, err := cleanGitTree(t.Context(), repository); err != nil {
		t.Fatalf("Discard() repository state error = %v", err)
	}
}

func TestTUIDeploymentWorkspaceRejectsUnsafeInputsAndHistory(t *testing.T) {
	t.Parallel()

	workspace, request, repository := newTUIDeploymentWorkspaceFixture(t, deploymentComposeFixture())
	for _, input := range []struct {
		field string
		value string
		unset bool
	}{
		{field: "unknown", value: "1"},
		{field: application.DeploymentCPUs.ID(), value: "0"},
		{field: application.DeploymentNoNewPrivileges.ID(), value: falseValue},
		{field: application.DeploymentNoNewPrivileges.ID(), unset: true},
	} {
		if _, err := workspace.Preview(
			t.Context(), request, input.field, input.value, input.unset,
		); !errors.Is(err, errDeploymentEditInvalid) {
			t.Fatalf("Preview(%#v) error = %v", input, err)
		}
	}
	if _, err := workspace.PreviewRestore(t.Context(), request, strings.Repeat("a", 40)); !errors.Is(
		err, errDeploymentEditInvalid,
	) {
		t.Fatalf("PreviewRestore(unlisted) error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(repository, "drift"), []byte("drift"), 0o600); err != nil {
		t.Fatalf("WriteFile(drift) error = %v", err)
	}
	if _, err := workspace.Fields(t.Context(), request); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("Fields(dirty repository) error = %v", err)
	}
}

func newTUIDeploymentWorkspaceFixture(
	t *testing.T,
	content []byte,
) (*tuiDeploymentWorkspace, application.Request, string) {
	t.Helper()

	repository := t.TempDir()
	path := filepath.Join(repository, filepath.FromSlash(deploymentComposeEntry))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	commitApplyTestRepository(t, repository, deploymentComposeEntry)
	runtimeBase := t.TempDir()
	environment := map[string]string{testComposeDisableEnvFile: trueValue}
	source, err := loadTrackedComposeSource(
		t.Context(), path, repository, environment, runtimeBase,
	)
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}

	return &tuiDeploymentWorkspace{environment: environment, runtimeBase: runtimeBase},
		application.Request{Source: source, Service: applyServiceValue}, repository
}

func assertTUIDeploymentContent(t *testing.T, repository, entry string, expected []byte) {
	t.Helper()

	actual, err := os.ReadFile( //nolint:gosec // Both values come from this test's temporary repository fixture.
		filepath.Join(repository, filepath.FromSlash(entry)),
	)
	if err != nil {
		t.Fatalf("ReadFile() error = %v", err)
	}
	if !bytes.Equal(actual, expected) {
		t.Fatalf("Compose content changed:\n%s", actual)
	}
}
