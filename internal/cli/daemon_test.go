package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestExecuteDaemonOnceRequiresRegistration(t *testing.T) {
	t.Parallel()

	err := executeDaemon(
		context.Background(),
		daemonInvocation{once: true},
		new(bytes.Buffer),
		map[string]string{homeKey: t.TempDir()},
		new(bytes.Buffer),
		func() (string, error) { return t.TempDir(), nil },
	)
	if err == nil {
		t.Fatal("executeDaemon() succeeded without registration")
	}
}

func TestExecuteDaemonStopsPollingWhenCancelled(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	err := executeDaemon(
		ctx,
		daemonInvocation{once: false, interval: time.Minute},
		new(bytes.Buffer),
		map[string]string{homeKey: t.TempDir()},
		new(bytes.Buffer),
		func() (string, error) { return t.TempDir(), nil },
	)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("executeDaemon(cancelled poll) = %v", err)
	}
}

func TestExecuteDaemonReconcilesRegisteredRepositoryOnce(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	root := initGitOpsTestRepository(t)
	environment := map[string]string{homeKey: home}
	if err := executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); err != nil {
		t.Fatalf("executeGitOpsInit() error = %v", err)
	}

	err := executeDaemon(
		t.Context(), daemonInvocation{once: true}, io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
	)
	if err != nil {
		t.Fatalf("executeDaemon(once) error = %v", err)
	}

	timed, cancel := context.WithTimeout(t.Context(), time.Second)
	defer cancel()
	err = executeDaemon(
		timed, daemonInvocation{interval: time.Hour}, io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
	)
	if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("executeDaemon(poll wait) error = %v", err)
	}

	rapid, stopRapid := context.WithTimeout(t.Context(), 500*time.Millisecond)
	defer stopRapid()
	if err = executeDaemon(
		rapid, daemonInvocation{interval: time.Nanosecond}, io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
	); err == nil {
		t.Fatal("executeDaemon(rapid poll) returned without cancellation")
	}
}

func TestReconcileRegisteredRepositoryRejectsStateAndCheckoutFailures(t *testing.T) {
	t.Parallel()

	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, nil, io.Discard, os.Getwd,
	); !errors.Is(err, errStateHomeUnavailable) {
		t.Fatalf("reconcileRegisteredRepository(missing state home) = %v", err)
	}

	root := initGitOpsTestRepository(t)
	environment := registerGitOpsTestRepository(t, root)
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return "", errClosedOutput },
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("reconcileRegisteredRepository(dependencies) = %v", err)
	}

	root = initGitOpsTestRepository(t)
	environment = registerGitOpsTestRepository(t, root)
	if err := os.WriteFile(filepath.Join(root, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(dirty) error = %v", err)
	}
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reconcileRegisteredRepository(dirty) = %v", err)
	}
}

func TestReconcileRegisteredRepositoryRejectsRecoveryAndFetchFailures(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	environment := registerGitOpsTestRepository(t, root)
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
	); err == nil {
		t.Fatal("reconcileRegisteredRepository(recovery failure) succeeded")
	}

	root = initGitOpsTestRepository(t)
	environment = registerGitOpsTestRepository(t, root)
	missingRemote := filepath.Join(t.TempDir(), "missing.git")
	if _, err := runGit(t.Context(), root, "remote", "set-url", gitOpsRemoteName, missingRemote); err != nil {
		t.Fatalf("git remote set-url error = %v", err)
	}
	if err := reconcileRegisteredRepository(
		t.Context(), io.Discard, environment, io.Discard,
		func() (string, error) { return root, nil },
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("reconcileRegisteredRepository(fetch failure) = %v", err)
	}
}

func registerGitOpsTestRepository(t *testing.T, root string) map[string]string {
	t.Helper()

	environment := map[string]string{homeKey: t.TempDir()}
	if err := executeGitOpsInit(t.Context(), gitOpsInitInvocation{
		repository: root,
		branch:     gitOpsTestBranch,
	}, environment); err != nil {
		t.Fatalf("executeGitOpsInit() error = %v", err)
	}

	return environment
}

