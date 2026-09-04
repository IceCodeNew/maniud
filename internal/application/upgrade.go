package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	retainedWorkloadPrefix       = "maniud-retained-"
	afterImagePullCursor         = 1
	afterImagePullActionSequence = 2
)

type upgradeRuntime interface {
	ImageRuntime
	WorkloadEffectRuntime
	WorkloadStartRuntime
	WorkloadTransitionRuntime
	WorkloadDiscardRuntime
	WorkloadArchiveRuntime
}

type upgradeJourney struct {
	stop   WorkloadTransition
	rename WorkloadTransition
	remove WorkloadTransition
}

type upgradeExecution struct {
	mutation    *boundMutation
	runtime     upgradeRuntime
	actions     []store.Action
	journey     upgradeJourney
	sources     []backedStorageSource
	inventories []backup.Inventory
	archives    [][]byte
	manifest    backup.Manifest
	capacity    backup.PublicationCapacity
	publication backup.Publication
	cursor      int
	sequence    int64
}

const archiveTransferLimit = 1 << 30

func runUpgrade(
	ctx context.Context,
	mutation *boundMutation,
	runtime upgradeRuntime,
	authenticator credential.Provider,
) error {
	if mutation == nil || runtime == nil || authenticator == nil || !validUpgradeMutation(mutation) {
		return ErrInvalidRequest
	}

	cursor, sequence, resolved, err := settleUpgradeImage(ctx, mutation, runtime, authenticator)
	if err != nil {
		if resolved {
			return resolveUpgradeFailure(ctx, mutation, store.TransactionFailed, err)
		}

		return err
	}
	if err = materializeMutationRuntime(mutation); err != nil {
		return err
	}

	execution := upgradeExecution{
		mutation: mutation,
		runtime:  runtime,
		actions:  mutation.preparation.Actions,
		journey:  newUpgradeJourney(mutation.preparation),
		sources:  backedStorageSources(mutation.preparation.Plan.Observation, mutation.preparation.Workload),
		cursor:   cursor,
		sequence: sequence,
	}
	stages := []func(context.Context) error{
		execution.inventory,
		execution.prepareBackupCapacity,
		execution.stop,
		execution.backup,
		execution.rename,
		execution.create,
		execution.restore,
		execution.start,
	}
	for _, stage := range stages {
		err = stage(ctx)
		if err != nil {
			return err
		}
	}

	return completeUpgrade(ctx, &execution)
}

func (execution *upgradeExecution) stop(ctx context.Context) error {
	action := execution.nextAction()
	satisfied, resolved, err := settleUpgradeTransition(
		ctx,
		execution.mutation,
		execution.runtime,
		action,
		execution.sequence,
		execution.journey.stop,
	)
	if err != nil || !satisfied {
		return resolveTransitionFailure(
			ctx,
			execution.mutation,
			store.TransactionFailed,
			satisfied,
			resolved,
			err,
		)
	}

	execution.sequence++

	return nil
}

func (execution *upgradeExecution) rename(ctx context.Context) error {
	action := execution.nextAction()
	satisfied, resolved, err := settleUpgradeTransition(
		ctx,
		execution.mutation,
		execution.runtime,
		action,
		execution.sequence,
		execution.journey.rename,
	)
	if err != nil || !satisfied {
		return resolveTransitionFailure(
			ctx,
			execution.mutation,
			store.TransactionDegraded,
			satisfied,
			resolved,
			err,
		)
	}

	execution.sequence++

	return nil
}

func (execution *upgradeExecution) create(ctx context.Context) error {
	if err := materializeMutationRuntime(execution.mutation); err != nil {
		return err
	}
	if err := prepareUpgradeReplacementBinds(execution); err != nil {
		return err
	}

	action := execution.nextAction()
	postcondition, err := settleWorkloadCreateResult(
		ctx,
		execution.mutation,
		execution.runtime,
		action,
		execution.sequence,
		upgradeCreateOptions(execution.sources),
	)
	if err != nil {
		return resolveEffectFailure(
			ctx,
			execution.mutation,
			postcondition,
			err,
		)
	}

	if !postcondition.Satisfied {
		return resolveEffectFailure(
			ctx,
			execution.mutation,
			postcondition,
			ErrConflictingState,
		)
	}

	execution.sequence++

	return nil
}

