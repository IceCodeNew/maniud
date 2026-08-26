package application

import (
	"context"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

// EventKind identifies one transient application observation. Events describe
// successful application-owned seams and never replace durable journal state.
type EventKind string

const (
	// EventPlanPrepared reports a successful dry-run or mutation preparation.
	EventPlanPrepared EventKind = "plan_prepared"
	// EventActionIntentRecorded reports a durable action intent.
	EventActionIntentRecorded EventKind = "action_intent_recorded"
	// EventRuntimeEffectStarted reports that an action entered the
	// effect-outcome-unknown state before its external effect.
	EventRuntimeEffectStarted EventKind = "runtime_effect_started"
	// EventPostconditionObserved reports one typed runtime postcondition.
	EventPostconditionObserved EventKind = "postcondition_observed"
	// EventActionCompleted reports a durably completed action.
	EventActionCompleted EventKind = "action_completed"
	// EventTransactionSucceeded reports a committed applied-service generation.
	EventTransactionSucceeded EventKind = "transaction_succeeded"
	// EventTransactionDegraded reports a transaction that requires restoration.
	EventTransactionDegraded EventKind = "transaction_degraded"
	// EventTransactionRestored reports completed automatic restoration. The
	// failed transaction remains the durable record of the unsuccessful upgrade.
	EventTransactionRestored EventKind = "transaction_restored"
	// EventTransactionFailed reports a terminal unsuccessful transaction.
	EventTransactionFailed EventKind = "transaction_failed"
)

// Event is a bounded, value-free projection of one application seam. Digest
// strings are opaque evidence identities; consumers must not interpret them as
// effect content.
type Event struct {
	Kind        EventKind
	Plan        PlanKind
	Project     string
	Service     string
	Runtime     domain.RuntimeKind
	Transaction string
	Action      string
	Sequence    int64
	Evidence    string
	Satisfied   bool
}

// EventSink accepts one transient event without waiting for transport or
// durable work. Implementations must return immediately. False reports a drop.
type EventSink interface {
	TryPublish(event Event) bool
}

// EventDropCounter reports observations dropped by one process-local sink.
// Implementations must be safe for concurrent reads and return immediately.
type EventDropCounter interface {
	DroppedEvents() uint64
}

type observedEffectJournal struct {
	EffectJournal

	events    EventSink
	context   Event
	action    string
	satisfied bool
}

func (journal *observedEffectJournal) RecordActionIntent(
	ctx context.Context,
	identifier store.TransactionID,
	intent store.ActionIntent,
) (store.Action, error) {
	action, err := journal.EffectJournal.RecordActionIntent(ctx, identifier, intent)
	if err == nil {
		journal.action = action.Kind
		journal.publish(EventActionIntentRecorded, action, "", false)
	}

	return action, err //nolint:wrapcheck // The observer decorator preserves the journal error contract.
}

func (journal *observedEffectJournal) MarkActionEffectOutcomeUnknown(
	ctx context.Context,
	identifier store.TransactionID,
	sequence int64,
) (store.Action, error) {
	action, err := journal.EffectJournal.MarkActionEffectOutcomeUnknown(ctx, identifier, sequence)
	if err == nil {
		journal.action = action.Kind
		journal.publish(EventRuntimeEffectStarted, action, "", false)
	}

	return action, err //nolint:wrapcheck // The observer decorator preserves the journal error contract.
}

func (journal *observedEffectJournal) CompleteAction(
	ctx context.Context,
	identifier store.TransactionID,
	sequence int64,
	postcondition domain.Digest,
) (store.Action, error) {
	action, err := journal.EffectJournal.CompleteAction(ctx, identifier, sequence, postcondition)
	if err == nil {
		journal.action = action.Kind
		journal.publish(EventActionCompleted, action, postcondition.String(), journal.satisfied)
	}

	return action, err //nolint:wrapcheck // The observer decorator preserves the journal error contract.
}

func (journal *observedEffectJournal) publishPostcondition(
	identifier store.TransactionID,
	sequence int64,
	postcondition EffectPostcondition,
) {
	journal.satisfied = postcondition.Satisfied
	journal.publish(EventPostconditionObserved, store.Action{
		TransactionID: identifier,
		Sequence:      sequence,
		Kind:          journal.action,
	}, postcondition.Digest.String(), postcondition.Satisfied)
}

func (journal *observedEffectJournal) publish(
	kind EventKind,
	action store.Action,
	evidence string,
	satisfied bool,
) {
	event := journal.context
	event.Kind = kind
	event.Transaction = action.TransactionID.String()
	event.Action = action.Kind
	event.Sequence = action.Sequence
	event.Evidence = evidence
	event.Satisfied = satisfied
	tryPublish(journal.events, event)
}

func tryPublish(sink EventSink, event Event) (published bool) {
	if sink == nil {
		return false
	}

	defer func() {
		if recover() != nil {
			published = false
		}
	}()

	return sink.TryPublish(event)
}
