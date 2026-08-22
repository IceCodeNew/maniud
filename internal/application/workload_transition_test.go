package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	testRetainedWorkloadName = "maniud-old-01020304"
	testReplacementWorkload  = "maniud-new-01020304"
)

type transitionRuntimeFixture struct {
	events             *[]string
	applied            bool
	resumed            bool
	transition         WorkloadTransition
	probe              WorkloadTransitionProbe
	probeAfterApply    *WorkloadTransitionProbe
	probeErrAfterApply error
	applyErr           error
	probeErr           error
}

func (runtime *transitionRuntimeFixture) ApplyWorkloadTransition(
	_ context.Context,
	transition WorkloadTransition,
) error {
	*runtime.events = append(*runtime.events, eventEffect)
	runtime.applied = true
	runtime.transition = transition
	if runtime.probeAfterApply != nil {
		runtime.probe = *runtime.probeAfterApply
	}
	if runtime.probeErrAfterApply != nil {
		runtime.probeErr = runtime.probeErrAfterApply
	}

	return runtime.applyErr
}

func (runtime *transitionRuntimeFixture) ResumeIncompleteWorkloadTransition(
	ctx context.Context,
	transition WorkloadTransition,
) error {
	runtime.resumed = true

	return runtime.ApplyWorkloadTransition(ctx, transition)
}

func (runtime *transitionRuntimeFixture) ProbeWorkloadTransition(
	_ context.Context,
	transition WorkloadTransition,
) (WorkloadTransitionProbe, error) {
	*runtime.events = append(*runtime.events, eventProbe)
	runtime.transition = transition

	return runtime.probe, runtime.probeErr
}

func TestRunWorkloadTransitionFencesEverySupportedOperation(t *testing.T) {
	t.Parallel()

	for _, transition := range testWorkloadTransitions() {
		t.Run(workloadTransitionActionKind(transition.Kind), func(t *testing.T) {
			t.Parallel()

			identifier := store.TransactionID{1}
			journal := imageEffectJournal(store.ActionStateIntent)
			runtime := transitionRuntimeAfter(&journal.events, transition)

			got, err := runWorkloadTransition(
				context.Background(), journal, identifier, 3, transition, runtime,
			)
			if err != nil || !got.Satisfied || got.Digest == (domain.Digest{}) ||
				!runtime.applied || runtime.transition != transition {
				t.Fatalf("runWorkloadTransition() = %#v, %v, runtime %#v", got, err, runtime)
			}

			intent := workloadTransitionIntent(3, transition)
			if journal.action.Kind != workloadTransitionActionKind(transition.Kind) ||
				journal.action.IntentDigest != intent.IntentDigest ||
				!equalEvents(journal.events, newEffectEvents()) {
				t.Fatalf("transition journal = %#v, events %q", journal.action, journal.events)
			}
		})
	}
}

func TestRunWorkloadTransitionRecoversUnknownWithoutReplay(t *testing.T) {
	t.Parallel()

	transition := testStopTransition()
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateEffectOutcomeUnknown)
	runtime := transitionRuntimeAfter(&journal.events, transition)

	got, err := runWorkloadTransition(context.Background(), journal, identifier, 1, transition, runtime)
	if err != nil || !got.Satisfied || runtime.applied ||
		!equalEvents(journal.events, []string{eventIntent, eventProbe, eventComplete}) {
		t.Fatalf("runWorkloadTransition(unknown) = %#v, %v, runtime %#v", got, err, runtime)
	}
}

func TestRunWorkloadTransitionCompletesResolvedNegativeResult(t *testing.T) {
	t.Parallel()

	transition := testStopTransition()
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := transitionRuntimeAfter(&journal.events, transition)
	runtime.probe.Workload = transition.Before
	runtime.applyErr = errTestBoundary

	got, err := runWorkloadTransition(context.Background(), journal, identifier, 1, transition, runtime)
	if !errors.Is(err, errTestBoundary) || got.Satisfied || got.Digest == (domain.Digest{}) ||
		journal.action.State != store.ActionStateCompleted {
		t.Fatalf("runWorkloadTransition(before) = %#v, %v", got, err)
	}
}