func (execution *upgradeExecution) start(ctx context.Context) error {
	action := execution.nextAction()
	postcondition, err := settleWorkloadStartResult(
		ctx,
		execution.mutation,
		execution.runtime,
		action,
		execution.sequence,
	)
	if err != nil {
		return resolveEffectFailure(
			ctx,
			execution.mutation,
			postcondition,
			err,
		)
	}

	if !postcondition.Satisfied {
		return resolveEffectFailure(
			ctx,
			execution.mutation,
			postcondition,
			ErrConflictingState,
		)
	}

	execution.sequence++

	return nil
}

func (execution *upgradeExecution) remove(ctx context.Context) error {
	action := execution.nextAction()
	satisfied, resolved, err := settleUpgradeTransition(
		ctx,
		execution.mutation,
		execution.runtime,
		action,
		execution.sequence,
		execution.journey.remove,
	)
	if err != nil || !satisfied {
		return resolveTransitionFailure(
			ctx,
			execution.mutation,
			store.TransactionDegraded,
			satisfied,
			resolved,
			err,
		)
	}

	return nil
}

func (execution *upgradeExecution) nextAction() store.Action {
	action, cursor := nextUpgradeAction(execution.actions, execution.cursor)
	execution.cursor = cursor

	return action
}

func settleUpgradeImage(
	ctx context.Context,
	mutation *boundMutation,
	runtime ImageRuntime,
	authenticator credential.Provider,
) (int, int64, bool, error) {
	actions := mutation.preparation.Actions
	if len(actions) > 0 && actions[0].Kind == imagePullActionKind {
		if mutation.preparation.Workload.Image.Origin != domain.ImageOriginRegistry {
			return 0, 0, false, ErrConflictingState
		}
		postcondition, err := settleUpgradeImagePull(
			ctx,
			mutation,
			runtime,
			authenticator,
			actions[0],
		)

		return afterImagePullCursor,
			afterImagePullActionSequence,
			resolvedNegative(postcondition),
			effectResultError(postcondition, err)
	}

	if len(actions) > 0 {
		return 0, 1, false, nil
	}

	present, err := imagePresent(ctx, runtime, mutation.preparation.Workload.Image)
	if err != nil {
		return 0, 0, false, err
	}

	if present {
		return 0, 1, false, nil
	}
	if mutation.preparation.Workload.Image.Origin == domain.ImageOriginDockerArchive {
		return 0, 0, false, ErrArchiveImageMissing
	}

	postcondition, err := runImagePull(
		ctx,
		mutation.effectJournal(),
		mutation.preparation.Transaction.ID,
		1,
		mutation.preparation.Workload.Image,
		runtime,
		authenticator,
	)

	return 0,
		afterImagePullActionSequence,
		resolvedNegative(postcondition),
		effectResultError(postcondition, err)
}

func settleUpgradeImagePull(
	ctx context.Context,
	mutation *boundMutation,
	runtime ImageRuntime,
	authenticator credential.Provider,
	action store.Action,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	preparation := mutation.preparation
	intent := imagePullIntent(1, preparation.Workload.Image)
	if !actionMatchesExpected(action, preparation.Transaction.ID, intent) {
		return empty, ErrConflictingState
	}

	if action.State == store.ActionStateCompleted {
		return completedUpgradeImagePull(ctx, action, runtime, preparation)
	}

	return runImagePull(
		ctx,
		mutation.effectJournal(),
		preparation.Transaction.ID,
		1,
		preparation.Workload.Image,
		runtime,
		authenticator,
	)
}

func completedUpgradeImagePull(
	ctx context.Context,
	action store.Action,
	runtime ImageRuntime,
	preparation Preparation,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	if action.PostconditionDigest == nil {
		return empty, ErrConflictingState
	}

	missing := imageEffectDigest(imageEffectMissing, preparation.Workload.Image)
	if *action.PostconditionDigest == missing {
		return EffectPostcondition{Digest: missing, Satisfied: false}, nil
	}

	observed := imageEffectDigest(imageEffectObserved, preparation.Workload.Image)
	if *action.PostconditionDigest != observed {
		return empty, ErrConflictingState
	}

	postcondition := EffectPostcondition{Digest: observed, Satisfied: true}
	present, err := imagePresent(ctx, runtime, preparation.Workload.Image)
	if err != nil {
		return postcondition, err
	}

	if !present {
		return postcondition, ErrConflictingState
	}

	return postcondition, nil
}

