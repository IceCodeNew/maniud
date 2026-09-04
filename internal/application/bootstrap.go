package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	bootstrapFirstActionSequence  int64 = 1
	bootstrapSecondActionSequence int64 = 2
)

type bootstrapRuntime interface {
	ImageRuntime
	WorkloadEffectRuntime
	WorkloadStartRuntime
}

func runBootstrap(
	ctx context.Context,
	mutation *boundMutation,
	runtime bootstrapRuntime,
	authenticator credential.Provider,
) error {
	if mutation == nil || !validBootstrapMutation(mutation) || runtime == nil || authenticator == nil {
		return ErrInvalidRequest
	}

	preparation := mutation.preparation
	actions := preparation.Actions
	cursor, sequence, err := settleBootstrapImage(ctx, mutation, runtime, authenticator)
	if err != nil {
		return err
	}
	if err = materializeMutationRuntime(mutation); err != nil {
		return err
	}

	create, cursor := nextBootstrapAction(actions, cursor)

	err = settleWorkloadCreate(ctx, mutation, runtime, create, sequence)
	if err != nil {
		return err
	}

	sequence++

	start, _ := nextBootstrapAction(actions, cursor)

	err = settleWorkloadStart(ctx, mutation, runtime, start, sequence)
	if err != nil {
		return err
	}

	return completeBootstrap(ctx, mutation, runtime)
}

func settleBootstrapImage(
	ctx context.Context,
	mutation *boundMutation,
	runtime bootstrapRuntime,
	authenticator credential.Provider,
) (int, int64, error) {
	actions := mutation.preparation.Actions
	if len(actions) > 0 && actions[0].Kind == imagePullActionKind {
		if mutation.preparation.Workload.Image.Origin != domain.ImageOriginRegistry {
			return 0, 0, ErrConflictingState
		}
		err := settleImagePull(
			ctx,
			mutation,
			runtime,
			authenticator,
			actions[0],
			bootstrapFirstActionSequence,
		)

		return 1, bootstrapSecondActionSequence, err
	}

	present, err := imagePresent(ctx, runtime, mutation.preparation.Workload.Image)
	if err != nil {
		return 0, 0, err
	}

	if present {
		return 0, bootstrapFirstActionSequence, nil
	}
	if mutation.preparation.Workload.Image.Origin == domain.ImageOriginDockerArchive {
		return 0, 0, ErrArchiveImageMissing
	}

	if len(actions) > 0 {
		return 0, 0, ErrConflictingState
	}

	err = settleImagePull(
		ctx,
		mutation,
		runtime,
		authenticator,
		store.Action{},
		bootstrapFirstActionSequence,
	)

	return 0, bootstrapSecondActionSequence, err
}

func nextBootstrapAction(actions []store.Action, cursor int) (store.Action, int) {
	if cursor >= len(actions) {
		return store.Action{}, cursor
	}

	return actions[cursor], cursor + 1
}

func completeBootstrap(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadStartRuntime,
) error {
	return completeAppliedMutation(ctx, mutation, runtime, "bootstrap", nil)
}

func completeAppliedMutation(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadStartRuntime,
	operation string,
	backup *store.BackupIndexIntent,
) error {
	evidence, err := proveAppliedMutation(ctx, mutation, runtime, operation)
	if err != nil {
		return err
	}
	if err = settleMutationConvergence(ctx, mutation, evidence); err != nil {
		return err
	}

	return publishAppliedMutation(ctx, mutation, evidence, operation, backup)
}

func proveAppliedMutation(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadStartRuntime,
	operation string,
) (WorkloadEffectEvidence, error) {
	var empty WorkloadEffectEvidence
	preparation := mutation.preparation
	probe, err := runtime.ProbeStartedWorkload(
		ctx,
		preparation.Workload,
		preparation.Transaction.ID.String(),
	)
	if err != nil {
		return empty, fmt.Errorf("prove %s workload: %w", operation, err)
	}

	if probe.State != WorkloadEffectProbeObserved || !startedWorkloadMatches(
		probe.Workload,
		preparation.Workload,
		preparation.Transaction.ID.String(),
	) {
		return empty, ErrConflictingState
	}

	return probe.Workload, nil
}

