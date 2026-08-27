package cli

import (
	"context"
	"errors"
	"io"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
)

func TestRecoverGitOpsSnapshotSkipsNewOperations(t *testing.T) {
	t.Parallel()

	root := initGitOpsSnapshotTestRepository(t)
	events := make([]string, 0, 16)
	operations := &applyOperationsFixture{
		events: &events, dryRunPlan: application.Plan{Kind: application.PlanBootstrap},
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}
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
