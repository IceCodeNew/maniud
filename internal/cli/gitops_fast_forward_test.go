package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

//nolint:cyclop // Assertions jointly prove remote selection, draft preservation, and checkout identity.
func TestFastForwardGitOpsCheckoutSelectsRemoteDescendant(t *testing.T) {
	t.Parallel()

	checkout, producer, registered := initFastForwardGitOpsTestRepositories(t)
	if err := os.Mkdir(filepath.Join(checkout, gitOpsServicesDirectory), 0o700); err != nil {
		t.Fatalf("Mkdir(services) error = %v", err)
	}
	draftPath := filepath.Join(checkout, gitOpsServicesDirectory, ".worker.yaml.swp")
	if err := os.WriteFile(draftPath, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(draft) error = %v", err)
	}
	writeGitOpsTestCommit(t, producer, "services/api.yaml", "services: {}\n", "add api")
	if _, err := runGit(t.Context(), producer, "push", "--quiet", gitOpsRemoteName, "HEAD:"+gitOpsTestBranch); err != nil {
		t.Fatalf("git push error = %v", err)
	}

	selection, err := fastForwardGitOpsCheckout(
		t.Context(), testGitOpsRegistration(t, checkout, registered),
	)
	if err != nil {
		t.Fatalf("fastForwardGitOpsCheckout() error = %v", err)
	}
	want, err := resolveGitObject(t.Context(), producer, "HEAD^{commit}")
	physicalCheckout, pathErr := filepath.EvalSymlinks(checkout)
	if err != nil || pathErr != nil || selection.commit != want || selection.root != physicalCheckout ||
		selection.awaitingPush {
		t.Fatalf("fastForwardGitOpsCheckout() = %#v, %v; want %q", selection, err, want)
	}
	info, err := os.Stat(filepath.Join(checkout, "services", "api.yaml"))
	if err != nil || !info.Mode().IsRegular() {
		t.Fatalf("selected service info = %#v, %v", info, err)
	}
	//nolint:gosec // draftPath is created inside the test's private temporary repository.
	if content, readErr := os.ReadFile(draftPath); readErr != nil || string(content) != "services: {}\n" {
		t.Fatalf("saved draft = %q, %v", content, readErr)
	}
}

func TestFastForwardGitOpsCheckoutReturnsAwaitingPushForLocalDescendant(t *testing.T) {
	t.Parallel()

	checkout, _, registered := initFastForwardGitOpsTestRepositories(t)
	writeGitOpsTestCommit(t, checkout, "services/api.yaml", "services: {}\n", "local api")
	want, err := resolveGitObject(t.Context(), checkout, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve local commit error = %v", err)
	}

	selection, err := fastForwardGitOpsCheckout(
		t.Context(), testGitOpsRegistration(t, checkout, registered),
	)
	if err != nil || !selection.awaitingPush || selection.commit != want {
		t.Fatalf("fastForwardGitOpsCheckout() = %#v, %v; want %q", selection, err, want)
	}
	state, stateErr := cleanGitTree(t.Context(), checkout)
	if stateErr != nil || state.head != want {
		t.Fatalf("awaiting-push checkout = %#v, %v", state, stateErr)
	}
}

func TestFastForwardGitOpsCheckoutRejectsRemoteRewrite(t *testing.T) {
	t.Parallel()

	checkout, producer, registered := initFastForwardGitOpsTestRepositories(t)
	if _, err := runGit(t.Context(), producer, "checkout", "--quiet", "--orphan", "rewritten"); err != nil {
		t.Fatalf("git checkout --orphan error = %v", err)
	}
	if err := os.Remove(filepath.Join(producer, "README.md")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	writeGitOpsTestCommit(t, producer, "services/api.yaml", "services: {}\n", "rewrite")
	if _, err := runGit(
		t.Context(), producer, "push", "--quiet", "--force", gitOpsRemoteName, "HEAD:"+gitOpsTestBranch,
	); err != nil {
		t.Fatalf("git force push error = %v", err)
	}

	_, err := fastForwardGitOpsCheckout(
		t.Context(), testGitOpsRegistration(t, checkout, registered),
	)
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fastForwardGitOpsCheckout(rewrite) = %v", err)
	}
	state, stateErr := cleanGitTree(t.Context(), checkout)
	if stateErr != nil || state.head != registered {
		t.Fatalf("checkout after rejected rewrite = %#v, %v", state, stateErr)
	}
}

