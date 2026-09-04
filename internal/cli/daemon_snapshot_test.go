package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

//nolint:cyclop // Assertions jointly prove partial convergence and event ordering.
func TestReconcileGitOpsSnapshotSkipsInvalidSourceAndMutatesValidService(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	operations := &applyOperationsFixture{
		events: &events, dryRunPlan: application.Plan{Kind: application.PlanBootstrap},
	}
	operations.dryRun = func(request application.Request) (application.Plan, error) {
		if string(request.Source.Content) == testInvalidValue {
			return application.Plan{}, compose.ErrInvalidSource
		}

		return operations.dryRunPlan, nil
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	dependencies.loadSource = func(_ context.Context, path string) (compose.Source, error) {
		if filepath.Base(path) == testBrokenComposeName {
			return compose.Source{Content: []byte(testInvalidValue), WorkingDir: root}, nil
		}

		return testComposeSource(t), nil
	}

	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	if err = os.WriteFile(
		filepath.Join(root, gitOpsServicesDirectory, ".draft.yaml.swp"),
		[]byte("services: {}\n"),
		0o600,
	); err != nil {
		t.Fatalf("WriteFile(draft) error = %v", err)
	}
	counts, err := reconcileGitOpsSnapshotResult(
		t.Context(), root, state.head, io.Discard, dependencies,
	)
	if mutations := countEvent(events, string(commandApply)); err != nil || mutations != 1 ||
		counts.applied != 1 || counts.skipped != 1 ||
		len(counts.skippedSources) != 1 || counts.skippedSources[0].Path != "services/broken.yaml" {
		t.Fatalf(
			"reconcileGitOpsSnapshotResult() counts = %#v, error = %v, mutations = %d",
			counts,
			err,
			mutations,
		)
	}
}

func TestReconcileGitOpsSnapshotCompletesPreflightBeforeMutation(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	operations := &applyOperationsFixture{events: &events}
	operations.dryRun = func(request application.Request) (application.Plan, error) {
		if request.Service == "worker" {
			return application.Plan{}, errApplyTest
		}

		return application.Plan{Kind: application.PlanBootstrap}, nil
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	dependencies.loadSource = func(_ context.Context, path string) (compose.Source, error) {
		if filepath.Base(path) != testBrokenComposeName {
			return testComposeSource(t), nil
		}

		return compose.Source{
			Content: []byte(`name: example
services:
  worker:
    container_name: example-worker
    image: example.com/team/worker:1
    network_mode: bridge
`),
			WorkingDir: root,
		}, nil
	}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	_, err = reconcileGitOpsSnapshotResult(t.Context(), root, state.head, io.Discard, dependencies)
	if mutations := countEvent(events, string(commandApply)); !errors.Is(err, errApplyTest) || mutations != 0 {
		t.Fatalf("reconcileGitOpsSnapshotResult() error = %v, mutations = %d", err, mutations)
	}
}

func TestReconcileGitOpsSnapshotEmitsValidatedServiceResults(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	output := new(bytes.Buffer)
	events := make([]string, 0, 24)
	plan := application.Plan{
		Kind: application.PlanBootstrap,
		Warnings: []application.Warning{{
			Code: application.WarningDaemonMountProbeUnavailable,
		}},
	}
	operations := &applyOperationsFixture{events: &events, dryRunPlan: plan, applyPlan: plan}
	dependencies := operationApplyDependencies(t, &events, operations)
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}

	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	_, err = reconcileGitOpsSnapshotResult(t.Context(), root, state.head, output, dependencies)
	mutations := countEvent(events, string(commandApply))
	if err != nil || mutations != 2 {
		t.Fatalf("reconcileGitOpsSnapshotResult() error = %v, mutations = %d", err, mutations)
	}
	decoder := json.NewDecoder(output)
	for index := range mutations {
		var got applyPlan
		if decodeErr := decoder.Decode(&got); decodeErr != nil || len(got.Warnings) != 1 ||
			got.Warnings[0].Code != application.WarningDaemonMountProbeUnavailable {
			t.Fatalf("reconcileGitOpsSnapshotResult() result %d = %#v, %v", index, got, decodeErr)
		}
	}
}

func TestReconcileGitOpsSnapshotCountsUnchangedServices(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	plan := application.Plan{Kind: application.PlanUnchanged}
	operations := &applyOperationsFixture{events: &events, dryRunPlan: plan, applyPlan: plan}
	dependencies := operationApplyDependencies(t, &events, operations)
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	counts, err := reconcileGitOpsSnapshotResult(
		t.Context(), root, state.head, io.Discard, dependencies,
	)
	if err != nil || counts.unchanged != 2 || counts.applied != 0 || counts.failed != 0 {
		t.Fatalf("reconcileGitOpsSnapshotResult() counts = %#v, error = %v", counts, err)
	}
}

func TestReconcileGitOpsSnapshotReturnsMutationFailure(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	plan := application.Plan{
		Kind: application.PlanBootstrap, Project: testProjectName, Service: applyServiceValue,
		Runtime: domain.RuntimeDocker,
	}
	operations := &applyOperationsFixture{
		events: &events, dryRunPlan: plan, applyErr: errApplyTest,
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	var observed []application.Event
	dependencies.events = cliEventSinkFunc(func(event application.Event) bool {
		observed = append(observed, event)

		return false
	})
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}

	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	counts, err := reconcileGitOpsSnapshotResult(
		t.Context(), root, state.head, io.Discard, dependencies,
	)
	if !errors.Is(err, errApplyTest) || counts.failed != 1 || counts.deferred != 1 {
		t.Fatalf("reconcileGitOpsSnapshotResult() counts = %#v, error = %v", counts, err)
	}
	want := application.Event{
		Kind: application.EventGitOpsServiceApplyFailed,
		Plan: plan.Kind, Project: plan.Project, Service: plan.Service, Runtime: plan.Runtime,
	}
	if len(observed) != 1 || observed[0] != want {
		t.Fatalf("GitOps failure events = %#v, want %#v", observed, want)
	}
}

func TestExecuteGitOpsMutationDoesNotPublishApplyFailureForSourceFailure(t *testing.T) {
	t.Parallel()

	var observed []application.Event
	plan := application.Plan{
		Kind: application.PlanBootstrap, Project: testProjectName, Service: applyServiceValue,
		Runtime: domain.RuntimeDocker,
	}
	service := gitOpsServiceSnapshot{
		arguments: applyInvocation{compose: "/repo/api.yaml"},
		dependencies: applyDependencies{
			loadSource: func(context.Context, string) (compose.Source, error) {
				return compose.Source{}, compose.ErrInvalidSource
			},
			events: cliEventSinkFunc(func(event application.Event) bool {
				observed = append(observed, event)

				return false
			}),
		},
		plan: plan,
	}
	if err := executeGitOpsMutation(
		t.Context(), service, io.Discard,
	); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("executeGitOpsMutation() error = %v", err)
	}
	if len(observed) != 0 {
		t.Fatalf("GitOps failure events = %#v", observed)
	}
}

