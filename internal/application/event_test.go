package application

import (
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type eventSinkFunc func(Event) bool

func (publish eventSinkFunc) TryPublish(event Event) bool {
	return publish(event)
}

func TestObservedServicePublishesPreparedPlan(t *testing.T) {
	t.Parallel()

	operation := newTestOperation(t)
	var events []Event
	operation.service = NewObservedService(
		operation.service.images,
		operation.runtime,
		operation.transactions,
		eventSinkFunc(func(event Event) bool {
			events = append(events, event)

			return true
		}),
	)

	plan, err := operation.service.DryRun(t.Context(), operation.request)
	if err != nil {
		t.Fatalf("DryRun() error = %v", err)
	}
	if len(events) != 1 || events[0] != (Event{
		Kind:    EventPlanPrepared,
		Plan:    plan.Kind,
		Project: plan.Project,
		Service: plan.Service,
		Runtime: plan.Runtime,
	}) {
		t.Fatalf("events = %#v", events)
	}
}

//nolint:cyclop // The assertions verify every field shared by the ordered event projection.
func TestObservedEffectJournalPublishesSuccessfulDurableSeams(t *testing.T) {
	t.Parallel()

	identifier := store.TransactionID{1}
	intent := store.ActionIntent{
		Sequence:     1,
		Kind:         testOtherValue,
		IntentDigest: domain.Hash([]byte("intent")),
	}
	postcondition := EffectPostcondition{
		Digest:    domain.Hash([]byte("postcondition")),
		Satisfied: true,
	}
	journalEvents := make([]string, 0, 6)
	journal := &testEffectJournal{
		events:   journalEvents,
		action:   store.Action{State: store.ActionStateIntent},
		failures: map[string]error{},
	}
	var events []Event
	observed := &observedEffectJournal{
		EffectJournal: journal,
		events: eventSinkFunc(func(event Event) bool {
			events = append(events, event)

			return true
		}),
		context: Event{
			Project: testProjectName,
			Service: testServiceName,
			Runtime: domain.RuntimeDocker,
		},
	}
	effect := testRuntimeEffect{events: &journal.events, postcondition: postcondition}

	got, err := runRuntimeEffect(t.Context(), observed, identifier, intent, effect)
	if err != nil || got != postcondition {
		t.Fatalf("runRuntimeEffect() = %#v, %v", got, err)
	}
	wantKinds := []EventKind{
		EventActionIntentRecorded,
		EventRuntimeEffectStarted,
		EventPostconditionObserved,
		EventActionCompleted,
	}
	if len(events) != len(wantKinds) {
		t.Fatalf("events = %#v", events)
	}
	for index, event := range events {
		if event.Kind != wantKinds[index] || event.Project != testProjectName ||
			event.Service != testServiceName || event.Runtime != domain.RuntimeDocker ||
			event.Transaction != identifier.String() || event.Action != intent.Kind ||
			event.Sequence != intent.Sequence {
			t.Fatalf("events[%d] = %#v", index, event)
		}
	}
	if events[2].Evidence != postcondition.Digest.String() || !events[2].Satisfied ||
		events[3].Evidence != postcondition.Digest.String() || !events[3].Satisfied {
		t.Fatalf("postcondition events = %#v", events[2:])
	}
}

func TestEventPublicationCannotChangeApplicationResult(t *testing.T) {
	t.Parallel()

	event := Event{Kind: EventPlanPrepared}
	if tryPublish(nil, event) {
		t.Fatal("nil sink published event")
	}
	if tryPublish(eventSinkFunc(func(Event) bool { return false }), event) {
		t.Fatal("dropping sink published event")
	}
	if tryPublish(eventSinkFunc(func(Event) bool { panic("observer failure") }), event) {
		t.Fatal("panicking sink published event")
	}
	if !tryPublish(eventSinkFunc(func(Event) bool { return true }), event) {
		t.Fatal("accepting sink dropped event")
	}

	journalEvents := make([]string, 0, 6)
	journal := &observedEffectJournal{
		EffectJournal: &testEffectJournal{
			events:   journalEvents,
			action:   store.Action{State: store.ActionStateIntent},
			failures: map[string]error{},
		},
		events: eventSinkFunc(func(Event) bool { panic("observer failure") }),
	}
	postcondition := EffectPostcondition{
		Digest:    domain.Hash([]byte("postcondition")),
		Satisfied: true,
	}
	effect := testRuntimeEffect{events: &journalEvents, postcondition: postcondition}
	got, err := runRuntimeEffect(t.Context(), journal, store.TransactionID{1}, store.ActionIntent{
		Sequence: 1, Kind: testOtherValue, IntentDigest: domain.Hash([]byte("intent")),
	}, effect)
	if err != nil || got != postcondition {
		t.Fatalf("runRuntimeEffect(panicking sink) = %#v, %v", got, err)
	}
}

func TestBoundMutationEventHelpersContainInvalidReceivers(t *testing.T) {
	t.Parallel()

	var missing *boundMutation
	if missing.effectJournal() != nil {
		t.Fatal("nil mutation returned an effect journal")
	}
	missing.publishTransaction(EventTransactionFailed)

	mutation := &boundMutation{}
	if mutation.effectJournal() != nil {
		t.Fatal("lockless mutation returned an effect journal")
	}

	var got Event
	mutation.preparation = Preparation{
		Plan: Plan{
			Kind:    PlanRestore,
			Project: testProjectName,
			Service: testServiceName,
			Runtime: domain.RuntimePodman,
		},
		Transaction: store.Transaction{ID: store.TransactionID{1}},
	}
	mutation.events = eventSinkFunc(func(event Event) bool {
		got = event

		return true
	})
	mutation.publishTransaction(EventTransactionFailed)
	if got != (Event{
		Kind:        EventTransactionFailed,
		Plan:        PlanRestore,
		Project:     testProjectName,
		Service:     testServiceName,
		Runtime:     domain.RuntimePodman,
		Transaction: store.TransactionID{1}.String(),
	}) {
		t.Fatalf("transaction event = %#v", got)
	}
}

func TestObservedEffectJournalDoesNotPublishFailedDurableSeams(t *testing.T) {
	t.Parallel()

	for _, seam := range []string{eventIntent, eventUnknown, eventComplete} {
		t.Run(seam, func(t *testing.T) {
			t.Parallel()

			published := false
			journal := &observedEffectJournal{
				EffectJournal: &testEffectJournal{
					failures: map[string]error{seam: errTestBoundary},
				},
				events: eventSinkFunc(func(Event) bool {
					published = true

					return true
				}),
			}
			identifier := store.TransactionID{1}
			switch seam {
			case eventIntent:
				_, _ = journal.RecordActionIntent(t.Context(), identifier, store.ActionIntent{})
			case eventUnknown:
				_, _ = journal.MarkActionEffectOutcomeUnknown(t.Context(), identifier, 1)
			case eventComplete:
				_, _ = journal.CompleteAction(t.Context(), identifier, 1, domain.Hash([]byte("proof")))
			}
			if published {
				t.Fatal("failed durable seam published an event")
			}
		})
	}
}
