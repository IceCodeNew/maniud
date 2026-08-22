package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

// EffectJournal is the fenced durable boundary used by one runtime effect.
type EffectJournal interface {
	Fence(ctx context.Context) error
	RecordActionIntent(
		ctx context.Context,
		identifier store.TransactionID,
		intent store.ActionIntent,
	) (store.Action, error)
	MarkActionEffectOutcomeUnknown(
		ctx context.Context,
		identifier store.TransactionID,
		sequence int64,
	) (store.Action, error)
	CompleteAction(
		ctx context.Context,
		identifier store.TransactionID,
		sequence int64,
		postcondition domain.Digest,
	) (store.Action, error)
}

// RuntimeEffect performs one external operation and independently probes its
// typed postcondition. Apply returning nil is not postcondition evidence.
type RuntimeEffect interface {
	Apply(ctx context.Context) error
	Probe(ctx context.Context) (EffectPostcondition, error)
}

// EffectPostcondition is typed runtime evidence that resolves an uncertain
// effect outcome. Satisfied reports whether it proves the intended result.
type EffectPostcondition struct {
	Digest    domain.Digest
	Satisfied bool
}

func runRuntimeEffect(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	intent store.ActionIntent,
	effect RuntimeEffect,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	if journal == nil || effect == nil {
		return empty, ErrInvalidRequest
	}

	action, err := journal.RecordActionIntent(ctx, identifier, intent)
	if err != nil {
		return empty, fmt.Errorf("record runtime effect intent: %w", err)
	}

	switch action.State {
	case store.ActionStateIntent:
		return applyRuntimeEffect(ctx, journal, identifier, intent, effect, action)
	case store.ActionStateEffectOutcomeUnknown:
		if !actionMatchesIntent(action, identifier, intent) {
			return empty, ErrConflictingState
		}

		return recoverRuntimeEffect(ctx, journal, identifier, intent.Sequence, effect)
	case store.ActionStateCompleted:
		return empty, ErrConflictingState
	default:
		return empty, ErrConflictingState
	}
}

func applyRuntimeEffect(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	intent store.ActionIntent,
	effect RuntimeEffect,
	action store.Action,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	if !actionMatchesIntent(action, identifier, intent) {
		return empty, ErrConflictingState
	}

	err := journal.Fence(ctx)
	if err != nil {
		return empty, fmt.Errorf("fence runtime effect intent: %w", err)
	}

	marked, err := journal.MarkActionEffectOutcomeUnknown(ctx, identifier, intent.Sequence)
	if err != nil {
		return empty, fmt.Errorf("mark runtime effect outcome: %w", err)
	}

	if !actionMatchesIntent(marked, identifier, intent) ||
		marked.State != store.ActionStateEffectOutcomeUnknown || marked.PostconditionDigest != nil {
		return empty, ErrConflictingState
	}

	effectErr := effect.Apply(ctx)

	return probeRuntimeEffect(ctx, journal, identifier, intent.Sequence, effect, effectErr)
}

func recoverRuntimeEffect(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	effect RuntimeEffect,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	postcondition, err := effect.Probe(ctx)
	if err != nil {
		return empty, fmt.Errorf("probe recovered runtime effect: %w", err)
	}
	if postcondition.Digest == (domain.Digest{}) {
		return empty, ErrConflictingState
	}
	if postcondition.Satisfied {
		return completeRuntimeEffect(ctx, journal, identifier, sequence, postcondition, nil)
	}

	if err := journal.Fence(ctx); err != nil {
		return empty, fmt.Errorf("fence recovered runtime effect: %w", err)
	}
	effectErr := effect.Apply(ctx)

	return probeRuntimeEffect(ctx, journal, identifier, sequence, effect, effectErr)
}

func probeRuntimeEffect(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	effect RuntimeEffect,
	effectErr error,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	postcondition, probeErr := effect.Probe(ctx)
	if probeErr != nil {
		return empty, errors.Join(effectErr, probeErr)
	}

	if postcondition.Digest == (domain.Digest{}) {
		return empty, ErrConflictingState
	}

	return completeRuntimeEffect(ctx, journal, identifier, sequence, postcondition, effectErr)
}

func completeRuntimeEffect(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	postcondition EffectPostcondition,
	effectErr error,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	completed, err := journal.CompleteAction(ctx, identifier, sequence, postcondition.Digest)
	if err != nil {
		return empty, fmt.Errorf("complete runtime effect: %w", err)
	}

	if !completedActionMatches(completed, identifier, sequence, postcondition.Digest) {
		return empty, ErrConflictingState
	}

	if postcondition.Satisfied {
		return postcondition, nil
	}

	if effectErr != nil {
		return postcondition, fmt.Errorf("apply runtime effect: %w", effectErr)
	}

	return postcondition, ErrConflictingState
}

func actionMatchesIntent(
	action store.Action,
	identifier store.TransactionID,
	intent store.ActionIntent,
) bool {
	return action.TransactionID == identifier && action.Sequence == intent.Sequence &&
		action.Kind == intent.Kind && action.IntentDigest == intent.IntentDigest &&
		action.PostconditionDigest == nil
}

func completedActionMatches(
	action store.Action,
	identifier store.TransactionID,
	sequence int64,
	postcondition domain.Digest,
) bool {
	return action.TransactionID == identifier && action.Sequence == sequence &&
		action.State == store.ActionStateCompleted && action.PostconditionDigest != nil &&
		*action.PostconditionDigest == postcondition
}