func TestFastForwardGitOpsCheckoutRejectsFetchedDivergence(t *testing.T) {
	t.Parallel()

	checkout, producer, registered := initFastForwardGitOpsTestRepositories(t)
	if _, err := runGit(t.Context(), producer, "checkout", "--quiet", "--orphan", "diverged"); err != nil {
		t.Fatalf("git checkout --orphan error = %v", err)
	}
	if err := os.Remove(filepath.Join(producer, "README.md")); err != nil {
		t.Fatalf("Remove() error = %v", err)
	}
	writeGitOpsTestCommit(t, producer, "services/api.yaml", "services: {}\n", "diverged")
	if _, err := runGit(
		t.Context(), producer, "push", "--quiet", "--force", gitOpsRemoteName, "HEAD:"+gitOpsTestBranch,
	); err != nil {
		t.Fatalf("git force push error = %v", err)
	}
	if _, err := runGit(
		t.Context(), checkout, "fetch", "--quiet", gitOpsRemoteName,
		"refs/heads/"+gitOpsTestBranch+":refs/maniud-test/diverged",
	); err != nil {
		t.Fatalf("git fetch diverged object error = %v", err)
	}
	diverged, err := resolveGitObject(t.Context(), checkout, "refs/maniud-test/diverged^{commit}")
	if err != nil {
		t.Fatalf("resolve diverged commit error = %v", err)
	}
	if _, err = runGit(
		t.Context(), checkout, "update-ref", "refs/remotes/"+gitOpsRemoteName+"/"+gitOpsTestBranch, diverged,
	); err != nil {
		t.Fatalf("git update-ref error = %v", err)
	}

	_, err = fastForwardGitOpsCheckout(t.Context(), testGitOpsRegistration(t, checkout, registered))
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fastForwardGitOpsCheckout(diverged) error = %v", err)
	}
}

func TestFastForwardGitOpsCheckoutContainsAdvanceFailure(t *testing.T) {
	t.Parallel()

	checkout, producer, registered := initFastForwardGitOpsTestRepositories(t)
	writeGitOpsTestCommit(t, producer, "services/api.yaml", "services: {}\n", "add api")
	if _, err := runGit(t.Context(), producer, "push", "--quiet", gitOpsRemoteName, "HEAD:"+gitOpsTestBranch); err != nil {
		t.Fatalf("git push error = %v", err)
	}
	if err := os.Chmod(checkout, 0o500); err != nil { //nolint:gosec // The test removes repository write access.
		t.Fatalf("Chmod(checkout) error = %v", err)
	}
	t.Cleanup(func() {
		_ = os.Chmod(checkout, 0o700) //nolint:gosec // Cleanup restores private owner access.
	})
	_, err := fastForwardGitOpsCheckout(t.Context(), testGitOpsRegistration(t, checkout, registered))
	if err == nil {
		t.Skip("filesystem allowed the checkout update without directory write permission")
	}
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fastForwardGitOpsCheckout(advance failure) error = %v", err)
	}
}

func TestFastForwardGitOpsCheckoutRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()

	_, err := fastForwardGitOpsCheckout(t.Context(), gitOpsRegistration{})
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fastForwardGitOpsCheckout(invalid registration) = %v", err)
	}
}

