package application

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type newApplyTest struct {
	name    string
	observe func(domain.DesiredWorkload) WorkloadObservation
	want    PlanKind
}

func TestDryRunClassifiesNewWorkloadsWithoutMutation(t *testing.T) {
	t.Parallel()

	for _, test := range newApplyTests() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operation := newTestOperation(t)

			var observation WorkloadObservation

			operation.runtime.observe = func(
				_ context.Context,
				workload domain.DesiredWorkload,
			) (WorkloadObservation, error) {
				*operation.events = append(*operation.events, "observe")
				observation = test.observe(workload)

				return observation, nil
			}

			plan, err := operation.service.DryRun(context.Background(), operation.request)
			if err != nil {
				t.Fatalf("DryRun() error = %v", err)
			}

			assertNewApplyPlan(t, plan, test, observation)
			assertEvents(t, operation.events, []string{"inspect", "resolve", "check", eventJournal, "observe"})
		})
	}
}

func newApplyTests() []newApplyTest {
	return []newApplyTest{
		{
			name:    "bootstrap",
			observe: fixedObservation(missingObservation()),
			want:    PlanBootstrap,
		},
		{
			name:    "adopt",
			observe: fixedObservation(presentObservation(true, true, domain.OwnershipUnmanaged)),
			want:    PlanAdopt,
		},
		{
			name:    "unchanged",
			observe: matchingManagedObservation,
			want:    PlanUnchanged,
		},
		{
			name:    "upgrade",
			observe: changedManagedObservation,
			want:    PlanUpgrade,
		},
	}
}

func assertNewApplyPlan(
	t *testing.T,
	plan Plan,
	test newApplyTest,
	observation WorkloadObservation,
) {
	t.Helper()

	valid := plan.Kind == test.want && plan.Project == testProjectName && plan.Service == testServiceName &&
		plan.Runtime == domain.RuntimeDocker && plan.Platform.OS == testOperatingSystem &&
		plan.Image.Reference == "example.com/team/api:1@"+plan.Image.ReferenceDigest.String() &&
		plan.Source != (domain.Digest{}) && plan.Desired != (domain.Digest{}) &&
		plan.Observation == observation
	if !valid {
		t.Fatalf("DryRun() = %#v", plan)
	}
}

func fixedObservation(value WorkloadObservation) func(domain.DesiredWorkload) WorkloadObservation {
	return func(domain.DesiredWorkload) WorkloadObservation { return value }
}

func matchingManagedObservation(workload domain.DesiredWorkload) WorkloadObservation {
	return WorkloadObservation{
		State:                WorkloadObservationPresent,
		ConfigurationMatches: true,
		Running:              true,
		Ownership:            desiredOwnership(workload),
	}
}

func changedManagedObservation(workload domain.DesiredWorkload) WorkloadObservation {
	observation := matchingManagedObservation(workload)
	observation.ConfigurationMatches = false
	observation.Ownership.DesiredState = domain.Hash([]byte("previous desired state"))

	return observation
}

func desiredOwnership(workload domain.DesiredWorkload) domain.WorkloadOwnership {
	return domain.WorkloadOwnership{
		Status:           domain.OwnershipManaged,
		Service:          workload.ServiceName,
		Transaction:      "01010101010101010101010101010101",
		DesiredState:     workload.EffectiveDigest,
		Reference:        workload.Image.ReferenceDigest,
		ImageConfig:      workload.Image.ImageConfig,
		PlatformManifest: workload.Image.PlatformManifest,
	}
}

type recoveryTest struct {
	name    string
	state   store.TransactionState
	actions func(store.Transaction) []store.Action
	want    PlanKind
}

func TestPrepareClassifiesDurableRecovery(t *testing.T) {
	t.Parallel()

	for _, test := range recoveryTests() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			testRecoveryClassification(t, test)
		})
	}
}