func TestReconcileGitOpsSnapshotDoesNotPublishApplyFailureForOutputFailure(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	plan := application.Plan{
		Kind:    application.PlanBootstrap,
		Project: testProjectName,
		Service: applyServiceValue,
		Runtime: domain.RuntimeDocker,
	}
	operations := &applyOperationsFixture{events: &events, dryRunPlan: plan, applyPlan: plan}
	dependencies := operationApplyDependencies(t, &events, operations)
	var observed []application.Event
	dependencies.events = cliEventSinkFunc(func(event application.Event) bool {
		observed = append(observed, event)

		return false
	})
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}

	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	_, err = reconcileGitOpsSnapshotResult(
		t.Context(),
		root,
		state.head,
		failingWriterWithError{err: errApplyOutputTest},
		dependencies,
	)
	if !errors.Is(err, errApplyOutputTest) {
		t.Fatalf("reconcileGitOpsSnapshotResult() error = %v", err)
	}
	if len(observed) != 0 {
		t.Fatalf("GitOps failure events = %#v", observed)
	}
}

func TestCaptureGitOpsSnapshotRejectsSourceAndCheckoutDrift(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	operations := &applyOperationsFixture{
		events: &events, dryRunPlan: application.Plan{Kind: application.PlanBootstrap},
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return compose.Source{}, compose.ErrInvalidSource
	}
	snapshot, err := prepareGitOpsSnapshot(t.Context(), root, state.head, dependencies)
	services := snapshot.services
	if err != nil || len(services) != 0 {
		t.Fatalf("prepareGitOpsSnapshot(load failure) = %#v, %v", snapshot, err)
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
	if _, err = prepareGitOpsSnapshot(
		t.Context(), root, state.head, dependencies,
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("prepareGitOpsSnapshot(checkout drift) = %v", err)
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
	if _, err = prepareGitOpsSnapshot(t.Context(), root, state.head, applyDependencies{}); err == nil {
		t.Fatal("prepareGitOpsSnapshot(service directory file) succeeded")
	}

	if err = os.WriteFile(filepath.Join(root, "dirty"), []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile(dirty) error = %v", err)
	}
	if _, err = prepareGitOpsSnapshot(
		t.Context(), root, state.head, applyDependencies{},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("prepareGitOpsSnapshot(dirty) = %v", err)
	}
	if _, err = recoverGitOpsSnapshotResult(
		t.Context(), root, state.head, io.Discard, applyDependencies{},
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("recoverGitOpsSnapshotResult(dirty) = %v", err)
	}
}

//nolint:cyclop,funlen // Subtests exercise independent source and checkout boundary failures.
func TestPrepareGitOpsSnapshotContainsSourceBoundaryFailures(t *testing.T) {
	t.Parallel()

	t.Run("prepared source blocker", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsSnapshotTestRepository(t)
		events := make([]string, 0, 4)
		operations := &applyOperationsFixture{
			events: &events, dryRunPlan: application.Plan{Kind: application.PlanBootstrap},
		}
		dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
		load := dependencies.loadSource
		dependencies.loadSource = func(ctx context.Context, path string) (compose.Source, error) {
			source, err := load(ctx, path)
			if source.Repository != nil {
				source.Repository.Root = filepath.Join(root, "other")
			}

			return source, err
		}
		state, err := cleanGitTree(t.Context(), root)
		if err != nil {
			t.Fatalf("cleanGitTree() error = %v", err)
		}
		snapshot, err := prepareGitOpsSnapshot(t.Context(), root, state.head, dependencies)
		if err != nil || len(snapshot.services) != 0 || len(snapshot.skipped) != 2 {
			t.Fatalf("prepareGitOpsSnapshot(blocker) = %#v, %v", snapshot, err)
		}
	})

	t.Run("prepare source", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsSnapshotTestRepository(t)
		events := make([]string, 0, 4)
		operations := &applyOperationsFixture{events: &events, dryRunErr: errApplyTest}
		dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
		state, err := cleanGitTree(t.Context(), root)
		if err != nil {
			t.Fatalf("cleanGitTree() error = %v", err)
		}
		if _, err = prepareGitOpsSnapshot(t.Context(), root, state.head, dependencies); !errors.Is(
			err,
			errApplyTest,
		) {
			t.Fatalf("prepareGitOpsSnapshot(prepare failure) error = %v", err)
		}
	})

	t.Run("capture source", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsSnapshotTestRepository(t)
		events := make([]string, 0, 4)
		operations := &applyOperationsFixture{events: &events}
		dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
		dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
			return compose.Source{}, io.ErrClosedPipe
		}
		state, err := cleanGitTree(t.Context(), root)
		if err != nil {
			t.Fatalf("cleanGitTree() error = %v", err)
		}
		if _, err = captureGitOpsSources(t.Context(), root, state.head, dependencies); !errors.Is(
			err,
			io.ErrClosedPipe,
		) {
			t.Fatalf("captureGitOpsSources(load failure) error = %v", err)
		}
	})

	t.Run("source location", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsSnapshotTestRepository(t)
		state, err := cleanGitTree(t.Context(), root)
		if err != nil {
			t.Fatalf("cleanGitTree() error = %v", err)
		}
		if _, err = captureGitOpsSourcesWithLocation(
			t.Context(), root, state.head, applyDependencies{},
			func(string, string, compose.RepositoryScope) (domain.Digest, error) {
				return domain.Digest{}, io.ErrClosedPipe
			},
		); !errors.Is(err, io.ErrClosedPipe) {
			t.Fatalf("captureGitOpsSourcesWithLocation() error = %v", err)
		}
	})

	t.Run("checkout drift after prepare", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsSnapshotTestRepository(t)
		events := make([]string, 0, 4)
		operations := &applyOperationsFixture{events: &events}
		operations.dryRun = func(application.Request) (application.Plan, error) {
			if err := os.WriteFile(filepath.Join(root, "drift"), []byte("drift\n"), 0o600); err != nil {
				return application.Plan{}, fmt.Errorf("write checkout drift: %w", err)
			}

			return application.Plan{Kind: application.PlanBootstrap}, nil
		}
		dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
		state, err := cleanGitTree(t.Context(), root)
		if err != nil {
			t.Fatalf("cleanGitTree() error = %v", err)
		}
		if _, err = prepareGitOpsSnapshot(t.Context(), root, state.head, dependencies); !errors.Is(
			err,
			errGitOpsRepositoryInvalid,
		) {
			t.Fatalf("prepareGitOpsSnapshot(checkout drift) error = %v", err)
		}
	})
}