func settleUpgradeTransition(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadTransitionRuntime,
	action store.Action,
	sequence int64,
	transition WorkloadTransition,
) (bool, bool, error) {
	intent := workloadTransitionIntent(sequence, transition)
	if action != (store.Action{}) && !actionMatchesExpected(
		action,
		mutation.preparation.Transaction.ID,
		intent,
	) {
		return false, false, ErrConflictingState
	}

	if action.State == store.ActionStateCompleted {
		satisfied, err := completedTransitionResult(action, transition)

		return satisfied, err == nil, err
	}

	postcondition, err := runWorkloadTransition(
		ctx,
		mutation.effectJournal(),
		mutation.preparation.Transaction.ID,
		sequence,
		transition,
		runtime,
	)

	return postcondition.Satisfied, postcondition.Digest != (domain.Digest{}), effectResultError(postcondition, err)
}

func resolvedNegative(postcondition EffectPostcondition) bool {
	return postcondition.Digest != (domain.Digest{}) && !postcondition.Satisfied
}

func completedTransitionResult(action store.Action, transition WorkloadTransition) (bool, error) {
	if action.PostconditionDigest == nil {
		return false, ErrConflictingState
	}

	satisfied := workloadTransitionPostcondition(transition, true)
	if *action.PostconditionDigest == satisfied.Digest {
		return true, nil
	}

	unsatisfied := workloadTransitionPostcondition(transition, false)
	if *action.PostconditionDigest == unsatisfied.Digest {
		return false, nil
	}

	return false, ErrConflictingState
}

func workloadTransitionPostcondition(
	transition WorkloadTransition,
	satisfied bool,
) EffectPostcondition {
	if satisfied && transition.Kind == WorkloadTransitionRemove {
		return EffectPostcondition{
			Digest:    workloadTransitionDigest(workloadTransitionAfter, transition, ExistingWorkload{}),
			Satisfied: true,
		}
	}

	state := byte(workloadTransitionBefore)
	workload := transition.Before
	if satisfied {
		state = workloadTransitionAfter
		workload = transition.After
	}

	return EffectPostcondition{
		Digest:    workloadTransitionDigest(state, transition, workload),
		Satisfied: satisfied,
	}
}

func effectResultError(postcondition EffectPostcondition, err error) error {
	if err != nil {
		return err
	}

	if !postcondition.Satisfied {
		return ErrConflictingState
	}

	return nil
}

func resolveTransitionFailure(
	ctx context.Context,
	mutation *boundMutation,
	target store.TransactionState,
	satisfied bool,
	resolved bool,
	cause error,
) error {
	if cause == nil && !satisfied {
		cause = ErrConflictingState
	}

	if !resolved {
		return cause
	}

	return resolveUpgradeFailure(ctx, mutation, target, cause)
}

func resolveEffectFailure(
	ctx context.Context,
	mutation *boundMutation,
	postcondition EffectPostcondition,
	cause error,
) error {
	if postcondition.Digest == (domain.Digest{}) {
		return cause
	}

	return resolveUpgradeFailure(ctx, mutation, store.TransactionDegraded, cause)
}

func resolveUpgradeFailure(
	ctx context.Context,
	mutation *boundMutation,
	target store.TransactionState,
	cause error,
) error {
	if mutation == nil || mutation.lock == nil || cause == nil {
		return errors.Join(cause, ErrInvalidRequest)
	}

	transaction, err := mutation.lock.SetTransactionState(
		context.WithoutCancel(ctx),
		mutation.preparation.Transaction.ID,
		target,
	)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("resolve upgrade transaction: %w", err))
	}

	mutation.preparation.Transaction = transaction
	event := EventTransactionFailed
	if transaction.State == store.TransactionDegraded {
		event = EventTransactionDegraded
	}
	mutation.publishTransaction(event)

	return cause
}

func completeUpgrade(
	ctx context.Context,
	execution *upgradeExecution,
) error {
	evidence, err := proveAppliedMutation(ctx, execution.mutation, execution.runtime, "upgrade")
	if err != nil {
		return err
	}
	if err = settleMutationConvergence(ctx, execution.mutation, evidence); err != nil {
		return err
	}
	if err = execution.remove(ctx); err != nil {
		return err
	}

	return publishAppliedMutation(
		ctx,
		execution.mutation,
		evidence,
		"upgrade",
		backupIndexIntent(execution.publication),
	)
}