func settleMutationConvergence(
	ctx context.Context,
	mutation *boundMutation,
	evidence WorkloadEffectEvidence,
) error {
	err := requireWorkloadConvergence(
		activeHealthcheck(mutation.preparation.Workload),
		evidence.Lifecycle,
		evidence.Health,
	)
	if err == nil || errors.Is(err, ErrHealthPending) {
		return err
	}
	if mutation.preparation.Transaction.State == store.TransactionHealthDegraded {
		return err
	}

	transaction, stateErr := mutation.lock.SetTransactionState(
		context.WithoutCancel(ctx),
		mutation.preparation.Transaction.ID,
		store.TransactionHealthDegraded,
	)
	if stateErr != nil {
		return errors.Join(err, fmt.Errorf("record degraded health convergence: %w", stateErr))
	}
	mutation.preparation.Transaction = transaction
	mutation.publishTransaction(EventTransactionDegraded)

	return err
}

func publishAppliedMutation(
	ctx context.Context,
	mutation *boundMutation,
	evidence WorkloadEffectEvidence,
	operation string,
	backup *store.BackupIndexIntent,
) error {
	preparation := mutation.preparation

	intent := appliedServiceIntent(preparation.Workload, evidence)
	intent.Backup = backup
	applied, err := mutation.lock.CommitAppliedService(
		ctx,
		preparation.Transaction.ID,
		intent,
	)
	if err != nil {
		return fmt.Errorf("complete %s transaction: %w", operation, err)
	}

	completed := preparation.Transaction
	completed.State = store.TransactionSucceeded
	mutation.preparation.Transaction = completed
	mutation.preparation.Applied = applied
	mutation.preparation.HasApplied = true
	mutation.publishTransaction(EventTransactionSucceeded)

	return nil
}

func appliedServiceIntent(
	workload domain.DesiredWorkload,
	evidence WorkloadEffectEvidence,
) store.AppliedServiceIntent {
	return store.AppliedServiceIntent{
		WorkloadID:             evidence.ID,
		ConfigurationDigest:    evidence.ConfigurationDigest,
		StorageDigest:          evidence.StorageDigest,
		ReferenceDigest:        workload.Image.ReferenceDigest,
		PlatformManifestDigest: workload.Image.PlatformManifest,
		ImageConfigDigest:      workload.Image.ImageConfig,
		Healthcheck:            activeHealthcheck(workload),
	}
}

func validBootstrapMutation(mutation *boundMutation) bool {
	if mutation == nil || mutation.lock == nil || !mutation.preparation.HasTransaction ||
		mutation.preparation.Transaction.State != store.TransactionActive &&
			mutation.preparation.Transaction.State != store.TransactionHealthDegraded {
		return false
	}

	return validBootstrapPlan(
		mutation.preparation.Plan.Kind,
		mutation.preparation.Plan.Observation.State,
		mutation.preparation.Actions,
	)
}

func validBootstrapPlan(kind PlanKind, observation WorkloadObservationState, actions []store.Action) bool {
	if kind == PlanBootstrap {
		return len(actions) == 0 && observation == WorkloadObservationMissing
	}

	if kind == PlanResume {
		return validBootstrapActions(actions) && (len(actions) > 0 || observation == WorkloadObservationMissing)
	}
	if kind == PlanHealthDegraded {
		return validBootstrapActions(actions) && len(actions) > 0
	}

	return kind == PlanProbeUnknownEffect && len(actions) > 0 && validBootstrapActions(actions)
}

func validBootstrapActions(actions []store.Action) bool {
	expected := []string{workloadCreateActionKind, workloadStartActionKind}
	if len(actions) > 0 && actions[0].Kind == imagePullActionKind {
		expected = append([]string{imagePullActionKind}, expected...)
	}

	if len(actions) > len(expected) {
		return false
	}

	for index, action := range actions {
		if action.Kind != expected[index] {
			return false
		}
	}

	return true
}

