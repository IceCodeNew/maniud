package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestReconcileGitOpsSnapshotValidatesEveryServiceBeforeMutation(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	reader := &applyReaderFixture{events: &events, closeErr: nil}
	runtime := &applyRuntimeFixture{events: &events, inspectErr: nil}
	dependencies := validApplyDependencies(t, &events, reader, runtime)
	dependencies.loadSource = func(_ context.Context, path string) (compose.Source, error) {
		if filepath.Base(path) == "broken.yaml" {
			return compose.Source{Content: []byte("invalid"), WorkingDir: root}, nil
		}

		return testComposeSource(t), nil
	}
	statePath := privateStatePath(t)
	dependencies.openState = func(ctx context.Context) (*store.Store, error) {
		return store.Open(ctx, statePath)
	}
	mutations := 0
	dependencies.mutate = func(
		context.Context,
		application.Request,
		*store.Store,
		applyRuntime,
	) (application.Plan, error) {
		mutations++

		return application.Plan{}, nil
	}

	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	err = reconcileGitOpsSnapshot(t.Context(), root, state.head, io.Discard, dependencies)
	if err == nil || mutations != 0 {
		t.Fatalf("reconcileGitOpsSnapshot() error = %v, mutations = %d", err, mutations)
	}
}

func TestReconcileGitOpsSnapshotMutatesValidatedServices(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 24)
	reader := &applyReaderFixture{events: &events, closeErr: nil}
	runtime := &applyRuntimeFixture{events: &events, inspectErr: nil}
	dependencies := validApplyDependencies(t, &events, reader, runtime)
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}
	statePath := privateStatePath(t)
	dependencies.openState = func(ctx context.Context) (*store.Store, error) {
		return store.Open(ctx, statePath)
	}
	mutations := 0
	dependencies.mutate = func(
		context.Context,
		application.Request,
		*store.Store,
		applyRuntime,
	) (application.Plan, error) {
		mutations++

		return application.Plan{Kind: application.PlanBootstrap}, nil
	}

	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	err = reconcileGitOpsSnapshot(t.Context(), root, state.head, io.Discard, dependencies)
	if err != nil || mutations != 2 {
		t.Fatalf("reconcileGitOpsSnapshot() error = %v, mutations = %d", err, mutations)
	}
}

func TestReconcileGitOpsSnapshotReturnsMutationFailure(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	dependencies := validApplyDependencies(
		t, &events,
		&applyReaderFixture{events: &events},
		&applyRuntimeFixture{events: &events},
	)
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}
	statePath := privateStatePath(t)
	dependencies.openState = func(ctx context.Context) (*store.Store, error) {
		return store.Open(ctx, statePath)
	}
	dependencies.mutate = func(
		context.Context,
		application.Request,
		*store.Store,
		applyRuntime,
	) (application.Plan, error) {
		return application.Plan{}, errApplyTest
	}

	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	if err = reconcileGitOpsSnapshot(
		t.Context(), root, state.head, io.Discard, dependencies,
	); !errors.Is(err, errApplyTest) {
		t.Fatalf("reconcileGitOpsSnapshot() error = %v", err)
	}
}

func TestCaptureGitOpsSnapshotRejectsSourceAndCheckoutDrift(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	dependencies := validApplyDependencies(
		t, &events,
		&applyReaderFixture{events: &events},
		&applyRuntimeFixture{events: &events},
	)
	statePath := privateStatePath(t)
	dependencies.openState = func(ctx context.Context) (*store.Store, error) {
		return store.Open(ctx, statePath)
	}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return compose.Source{}, compose.ErrInvalidSource
	}
	if _, err = captureGitOpsSnapshot(
		t.Context(), root, state.head, dependencies,
	); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("captureGitOpsSnapshot(load failure) = %v", err)
	}

	modified := false
	dependencies.loadSource = func(_ context.Context, path string) (compose.Source, error) {
		if !modified {
			modified = true
			if writeErr := os.WriteFile(path, []byte("services: {changed: {}}\n"), 0o600); writeErr != nil {
				return compose.Source{}, fmt.Errorf("write checkout drift: %w", writeErr)
			}
		}

		return testComposeSource(t), nil
	}
	if _, err = captureGitOpsSnapshot(
		t.Context(), root, state.head, dependencies,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("captureGitOpsSnapshot(checkout drift) = %v", err)
	}
}

func TestCaptureGitOpsSnapshotRejectsInvalidServiceDirectory(t *testing.T) {
	t.Parallel()

	root := initGitOpsTestRepository(t)
	if err := os.WriteFile(filepath.Join(root, gitOpsServicesDirectory), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(services) error = %v", err)
	}
	if _, err := runGit(t.Context(), root, "add", "--", gitOpsServicesDirectory); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if _, err := runGit(
		t.Context(), root,
		"-c", "user.name=Maniud Tests", "-c", "user.email=maniud@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "block services directory",
	); err != nil {
		t.Fatalf("git commit error = %v", err)
	}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	if _, err = captureGitOpsSnapshot(t.Context(), root, state.head, applyDependencies{}); err == nil {
		t.Fatal("captureGitOpsSnapshot(service directory file) succeeded")
	}

	if err = os.WriteFile(filepath.Join(root, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(dirty) error = %v", err)
	}
	if _, err = captureGitOpsSnapshot(
		t.Context(), root, state.head, applyDependencies{},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("captureGitOpsSnapshot(dirty) = %v", err)
	}
	if err = recoverGitOpsSnapshot(
		t.Context(), root, state.head, io.Discard, applyDependencies{},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("recoverGitOpsSnapshot(dirty) = %v", err)
	}
}

func TestDependenciesWithApplySourceRejectsAnotherPath(t *testing.T) {
	t.Parallel()

	dependencies := dependenciesWithApplySource(applyDependencies{}, "/repo/api.yaml", testComposeSource(t))
	if _, err := dependencies.loadSource(t.Context(), "/repo/other.yaml"); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadSource(other path) = %v", err)
	}
}

func initGitOpsSnapshotTestRepository(t *testing.T) string {
	t.Helper()

	root := initGitOpsTestRepository(t)
	services := filepath.Join(root, gitOpsServicesDirectory)
	if err := os.MkdirAll(services, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, name := range []string{"api.yaml", "broken.yaml"} {
		if err := os.WriteFile(filepath.Join(services, name), []byte("services: {}\n"), 0o600); err != nil {
			t.Fatalf("WriteFile() error = %v", err)
		}
	}
	if _, err := runGit(t.Context(), root, "add", "--", gitOpsServicesDirectory); err != nil {
		t.Fatalf("git add error = %v", err)
	}
	if _, err := runGit(t.Context(), root,
		"-c", "user.name=Maniud Tests", "-c", "user.email=maniud@example.invalid",
		"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "add services",
	); err != nil {
		t.Fatalf("git commit error = %v", err)
	}

	return root
}