func TestGitOpsFastForwardRejectsInvalidLifecycleState(t *testing.T) {
	t.Parallel()

	checkout, _, registered := initFastForwardGitOpsTestRepositories(t)
	registration := testGitOpsRegistration(t, checkout, registered)
	registration.Repository = filepath.Join(t.TempDir(), testMissingName)
	if _, _, _, err := registeredGitOpsCheckout(t.Context(), registration); !errors.Is(
		err, errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("registeredGitOpsCheckout(missing) = %v", err)
	}
	remote := testGitOpsRegistration(t, checkout, registered).RemoteURL
	if _, err := fetchGitOpsCommit(
		t.Context(), checkout, testMissingName, remote,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fetchGitOpsCommit(missing branch) = %v", err)
	}
	if _, err := runGit(
		t.Context(), checkout, "remote", "set-url", gitOpsRemoteName, "ssh://example.invalid/repo",
	); err != nil {
		t.Fatalf("git remote set-url error = %v", err)
	}
	if _, err := fetchGitOpsCommit(
		t.Context(), checkout, gitOpsTestBranch, remote,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fetchGitOpsCommit(invalid remote) = %v", err)
	}
	if err := advanceGitOpsCheckout(
		t.Context(), checkout, registered, "invalid",
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("advanceGitOpsCheckout(invalid upstream) = %v", err)
	}

	writeGitOpsTestCommit(t, checkout, "second.txt", "second\n", "second")
	if err := advanceGitOpsCheckout(
		t.Context(), checkout, registered, registered,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("advanceGitOpsCheckout(head mismatch) = %v", err)
	}

	checkout, _, _ = initFastForwardGitOpsTestRepositories(t)
	remote = testGitOpsRegistration(t, checkout, registered).RemoteURL
	if _, err := fetchGitOpsCommitWithResolver(
		t.Context(), checkout, gitOpsTestBranch, remote,
		func(context.Context, string, string) (string, error) {
			return "", errClosedOutput
		},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fetchGitOpsCommitWithResolver(resolve failure) = %v", err)
	}
}

func TestFastForwardGitOpsCheckoutRejectsRegisteredRemoteDrift(t *testing.T) {
	t.Parallel()

	checkout, _, registered := initFastForwardGitOpsTestRepositories(t)
	registration := testGitOpsRegistration(t, checkout, registered)
	otherRemote := filepath.Join(t.TempDir(), "other.git")
	if _, err := runGit(
		t.Context(), filepath.Dir(otherRemote), "clone", "--quiet", "--bare", "--", checkout, otherRemote,
	); err != nil {
		t.Fatalf("create other remote: %v", err)
	}
	if _, err := runGit(
		t.Context(), checkout, "remote", "set-url", gitOpsRemoteName, otherRemote,
	); err != nil {
		t.Fatalf("change registered remote: %v", err)
	}
	if _, err := fastForwardGitOpsCheckout(t.Context(), registration); !errors.Is(
		err, errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("fastForwardGitOpsCheckout(remote drift) = %v", err)
	}
}

func TestBindGitOpsRemotePersistsIdentityBeforeUse(t *testing.T) {
	t.Parallel()

	repository := initGitOpsTestRepository(t)
	head, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve test HEAD: %v", err)
	}
	registration := testGitOpsRegistration(t, repository, head)
	wantRemote := registration.RemoteURL
	registration.RemoteURL = ""
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	if err = writeGitOpsRegistration(registrationPath, registration); err != nil {
		t.Fatalf("write unbound registration: %v", err)
	}
	bound, err := bindGitOpsRemote(t.Context(), registrationPath, registration)
	if err != nil || bound.RemoteURL != wantRemote {
		t.Fatalf("bindGitOpsRemote() = %#v, %v", bound, err)
	}
	persisted, err := readGitOpsRegistration(registrationPath)
	if err != nil || persisted != bound {
		t.Fatalf("persisted registration = %#v, %v", persisted, err)
	}
	otherRemote := filepath.Join(t.TempDir(), "other.git")
	if _, err = runGit(
		t.Context(), filepath.Dir(otherRemote), "clone", "--quiet", "--bare", "--", repository, otherRemote,
	); err != nil {
		t.Fatalf("create replacement remote: %v", err)
	}
	if _, err = runGit(
		t.Context(), repository, "remote", "set-url", gitOpsRemoteName, otherRemote,
	); err != nil {
		t.Fatalf("replace remote: %v", err)
	}
	if _, err = bindGitOpsRemote(t.Context(), registrationPath, persisted); !errors.Is(
		err, errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("bindGitOpsRemote(remote drift) = %v", err)
	}
}