func newUpgradeJourney(preparation Preparation) upgradeJourney {
	before := ExistingWorkload{
		ID:                  preparation.Applied.WorkloadID,
		Name:                preparation.Workload.ContainerName,
		ConfigurationDigest: preparation.Applied.ConfigurationDigest,
		Lifecycle:           WorkloadLifecycleRunning,
		Ownership:           appliedWorkloadOwnership(preparation.Applied, preparation.Workload.ServiceName),
	}
	stopped := before
	stopped.Lifecycle = WorkloadLifecycleExited
	renamed := stopped
	renamed.Name = retainedWorkloadPrefix + preparation.Transaction.ID.String()

	return upgradeJourney{
		stop: WorkloadTransition{
			Kind:   WorkloadTransitionStop,
			Before: before,
			After:  stopped,
		},
		rename: WorkloadTransition{
			Kind:   WorkloadTransitionRename,
			Before: stopped,
			After:  renamed,
		},
		remove: WorkloadTransition{
			Kind:   WorkloadTransitionRemove,
			Before: renamed,
		},
	}
}

func appliedWorkloadOwnership(applied store.AppliedService, service string) domain.WorkloadOwnership {
	if applied.Kind == store.TransactionAdopt {
		return domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged}
	}

	return domain.WorkloadOwnership{
		Status:           domain.OwnershipManaged,
		Service:          service,
		Transaction:      applied.TransactionID.String(),
		DesiredState:     applied.EffectiveDigest,
		Reference:        applied.ReferenceDigest,
		ImageConfig:      applied.ImageConfigDigest,
		PlatformManifest: applied.PlatformManifestDigest,
	}
}

func nextUpgradeAction(actions []store.Action, cursor int) (store.Action, int) {
	if cursor >= len(actions) {
		return store.Action{}, cursor
	}

	return actions[cursor], cursor + 1
}

func validUpgradeMutation(mutation *boundMutation) bool {
	if mutation == nil || mutation.lock == nil {
		return false
	}

	preparation := mutation.preparation
	if !preparation.HasTransaction || !preparation.HasApplied ||
		preparation.Transaction.Kind != store.TransactionUpgrade ||
		preparation.Transaction.BaseTransactionID != preparation.Applied.TransactionID ||
		preparation.Transaction.PredecessorWorkloadID != preparation.Applied.WorkloadID {
		return false
	}
	if !validUpgradeTransactionState(preparation.Transaction.State) {
		return false
	}

	return activeUpgradePlan(preparation.Plan.Kind) && validUpgradeActions(preparation.Actions)
}

func validUpgradeTransactionState(state store.TransactionState) bool {
	switch state {
	case store.TransactionActive, store.TransactionHealthDegraded:
		return true
	case store.TransactionDegraded, store.TransactionFailed, store.TransactionSucceeded:
		return false
	default:
		return false
	}
}

func activeUpgradePlan(kind PlanKind) bool {
	return kind == PlanUpgrade || kind == PlanResume || kind == PlanProbeUnknownEffect ||
		kind == PlanHealthDegraded
}

func validUpgradeActions(actions []store.Action) bool {
	if len(actions) == 0 {
		return true
	}

	kinds := make([]string, 0, len(actions))
	for _, action := range actions {
		kinds = append(kinds, action.Kind)
	}

	return matchUpgradeActionPrefix(kinds)
}

func matchUpgradeActionPrefix(kinds []string) bool {
	expected := upgradeActionKinds(
		len(kinds) > 0 && kinds[0] == imagePullActionKind,
		containsStorageUpgradeAction(kinds),
	)
	if len(kinds) > len(expected) {
		return false
	}

	for index, kind := range kinds {
		if kind != expected[index] {
			return false
		}
	}

	return true
}

func containsStorageUpgradeAction(kinds []string) bool {
	for _, kind := range kinds {
		switch kind {
		case storageInventoryActionKind, storageBackupActionKind, storageRestoreActionKind:
			return true
		}
	}

	return false
}

func upgradeActionKinds(withImage, withStorage bool) []string {
	expected := make([]string, 0, upgradeCoreActionCapacity)
	if withImage {
		expected = append(expected, imagePullActionKind)
	}
	if withStorage {
		expected = append(expected, storageInventoryActionKind)
	}

	expected = append(expected, workloadStopActionKind)
	if withStorage {
		expected = append(expected, storageBackupActionKind)
	}

	expected = append(expected,
		workloadRenameActionKind,
		workloadCreateActionKind,
	)
	if withStorage {
		expected = append(expected, storageRestoreActionKind)
	}

	return append(expected, workloadStartActionKind, workloadRemoveActionKind)
}
