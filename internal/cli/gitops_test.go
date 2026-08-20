package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"strings"
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

	if err = executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: root,
		branch:     "invalid branch",
	}, map[string]string{homeKey: home}); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("executeGitOpsInit(invalid branch) = %v", err)
	}

	clean := initGitOpsTestRepository(t)
	if err = executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: clean,
		branch:     gitOpsTestBranch,
	}, nil); !errors.Is(err, errStateHomeUnavailable) {
		t.Fatalf("executeGitOpsInit(missing state home) = %v", err)
	}
}

func TestClassifyGitOpsCommandFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		code domain.ErrorCode
	}{
		{err: errGitOpsRegistrationExists, code: domain.ErrorApplyFailed},
		{err: errGitOpsRepositoryInvalid, code: domain.ErrorInvalidInput},
		{err: compose.ErrInvalidSource, code: domain.ErrorInvalidInput},
		{err: errStateHomeUnavailable, code: domain.ErrorInvalidInput},
		{err: errStateHomeInvalid, code: domain.ErrorInvalidInput},
		{err: errClosedOutput, code: domain.ErrorInternal},
	}
	if classifyGitOpsCommandFailure(nil) != nil {
		t.Fatal("classifyGitOpsCommandFailure(nil) returned a failure")
	}
	for _, test := range tests {
		got := classifyGitOpsCommandFailure(test.err)
		if got == nil || got.Code() != test.code {
			t.Fatalf("classifyGitOpsCommandFailure(%v) = %#v", test.err, got)
		}
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

func TestGitOpsRegistrationRejectsInvalidState(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	statePath := filepath.Join(root, stateDatabaseName)
	path := filepath.Join(root, gitOpsRegistrationName)

	if got := gitOpsRegistrationPath(statePath); got != path {
		t.Fatalf("gitOpsRegistrationPath() = %q, want %q", got, path)
	}
	if err := writeGitOpsRegistration(path, gitOpsRegistration{}); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("writeGitOpsRegistration(invalid) = %v", err)
	}
	if errGitOpsRepositoryInvalid.Error() == "" {
		t.Fatal("gitOpsRepositoryError.Error() returned an empty string")
	}
}

func TestGitOpsRegistrationRejectsInvalidFiles(t *testing.T) {
	t.Parallel()

	valid := gitOpsRegistration{
		Version:    gitOpsRegistrationVersion,
		Repository: "/repo",
		Branch:     gitOpsTestBranch,
		Remote:     gitOpsRemoteName,
		Commit:     "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
	}
	root := t.TempDir()
	path := filepath.Join(root, gitOpsRegistrationName)
	if err := os.WriteFile(path, []byte("invalid"), gitOpsRegistrationMode); err != nil {
		t.Fatalf("WriteFile(invalid registration) error = %v", err)
	}
	if _, err := readGitOpsRegistration(path); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("readGitOpsRegistration(invalid) = %v", err)
	}
	if err := writeGitOpsRegistration(path, valid); !errors.Is(err, errGitOpsRegistrationExists) {
		t.Fatalf("writeGitOpsRegistration(invalid existing) = %v", err)
	}

	directoryPath := filepath.Join(root, "registration-directory")
	if err := os.Mkdir(directoryPath, 0o700); err != nil {
		t.Fatalf("Mkdir(registration) error = %v", err)
	}
	if _, err := readGitOpsRegistration(directoryPath); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("readGitOpsRegistration(directory) = %v", err)
	}

	parentFile := filepath.Join(root, "parent-file")
	if err := os.WriteFile(parentFile, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(parent) error = %v", err)
	}
	if err := writeGitOpsRegistration(filepath.Join(parentFile, gitOpsRegistrationName), valid); err == nil {
		t.Fatal("writeGitOpsRegistration(parent file) succeeded")
	}

	oversized := filepath.Join(root, strings.Repeat("x", 4096))
	if err := writeGitOpsRegistration(oversized, valid); err == nil {
		t.Fatal("writeGitOpsRegistration(oversized path) succeeded")
	}
}

func TestPublishGitOpsRegistrationReportsFilesystemFailures(t *testing.T) {
	t.Parallel()

	testErr := errClosedOutput
	err := publishGitOpsRegistration(
		"registration", []byte("{}"),
		func(string, []byte, os.FileMode) error { return testErr },
		func(string, string) error { return nil },
		func(string) error { return nil },
	)
	if !errors.Is(err, testErr) {
		t.Fatalf("publishGitOpsRegistration(write) = %v", err)
	}

	removed := false
	err = publishGitOpsRegistration(
		"registration", []byte("{}"),
		func(string, []byte, os.FileMode) error { return nil },
		func(string, string) error { return testErr },
		func(string) error {
			removed = true

			return nil
		},
	)
	if !errors.Is(err, testErr) || !removed {
		t.Fatalf("publishGitOpsRegistration(rename) = %v, removed = %t", err, removed)
	}

	err = publishGitOpsRegistration(
		"registration", []byte("{}"),
		func(string, []byte, os.FileMode) error { return nil },
		func(string, string) error { return nil },
		func(string) error { return testErr },
	)
	if err != nil {
		t.Fatalf("publishGitOpsRegistration(success) = %v", err)
	}
}

