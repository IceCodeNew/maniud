package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/store"
)

func TestRunRestoreRecoversEveryDegradedUpgradeBoundary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
	}{
		{
			name: "rename negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRename] = true
			},
		},
		{
			name: "create negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.createMissing = true
			},
		},
		{
			name: "start negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startUnchanged = true
			},
		},
		{
			name: "remove negative",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRemove] = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			test.configure(runtime)

			err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
			if !errors.Is(err, ErrConflictingState) ||
				mutation.preparation.Transaction.State != store.TransactionDegraded {
				t.Fatalf("runUpgrade() = %v, transaction %#v", err, mutation.preparation.Transaction)
			}

			prepareRestoreMutation(t, state, mutation)
			err = runRestore(context.Background(), mutation, runtime)
			if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed {
				t.Fatalf("runRestore() = %v, transaction %#v", err, mutation.preparation.Transaction)
			}

			if runtime.predecessor != newUpgradeJourney(mutation.preparation).stop.Before {
				t.Fatalf("restored predecessor = %#v", runtime.predecessor)
			}
		})
	}
}

func TestRunRestoreResumesUnknownRecoveryActionsWithoutReplay(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
		interrupt func(*upgradeRuntimeFixture)
		clear     func(*upgradeRuntimeFixture)
		effects   func(*upgradeRuntimeFixture) int
	}{
		{
			name: "discard",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startUnchanged = true
			},
			interrupt: func(runtime *upgradeRuntimeFixture) {
				runtime.discardProbeErr = errTestBoundary
			},
			clear: func(runtime *upgradeRuntimeFixture) {
				runtime.discardProbeErr = nil
			},
			effects: func(runtime *upgradeRuntimeFixture) int { return runtime.discards },
		},
		{
			name: "reverse rename",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startUnchanged = true
			},
			interrupt: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionProbeAt[runtime.transitionProbes+1] = errTestBoundary
			},
			clear: func(runtime *upgradeRuntimeFixture) {
				delete(runtime.transitionProbeAt, runtime.transitionProbes)
			},
			effects: func(runtime *upgradeRuntimeFixture) int {
				return runtime.transitionApplies[WorkloadTransitionRename]
			},
		},
		{
			name: "restore start",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRename] = true
			},
			interrupt: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionProbeAt[runtime.transitionProbes+1] = errTestBoundary
			},
			clear: func(runtime *upgradeRuntimeFixture) {
				delete(runtime.transitionProbeAt, runtime.transitionProbes)
			},
			effects: func(runtime *upgradeRuntimeFixture) int {
				return runtime.transitionApplies[WorkloadTransitionRestoreStart]
			},
		},
	}

	runUnknownRestoreCases(t, tests)
}

func TestRunRestoreRejectsNegativeRecoveryPostconditions(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
		breakStep func(*upgradeRuntimeFixture)
	}{
		{
			name: "reverse rename",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.startUnchanged = true
			},
			breakStep: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRename] = true
			},
		},
		{
			name: "restore start",
			configure: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRename] = true
			},
			breakStep: func(runtime *upgradeRuntimeFixture) {
				runtime.transitionSkip[WorkloadTransitionRestoreStart] = true
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			test.configure(runtime)
			degradeUpgrade(t, state, mutation, runtime)
			test.breakStep(runtime)

			err := runRestore(context.Background(), mutation, runtime)
			if !errors.Is(err, ErrConflictingState) ||
				mutation.preparation.Transaction.State != store.TransactionDegraded {
				t.Fatalf("runRestore() = %v, transaction %#v", err, mutation.preparation.Transaction)
			}
		})
	}
}

func degradeUpgrade(
	t *testing.T,
	state *store.Store,
	mutation *boundMutation,
	runtime *upgradeRuntimeFixture,
) {
	t.Helper()

	err := runUpgrade(context.Background(), mutation, runtime, bootstrapCredentials{})
	if err == nil || mutation.preparation.Transaction.State != store.TransactionDegraded {
		t.Fatalf("runUpgrade() = %v, transaction %#v", err, mutation.preparation.Transaction)
	}

	prepareRestoreMutation(t, state, mutation)
}

func prepareRestoreMutation(t *testing.T, state *store.Store, mutation *boundMutation) {
	t.Helper()

	mutation.preparation.Plan.Kind = PlanRestore
	mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
}

func runUnknownRestoreCases(
	t *testing.T,
	tests []struct {
		name      string
		configure func(*upgradeRuntimeFixture)
		interrupt func(*upgradeRuntimeFixture)
		clear     func(*upgradeRuntimeFixture)
		effects   func(*upgradeRuntimeFixture) int
	},
) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			test.configure(runtime)
			degradeUpgrade(t, state, mutation, runtime)
			test.interrupt(runtime)

			err := runRestore(context.Background(), mutation, runtime)
			if !errors.Is(err, errTestBoundary) {
				t.Fatalf("runRestore(interrupted) = %v", err)
			}
			before := test.effects(runtime)
			mutation.preparation.Actions = readUpgradeActions(t, state, mutation)
			test.clear(runtime)

			err = runRestore(context.Background(), mutation, runtime)
			if err != nil || mutation.preparation.Transaction.State != store.TransactionFailed {
				t.Fatalf("runRestore(resumed) = %v, transaction %#v", err, mutation.preparation.Transaction)
			}
			if test.effects(runtime) != before {
				t.Fatalf("replayed recovery effect: before %d, after %d", before, test.effects(runtime))
			}
		})
	}
}