func TestDaemonPollingBoundaries(t *testing.T) {
	t.Parallel()

	output := io.Discard
	environment := map[string]string{homeKey: t.TempDir()}
	stderr := io.Discard
	getwd := func() (string, error) { return t.TempDir(), nil }
	if err := pollRegisteredRepository(
		t.Context(), 0, output, environment, stderr, getwd,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("pollRegisteredRepository(zero interval) = %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := pollRegisteredRepository(
		cancelled, time.Second, output, environment, stderr, getwd,
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("pollRegisteredRepository(cancelled) = %v", err)
	}
	if err := pollRegisteredRepository(
		t.Context(), time.Second, output, environment, stderr, getwd,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("pollRegisteredRepository(unregistered) = %v", err)
	}

	if err := waitDaemonInterval(t.Context(), time.Nanosecond); err != nil {
		t.Fatalf("waitDaemonInterval(elapsed) = %v", err)
	}
	if err := waitDaemonInterval(cancelled, time.Second); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitDaemonInterval(cancelled) = %v", err)
	}
}

func TestListGitOpsServiceFilesIgnoresDirectories(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if err := os.MkdirAll(filepath.Join(root, gitOpsServicesDirectory, "nested"), 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, gitOpsServicesDirectory, "api.yaml"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, gitOpsServicesDirectory, "ignore.txt"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}

	got, err := listGitOpsServiceFiles(root)
	if err != nil || len(got) != 1 || filepath.Base(got[0]) != "api.yaml" {
		t.Fatalf("listGitOpsServiceFiles() = %#v, %v", got, err)
	}
}

func TestListGitOpsServiceFilesHandlesMissingAndInvalidDirectories(t *testing.T) {
	t.Parallel()

	missing, err := listGitOpsServiceFiles(t.TempDir())
	if err != nil || missing != nil {
		t.Fatalf("listGitOpsServiceFiles(missing) = %#v, %v", missing, err)
	}

	blocked := t.TempDir()
	if err = os.WriteFile(filepath.Join(blocked, gitOpsServicesDirectory), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(services) error = %v", err)
	}
	if _, err = listGitOpsServiceFiles(blocked); err == nil {
		t.Fatal("listGitOpsServiceFiles(file) succeeded")
	}
}

func TestClassifyDaemonCommandFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		code domain.ErrorCode
	}{
		{err: context.Canceled, code: domain.ErrorOperationCancelled},
		{err: errGitOpsRegistrationExists, code: domain.ErrorApplyFailed},
		{err: errGitOpsRepositoryInvalid, code: domain.ErrorInvalidInput},
		{err: compose.ErrInvalidSource, code: domain.ErrorInvalidInput},
		{err: errStateHomeUnavailable, code: domain.ErrorInvalidInput},
		{err: errStateHomeInvalid, code: domain.ErrorInvalidInput},
		{err: errApplyTest, code: domain.ErrorApplyFailed},
	}
	if classifyDaemonCommandFailure(nil) != nil {
		t.Fatal("classifyDaemonCommandFailure(nil) returned a failure")
	}
	for _, test := range tests {
		got := classifyDaemonCommandFailure(test.err)
		if got == nil || got.Code() != test.code {
			t.Fatalf("classifyDaemonCommandFailure(%v) = %#v", test.err, got)
		}
	}
}

func TestRunDaemonRequiresOnceExecutor(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	status := runDaemon(invocation{arguments: daemonInvocation{once: true}, debug: false}, output, nil, nil)
	if status != 1 || output.String() != internalErrorJSON {
		t.Fatalf("runDaemon() = %d, %q", status, output.String())
	}

	output.Reset()
	status = runDaemon(invocation{arguments: daemonInvocation{once: true}}, output, nil, func(daemonInvocation) error {
		return nil
	})
	if status != 0 || output.Len() != 0 {
		t.Fatalf("runDaemon(success) = %d, %q", status, output.String())
	}
}
