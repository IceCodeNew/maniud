package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/store"
)

func TestRunUpgradeResolvesTypedStageFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
		wantState store.TransactionState
	}{
		{
			name: "image pull negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}
				runtime.pullUnchanged = true
			},
			wantState: store.TransactionFailed,
		},
		{
			name: "stop negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionStop] = true
			},
			wantState: store.TransactionFailed,
		},
		{
			name: "rename negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRename] = true
			},
			wantState: store.TransactionDegraded,
		},
		{
			name: "create negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.createMissing = true
			},
			wantState: store.TransactionDegraded,
		},
		{
			name: "start negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startUnchanged = true
			},
			wantState: store.TransactionDegraded,
		},
		{
			name: "remove negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRemove] = true
			},
			wantState: store.TransactionDegraded,
		},
	}

	runUpgradeFailureCases(t, tests, ErrConflictingState)
}

func TestRunUpgradePersistsNegativePostconditionAfterEffectError(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
		wantState store.TransactionState
	}{
		{
			name: "stop",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionApply[WorkloadTransitionStop] = errTestBoundary
			},
			wantState: store.TransactionFailed,
		},
		{
			name: "rename",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionApply[WorkloadTransitionRename] = errTestBoundary
			},
			wantState: store.TransactionDegraded,
		},
		{
			name: "create",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.createApplyErr = errTestBoundary
			},
			wantState: store.TransactionDegraded,
		},
		{
			name: "start",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startApplyErr = errTestBoundary
			},
			wantState: store.TransactionDegraded,
		},
		{
			name: "remove",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionApply[WorkloadTransitionRemove] = errTestBoundary
			},
			wantState: store.TransactionDegraded,
		},
	}

	runCompletedUpgradeFailureCases(t, tests)
}

func TestRunUpgradeKeepsUnresolvedProbeFailureActive(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
	}{
		{
			name: "image before effect",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.imageProbeErrAt[1] = errTestBoundary
			},
		},
		{
			name: "image after effect",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.image = ImageProbe{State: ImageProbeMissing, Image: emptyImageEvidence()}
				runtime.imageProbeErrAt[2] = errTestBoundary
			},
		},
		{
			name: "stop",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionProbeAt[1] = errTestBoundary
			},
		},
		{
			name: "rename",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionProbeAt[2] = errTestBoundary
			},
		},
		{
			name: "create",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.createProbeErr = errTestBoundary
			},
		},
		{
			name: "start",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startProbeErrAt[1] = errTestBoundary
			},
		},
		{
			name: "remove",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionProbeAt[3] = errTestBoundary
			},
		},
		{
			name: "completion",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startProbeErrAt[2] = errTestBoundary
			},
		},
	}

	runUnknownUpgradeFailureCases(t, tests)
}

func runUpgradeFailureCases(
	t *testing.T,
	tests []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
		wantState store.TransactionState
	},
	want error,
) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			test.configure(runtime)

			err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
			if !errors.Is(err, want) || mutation.preparation.Transaction.State != test.wantState {
				t.Fatalf("runUpgrade() = %v, transaction %#v", err, mutation.preparation.Transaction)
			}
		})
	}
}

func runCompletedUpgradeFailureCases(
	t *testing.T,
	tests []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
		wantState store.TransactionState
	},
) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			test.configure(runtime)

			err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
			if !errors.Is(err, errTestBoundary) ||
				mutation.preparation.Transaction.State != test.wantState {
				t.Fatalf("runUpgrade() = %v, transaction %#v", err, mutation.preparation.Transaction)
			}

			actions := readUpgradeActions(t, state, mutation)
			if actions[len(actions)-1].State != store.ActionStateCompleted {
				t.Fatalf("failure action = %#v", actions[len(actions)-1])
			}
		})
	}
}

func runUnknownUpgradeFailureCases(
	t *testing.T,
	tests []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
	},
) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			test.configure(runtime)

			err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
			if !errors.Is(err, errTestBoundary) ||
				mutation.preparation.Transaction.State != store.TransactionActive {
				t.Fatalf("runUpgrade() = %v, transaction %#v", err, mutation.preparation.Transaction)
			}
		})
	}
}