func TestRunWorkloadRemovalResumesIncompleteUnknownEffect(t *testing.T) {
	t.Parallel()

	transition := testWorkloadTransitions()[2]
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateEffectOutcomeUnknown)
	missing := WorkloadTransitionProbe{State: WorkloadEffectProbeMissing}
	removing := transition.Before
	removing.Lifecycle = WorkloadLifecycleRemoving
	runtime := &transitionRuntimeFixture{
		events: &journal.events,
		probe: WorkloadTransitionProbe{
			State: WorkloadEffectProbeObserved, Workload: removing,
		},
		probeAfterApply: &missing,
	}

	got, err := runWorkloadTransition(context.Background(), journal, identifier, 1, transition, runtime)
	if err != nil || !got.Satisfied || !runtime.applied || !runtime.resumed ||
		journal.action.State != store.ActionStateCompleted ||
		!equalEvents(journal.events, []string{
			eventIntent, eventProbe, eventFence, eventEffect, eventProbe, eventComplete,
		}) {
		t.Fatalf(
			"runWorkloadTransition(incomplete remove) = %#v, %v, runtime %#v, events %q",
			got, err, runtime, journal.events,
		)
	}
}

func TestRunWorkloadRemovalRetainsUnknownEffectWhenResumeIsIncomplete(t *testing.T) {
	t.Parallel()

	transition := testWorkloadTransitions()[2]
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	removing := transition.Before
	removing.Lifecycle = WorkloadLifecycleRemoving
	runtime := &transitionRuntimeFixture{
		events: &journal.events,
		probe: WorkloadTransitionProbe{
			State: WorkloadEffectProbeObserved, Workload: removing,
		},
		applyErr: errTestBoundary,
	}

	got, err := runWorkloadTransition(context.Background(), journal, identifier, 1, transition, runtime)
	if !errors.Is(err, errTestBoundary) || got != emptyEffectPostcondition() || !runtime.applied ||
		journal.action.State != store.ActionStateEffectOutcomeUnknown ||
		!equalEvents(journal.events, []string{
			eventIntent, eventFence, eventUnknown, eventEffect, eventProbe, eventFence, eventEffect, eventProbe,
		}) {
		t.Fatalf(
			"runWorkloadTransition(persistent remove failure) = %#v, %v, runtime %#v, events %q",
			got, err, runtime, journal.events,
		)
	}
}

func TestRunWorkloadRemovalContainsResumeBoundaryFailures(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name     string
		fenceErr error
		probeErr error
		events   []string
	}{
		{
			name: eventFence, fenceErr: errTestBoundary,
			events: []string{eventIntent, eventProbe, eventFence},
		},
		{
			name: "resumed probe", probeErr: errTestBoundary,
			events: []string{eventIntent, eventProbe, eventFence, eventEffect, eventProbe},
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			transition := testWorkloadTransitions()[2]
			removing := transition.Before
			removing.Lifecycle = WorkloadLifecycleRemoving
			journal := imageEffectJournal(store.ActionStateEffectOutcomeUnknown)
			journal.failures[eventFence] = test.fenceErr
			runtime := &transitionRuntimeFixture{
				events: &journal.events,
				probe: WorkloadTransitionProbe{
					State: WorkloadEffectProbeObserved, Workload: removing,
				},
				probeErrAfterApply: test.probeErr,
			}

			got, err := runWorkloadTransition(
				context.Background(), journal, store.TransactionID{1}, 1, transition, runtime,
			)
			if !errors.Is(err, errTestBoundary) || got != emptyEffectPostcondition() ||
				journal.action.State != store.ActionStateEffectOutcomeUnknown ||
				!equalEvents(journal.events, test.events) {
				t.Fatalf("runWorkloadTransition(%s) = %#v, %v, events %q", test.name, got, err, journal.events)
			}
		})
	}
}

