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

const (
	gitOpsTestBranch     = "main"
	gitOpsTestCommit     = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	gitOpsTestRepository = "/repo"
)

func TestExecuteGitOpsInitCreatesRepository(t *testing.T) {
	t.Parallel()

	root := filepath.Join(t.TempDir(), "desired-state")
	environment := map[string]string{homeKey: t.TempDir()}
	arguments := gitOpsInitInvocation{repository: root, branch: gitOpsTestBranch}
	if err := executeGitOpsInit(t.Context(), arguments, environment); err != nil {
		t.Fatalf("executeGitOpsInit(new) error = %v", err)
	}
	assertInitializedGitOpsRepository(t, root, environment)
	assertRegisteredGitOpsBaseline(t, root, environment)
	writeGitOpsTestCommit(t, root, "services/api.yaml", "services: {}\n", "add api")
	if err := executeGitOpsInit(t.Context(), arguments, environment); err != nil {
		t.Fatalf("executeGitOpsInit(idempotent) error = %v", err)
	}
}

func TestExecuteGitOpsInitRemovesRepositoryWhenRegistrationFails(t *testing.T) {
	t.Parallel()

	environment := map[string]string{homeKey: t.TempDir()}
	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	existing := gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: gitOpsTestRepository, Branch: gitOpsTestBranch,
		Remote: gitOpsRemoteName, RemoteURL: gitOpsTestRepository, BaselineCommit: gitOpsTestCommit,
	}
	if err = writeGitOpsRegistration(gitOpsRegistrationPath(statePath), existing); err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}
	root := filepath.Join(t.TempDir(), "desired-state")
	if err = executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); !errors.Is(err, errGitOpsRegistrationExists) {
		t.Fatalf("executeGitOpsInit(conflict) = %v", err)
	}
	if _, err = os.Stat(root); !os.IsNotExist(err) {
		t.Fatalf("removed repository error = %v", err)
	}
}

func TestInitializeGitOpsCheckoutRejectsCreationFailures(t *testing.T) {
	t.Parallel()

	initializer := successfulGitOpsCheckoutInitializer(gitOpsTestRepository)
	if _, _, _, err := initializeGitOpsCheckoutWith(
		t.Context(), "relative", gitOpsTestBranch, initializer,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("initializeGitOpsCheckoutWith(relative) = %v", err)
	}
	initializer.mkdir = func(string, os.FileMode) error { return os.ErrExist }
	if root, commit, created, err := initializeGitOpsCheckoutWith(
		t.Context(), gitOpsTestRepository, gitOpsTestBranch, initializer,
	); err != nil || created || root != "" || commit != "" {
		t.Fatalf("initializeGitOpsCheckoutWith(existing) = %q, %q, %t, %v", root, commit, created, err)
	}
	initializer.mkdir = func(string, os.FileMode) error { return errClosedOutput }
	if _, _, _, err := initializeGitOpsCheckoutWith(
		t.Context(), gitOpsTestRepository, gitOpsTestBranch, initializer,
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("initializeGitOpsCheckoutWith(create failure) = %v", err)
	}
}

func TestInitializeGitOpsCheckoutCleansIncompleteRepository(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		change func(*gitOpsCheckoutInitializer)
	}{
		{name: "services", change: failGitOpsServicesDirectory},
		{name: "init", change: failGitOpsCommand(1)},
		{name: "commit", change: failGitOpsCommand(2)},
		{name: "object lookup", change: func(value *gitOpsCheckoutInitializer) {
			value.resolve = func(context.Context, string, string) (string, error) { return "", errClosedOutput }
		}},
		{name: "invalid commit", change: func(value *gitOpsCheckoutInitializer) {
			value.resolve = func(context.Context, string, string) (string, error) { return "invalid", nil }
		}},
		{name: "evaluate", change: func(value *gitOpsCheckoutInitializer) {
			value.evaluate = func(string) (string, error) { return "", errClosedOutput }
		}},
		{name: "mismatch", change: func(value *gitOpsCheckoutInitializer) {
			value.evaluate = func(string) (string, error) { return "/other", nil }
		}},
	}
	for _, test := range tests {
		initializer := successfulGitOpsCheckoutInitializer(gitOpsTestRepository)
		removed := false
		initializer.removeAll = func(string) error {
			removed = true

			return nil
		}
		test.change(&initializer)
		if _, _, created, err := initializeGitOpsCheckoutWith(
			t.Context(), gitOpsTestRepository, gitOpsTestBranch, initializer,
		); err == nil || created || !removed {
			t.Fatalf("initializeGitOpsCheckoutWith(%s) = created %t, removed %t, %v", test.name, created, removed, err)
		}
	}
}