func TestGitOpsRepositoryRejectsInvalidCheckoutBoundaries(t *testing.T) {
	t.Parallel()

	if _, err := resolveGitOpsRepository(
		t.Context(), repositoryValue,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("resolveGitOpsRepository(relative) = %v", err)
	}
	if _, _, err := inspectGitOpsCheckout(
		t.Context(), repositoryValue, gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("inspectGitOpsCheckout(relative) = %v", err)
	}
	if _, err := resolveGitOpsRepository(
		t.Context(), filepath.Join(t.TempDir(), "missing"),
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("resolveGitOpsRepository(missing) = %v", err)
	}
	if _, err := resolveGitOpsRepository(
		t.Context(), t.TempDir(),
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("resolveGitOpsRepository(non-repository) = %v", err)
	}

	root := initGitOpsTestRepository(t)
	subdirectory := filepath.Join(root, "subdirectory")
	if err := os.Mkdir(subdirectory, 0o700); err != nil {
		t.Fatalf("Mkdir(subdirectory) error = %v", err)
	}
	if _, err := resolveGitOpsRepository(
		t.Context(), subdirectory,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("resolveGitOpsRepository(subdirectory) = %v", err)
	}
	if _, _, err := inspectGitOpsCheckout(
		t.Context(), root, "other",
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("inspectGitOpsCheckout(wrong branch) = %v", err)
	}
}

func TestGitOpsRepositoryRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	root := initGitOpsTestRepository(t)
	if _, err := runGit(
		t.Context(), root, "remote", "set-url", gitOpsRemoteName, "ssh://example.invalid/repo",
	); err != nil {
		t.Fatalf("git remote set-url error = %v", err)
	}
	if _, _, err := inspectGitOpsCheckout(
		t.Context(), root, gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("inspectGitOpsCheckout(invalid remote) = %v", err)
	}

	root = initGitOpsTestRepository(t)
	if _, err := runGit(t.Context(), root, "config", "--local", "include.path", "/tmp/hostile"); err != nil {
		t.Fatalf("git config include.path error = %v", err)
	}
	if _, err := resolveGitOpsRepository(t.Context(), root); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("resolveGitOpsRepository(hostile config) = %v", err)
	}
}

func TestGitOpsRepositoryRejectsInvalidGitState(t *testing.T) {
	t.Parallel()

	root := initGitOpsTestRepository(t)
	if _, err := runGit(t.Context(), root, "checkout", "--quiet", "--detach"); err != nil {
		t.Fatalf("git checkout --detach error = %v", err)
	}
	if _, err := currentGitBranch(t.Context(), root); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("currentGitBranch(detached) = %v", err)
	}

	root = initGitOpsTestRepository(t)
	if _, err := runGit(t.Context(), root, "remote", "remove", gitOpsRemoteName); err != nil {
		t.Fatalf("git remote remove error = %v", err)
	}
	if _, err := gitRemoteURL(t.Context(), root, gitOpsRemoteName); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("gitRemoteURL(missing) = %v", err)
	}

	if err := requireFastForward(
		t.Context(), root, "invalid", "also-invalid",
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("requireFastForward(invalid revisions) = %v", err)
	}
}

func TestProveGitOpsCheckoutRejectsMissingUpstream(t *testing.T) {
	t.Parallel()

	root := initGitOpsTestRepository(t)
	ref := "refs/remotes/" + gitOpsRemoteName + "/" + gitOpsTestBranch
	if _, err := runGit(t.Context(), root, "update-ref", "-d", ref); err != nil {
		t.Fatalf("git update-ref -d error = %v", err)
	}
	if _, _, err := proveGitOpsCheckout(t.Context(), root, gitOpsTestBranch); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("proveGitOpsCheckout(missing upstream) = %v", err)
	}

	root = initGitOpsTestRepository(t)
	writeGitOpsTestCommit(t, root, "ahead.txt", "ahead\n", "advance upstream")
	ref = "refs/remotes/" + gitOpsRemoteName + "/" + gitOpsTestBranch
	if _, err := runGit(t.Context(), root, "update-ref", ref, "HEAD"); err != nil {
		t.Fatalf("git update-ref upstream error = %v", err)
	}
	if _, err := runGit(t.Context(), root, "reset", "--quiet", "--hard", "HEAD~1"); err != nil {
		t.Fatalf("git reset --hard error = %v", err)
	}
	if _, _, err := proveGitOpsCheckout(t.Context(), root, gitOpsTestBranch); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("proveGitOpsCheckout(upstream ahead) = %v", err)
	}
}

func TestProveGitOpsCheckoutRejectsFinalCheckoutDrift(t *testing.T) {
	t.Parallel()

	root := initGitOpsTestRepository(t)
	_, _, err := proveGitOpsCheckoutWithFinalCheck(
		t.Context(), root, gitOpsTestBranch,
		func(context.Context, string) (gitTreeState, error) {
			return gitTreeState{}, compose.ErrInvalidSource
		},
	)
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("proveGitOpsCheckoutWithFinalCheck() = %v", err)
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
