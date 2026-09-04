package application

import (
	"context"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type restoreRuntime interface {
	WorkloadDiscardRuntime
	WorkloadTransitionRuntime
}

type restoreJourney struct {
	coreActions    int
	discard        bool
	reverseRename  bool
	rename         WorkloadTransition
	restoreStart   WorkloadTransition
	nextSequence   int64
	recoveryCursor int
	retryCursor    int
	retrySequence  int64
}

type restoreExecution struct {
	mutation *boundMutation
	runtime  restoreRuntime
	actions  []store.Action
	journey  restoreJourney
	cursor   int
	sequence int64
}

type restoreCoreEvidence struct {
	discard       bool
	reverseRename bool
}

const (
	upgradeCoreActionCapacity = 9
	restoreActionCapacity     = 3
)

func runRestore(ctx context.Context, mutation *boundMutation, runtime restoreRuntime) error {
	if mutation == nil || runtime == nil {
		return ErrInvalidRequest
	}

	journey, err := prepareRestoreJourney(ctx, mutation)
	if err != nil {
		return err
	}

	execution := restoreExecution{
		mutation: mutation,
		runtime:  runtime,
		actions:  mutation.preparation.Actions,
		journey:  journey,
		cursor:   journey.recoveryCursor,
		sequence: journey.nextSequence,
	}
	if journey.retrySequence > 0 {
		execution.journey.discard = false
		execution.journey.reverseRename = false
		execution.cursor = journey.retryCursor
		execution.sequence = journey.retrySequence
	}
	if err = execution.discard(ctx); err != nil {
		return err
	}
	if err = execution.rename(ctx); err != nil {
		return err
	}
	if err = execution.start(ctx); err != nil {
		return err
	}

	return completeRestore(ctx, mutation, runtime, journey.restoreStart)
}

func (execution *restoreExecution) discard(ctx context.Context) error {
	if !execution.journey.discard {
		return nil
	}

	err := settleWorkloadDiscard(
		ctx,
		execution.mutation,
		execution.runtime,
		execution.nextAction(),
		execution.sequence,
	)
	if err != nil {
		return err
	}

	execution.sequence++

	return nil
}

func (execution *restoreExecution) rename(ctx context.Context) error {
	if !execution.journey.reverseRename {
		return nil
	}

	satisfied, _, err := settleUpgradeTransition(
		ctx,
		execution.mutation,
		execution.runtime,
		execution.nextAction(),
		execution.sequence,
		execution.journey.rename,
	)
	if err != nil {
		return err
	}

	if !satisfied {
		return ErrConflictingState
	}

	execution.sequence++

	return nil
}

func (execution *restoreExecution) start(ctx context.Context) error {
	satisfied, _, err := settleUpgradeTransition(
		ctx,
		execution.mutation,
		execution.runtime,
		execution.nextAction(),
		execution.sequence,
		execution.journey.restoreStart,
	)
	if err != nil {
		return err
	}

	if !satisfied {
		return ErrConflictingState
	}

	return nil
}

func (execution *restoreExecution) nextAction() store.Action {
	action, cursor := nextUpgradeAction(execution.actions, execution.cursor)
	execution.cursor = cursor

	return action
}

func settleWorkloadDiscard(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadDiscardRuntime,
	action store.Action,
	sequence int64,
) error {
	preparation := mutation.preparation
	transaction := preparation.Transaction.ID.String()
	intent := workloadDiscardIntent(sequence, preparation.Workload, transaction)

	if action != (store.Action{}) && !actionMatchesExpected(action, preparation.Transaction.ID, intent) {
		return ErrConflictingState
	}

	if action.State == store.ActionStateCompleted {
		expected := workloadEffectDigest(
			workloadEffectMissing,
			workloadDiscardActionKind,
			preparation.Workload,
			transaction,
			"",
		)
		if action.PostconditionDigest == nil || *action.PostconditionDigest != expected {
			return ErrConflictingState
		}

		return nil
	}

	postcondition, err := runWorkloadDiscard(
		ctx,
		mutation.effectJournal(),
		preparation.Transaction.ID,
		sequence,
		preparation.Workload,
		runtime,
	)

	return requireSatisfiedEffect(postcondition, err)
}

func prepareRestoreJourney(ctx context.Context, mutation *boundMutation) (restoreJourney, error) {
	var empty restoreJourney

	if mutation == nil || !validRestoreMutation(mutation) {
		return empty, ErrInvalidRequest
	}

	preparation := mutation.preparation
	upgrade := newUpgradeJourney(preparation)
	publication, err := loadRestorePublication(ctx, mutation)
	if err != nil {
		return empty, err
	}

	intents := upgradeCoreIntents(preparation, upgrade, publication)
	coreActions := restoreCoreActionCount(preparation.Actions, intents)
	if coreActions == 0 {
		return empty, ErrConflictingState
	}

	err = validateCompletedRestoreCore(preparation, intents, coreActions)
	if err != nil {
		return empty, err
	}

	evidence, err := inspectRestoreCore(preparation, upgrade, coreActions, len(intents))
	if err != nil {
		return empty, err
	}

	recoveryCursor, err := completedHealthStopPrefix(preparation, coreActions)
	if err != nil {
		return empty, err
	}

	journey := newRestoreJourney(upgrade, recoveryCursor, evidence.discard, evidence.reverseRename)
	journey, valid := validatedRestoreSuffix(preparation, journey)
	if !valid {
		return empty, ErrConflictingState
	}

	return journey, nil
}

func completedHealthStopPrefix(preparation Preparation, coreActions int) (int, error) {
	if coreActions >= len(preparation.Actions) ||
		preparation.Actions[coreActions].Kind != workloadHealthStopActionKind {
		return coreActions, nil
	}

	action := preparation.Actions[coreActions]
	intent := workloadHealthStopIntent(
		int64(coreActions+1),
		preparation.Workload,
		preparation.Transaction.ID.String(),
	)
	if action.State != store.ActionStateCompleted || action.PostconditionDigest == nil ||
		!actionMatchesExpected(action, preparation.Transaction.ID, intent) {
		return 0, ErrConflictingState
	}

	return coreActions + 1, nil
}

func validateCompletedRestoreCore(
	preparation Preparation,
	intents []store.ActionIntent,
	coreActions int,
) error {
	for index := range coreActions {
		action := preparation.Actions[index]
		if action.State != store.ActionStateCompleted ||
			!actionMatchesExpected(action, preparation.Transaction.ID, intents[index]) {
			return ErrConflictingState
		}
	}

	return nil
}

func inspectRestoreCore(
	preparation Preparation,
	upgrade upgradeJourney,
	coreActions int,
	coreIntents int,
) (restoreCoreEvidence, error) {
	var empty restoreCoreEvidence
	stopIndex := coreStopIndex(preparation.Actions)
	if stopIndex < 0 || coreActions <= stopIndex || preparation.Actions[stopIndex].Kind != workloadStopActionKind {
		return empty, ErrConflictingState
	}

	stopSatisfied, err := completedTransitionResult(preparation.Actions[stopIndex], upgrade.stop)
	if err != nil || !stopSatisfied {
		return empty, ErrConflictingState
	}

	renameSatisfied, err := restoreRenameSatisfied(
		preparation.Actions,
		upgrade.rename,
		coreActions,
		coreActionIndex(preparation.Actions[:coreActions], workloadRenameActionKind),
	)
	if err != nil {
		return empty, err
	}

	discard := coreContainsAction(preparation.Actions[:coreActions], workloadCreateActionKind)
	if discard && !renameSatisfied {
		return empty, ErrConflictingState
	}

	err = validateRestoreRemove(preparation.Actions, upgrade.remove, coreActions, coreIntents-1)

	return restoreCoreEvidence{discard: discard, reverseRename: renameSatisfied}, err
}

func restoreRenameSatisfied(
	actions []store.Action,
	rename WorkloadTransition,
	coreActions int,
	renameIndex int,
) (bool, error) {
	if renameIndex < 0 || renameIndex >= len(actions) || coreActions <= renameIndex ||
		actions[renameIndex].Kind != workloadRenameActionKind {
		return false, nil
	}

	return completedTransitionResult(actions[renameIndex], rename)
}

func validateRestoreRemove(
	actions []store.Action,
	remove WorkloadTransition,
	coreActions int,
	removeIndex int,
) error {
	if coreActions <= removeIndex || actions[removeIndex].Kind != workloadRemoveActionKind {
		return nil
	}

	satisfied, err := completedTransitionResult(actions[removeIndex], remove)
	if err != nil || satisfied {
		return ErrConflictingState
	}

	return nil
}

func newRestoreJourney(
	upgrade upgradeJourney,
	coreActions int,
	discard bool,
	reverseRename bool,
) restoreJourney {
	stoppedOriginal := upgrade.stop.After
	stoppedRetained := upgrade.rename.After

	return restoreJourney{
		coreActions:   coreActions,
		discard:       discard,
		reverseRename: reverseRename,
		rename: WorkloadTransition{
			Kind:   WorkloadTransitionRename,
			Before: stoppedRetained,
			After:  stoppedOriginal,
		},
		restoreStart: WorkloadTransition{
			Kind:   WorkloadTransitionRestoreStart,
			Before: stoppedOriginal,
			After:  upgrade.stop.Before,
		},
		nextSequence:   int64(coreActions + 1),
		recoveryCursor: coreActions,
	}
}

func upgradeCoreIntents(
	preparation Preparation,
	journey upgradeJourney,
	publication backup.Publication,
) []store.ActionIntent {
	sequence := int64(1)
	intents := make([]store.ActionIntent, 0, upgradeCoreActionCapacity)
	withStorage := publication.ManifestDigest != (domain.Digest{}) ||
		coreContainsAction(preparation.Actions, storageInventoryActionKind) ||
		coreContainsAction(preparation.Actions, storageBackupActionKind) ||
		coreContainsAction(preparation.Actions, storageRestoreActionKind)
	if len(preparation.Actions) > 0 && preparation.Actions[0].Kind == imagePullActionKind {
		intents = append(intents, imagePullIntent(sequence, preparation.Workload.Image))
		sequence++
	}
	if withStorage {
		intents = append(intents, storageInventoryIntent(
			sequence,
			preparation.Applied.TransactionID.String(),
			backedStorageSources(preparation.Plan.Observation, preparation.Workload),
		))
		sequence++
	}

	intents = append(intents, workloadTransitionIntent(sequence, journey.stop))
	sequence++
	if withStorage {
		intents = append(intents, storageBackupIntent(sequence, restoreBackupManifest(preparation, publication)))
		sequence++
	}

	intents = append(intents, workloadTransitionIntent(sequence, journey.rename))
	sequence++
	intents = append(intents, workloadCreateIntent(
		sequence,
		preparation.Workload,
		preparation.Transaction.ID.String(),
	))
	sequence++
	if withStorage {
		intents = append(intents, storageRestoreIntent(
			sequence,
			preparation.Transaction.ID.String(),
			restoreBackupManifest(preparation, publication),
		))
		sequence++
	}

	intents = append(intents, workloadStartIntent(
		sequence,
		preparation.Workload,
		preparation.Transaction.ID.String(),
	))
	sequence++
	intents = append(intents, workloadTransitionIntent(sequence, journey.remove))

	return intents
}

func loadRestorePublication(ctx context.Context, mutation *boundMutation) (backup.Publication, error) {
	var empty backup.Publication
	if mutation == nil || mutation.backupRoot == "" ||
		!coreContainsAction(mutation.preparation.Actions, storageBackupActionKind) {
		return empty, nil
	}

	publication, found, err := backup.Open(
		ctx,
		mutation.backupRoot,
		backup.Identifier(mutation.preparation.Transaction.ID),
	)
	if err != nil {
		return empty, fmt.Errorf("open restore backup: %w", err)
	}
	if !found {
		return empty, ErrConflictingState
	}

	return publication, nil
}

func restoreBackupManifest(preparation Preparation, publication backup.Publication) backup.Manifest {
	if publication.ManifestDigest != (domain.Digest{}) {
		return publication.Manifest
	}

	sources := backedStorageSources(preparation.Plan.Observation, preparation.Workload)
	artifacts := make([]backup.Artifact, len(sources))
	for index, source := range sources {
		artifacts[index] = backup.Artifact{
			Mount:    source.Mount,
			FileName: backupArtifactName(index),
		}
		if source.Mount.Kind == domain.MountBind {
			artifacts[index].ProvenanceDigest = preparation.Applied.SourceDigest
		}
	}

	return backup.Manifest{
		TransactionID:     backup.Identifier(preparation.Transaction.ID),
		BaseTransactionID: backup.Identifier(preparation.Applied.TransactionID),
		Artifacts:         artifacts,
	}
}

func coreActionIndex(actions []store.Action, kind string) int {
	for index, action := range actions {
		if action.Kind == kind {
			return index
		}
	}

	return -1
}

func restoreCoreActionCount(actions []store.Action, intents []store.ActionIntent) int {
	limit := min(len(actions), len(intents))
	for index, action := range actions[:limit] {
		if action.Kind != intents[index].Kind {
			return index
		}
	}

	return limit
}

func coreStopIndex(actions []store.Action) int {
	return coreActionIndex(actions, workloadStopActionKind)
}

func coreContainsAction(actions []store.Action, kind string) bool {
	for _, action := range actions {
		if action.Kind == kind {
			return true
		}
	}

	return false
}

func validatedRestoreSuffix(preparation Preparation, journey restoreJourney) (restoreJourney, bool) {
	expected := make([]store.ActionIntent, 0, restoreActionCapacity)
	sequence := journey.nextSequence
	if journey.discard {
		expected = append(expected, workloadDiscardIntent(
			sequence,
			preparation.Workload,
			preparation.Transaction.ID.String(),
		))
		sequence++
	}

	if journey.reverseRename {
		expected = append(expected, workloadTransitionIntent(sequence, journey.rename))
		sequence++
	}

	expected = append(expected, workloadTransitionIntent(sequence, journey.restoreStart))
	suffix := preparation.Actions[journey.coreActions:]
	prefix := min(len(suffix), len(expected))
	for index, action := range suffix[:prefix] {
		if !actionMatchesExpected(action, preparation.Transaction.ID, expected[index]) {
			return restoreJourney{}, false
		}
	}
	if len(suffix) <= len(expected) {
		return journey, true
	}

	return validatedRestoreRetries(preparation.Transaction.ID, journey, suffix, expected, sequence)
}

func validatedRestoreRetries(
	transactionID store.TransactionID,
	journey restoreJourney,
	suffix []store.Action,
	expected []store.ActionIntent,
	sequence int64,
) (restoreJourney, bool) {
	for index, action := range suffix[len(expected):] {
		if index > 0 {
			previous := suffix[len(expected)+index-1]
			_, err := completedTransitionResult(previous, journey.restoreStart)
			if previous.State != store.ActionStateCompleted || err != nil {
				return restoreJourney{}, false
			}
		}
		sequence++
		if !actionMatchesExpected(
			action,
			transactionID,
			workloadTransitionIntent(sequence, journey.restoreStart),
		) {
			return restoreJourney{}, false
		}
		journey.retryCursor = journey.coreActions + len(expected) + index
		journey.retrySequence = sequence
	}
	initialStart := suffix[len(expected)-1]
	satisfied, err := completedTransitionResult(initialStart, journey.restoreStart)
	if initialStart.State != store.ActionStateCompleted || !satisfied || err != nil {
		return restoreJourney{}, false
	}

	return journey, true
}

func completeRestore(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadTransitionRuntime,
	restore WorkloadTransition,
) error {
	probe, err := runtime.ProbeWorkloadTransition(ctx, restore)
	if err != nil {
		return fmt.Errorf("prove restored workload: %w", err)
	}

	if probe.State != WorkloadEffectProbeObserved || probe.Workload != restore.After {
		return ErrConflictingState
	}
	if err = requireWorkloadConvergence(
		mutation.preparation.Applied.Healthcheck,
		probe.Workload.Lifecycle,
		probe.Health,
	); err != nil {
		return err
	}

	transaction, err := mutation.lock.SetTransactionState(
		ctx,
		mutation.preparation.Transaction.ID,
		store.TransactionFailed,
	)
	if err != nil {
		return fmt.Errorf("complete restore transaction: %w", err)
	}

	mutation.preparation.Transaction = transaction
	mutation.publishTransaction(EventTransactionRestored)

	return nil
}

func validRestoreMutation(mutation *boundMutation) bool {
	if mutation == nil || mutation.lock == nil {
		return false
	}

	preparation := mutation.preparation

	return preparation.HasTransaction && preparation.HasApplied &&
		preparation.Plan.Kind == PlanRestore &&
		preparation.Transaction.Kind == store.TransactionUpgrade &&
		preparation.Transaction.State == store.TransactionDegraded &&
		preparation.Transaction.BaseTransactionID == preparation.Applied.TransactionID &&
		preparation.Transaction.PredecessorWorkloadID == preparation.Applied.WorkloadID
}
