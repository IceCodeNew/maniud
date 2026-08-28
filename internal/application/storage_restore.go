package application

import (
	"bytes"
	"context"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/store"
)

func (execution *upgradeExecution) restore(ctx context.Context) error {
	if len(execution.sources) == 0 {
		return nil
	}

	publication, err := execution.publishedBackup(ctx)
	if err != nil {
		return err
	}

	action := execution.nextAction()
	postcondition, err := settleStorageRestore(
		ctx,
		execution.mutation,
		execution.runtime,
		action,
		execution.sequence,
		publication,
		execution.archives,
	)
	if err != nil {
		return resolveEffectFailure(ctx, execution.mutation, postcondition, err)
	}
	if !postcondition.Satisfied {
		return resolveEffectFailure(ctx, execution.mutation, postcondition, ErrConflictingState)
	}

	execution.sequence++

	return nil
}

func settleStorageRestore(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadArchiveRuntime,
	action store.Action,
	sequence int64,
	publication backup.Publication,
	archives [][]byte,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	if mutation == nil || runtime == nil || len(publication.Manifest.Artifacts) == 0 {
		return empty, ErrInvalidRequest
	}

	selector := mutation.preparation.Transaction.ID.String()
	intent := storageRestoreIntent(sequence, selector, publication.Manifest)
	if action != (store.Action{}) && !actionMatchesExpected(action, mutation.preparation.Transaction.ID, intent) {
		return empty, ErrConflictingState
	}

	effect := &storageRestoreEffect{
		runtime:     runtime,
		workload:    mutation.preparation.Workload,
		selector:    selector,
		publication: publication,
		archives:    cloneArchives(archives),
	}
	if action.State == store.ActionStateCompleted {
		return completedStorageRestore(ctx, action, effect)
	}

	return runRuntimeEffect(ctx, mutation.effectJournal(), mutation.preparation.Transaction.ID, intent, effect)
}

func completedStorageRestore(
	ctx context.Context,
	action store.Action,
	effect *storageRestoreEffect,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	if action.PostconditionDigest == nil {
		return empty, ErrConflictingState
	}

	postcondition, err := effect.Probe(ctx)
	if err != nil {
		return empty, err
	}
	if postcondition.Digest != *action.PostconditionDigest {
		return empty, ErrConflictingState
	}

	return postcondition, nil
}

func (effect *storageRestoreEffect) Apply(ctx context.Context) error {
	if len(effect.archives) != len(effect.publication.Manifest.Artifacts) {
		return ErrConflictingState
	}

	for index, artifact := range effect.publication.Manifest.Artifacts {
		err := effect.runtime.PutWorkloadArchive(
			ctx,
			effect.workload,
			effect.selector,
			artifact.Mount.Target,
			bytes.NewReader(effect.archives[index]),
		)
		if err != nil {
			return fmt.Errorf("restore workload archive: %w", err)
		}
	}

	return nil
}

func (effect *storageRestoreEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition
	for _, artifact := range effect.publication.Manifest.Artifacts {
		var restored bytes.Buffer
		_, err := effect.runtime.GetWorkloadArchive(
			ctx,
			effect.workload,
			effect.selector,
			artifact.Mount.Target,
			&restored,
			archiveTransferLimit,
		)
		if err != nil {
			return empty, fmt.Errorf("verify restored workload archive: %w", err)
		}

		inventory, err := backup.Analyze(ctx, bytes.NewReader(restored.Bytes()), archiveTransferLimit)
		if err != nil {
			return empty, fmt.Errorf("analyze restored workload archive: %w", err)
		}
		if !backup.SameContent(inventory, artifact.Inventory) {
			return storageRestorePostcondition(effect.selector, effect.publication.Manifest, false), nil
		}
	}

	return storageRestorePostcondition(effect.selector, effect.publication.Manifest, true), nil
}

func storageRestoreIntent(sequence int64, selector string, manifest backup.Manifest) store.ActionIntent {
	return store.ActionIntent{
		Sequence:     sequence,
		Kind:         storageRestoreActionKind,
		IntentDigest: storageRestoreDigest(storageEffectIntent, selector, manifest),
	}
}

func storageRestorePostcondition(selector string, manifest backup.Manifest, satisfied bool) EffectPostcondition {
	state := byte(storageEffectMissing)
	if satisfied {
		state = storageEffectObserved
	}

	return EffectPostcondition{
		Digest:    storageRestoreDigest(state, selector, manifest),
		Satisfied: satisfied,
	}
}