func TestWorkloadRemovalRejectsResumeWithoutRuntimeSupport(t *testing.T) {
	t.Parallel()

	effect := &workloadTransitionEffect{
		runtime: &transitionRuntimeWithoutResume{}, transition: testWorkloadTransitions()[2], resumeNeeded: true,
	}
	if err := effect.resumeIncomplete(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("resumeIncomplete() = %v", err)
	}
}

type transitionRuntimeWithoutResume struct{}

func (*transitionRuntimeWithoutResume) ApplyWorkloadTransition(context.Context, WorkloadTransition) error {
	return nil
}

func (*transitionRuntimeWithoutResume) ProbeWorkloadTransition(
	context.Context,
	WorkloadTransition,
) (WorkloadTransitionProbe, error) {
	return WorkloadTransitionProbe{}, nil
}

func TestWorkloadTransitionEffectRejectsUnprovenPostconditions(t *testing.T) {
	t.Parallel()

	transition := testStopTransition()
	unexpected := transition.After
	unexpected.ID = testDifferentWorkload

	for _, test := range []struct {
		name     string
		probe    WorkloadTransitionProbe
		probeErr error
		want     error
	}{
		{
			name:     "probe failure",
			probe:    WorkloadTransitionProbe{State: WorkloadEffectProbeUnknown},
			probeErr: errTestBoundary,
			want:     errTestBoundary,
		},
		{
			name:  "missing stop",
			probe: WorkloadTransitionProbe{State: WorkloadEffectProbeMissing},
			want:  ErrConflictingState,
		},
		{
			name:  "missing with evidence",
			probe: WorkloadTransitionProbe{State: WorkloadEffectProbeMissing, Workload: transition.Before},
			want:  ErrConflictingState,
		},
		{
			name:  "unexpected observed",
			probe: WorkloadTransitionProbe{State: WorkloadEffectProbeObserved, Workload: unexpected},
			want:  ErrConflictingState,
		},
		{
			name:  eventUnknown,
			probe: WorkloadTransitionProbe{State: WorkloadEffectProbeUnknown},
			want:  ErrConflictingState,
		},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make([]string, 0, 2)
			effect := workloadTransitionEffect{
				runtime: &transitionRuntimeFixture{
					events:   &events,
					probe:    test.probe,
					probeErr: test.probeErr,
				},
				transition: transition,
			}

			got, err := effect.Probe(context.Background())
			if !errors.Is(err, test.want) || got != emptyEffectPostcondition() {
				t.Fatalf("Probe(%s) = %#v, %v", test.name, got, err)
			}
		})
	}
}

func TestRunWorkloadTransitionRejectsInvalidInputsAndTransitions(t *testing.T) {
	t.Parallel()

	valid := testStopTransition()
	identifier := store.TransactionID{1}
	journal := imageEffectJournal(store.ActionStateIntent)
	runtime := transitionRuntimeAfter(&journal.events, valid)

	invalidCalls := []func() (EffectPostcondition, error){
		func() (EffectPostcondition, error) {
			return runWorkloadTransition(context.Background(), journal, identifier, 0, valid, runtime)
		},
		func() (EffectPostcondition, error) {
			return runWorkloadTransition(context.Background(), journal, identifier, 1, valid, nil)
		},
		func() (EffectPostcondition, error) {
			return runWorkloadTransition(context.Background(), journal, store.TransactionID{}, 1, valid, runtime)
		},
	}
	for _, call := range invalidCalls {
		got, err := call()
		if !errors.Is(err, ErrInvalidRequest) || got != emptyEffectPostcondition() {
			t.Fatalf("runWorkloadTransition(invalid) = %#v, %v", got, err)
		}
	}

	for index, transition := range invalidWorkloadTransitions() {
		if transition.Valid() {
			t.Fatalf("transition %d is valid: %#v", index, transition)
		}
	}

	unmanaged := valid
	unmanaged.Before.Ownership = domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged}
	unmanaged.After.Ownership = unmanaged.Before.Ownership
	if !unmanaged.Valid() {
		t.Fatal("valid unmanaged transition was rejected")
	}

	if workloadTransitionActionKind(WorkloadTransitionUnknown) != "" {
		t.Fatal("unknown workload transition has an action kind")
	}
}