func TestGitOpsSnapshotSourceHelpersContainInvalidEvidence(t *testing.T) {
	t.Parallel()

	if sourceMatchesRepositoryInventory(gitOpsSourceSnapshot{}, []application.RepositoryTransaction{{}}) {
		t.Fatal("sourceMatchesRepositoryInventory(nil source) succeeded")
	}
	root := t.TempDir()
	scope, err := compose.NewRepositoryScope(root, "https://example.com/repository.git", gitOpsTestBranch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	if _, err = gitOpsSourceLocation(testRelativePath, filepath.Join(root, "api.yaml"), scope); !errors.Is(
		err,
		errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("gitOpsSourceLocation(relative root) error = %v", err)
	}
	if _, err = gitOpsSourceLocation(root, filepath.Join(filepath.Dir(root), "outside.yaml"), scope); !errors.Is(
		err,
		errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("gitOpsSourceLocation(outside root) error = %v", err)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	source := testComposeSource(t)
	dependencies := applyDependencies{
		loadSource: func(context.Context, string) (compose.Source, error) { return source, nil },
	}
	if _, err = captureGitOpsSource(cancelled, "api.yaml", domain.Digest{}, dependencies); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("captureGitOpsSource(cancelled validation) error = %v", err)
	}
}

func TestPrepareRepositorySourceRecoveriesContainsComposeBlocker(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 4)
	operations := &applyOperationsFixture{events: &events}
	dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
	path := filepath.Join(root, filepath.FromSlash(tuiTestServicePath))
	source := repositoryRecoverySource(t, root, path)
	location, err := dependencies.repository.Location(source.Repository.Entry)
	if err != nil {
		t.Fatalf("RepositoryScope.Location() error = %v", err)
	}
	source.Repository.Root = filepath.Join(root, "other")
	_, err = prepareRepositorySourceRecoveries(
		t.Context(),
		gitOpsSourceSnapshot{
			path: path, source: source, services: []string{applyServiceValue}, location: location,
		},
		[]application.RepositoryTransaction{{Source: source.Repository.Digest, Location: location}},
		dependencies,
	)
	if !errors.Is(err, errGitOpsRecoverySourceBlocked) {
		t.Fatalf("prepareRepositorySourceRecoveries(compose blocker) error = %v", err)
	}
}

