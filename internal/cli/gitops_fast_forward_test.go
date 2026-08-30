package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

func TestFastForwardGitOpsCheckoutSelectsRemoteDescendant(t *testing.T) {
	t.Parallel()

	checkout, producer, registered := initFastForwardGitOpsTestRepositories(t)
	writeGitOpsTestCommit(t, producer, "services/api.yaml", "services: {}\n", "add api")
	if _, err := runGit(t.Context(), producer, "push", "--quiet", gitOpsRemoteName, "HEAD:"+gitOpsTestBranch); err != nil {
		t.Fatalf("git push error = %v", err)
	}

	selection, err := fastForwardGitOpsCheckout(
		context.Background(), checkout, gitOpsTestBranch, registered,
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
		t.Context(), checkout, gitOpsTestBranch, registered,
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

	_, err := fastForwardGitOpsCheckout(context.Background(), checkout, gitOpsTestBranch, registered)
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fastForwardGitOpsCheckout(rewrite) = %v", err)
	}
	state, stateErr := cleanGitTree(t.Context(), checkout)
	if stateErr != nil || state.head != registered {
		t.Fatalf("checkout after rejected rewrite = %#v, %v", state, stateErr)
	}
}

func TestFastForwardGitOpsCheckoutRejectsInvalidRegistration(t *testing.T) {
	t.Parallel()

	_, err := fastForwardGitOpsCheckout(context.Background(), "/repo", gitOpsTestBranch, "invalid")
	if !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fastForwardGitOpsCheckout(invalid registration) = %v", err)
	}
}

func TestGitOpsFastForwardRejectsInvalidLifecycleState(t *testing.T) {
	t.Parallel()

	checkout, _, registered := initFastForwardGitOpsTestRepositories(t)
	if _, _, err := registeredGitOpsCheckout(
		t.Context(), filepath.Join(t.TempDir(), "missing"), gitOpsTestBranch, registered,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("registeredGitOpsCheckout(missing) = %v", err)
	}
	if _, err := fetchGitOpsCommit(t.Context(), checkout, "missing"); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fetchGitOpsCommit(missing branch) = %v", err)
	}
	if _, err := runGit(
		t.Context(), checkout, "remote", "set-url", gitOpsRemoteName, "ssh://example.invalid/repo",
	); err != nil {
		t.Fatalf("git remote set-url error = %v", err)
	}
	if _, err := fetchGitOpsCommit(t.Context(), checkout, gitOpsTestBranch); !errors.Is(err, errGitOpsRepositoryInvalid) {
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
	if _, err := fetchGitOpsCommitWithResolver(
		t.Context(), checkout, gitOpsTestBranch,
		func(context.Context, string, string) (string, error) {
			return "", errClosedOutput
		},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("fetchGitOpsCommitWithResolver(resolve failure) = %v", err)
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
