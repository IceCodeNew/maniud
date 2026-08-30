package cli

import (
	"context"
	"errors"
	"io"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestRecoverGitOpsSnapshotSkipsNewOperations(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	operations := &applyOperationsFixture{
		events: &events, dryRunPlan: application.Plan{Kind: application.PlanBootstrap},
	}
	dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}
	if err = recoverGitOpsSnapshot(t.Context(), root, state.head, io.Discard, dependencies); err != nil {
		t.Fatalf("recoverGitOpsSnapshot() error = %v", err)
	}
	if err = recoverGitOpsSnapshot(
		t.Context(), root, "ffffffffffffffffffffffffffffffffffffffff", io.Discard, dependencies,
	); err == nil {
		t.Fatal("recoverGitOpsSnapshot(mismatched commit) succeeded")
	}
}

func TestRecoverGitOpsSnapshotReadsInventoryBeforeSources(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 4)
	operations := &applyOperationsFixture{events: &events, inventoryErr: errApplyTest}
	dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
	loaded := false
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		loaded = true

		return compose.Source{}, compose.ErrInvalidSource
	}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	err = recoverGitOpsSnapshot(t.Context(), root, state.head, io.Discard, dependencies)
	if !errors.Is(err, errApplyTest) || loaded {
		t.Fatalf("recoverGitOpsSnapshot() error = %v, loaded = %t", err, loaded)
	}
}

func TestRecoverGitOpsSnapshotMatchesEveryRepositoryTransaction(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 8)
	operations := &applyOperationsFixture{
		events: &events,
		dryRunPlan: application.Plan{
			Kind: application.PlanResume,
		},
		applyPlan: application.Plan{Kind: application.PlanResume},
	}
	dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
	path := filepath.Join(root, gitOpsServicesDirectory, "api.yaml")
	source := repositoryRecoverySource(t, root, path)
	location, err := dependencies.repository.Location(source.Repository.Entry)
	if err != nil {
		t.Fatalf("RepositoryScope.Location() error = %v", err)
	}
	operations.inventory = []application.RepositoryTransaction{{
		Source: source.Repository.Digest, Location: location,
	}}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	err = recoverGitOpsSnapshot(t.Context(), root, state.head, io.Discard, dependencies)
	if mutations := countEvent(events, string(commandApply)); err != nil || mutations != 1 ||
		operations.inventoryScope != dependencies.repository {
		t.Fatalf(
			"recoverGitOpsSnapshot() error = %v, mutations = %d, scope = %#v",
			err,
			mutations,
			operations.inventoryScope,
		)
	}
}

func TestRecoverGitOpsSnapshotEnumeratesMultipleServicesInOneSource(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 12)
	operations := &applyOperationsFixture{
		events:     &events,
		dryRunPlan: application.Plan{Kind: application.PlanResume},
		applyPlan:  application.Plan{Kind: application.PlanResume},
	}
	dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
	path := filepath.Join(root, gitOpsServicesDirectory, "api.yaml")
	content := []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
  worker:
    container_name: example-worker
    image: example.com/team/worker:1
    network_mode: bridge
