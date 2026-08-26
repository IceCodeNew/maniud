package application

import (
	"bytes"
	"context"
	"fmt"
	"slices"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func (execution *upgradeExecution) inventory(ctx context.Context) error {
	if len(execution.sources) == 0 {
		return nil
	}

	action := execution.nextAction()
	postcondition, err := settleStorageInventory(
		ctx,
		execution.mutation,
		execution.runtime,
		action,
		execution.sequence,
		execution.sources,
		&execution.inventories,
		&execution.archives,
	)
	if err != nil {
		return resolveEffectFailure(ctx, execution.mutation, postcondition, err)
	}

	execution.sequence++

	return nil
}

func settleStorageInventory(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadArchiveRuntime,
	action store.Action,
	sequence int64,
	sources []backedStorageSource,
	inventories *[]backup.Inventory,
	archives *[][]byte,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	if mutation == nil || !validStorageInventoryRequest(runtime, inventories, archives, sources) {
		return empty, ErrInvalidRequest
	}

	selector := mutation.preparation.Applied.TransactionID.String()
	intent := storageInventoryIntent(sequence, selector, sources)
	if action != (store.Action{}) && !actionMatchesExpected(action, mutation.preparation.Transaction.ID, intent) {
		return empty, ErrConflictingState
	}

	effect := newStorageInventoryEffect(runtime, mutation.preparation.Workload, selector, sources)
	if action.State == store.ActionStateCompleted {
		return completedStorageInventory(ctx, action, effect, inventories, archives)
	}

	postcondition, err := runRuntimeEffect(
		ctx, mutation.effectJournal(), mutation.preparation.Transaction.ID, intent, effect,
	)
	if err != nil || !postcondition.Satisfied {
		return postcondition, err
	}

	*inventories = slices.Clone(effect.inventories)
	*archives = cloneArchives(effect.archives)

	return postcondition, nil
}

func validStorageInventoryRequest(
	runtime WorkloadArchiveRuntime,
	inventories *[]backup.Inventory,
	archives *[][]byte,
	sources []backedStorageSource,
) bool {
	return runtime != nil && inventories != nil && archives != nil && len(sources) > 0
}

func newStorageInventoryEffect(
	runtime WorkloadArchiveRuntime,
	workload domain.DesiredWorkload,
	selector string,
	sources []backedStorageSource,
) *storageInventoryEffect {
	return &storageInventoryEffect{
		runtime:  runtime,
		workload: workload,
		selector: selector,
		sources:  slices.Clone(sources),
	}
}

func completedStorageInventory(
	ctx context.Context,
	action store.Action,
	effect *storageInventoryEffect,
	inventories *[]backup.Inventory,
	archives *[][]byte,
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

	*inventories = slices.Clone(effect.inventories)
	*archives = cloneArchives(effect.archives)

	return postcondition, nil
}

func (effect *storageInventoryEffect) Apply(ctx context.Context) error {
	return effect.capture(ctx)
}

func (effect *storageInventoryEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition
	if err := effect.capture(ctx); err != nil {
		return empty, err
	}

	return storageInventoryPostcondition(effect.selector, effect.sources, effect.inventories, true), nil
}

func (effect *storageInventoryEffect) capture(ctx context.Context) error {
	inventories := make([]backup.Inventory, 0, len(effect.sources))
	archives := make([][]byte, 0, len(effect.sources))
	for _, source := range effect.sources {
		var archive bytes.Buffer
		_, err := effect.runtime.GetWorkloadArchive(
			ctx,
			effect.workload,
			effect.selector,
			source.Mount.Target,
			&archive,
			archiveTransferLimit,
		)
		if err != nil {
			return fmt.Errorf("inventory workload archive: %w", err)
		}

		raw := bytes.Clone(archive.Bytes())
		inventory, err := backup.Analyze(ctx, bytes.NewReader(raw), archiveTransferLimit)
		if err != nil {
			return fmt.Errorf("analyze workload archive: %w", err)
		}

		inventories = append(inventories, inventory)
		archives = append(archives, raw)
	}

	effect.inventories = inventories
	effect.archives = archives

	return nil
}

func storageInventoryIntent(
	sequence int64,
	selector string,
	sources []backedStorageSource,
) store.ActionIntent {
	return store.ActionIntent{
		Sequence:     sequence,
		Kind:         storageInventoryActionKind,
		IntentDigest: storageInventoryDigest(storageEffectIntent, selector, sources, nil),
	}
}

func storageInventoryPostcondition(
	selector string,
	sources []backedStorageSource,
	inventories []backup.Inventory,
	satisfied bool,
) EffectPostcondition {
	state := byte(storageEffectMissing)
	if satisfied {
		state = storageEffectObserved
	}

	return EffectPostcondition{
		Digest:    storageInventoryDigest(state, selector, sources, inventories),
		Satisfied: satisfied,
	}
}
