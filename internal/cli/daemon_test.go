package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

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

func TestClassifyDaemonCommandFailure(t *testing.T) {
	t.Parallel()

	if got := classifyDaemonCommandFailure(errGitOpsRepositoryInvalid); got.Code() != domain.ErrorInvalidInput {
		t.Fatalf("classify(invalid) = %#v", got)
	}
}

func TestRunDaemonRequiresOnceExecutor(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	status := runDaemon(invocation{arguments: daemonInvocation{once: true}, debug: false}, output, nil, nil)
	if status != 1 || output.String() != internalErrorJSON {
		t.Fatalf("runDaemon() = %d, %q", status, output.String())
	}
}