func TestWorkloadTransitionApplyWrapsRuntimeFailure(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 1)
	effect := workloadTransitionEffect{
		runtime:    &transitionRuntimeFixture{events: &events, applyErr: errTestBoundary},
		transition: testStopTransition(),
	}

	if err := effect.Apply(context.Background()); !errors.Is(err, errTestBoundary) {
		t.Fatalf("Apply() = %v", err)
	}
}

func testWorkloadTransitions() []WorkloadTransition {
	stop := testStopTransition()
	rename := WorkloadTransition{
		Kind:   WorkloadTransitionRename,
		Before: stop.After,
		After:  stop.After,
	}
	rename.After.Name = testRetainedWorkloadName
	remove := WorkloadTransition{Kind: WorkloadTransitionRemove, Before: rename.After}
	restore := WorkloadTransition{
		Kind:   WorkloadTransitionRestoreStart,
		Before: stop.After,
		After:  stop.Before,
	}

	return []WorkloadTransition{stop, rename, remove, restore}
}

func testStopTransition() WorkloadTransition {
	before := ExistingWorkload{
		ID:                  testWorkloadEffectID,
		Name:                testReplacementWorkload,
		ConfigurationDigest: domain.Hash([]byte("runtime configuration")),
		Lifecycle:           WorkloadLifecycleRunning,
		Ownership: domain.WorkloadOwnership{
			Status:           domain.OwnershipManaged,
			Service:          testServiceName,
			Transaction:      store.TransactionID{1}.String(),
			DesiredState:     domain.Hash([]byte("desired")),
			Reference:        domain.Hash([]byte("reference")),
			ImageConfig:      domain.Hash([]byte("image config")),
			PlatformManifest: domain.Hash([]byte("platform manifest")),
		},
	}
	after := before
	after.Lifecycle = WorkloadLifecycleExited

	return WorkloadTransition{Kind: WorkloadTransitionStop, Before: before, After: after}
}

func transitionRuntimeAfter(
	events *[]string,
	transition WorkloadTransition,
) *transitionRuntimeFixture {
	probe := WorkloadTransitionProbe{
		State:    WorkloadEffectProbeObserved,
		Workload: transition.After,
	}
	if transition.Kind == WorkloadTransitionRemove {
		probe = WorkloadTransitionProbe{State: WorkloadEffectProbeMissing}
	}

	return &transitionRuntimeFixture{events: events, probe: probe}
}

func invalidWorkloadTransitions() []WorkloadTransition {
	valid := testStopTransition()
	unknownKind := valid
	unknownKind.Kind = WorkloadTransitionUnknown
	invalidIdentity := valid
	invalidIdentity.Before.ID = ""
	invalidAfter := valid
	invalidAfter.After.ID = testDifferentWorkload
	invalidLifecycle := valid
	invalidLifecycle.Before.Lifecycle = WorkloadLifecycleCreated
	invalidRename := testWorkloadTransitions()[1]
	invalidRename.After.Name = invalidRename.Before.Name
	invalidRemove := testWorkloadTransitions()[2]
	invalidRemove.After = invalidRemove.Before
	invalidOwnership := valid
	invalidOwnership.Before.Ownership.Service = ""

	return []WorkloadTransition{
		{}, unknownKind, invalidIdentity, invalidAfter, invalidLifecycle,
		invalidRename, invalidRemove, invalidOwnership,
	}
}