func TestDependenciesWithApplySourceRejectsAnotherPath(t *testing.T) {
	t.Parallel()

	dependencies := dependenciesWithApplySource(applyDependencies{}, "/repo/api.yaml", testComposeSource(t))
	if _, err := dependencies.loadSource(t.Context(), "/repo/other.yaml"); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadSource(other path) = %v", err)
	}
}

func TestGitOpsSourceBlockerRecognizesOnlyComposeValidationFailures(t *testing.T) {
	t.Parallel()

	for _, err := range []error{
		compose.ErrInvalidSource,
		fmt.Errorf("wrapped: %w", compose.ErrExternalSource),
	} {
		if !gitOpsSourceBlocker(err) {
			t.Fatalf("gitOpsSourceBlocker(%v) = false", err)
		}
	}
	for _, err := range []error{nil, errApplyTest} {
		if gitOpsSourceBlocker(err) {
			t.Fatalf("gitOpsSourceBlocker(%v) = true", err)
		}
	}
}

func initGitOpsSnapshotTestRepository(t *testing.T) string {
	t.Helper()

	root := initGitOpsTestRepository(t)
	services := filepath.Join(root, gitOpsServicesDirectory)
	if err := os.MkdirAll(services, 0o700); err != nil {
		t.Fatalf("MkdirAll() error = %v", err)
	}
	for _, name := range []string{"api.yaml", testBrokenComposeName} {
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