func recoveryTests() []recoveryTest {
	return []recoveryTest{
		{name: "active before effects", state: store.TransactionActive, actions: nil, want: PlanResume},
		{
			name:  "active completed effect",
			state: store.TransactionActive,
			actions: func(transaction store.Transaction) []store.Action {
				return []store.Action{action(transaction, 1, store.ActionStateCompleted)}
			},
			want: PlanResume,
		},
		{
			name:  "active intent",
			state: store.TransactionActive,
			actions: func(transaction store.Transaction) []store.Action {
				return []store.Action{action(transaction, 1, store.ActionStateIntent)}
			},
			want: PlanResume,
		},
		{
			name:  "unknown effect",
			state: store.TransactionActive,
			actions: func(transaction store.Transaction) []store.Action {
				return []store.Action{action(transaction, 1, store.ActionStateEffectOutcomeUnknown)}
			},
			want: PlanProbeUnknownEffect,
		},
		{name: "degraded", state: store.TransactionDegraded, actions: nil, want: PlanRestore},
	}
}

func testRecoveryClassification(t *testing.T, test recoveryTest) {
	t.Helper()

	operation := newTestOperation(t)

	baseline, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare(baseline) error = %v", err)
	}

	transaction := exactTransaction(baseline, test.state)
	actions := recoveryActions(test, transaction)
	*operation.events = (*operation.events)[:0]
	installRecoveryEvidence(operation, transaction, actions)

	preparation, err := operation.service.Prepare(context.Background(), operation.request)
	if err != nil {
		t.Fatalf("Prepare() error = %v", err)
	}

	valid := preparation.Plan.Kind == test.want && preparation.HasTransaction &&
		preparation.Transaction == transaction && reflect.DeepEqual(preparation.Actions, actions)
	if !valid {
		t.Fatalf("Prepare() = %#v", preparation)
	}

	assertEvents(
		t,
		operation.events,
		[]string{"inspect", "resolve", "check", eventJournal, "observe", "actions"},
	)
}

func recoveryActions(test recoveryTest, transaction store.Transaction) []store.Action {
	if test.actions == nil {
		return nil
	}

	return test.actions(transaction)
}

func installRecoveryEvidence(
	operation testOperation,
	transaction store.Transaction,
	actions []store.Action,
) {
	operation.transactions.unresolved = func(
		context.Context,
		string,
		string,
	) (store.Transaction, bool, error) {
		*operation.events = append(*operation.events, eventJournal)

		return transaction, true, nil
	}
	operation.transactions.actions = func(
		context.Context,
		store.TransactionID,
	) ([]store.Action, error) {
		*operation.events = append(*operation.events, "actions")

		return actions, nil
	}
}

func TestPrepareRejectsConflictingNewRuntimeState(t *testing.T) {
	t.Parallel()

	tests := []WorkloadObservation{
		emptyObservation(),
		presentObservation(false, false, domain.OwnershipConflicting),
		presentObservation(true, false, domain.OwnershipUnmanaged),
		presentObservation(true, true, domain.OwnershipManaged),
	}

	for index, observation := range tests {
		operation := newTestOperation(t)
		operation.runtime.observe = func(context.Context, domain.DesiredWorkload) (WorkloadObservation, error) {
			return observation, nil
		}

		_, err := operation.service.Prepare(context.Background(), operation.request)
		if !errors.Is(err, ErrConflictingState) {
			t.Fatalf("Prepare(case %d) error = %v, want ErrConflictingState", index, err)
		}
	}
}

func presentObservation(
	configurationMatches bool,
	running bool,
	status domain.OwnershipStatus,
) WorkloadObservation {
	return WorkloadObservation{
		State:                WorkloadObservationPresent,
		ConfigurationMatches: configurationMatches,
		Running:              running,
		Ownership:            testOwnership(status),
	}
}

func assertEvents(t *testing.T, got *[]string, want []string) {
	t.Helper()

	if !reflect.DeepEqual(*got, want) {
		t.Fatalf("events = %#v, want %#v", *got, want)
	}
}