func imagePresent(
	ctx context.Context,
	runtime ImageRuntime,
	expected domain.ImageIdentity,
) (bool, error) {
	probe, err := runtime.ProbeImage(ctx, expected)
	if err != nil {
		return false, fmt.Errorf("probe bootstrap image: %w", err)
	}

	switch probe.State {
	case ImageProbeMissing:
		if probe.Image != emptyImageEvidence() {
			return false, ErrConflictingState
		}

		return false, nil
	case ImageProbeObserved:
		if !probe.Matches(expected) {
			return false, ErrConflictingState
		}

		return true, nil
	case ImageProbeUnknown:
		return false, ErrConflictingState
	default:
		return false, ErrConflictingState
	}
}

func settleImagePull(
	ctx context.Context,
	mutation *boundMutation,
	runtime ImageRuntime,
	authenticator credential.Provider,
	action store.Action,
	sequence int64,
) error {
	preparation := mutation.preparation
	intent := imagePullIntent(sequence, preparation.Workload.Image)

	if action != (store.Action{}) && !actionMatchesExpected(action, preparation.Transaction.ID, intent) {
		return ErrConflictingState
	}

	if action.State == store.ActionStateCompleted {
		effect := imagePullEffect{
			runtime:       runtime,
			authenticator: authenticator,
			expected:      preparation.Workload.Image,
		}

		return verifyCompletedRuntimeEffect(ctx, action, effect)
	}

	postcondition, err := runImagePull(
		ctx,
		mutation.effectJournal(),
		preparation.Transaction.ID,
		sequence,
		preparation.Workload.Image,
		runtime,
		authenticator,
	)

	return requireSatisfiedEffect(postcondition, err)
}

func settleWorkloadCreate(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadEffectRuntime,
	action store.Action,
	sequence int64,
) error {
	postcondition, err := settleWorkloadCreateResult(
		ctx, mutation, runtime, action, sequence, defaultWorkloadCreateOptions(),
	)

	return requireSatisfiedEffect(postcondition, err)
}

func settleWorkloadCreateResult(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadEffectRuntime,
	action store.Action,
	sequence int64,
	options WorkloadCreateOptions,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	preparation := mutation.preparation
	transaction := preparation.Transaction.ID.String()
	intent := workloadCreateIntent(sequence, preparation.Workload, transaction)

	if action != (store.Action{}) && !actionMatchesExpected(action, preparation.Transaction.ID, intent) {
		return empty, ErrConflictingState
	}

	if action.State == store.ActionStateCompleted {
		return completedWorkloadCreateResult(ctx, action, runtime, preparation)
	}

	return runWorkloadCreate(
		ctx,
		mutation.effectJournal(),
		preparation.Transaction.ID,
		sequence,
		preparation.Workload,
		runtime,
		options,
	)
}

func completedWorkloadCreateResult(
	ctx context.Context,
	action store.Action,
	runtime WorkloadEffectRuntime,
	preparation Preparation,
) (EffectPostcondition, error) {
	transaction := preparation.Transaction.ID.String()
	missing := workloadEffectDigest(
		workloadEffectMissing,
		workloadCreateActionKind,
		preparation.Workload,
		transaction,
		"",
	)
	if action.PostconditionDigest != nil && *action.PostconditionDigest == missing {
		return EffectPostcondition{Digest: missing, Satisfied: false}, nil
	}

	postcondition := EffectPostcondition{Digest: completedActionDigest(action), Satisfied: true}

	return postcondition, verifyCompletedWorkloadCreate(ctx, action, runtime, preparation)
}

func settleWorkloadStart(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadStartRuntime,
	action store.Action,
	sequence int64,
) error {
	postcondition, err := settleWorkloadStartResult(ctx, mutation, runtime, action, sequence)

	return requireSatisfiedEffect(postcondition, err)
}

