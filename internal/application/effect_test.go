package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	eventIntent   = "intent"
	eventUnknown  = "unknown"
	eventFence    = "fence"
	eventEffect   = "effect"
	eventProbe    = "probe"
	eventComplete = "complete"
)

type testEffectJournal struct {
	events         []string
	action         store.Action
	failures       map[string]error
	mutateRecord   func(*store.Action)
	mutateMark     func(*store.Action)
	mutateComplete func(*store.Action)
}

func (journal *testEffectJournal) Fence(context.Context) error {
	journal.events = append(journal.events, eventFence)

	return journal.failures[eventFence]
}

func (journal *testEffectJournal) RecordActionIntent(
	_ context.Context,
	identifier store.TransactionID,
	intent store.ActionIntent,
) (store.Action, error) {
	journal.events = append(journal.events, eventIntent)

	err := journal.failures[eventIntent]
	if err != nil {
		return store.Action{}, err
	}

	journal.action.TransactionID = identifier
	journal.action.Sequence = intent.Sequence
	journal.action.Kind = intent.Kind
	journal.action.IntentDigest = intent.IntentDigest

	if journal.mutateRecord != nil {
		journal.mutateRecord(&journal.action)
	}

	return journal.action, nil
}

func (journal *testEffectJournal) MarkActionEffectOutcomeUnknown(
	_ context.Context,
	_ store.TransactionID,
	_ int64,
) (store.Action, error) {
	journal.events = append(journal.events, eventUnknown)

	err := journal.failures[eventUnknown]
	if err != nil {
		return store.Action{}, err
	}

	journal.action.State = store.ActionStateEffectOutcomeUnknown
	if journal.mutateMark != nil {
		journal.mutateMark(&journal.action)
	}

	return journal.action, nil
}

func (journal *testEffectJournal) CompleteAction(
	_ context.Context,
	_ store.TransactionID,
	_ int64,
	postcondition domain.Digest,
) (store.Action, error) {
	journal.events = append(journal.events, eventComplete)

	err := journal.failures[eventComplete]
	if err != nil {
		return store.Action{}, err
	}

	journal.action.State = store.ActionStateCompleted
	journal.action.PostconditionDigest = &postcondition

	if journal.mutateComplete != nil {
		journal.mutateComplete(&journal.action)
	}

	return journal.action, nil
}

type testRuntimeEffect struct {
	events        *[]string
	postcondition EffectPostcondition
	applyErr      error
	probeErr      error
}

func (effect testRuntimeEffect) Apply(context.Context) error {
	*effect.events = append(*effect.events, eventEffect)

	return effect.applyErr
}

func (effect testRuntimeEffect) Probe(context.Context) (EffectPostcondition, error) {
	*effect.events = append(*effect.events, eventProbe)

	return effect.postcondition, effect.probeErr
}

func TestRunRuntimeEffectPersistsBoundariesAndTrustsTypedProbe(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		state      store.ActionState
		applyErr   error
		wantEvents []string
	}{
		{
			name:       "new effect",
			state:      store.ActionStateIntent,
			applyErr:   nil,
			wantEvents: newEffectEvents(),
		},
		{
			name:       "successful postcondition supersedes response error",
			state:      store.ActionStateIntent,
			applyErr:   errTestBoundary,
			wantEvents: newEffectEvents(),
		},
		{
			name:       "unknown recovery probes without replay",
			state:      store.ActionStateEffectOutcomeUnknown,
			applyErr:   nil,
			wantEvents: []string{eventIntent, eventProbe, eventComplete},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			journal, effect, identifier, intent := newRuntimeEffectTest(test.state)
			effect.applyErr = test.applyErr
			postcondition := effect.postcondition

			got, err := runRuntimeEffect(context.Background(), journal, identifier, intent, effect)
			if err != nil || got != postcondition {
				t.Fatalf("runRuntimeEffect() = %#v, %v", got, err)
			}

			if !equalEvents(journal.events, test.wantEvents) {
				t.Fatalf("events = %q, want %q", journal.events, test.wantEvents)
			}
		})
	}
}

func TestRunRuntimeEffectResolvesNegativePostconditions(t *testing.T) {
	t.Parallel()

	for _, effectErr := range []error{nil, errTestBoundary} {
		journal, effect, identifier, intent := newRuntimeEffectTest(store.ActionStateIntent)
		effect.applyErr = effectErr
		effect.postcondition.Satisfied = false

		got, err := runRuntimeEffect(context.Background(), journal, identifier, intent, effect)
		wantErr := ErrConflictingState

		if effectErr != nil {
			wantErr = effectErr
		}

		if !errors.Is(err, wantErr) || got != effect.postcondition ||
			journal.action.State != store.ActionStateCompleted {
			t.Fatalf("runRuntimeEffect(negative, %v) = %#v, %v", effectErr, got, err)
		}
	}
}

func TestRunRuntimeEffectContainsBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		state     store.ActionState
		failureAt string
		mutate    func(*testEffectJournal, *testRuntimeEffect)
		want      error
	}{
		{name: "intent write", state: store.ActionStateIntent, failureAt: eventIntent, mutate: nil, want: errTestBoundary},
		{name: "unknown write", state: store.ActionStateIntent, failureAt: eventUnknown, mutate: nil, want: errTestBoundary},
		{name: "fence", state: store.ActionStateIntent, failureAt: eventFence, mutate: nil, want: errTestBoundary},
		{
			name: "probe", state: store.ActionStateIntent, failureAt: "",
			mutate: func(_ *testEffectJournal, effect *testRuntimeEffect) {
				effect.applyErr = errTestBoundary
				effect.probeErr = context.Canceled
			},
			want: context.Canceled,
		},
		{name: "completion", state: store.ActionStateIntent, failureAt: eventComplete, mutate: nil, want: errTestBoundary},
		{
			name: "empty postcondition", state: store.ActionStateIntent, failureAt: "",
			mutate: func(_ *testEffectJournal, effect *testRuntimeEffect) {
				effect.postcondition.Digest = domain.Digest{}
			},
			want: ErrConflictingState,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			journal, effect, identifier, intent := newRuntimeEffectTest(test.state)

			journal.failures[test.failureAt] = errTestBoundary
			if test.mutate != nil {
				test.mutate(journal, &effect)
			}

			got, err := runRuntimeEffect(context.Background(), journal, identifier, intent, effect)
			if !errors.Is(err, test.want) || got != emptyEffectPostcondition() {
				t.Fatalf("runRuntimeEffect() = %#v, %v, want %v", got, err, test.want)
			}
		})
	}
}

func TestRunRuntimeEffectRejectsInvalidDependenciesAndJournalEvidence(t *testing.T) {
	t.Parallel()

	journal, effect, identifier, intent := newRuntimeEffectTest(store.ActionStateIntent)

	invalidCalls := []func() (EffectPostcondition, error){
		func() (EffectPostcondition, error) {
			return runRuntimeEffect(context.Background(), nil, identifier, intent, effect)
		},
		func() (EffectPostcondition, error) {
			return runRuntimeEffect(context.Background(), journal, identifier, intent, nil)
		},
	}
	for _, call := range invalidCalls {
		got, err := call()
		if !errors.Is(err, ErrInvalidRequest) || got != emptyEffectPostcondition() {
			t.Fatalf("runRuntimeEffect(invalid) = %#v, %v", got, err)
		}
	}

	tests := []struct {
		name   string
		state  store.ActionState
		mutate func(*testEffectJournal)
	}{
		{name: "completed", state: store.ActionStateCompleted, mutate: func(*testEffectJournal) {}},
		{name: testInvalidStateName, state: "invalid", mutate: func(*testEffectJournal) {}},
		{name: "intent identity", state: store.ActionStateIntent, mutate: func(journal *testEffectJournal) {
			journal.mutateRecord = func(action *store.Action) { action.Kind = "workload.create" }
		}},
		{name: "unknown identity", state: store.ActionStateEffectOutcomeUnknown, mutate: func(journal *testEffectJournal) {
			journal.mutateRecord = func(action *store.Action) { action.Sequence++ }
		}},
		{name: "marked evidence", state: store.ActionStateIntent, mutate: func(journal *testEffectJournal) {
			journal.mutateMark = func(action *store.Action) {
				value := domain.Hash([]byte("unexpected"))
				action.PostconditionDigest = &value
			}
		}},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			currentJournal, currentEffect, currentIdentifier, currentIntent := newRuntimeEffectTest(test.state)
			test.mutate(currentJournal)

			got, err := runRuntimeEffect(
				context.Background(),
				currentJournal,
				currentIdentifier,
				currentIntent,
				currentEffect,
			)
			if !errors.Is(err, ErrConflictingState) || got != emptyEffectPostcondition() {
				t.Fatalf("runRuntimeEffect(%s) = %#v, %v", test.name, got, err)
			}
		})
	}
}

func TestRunRuntimeEffectRejectsMismatchedCompletion(t *testing.T) {
	t.Parallel()

	journal, effect, identifier, intent := newRuntimeEffectTest(store.ActionStateIntent)
	journal.mutateComplete = func(action *store.Action) { action.TransactionID = store.TransactionID{9} }

	got, err := probeRuntimeEffect(
		context.Background(),
		journal,
		identifier,
		intent.Sequence,
		effect,
		nil,
	)
	if !errors.Is(err, ErrConflictingState) || got != emptyEffectPostcondition() {
		t.Fatalf("probeRuntimeEffect(mismatched completion) = %#v, %v", got, err)
	}
}

func newRuntimeEffectTest(
	state store.ActionState,
) (*testEffectJournal, testRuntimeEffect, store.TransactionID, store.ActionIntent) {
	identifier := store.TransactionID{1}
	intent := store.ActionIntent{
		Sequence:     1,
		Kind:         "image.pull",
		IntentDigest: domain.Hash([]byte("pull intent")),
	}
	journal := &testEffectJournal{
		events: nil,
		action: store.Action{
			TransactionID:       store.TransactionID{},
			Sequence:            0,
			Kind:                "",
			State:               state,
			IntentDigest:        domain.Digest{},
			PostconditionDigest: nil,
		},
		failures:       make(map[string]error),
		mutateRecord:   nil,
		mutateMark:     nil,
		mutateComplete: nil,
	}
	effect := testRuntimeEffect{
		events: &journal.events,
		postcondition: EffectPostcondition{
			Digest:    domain.Hash([]byte("postcondition")),
			Satisfied: true,
		},
		applyErr: nil,
		probeErr: nil,
	}

	return journal, effect, identifier, intent
}

func equalEvents(got, want []string) bool {
	if len(got) != len(want) {
		return false
	}

	for index := range got {
		if got[index] != want[index] {
			return false
		}
	}

	return true
}

func newEffectEvents() []string {
	return []string{eventIntent, eventUnknown, eventFence, eventEffect, eventProbe, eventComplete}
}

func emptyEffectPostcondition() EffectPostcondition {
	return EffectPostcondition{Digest: domain.Digest{}, Satisfied: false}
}