func TestBindGitOpsRemoteContainsPersistenceFailure(t *testing.T) {
	t.Parallel()

	repository := initGitOpsTestRepository(t)
	head, err := resolveGitObject(t.Context(), repository, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve test HEAD: %v", err)
	}
	registration := testGitOpsRegistration(t, repository, head)
	registration.RemoteURL = ""
	parentFile := filepath.Join(t.TempDir(), "parent")
	if err = os.WriteFile(parentFile, []byte("not a directory\n"), 0o600); err != nil {
		t.Fatalf("write parent file: %v", err)
	}
	if _, err = bindGitOpsRemote(
		t.Context(), filepath.Join(parentFile, gitOpsRegistrationName), registration,
	); err == nil {
		t.Fatal("bindGitOpsRemote(persistence failure) succeeded")
	}
}

func initFastForwardGitOpsTestRepositories(t *testing.T) (string, string, string) {
	t.Helper()

	remote := filepath.Join(t.TempDir(), "remote.git")
	if _, err := runGit(t.Context(), filepath.Dir(remote),
		"init", "--quiet", "--bare", "--initial-branch="+gitOpsTestBranch, remote,
	); err != nil {
		t.Fatalf("git init --bare error = %v", err)
	}

	producer := filepath.Join(t.TempDir(), "producer")
	if _, err := runGit(t.Context(), filepath.Dir(producer),
		"init", "--quiet", "--initial-branch="+gitOpsTestBranch, producer,
	); err != nil {
		t.Fatalf("git init producer error = %v", err)
	}
	writeGitOpsTestCommit(t, producer, "README.md", "fixture\n", "initial")
	if _, err := runGit(t.Context(), producer, "remote", "add", gitOpsRemoteName, remote); err != nil {
		t.Fatalf("git remote add error = %v", err)
	}
	if _, err := runGit(t.Context(), producer, "push", "--quiet", gitOpsRemoteName, gitOpsTestBranch); err != nil {
		t.Fatalf("git push initial error = %v", err)
	}

	checkout := filepath.Join(t.TempDir(), "checkout")
	if _, err := runGit(t.Context(), filepath.Dir(checkout),
		"clone", "--quiet", "--branch", gitOpsTestBranch, "--", remote, checkout,
	); err != nil {
		t.Fatalf("git clone error = %v", err)
	}
	registered, err := resolveGitObject(t.Context(), checkout, "HEAD^{commit}")
	if err != nil {
		t.Fatalf("resolve registered commit error = %v", err)
	}

	return checkout, producer, registered
}

func testGitOpsRegistration(t *testing.T, repository, baseline string) gitOpsRegistration {
	t.Helper()

	root, err := filepath.EvalSymlinks(repository)
	if err != nil {
		t.Fatalf("resolve test repository: %v", err)
	}
	remote, err := gitRemoteURL(t.Context(), root, gitOpsRemoteName)
	if err != nil {
		t.Fatalf("resolve test remote: %v", err)
	}

	return gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: root, Branch: gitOpsTestBranch,
		Remote: gitOpsRemoteName, RemoteURL: remote, BaselineCommit: baseline,
	}
}

func writeGitOpsTestCommit(t *testing.T, root, name, content, message string) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(path, []byte(content), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, err := runGit(t.Context(), root, "add", "--", name); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if _, err := runGit(t.Context(), root,
		"-c", "user.name=Maniud Tests",
		"-c", "user.email=maniud@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", message,
	); err != nil {
		t.Fatalf("git commit error = %v", err)
	}
}