`)
	source := repositoryRecoverySourceWithContent(t, root, path, content)
	load := dependencies.loadSource
	dependencies.loadSource = func(ctx context.Context, requested string) (compose.Source, error) {
		if requested == path {
			return source, nil
		}

		return load(ctx, requested)
	}
	location, err := dependencies.repository.Location(source.Repository.Entry)
	if err != nil {
		t.Fatalf("RepositoryScope.Location() error = %v", err)
	}
	operations.inventory = []application.RepositoryTransaction{
		{Source: source.Repository.Digest, Location: location},
		{Source: source.Repository.Digest, Location: location},
	}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	err = recoverGitOpsSnapshot(t.Context(), root, state.head, io.Discard, dependencies)
	if mutations := countEvent(events, string(commandApply)); err != nil || mutations != 2 {
		t.Fatalf("recoverGitOpsSnapshot() error = %v, mutations = %d", err, mutations)
	}
}

type blockedRecoverySourceTest struct {
	name        string
	entry       string
	drift       bool
	loadFail    bool
	notRecovery bool
}

func TestRecoverGitOpsSnapshotBlocksUnprovableRecoverySources(t *testing.T) {
	t.Parallel()

	tests := []blockedRecoverySourceTest{
		{name: "absent source", entry: "services/missing.yaml"},
		{name: "malformed source", entry: tuiTestServicePath, loadFail: true},
		{name: "drift", entry: tuiTestServicePath, drift: true},
		{name: "transaction not recovered", entry: tuiTestServicePath, notRecovery: true},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assertBlockedRecoverySource(t, test)
		})
	}
}

func assertBlockedRecoverySource(t *testing.T, test blockedRecoverySourceTest) {
	t.Helper()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 8)
	operations := &applyOperationsFixture{
		events: &events, dryRunPlan: application.Plan{Kind: application.PlanResume},
	}
	if test.notRecovery {
		operations.dryRunPlan.Kind = application.PlanBootstrap
	}
	dependencies := repositoryRecoveryDependencies(t, root, &events, operations)
	location, err := dependencies.repository.Location(test.entry)
	if err != nil {
		t.Fatalf("RepositoryScope.Location() error = %v", err)
	}
	path := filepath.Join(root, filepath.FromSlash(test.entry))
	source := repositoryRecoverySource(t, root, path)
	digest := source.Repository.Digest
	if test.drift {
		digest = domain.Hash([]byte("superseded source"))
	}
	operations.inventory = []application.RepositoryTransaction{{
		Source: digest, Location: location,
	}}
	if test.loadFail {
		load := dependencies.loadSource
		dependencies.loadSource = func(ctx context.Context, requested string) (compose.Source, error) {
			if requested == path {
				return compose.Source{}, compose.ErrInvalidSource
			}

			return load(ctx, requested)
		}
	}
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	err = recoverGitOpsSnapshot(t.Context(), root, state.head, io.Discard, dependencies)
	mutations := countEvent(events, string(commandApply))
	if !errors.Is(err, errGitOpsRecoverySourceBlocked) || mutations != 0 {
		t.Fatalf("recoverGitOpsSnapshot() error = %v, mutations = %d", err, mutations)
	}
}

func TestRecoverGitOpsServicesRunsOnlyRecoveryPlans(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 8)
	operations := &applyOperationsFixture{
		events: &events, applyPlan: application.Plan{Kind: application.PlanResume},
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	services := []gitOpsServiceSnapshot{
		{plan: application.Plan{Kind: application.PlanUpgrade}},
		{
			arguments: applyInvocation{
				compose: composeFileValue,
				service: applyServiceValue,
			},
			dependencies: dependencies,
			plan:         application.Plan{Kind: application.PlanResume},
		},
	}
	if err := recoverGitOpsServices(t.Context(), services, io.Discard); err != nil {
		t.Fatalf("recoverGitOpsServices() error = %v", err)
	}
	if got := countEvent(events, string(commandApply)); got != 1 {
		t.Fatalf("mutation events = %d, events = %q", got, events)
	}

	operations.applyErr = errApplyTest
	services[1].dependencies = dependencies
	if err := recoverGitOpsServices(t.Context(), services[1:], io.Discard); !errors.Is(err, errApplyTest) {
		t.Fatalf("recoverGitOpsServices(failure) = %v", err)
	}
}

func TestGitOpsRecoveryPlan(t *testing.T) {
	t.Parallel()

	for _, kind := range []application.PlanKind{
		application.PlanResume,
		application.PlanProbeUnknownEffect,
		application.PlanRestore,
	} {
		if !gitOpsRecoveryPlan(kind) {
			t.Fatalf("gitOpsRecoveryPlan(%q) = false", kind)
		}
	}
	if gitOpsRecoveryPlan(application.PlanUpgrade) {
		t.Fatal("gitOpsRecoveryPlan(upgrade) = true")
	}
}

func countEvent(events []string, wanted string) int {
	count := 0
	for _, event := range events {
		if event == wanted {
			count++
		}
	}

	return count
}

func repositoryRecoveryDependencies(
	t *testing.T,
	root string,
	events *[]string,
	operations *applyOperationsFixture,
) applyDependencies {
	t.Helper()

	scope, err := compose.NewRepositoryScope(
		root,
		filepath.Join(root, "remote.git"),
		gitOpsTestBranch,
	)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	dependencies := operationApplyDependencies(t, events, operations)
	dependencies.repositoryRoot = root
	dependencies.repository = scope
	dependencies.loadSource = func(_ context.Context, path string) (compose.Source, error) {
		return repositoryRecoverySource(t, root, path), nil
	}

	return dependencies
}

func repositoryRecoverySource(t *testing.T, root, path string) compose.Source {
	t.Helper()

	return repositoryRecoverySourceWithContent(t, root, path, testComposeSource(t).Content)
}

func repositoryRecoverySourceWithContent(
	t *testing.T,
	root string,
	path string,
	content []byte,
) compose.Source {
	t.Helper()

	entry, err := filepath.Rel(root, path)
	if err != nil {
		t.Fatalf("filepath.Rel() error = %v", err)
	}
	entry = filepath.ToSlash(entry)
	source, err := compose.CaptureRepositorySource(
		root,
		entry,
		nil,
		func(name string) (compose.RepositoryFile, bool, error) {
			if name != entry {
				return compose.RepositoryFile{}, false, nil
			}

			return compose.RepositoryFile{Content: content}, true, nil
		},
		func(string) (compose.RepositoryPathSnapshot, error) {
			return compose.RepositoryPathSnapshot{}, compose.ErrInvalidSource
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}

	return source
}