func TestReuseInitializedGitOpsCheckoutRejectsDrift(t *testing.T) {
	t.Parallel()

	root := initGitOpsTestRepository(t)
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	registration := testGitOpsRegistration(t, root, state.head)
	if err = reuseInitializedGitOpsCheckout(
		t.Context(), registration, root+"-other", gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reuseInitializedGitOpsCheckout(repository) = %v", err)
	}
	if err = os.WriteFile(filepath.Join(root, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err = reuseInitializedGitOpsCheckout(
		t.Context(), registration, root, gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reuseInitializedGitOpsCheckout(dirty) = %v", err)
	}
	if err = os.Remove(filepath.Join(root, "dirty")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	registration.BaselineCommit = gitOpsTestCommit
	if err = reuseInitializedGitOpsCheckout(
		t.Context(), registration, root, gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reuseInitializedGitOpsCheckout(baseline) = %v", err)
	}
}

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
		registration.Remote != gitOpsRemoteName || !validGitObjectID(registration.BaselineCommit) {
		t.Fatalf("registration = %#v, %v", registration, err)
	}

	if err = executeGitOpsInit(context.Background(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); err != nil {
		t.Fatalf("executeGitOpsInit(idempotent) error = %v", err)
	}
}

func TestExecuteGitOpsInitRejectsInvalidExistingRegistration(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	statePath, err := defaultStatePath(map[string]string{homeKey: home})
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	registrationPath := gitOpsRegistrationPath(statePath)
	if err = os.MkdirAll(filepath.Dir(registrationPath), gitOpsRegistrationDirMode); err != nil {
		t.Fatalf("MkdirAll(registration) error = %v", err)
	}
	if err = os.WriteFile(registrationPath, []byte("invalid\n"), gitOpsRegistrationMode); err != nil {
		t.Fatalf("WriteFile(registration) error = %v", err)
	}
	err = executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: initGitOpsTestRepository(t),
		branch:     gitOpsTestBranch,
	}, map[string]string{homeKey: home})
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("executeGitOpsInit(invalid registration) = %v", err)
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
	missingParent := filepath.Join(t.TempDir(), testMissingName, "desired-state")
	if err = executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: missingParent,
		branch:     gitOpsTestBranch,
	}, map[string]string{homeKey: home}); err == nil {
		t.Fatal("executeGitOpsInit(missing parent) succeeded")
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
		Version:        gitOpsRegistrationVersion,
		Repository:     gitOpsTestRepository,
		Branch:         gitOpsTestBranch,
		Remote:         gitOpsRemoteName,
		RemoteURL:      gitOpsTestRepository,
		BaselineCommit: gitOpsTestCommit,
	}
	if err := writeGitOpsRegistration(path, first); err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}
	if err := writeGitOpsRegistration(path, first); err != nil {
		t.Fatalf("writeGitOpsRegistration(idempotent) error = %v", err)
	}

	second := first
	second.BaselineCommit = "bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	if err := writeGitOpsRegistration(path, second); !errors.Is(err, errGitOpsRegistrationExists) {
		t.Fatalf("writeGitOpsRegistration(conflict) = %v", err)
	}

	legacyPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	legacy := first
	legacy.RemoteURL = ""
	if err := writeGitOpsRegistration(legacyPath, legacy); err != nil {
		t.Fatalf("writeGitOpsRegistration(legacy) error = %v", err)
	}
	if err := writeGitOpsRegistration(legacyPath, second); !errors.Is(err, errGitOpsRegistrationExists) {
		t.Fatalf("writeGitOpsRegistration(legacy conflict) = %v", err)
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
		Version:        gitOpsRegistrationVersion,
		Repository:     gitOpsTestRepository,
		Branch:         gitOpsTestBranch,
		Remote:         gitOpsRemoteName,
		RemoteURL:      gitOpsTestRepository,
		BaselineCommit: gitOpsTestCommit,
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
	if _, _, _, err := inspectGitOpsCheckout(
		t.Context(), repositoryValue, gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("inspectGitOpsCheckout(relative) = %v", err)
	}
	if _, err := resolveGitOpsRepository(
		t.Context(), filepath.Join(t.TempDir(), testMissingName),
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
	if _, _, _, err := inspectGitOpsCheckout(
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
	if _, _, _, err := inspectGitOpsCheckout(
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
	if _, err := gitOpsRepositoryScope(t.Context(), gitOpsRegistration{
		Remote: gitOpsRemoteName, Branch: gitOpsTestBranch,
	}, root); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("gitOpsRepositoryScope(missing remote) = %v", err)
	}

	root = initGitOpsTestRepository(t)
	remote, err := gitRemoteURL(t.Context(), root, gitOpsRemoteName)
	if err != nil {
		t.Fatalf("gitRemoteURL(valid) = %v", err)
	}
	if _, err := gitOpsRepositoryScope(t.Context(), gitOpsRegistration{
		Remote: gitOpsRemoteName, RemoteURL: remote,
	}, root); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("gitOpsRepositoryScope(invalid branch) = %v", err)
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
	if _, _, _, err := proveGitOpsCheckout(
		t.Context(), root, gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
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
	if _, _, _, err := proveGitOpsCheckout(
		t.Context(), root, gitOpsTestBranch,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("proveGitOpsCheckout(upstream ahead) = %v", err)
	}
}

func TestProveGitOpsCheckoutRejectsFinalCheckoutDrift(t *testing.T) {
	t.Parallel()

	root := initGitOpsTestRepository(t)
	if _, _, _, err := proveGitOpsCheckoutWithFinalCheck(
		t.Context(), root, gitOpsTestBranch,
		func(context.Context, string) (gitTreeState, error) {
			return gitTreeState{}, compose.ErrInvalidSource
		},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
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

func assertInitializedGitOpsRepository(t *testing.T, root string, environment map[string]string) {
	t.Helper()

	if info, err := os.Stat(filepath.Join(root, gitOpsServicesDirectory)); err != nil || !info.IsDir() {
		t.Fatalf("services directory = %#v, %v", info, err)
	}
	if branch, err := currentGitBranch(t.Context(), root); err != nil || branch != gitOpsTestBranch {
		t.Fatalf("currentGitBranch() = %q, %v", branch, err)
	}
	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	registration, err := readGitOpsRegistration(gitOpsRegistrationPath(statePath))
	if err != nil || registration.Repository != root || !validGitObjectID(registration.BaselineCommit) {
		t.Fatalf("registration = %#v, %v", registration, err)
	}
}

func assertRegisteredGitOpsBaseline(t *testing.T, root string, environment map[string]string) {
	t.Helper()

	statePath, err := defaultStatePath(environment)
	if err != nil {
		t.Fatalf("defaultStatePath() error = %v", err)
	}
	registration, err := readGitOpsRegistration(gitOpsRegistrationPath(statePath))
	if err != nil {
		t.Fatalf("readGitOpsRegistration() error = %v", err)
	}
	head, err := resolveGitObject(t.Context(), root, "HEAD^{commit}")
	if err != nil || registration.BaselineCommit != head {
		t.Fatalf("registered baseline = %q, HEAD = %q, %v", registration.BaselineCommit, head, err)
	}
}

func successfulGitOpsCheckoutInitializer(path string) gitOpsCheckoutInitializer {
	return gitOpsCheckoutInitializer{
		mkdir: func(string, os.FileMode) error { return nil },
		run: func(context.Context, string, ...string) ([]byte, error) {
			return nil, nil
		},
		resolve:   func(context.Context, string, string) (string, error) { return gitOpsTestCommit, nil },
		evaluate:  func(string) (string, error) { return path, nil },
		removeAll: func(string) error { return nil },
	}
}

func failGitOpsServicesDirectory(initializer *gitOpsCheckoutInitializer) {
	calls := 0
	initializer.mkdir = func(string, os.FileMode) error {
		calls++
		if calls == 2 {
			return errClosedOutput
		}

		return nil
	}
}

func failGitOpsCommand(target int) func(*gitOpsCheckoutInitializer) {
	return func(initializer *gitOpsCheckoutInitializer) {
		calls := 0
		initializer.run = func(context.Context, string, ...string) ([]byte, error) {
			calls++
			if calls == target {
				return nil, errClosedOutput
			}

			return nil, nil
		}
	}
}
