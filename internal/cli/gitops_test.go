package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const gitOpsTestBranch = "main"

func TestExecuteGitOpsInitRegistersCleanFastForwardCheckout(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	root := initGitOpsTestRepository(t)
	environment := map[string]string{homeKey: home}

	err := executeGitOpsInit(context.Background(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment)
	if err != nil {
		t.Fatalf("executeGitOpsInit() error = %v", err)
	}

	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}

	resolved, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatalf("EvalSymlinks() error = %v", err)
	}

	registration, err := readGitOpsRegistration(gitOpsRegistrationPath(statePath))
	if err != nil || registration.Repository != resolved || registration.Branch != gitOpsTestBranch ||
		registration.Remote != gitOpsRemoteName || !validGitObjectID(registration.Commit) {
		t.Fatalf("registration = %#v, %v", registration, err)
	}

	if err = executeGitOpsInit(context.Background(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); err != nil {
		t.Fatalf("executeGitOpsInit(idempotent) error = %v", err)
	}
}

func TestExecuteGitOpsInitRejectsDirtyOrMismatchedCheckout(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	root := initGitOpsTestRepository(t)
	if err := os.WriteFile(filepath.Join(root, "dirty.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	err := executeGitOpsInit(context.Background(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, map[string]string{homeKey: home})
	if !errors.Is(err, errGitOpsRepositoryInvalid) && !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("executeGitOpsInit(dirty) = %v", err)
	}
}

func TestClassifyGitOpsCommandFailure(t *testing.T) {
	t.Parallel()

	if got := classifyGitOpsCommandFailure(errGitOpsRepositoryInvalid); got.Code() != domain.ErrorInvalidInput {
		t.Fatalf("classify(invalid) = %#v", got)
	}
	if got := classifyGitOpsCommandFailure(errGitOpsRegistrationExists); got.Code() != domain.ErrorApplyFailed {
		t.Fatalf("classify(exists) = %#v", got)
	}
}

func TestWriteGitOpsRegistrationRejectsDifferentExistingFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	first := gitOpsRegistration{
		Version:    gitOpsRegistrationVersion,
		Repository: "/repo",
		Branch:     gitOpsTestBranch,
		Remote:     gitOpsRemoteName,
		Commit:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	if err := writeGitOpsRegistration(path, first); err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}

	second := first
	second.Commit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := writeGitOpsRegistration(path, second); !errors.Is(err, errGitOpsRegistrationExists) {
		t.Fatalf("writeGitOpsRegistration(conflict) = %v", err)
	}
}

func initGitOpsTestRepository(t *testing.T) string {
	t.Helper()

	root := t.TempDir()
	if _, err := runGit(t.Context(), root, "init", "--quiet", "--initial-branch="+gitOpsTestBranch); err != nil {
		t.Fatalf("git init error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "compose.yaml"), []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := runGit(t.Context(), root, "add", "--", "compose.yaml"); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if _, err := runGit(t.Context(), root,
		"-c", "user.name=Maniud Tests",
		"-c", "user.email=maniud@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", "initial",
	); err != nil {
		t.Fatalf("git commit error = %v", err)
	}
	if _, err := runGit(t.Context(), root, "remote", "add", gitOpsRemoteName, root); err != nil {
		t.Fatalf("git remote add error = %v", err)
	}
	ref := "refs/remotes/" + gitOpsRemoteName + "/" + gitOpsTestBranch
	if _, err := runGit(t.Context(), root, "update-ref", ref, "HEAD"); err != nil {
		t.Fatalf("git update-ref error = %v", err)
	}

	return root
}