func settleWorkloadStartResult(
	ctx context.Context,
	mutation *boundMutation,
	runtime WorkloadStartRuntime,
	action store.Action,
	sequence int64,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	preparation := mutation.preparation
	transaction := preparation.Transaction.ID.String()
	intent := workloadStartIntent(sequence, preparation.Workload, transaction)

	if action != (store.Action{}) && !actionMatchesExpected(action, preparation.Transaction.ID, intent) {
		return empty, ErrConflictingState
	}

	if action.State == store.ActionStateCompleted {
		return completedWorkloadStartResult(ctx, action, runtime, preparation)
	}

	return runWorkloadStart(
		ctx,
		mutation.effectJournal(),
		preparation.Transaction.ID,
		sequence,
		preparation.Workload,
		runtime,
	)
}

func completedWorkloadStartResult(
	ctx context.Context,
	action store.Action,
	runtime WorkloadStartRuntime,
	preparation Preparation,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	effect := workloadStartEffect{
		runtime:     runtime,
		workload:    preparation.Workload,
		transaction: preparation.Transaction.ID.String(),
	}

	postcondition, err := effect.Probe(ctx)
	if err != nil {
		return empty, err
	}

	if action.PostconditionDigest == nil || *action.PostconditionDigest != postcondition.Digest {
		return empty, ErrConflictingState
	}

	return postcondition, nil
}

func completedActionDigest(action store.Action) domain.Digest {
	if action.PostconditionDigest == nil {
		return domain.Digest{}
	}

	return *action.PostconditionDigest
}

func requireSatisfiedEffect(postcondition EffectPostcondition, err error) error {
	if err != nil {
		return err
	}

	if !postcondition.Satisfied {
		return ErrConflictingState
	}

	return nil
}

func verifyCompletedWorkloadCreate(
	ctx context.Context,
	action store.Action,
	runtime WorkloadEffectRuntime,
	preparation Preparation,
) error {
	transaction := preparation.Transaction.ID.String()
	probe, err := runtime.ProbeCreatedWorkload(ctx, preparation.Workload, transaction, "")
	if err != nil {
		return fmt.Errorf("verify completed workload create: %w", err)
	}

	if probe.State != WorkloadEffectProbeObserved ||
		!createdWorkloadIdentityMatches(probe.Workload, preparation.Workload, transaction) {
		return ErrConflictingState
	}

	digest := workloadObservedEffectDigest(
		workloadEffectObserved,
		workloadCreateActionKind,
		preparation.Workload,
		transaction,
		probe.Workload.ID,
		probe.Workload.StorageDigest,
	)
	if action.PostconditionDigest == nil || *action.PostconditionDigest != digest {
		return ErrConflictingState
	}

	return nil
}

func createdWorkloadIdentityMatches(
	evidence WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
) bool {
	if evidence.Lifecycle != WorkloadLifecycleCreated && evidence.Lifecycle != WorkloadLifecycleRunning {
		return false
	}

	normalized := evidence
	normalized.Lifecycle = WorkloadLifecycleCreated

	return createdWorkloadMatches(normalized, workload, transaction, "")
}

func verifyCompletedRuntimeEffect(
	ctx context.Context,
	action store.Action,
	effect RuntimeEffect,
) error {
	postcondition, err := effect.Probe(ctx)
	if err != nil {
		return fmt.Errorf("verify completed runtime effect: %w", err)
	}

	if !postcondition.Satisfied || action.PostconditionDigest == nil ||
		*action.PostconditionDigest != postcondition.Digest {
		return ErrConflictingState
	}

	return nil
}

func actionMatchesExpected(
	action store.Action,
	identifier store.TransactionID,
	intent store.ActionIntent,
) bool {
	return action.TransactionID == identifier && action.Sequence == intent.Sequence &&
		action.Kind == intent.Kind && action.IntentDigest == intent.IntentDigest
}
