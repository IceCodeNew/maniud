package application

import (
	"context"
	"errors"
	"fmt"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

// HealthResolutionAction identifies one explicit operator decision for an
// unresolved health gate.
type HealthResolutionAction string

const (
	// HealthResolutionRollback stops and discards a transaction-owned candidate
	// before restoring an upgrade predecessor when one exists.
	HealthResolutionRollback HealthResolutionAction = "rollback"
	// HealthResolutionCancelAdoption abandons only the local adoption intent.
	HealthResolutionCancelAdoption HealthResolutionAction = "cancel_adoption"
	// HealthResolutionRetryRestoreStart restarts an exact stopped predecessor
	// without changing the candidate rollback result.
	HealthResolutionRetryRestoreStart HealthResolutionAction = "retry_restore_start"
)

// HealthResolution binds an explicit decision to the transaction shown by a
// correlated snapshot. Transaction is the opaque snapshot transaction ID.
type HealthResolution struct {
	Transaction string
	Action      HealthResolutionAction
	Observation HealthResolutionObservation
}

// HealthResolutionObservation binds a confirmation to the bounded workload
// identity, lifecycle, and health state shown by the correlated snapshot.
type HealthResolutionObservation struct {
	State                WorkloadObservationState
	WorkloadID           string
	StartedAt            time.Time
	Configuration        domain.Digest
	Storage              domain.Digest
	ConfigurationMatches bool
	Lifecycle            WorkloadLifecycle
	Health               WorkloadHealth
	Ownership            domain.WorkloadOwnership
}

//nolint:cyclop // Keeping the closed transaction/plan matrix together makes missing states visible.
func healthResolutionForSnapshot(preparation Preparation) (HealthResolutionAction, bool) {
	if !preparation.HasTransaction {
		return "", false
	}
	transaction := preparation.Transaction
	plan := preparation.Plan
	if transaction.Kind == store.TransactionAdopt {
		if plan.Health == HealthConvergencePending || plan.Health == HealthConvergenceDegraded {
			return HealthResolutionCancelAdoption, false
		}

		return "", false
	}
	if transaction.Kind != store.TransactionBootstrap && transaction.Kind != store.TransactionUpgrade {
		return "", false
	}
	if transaction.State == store.TransactionHealthDegraded && plan.Health == HealthConvergenceDegraded {
		return HealthResolutionRollback, transaction.Kind == store.TransactionUpgrade
	}
	if transaction.Kind == store.TransactionUpgrade && transaction.State == store.TransactionDegraded &&
		plan.Kind == PlanRestore && plan.Health == HealthConvergenceDegraded &&
		plan.Observation.State == WorkloadObservationPresent &&
		plan.Observation.Lifecycle == WorkloadLifecycleExited {
		return HealthResolutionRetryRestoreStart, false
	}

	return "", false
}

type healthResolutionRuntime interface {
	restoreRuntime
	workloadHealthStopRuntime
}

// ResolveHealth executes one confirmed health rollback or adoption
// cancellation after re-preparing the service under its writer lock.
//
//nolint:cyclop // Resource acquisition and cleanup mirror Apply's fail-closed façade boundary.
func (facade *ApplyFacade) ResolveHealth(
	ctx context.Context,
	request Request,
	resolution HealthResolution,
) (Plan, error) {
	var empty Plan
	if !facade.valid() || !validHealthResolution(resolution) {
		return empty, ErrInvalidRequest
	}

	runtimeKind, err := applyRuntimeKind(ctx, request)
	if err != nil {
		return empty, fmt.Errorf("select health resolution runtime: %w", err)
	}
	openRuntime, err := facade.runtimeFactory(runtimeKind)
	if err != nil {
		return empty, err
	}
	runtime, err := openRuntime(ctx)
	if err != nil {
		return empty, fmt.Errorf("open health resolution runtime: %w", err)
	}
	if runtime == nil {
		return empty, ErrInvalidRequest
	}

	state, err := facade.openState(ctx)
	if err != nil {
		runtime.CloseIdleConnections()

		return empty, fmt.Errorf("open health resolution state: %w", err)
	}
	if state == nil {
		runtime.CloseIdleConnections()

		return empty, ErrInvalidRequest
	}

	plan, runErr := facade.resolveHealth(ctx, request, resolution, state, runtime)
	runtime.CloseIdleConnections()
	closeErr := state.Close()
	if runErr != nil || closeErr != nil {
		return empty, errors.Join(runErr, closeErr)
	}

	return plan, nil
}

func validHealthResolution(resolution HealthResolution) bool {
	if !validTransactionID(resolution.Transaction) ||
		!validHealthResolutionObservation(resolution.Observation) {
		return false
	}

	return resolution.Action == HealthResolutionRollback ||
		resolution.Action == HealthResolutionCancelAdoption ||
		resolution.Action == HealthResolutionRetryRestoreStart
}

//nolint:cyclop // Closed states and complete stale-token fields form one trust boundary.
func validHealthResolutionObservation(observation HealthResolutionObservation) bool {
	switch observation.State {
	case WorkloadObservationMissing:
		return observation == (HealthResolutionObservation{State: WorkloadObservationMissing})
	case WorkloadObservationPresent:
		return observation.WorkloadID != "" && observation.Configuration != (domain.Digest{}) &&
			observation.Storage != (domain.Digest{}) && validWorkloadLifecycle(observation.Lifecycle) &&
			validWorkloadHealth(observation.Health) && validObservationStartedAt(observation.StartedAt) &&
			observation.Ownership.Status <= domain.OwnershipManaged
	case WorkloadObservationUnknown:
		return false
	default:
		return false
	}
}

func healthResolutionObservation(observation WorkloadObservation) HealthResolutionObservation {
	return HealthResolutionObservation{
		State: observation.State, WorkloadID: observation.ID,
		StartedAt:     observation.StartedAt,
		Configuration: observation.ConfigurationDigest, Storage: observation.StorageDigest,
		ConfigurationMatches: observation.ConfigurationMatches,
		Lifecycle:            observation.Lifecycle,
		Health:               observation.Health,
		Ownership:            observation.Ownership,
	}
}

func (facade *ApplyFacade) resolveHealth(
	ctx context.Context,
	request Request,
	resolution HealthResolution,
	state *store.Store,
	operationRuntime OperationRuntime,
) (plan Plan, err error) {
	runtime, valid := operationRuntime.(healthResolutionRuntime)
	if !valid {
		return Plan{}, ErrInvalidRequest
	}
	service := newService(facade.images, operationRuntime, state, facade.events)
	mutation, err := service.bindMutation(ctx, request, state)
	if err != nil {
		return Plan{}, err
	}
	defer func() { //nolint:contextcheck // ServiceLock.Close owns its non-cancelled lease-release context.
		err = errors.Join(err, mutation.close())
	}()

	if mutation.preparation.Transaction.ID.String() != resolution.Transaction ||
		healthResolutionObservation(mutation.preparation.Plan.Observation) != resolution.Observation {
		return Plan{}, ErrSnapshotStale
	}
	plan = mutation.preparation.Plan

	switch resolution.Action {
	case HealthResolutionRollback:
		err = rollbackHealthCandidate(ctx, mutation, state, runtime)
	case HealthResolutionCancelAdoption:
		err = cancelPendingAdoption(ctx, mutation)
	case HealthResolutionRetryRestoreStart:
		err = retryRestoreStart(ctx, mutation, runtime)
	default:
		err = ErrInvalidRequest
	}
	if err != nil {
		return Plan{}, err
	}

	return plan, nil
}

func retryRestoreStart(
	ctx context.Context,
	mutation *boundMutation,
	runtime restoreRuntime,
) error {
	journey, err := prepareRestoreJourney(ctx, mutation)
	if err != nil {
		return err
	}
	if !retryableRestoreObservation(mutation.preparation) {
		return ErrConflictingState
	}

	action := store.Action{}
	sequence := int64(len(mutation.preparation.Actions) + 1)
	if journey.retrySequence > 0 {
		candidate := mutation.preparation.Actions[journey.retryCursor]
		if candidate.State != store.ActionStateCompleted {
			action = candidate
			sequence = journey.retrySequence
		}
	}

	_, _, err = settleUpgradeTransition(
		ctx,
		mutation,
		runtime,
		action,
		sequence,
		journey.restoreStart,
	)
	if err != nil {
		return err
	}

	return completeRestore(ctx, mutation, runtime, journey.restoreStart)
}

func retryableRestoreObservation(preparation Preparation) bool {
	observation := preparation.Plan.Observation

	return observation.State == WorkloadObservationPresent &&
		observation.Lifecycle == WorkloadLifecycleExited &&
		observationMatchesApplied(observation, preparation.Workload, preparation.Applied)
}

func rollbackHealthCandidate(
	ctx context.Context,
	mutation *boundMutation,
	state *store.Store,
	runtime healthResolutionRuntime,
) error {
	if !validHealthRollbackMutation(mutation, state, runtime) {
		return ErrInvalidRequest
	}

	if err := settleHealthRollbackStop(ctx, mutation, state, runtime); err != nil {
		return err
	}

	if mutation.preparation.Transaction.Kind == store.TransactionBootstrap {
		return rollbackBootstrapHealth(ctx, mutation, state, runtime)
	}

	return rollbackUpgradeHealth(ctx, mutation, state, runtime)
}

func validHealthRollbackMutation(
	mutation *boundMutation,
	state *store.Store,
	runtime healthResolutionRuntime,
) bool {
	return mutation != nil && state != nil && runtime != nil &&
		(mutation.preparation.Transaction.Kind == store.TransactionBootstrap ||
			mutation.preparation.Transaction.Kind == store.TransactionUpgrade) &&
		mutation.preparation.Transaction.State == store.TransactionHealthDegraded &&
		mutation.preparation.Plan.Kind == PlanHealthDegraded
}

func settleHealthRollbackStop(
	ctx context.Context,
	mutation *boundMutation,
	state *store.Store,
	runtime healthResolutionRuntime,
) error {
	stopAction, sequence, err := prepareHealthRollbackStop(ctx, mutation)
	if err != nil {
		return err
	}
	needsStop, err := healthRollbackNeedsStop(mutation.preparation)
	if err != nil {
		return err
	}
	if stopAction != (store.Action{}) || needsStop {
		if err = settleWorkloadHealthStop(ctx, mutation, runtime, stopAction, sequence); err != nil {
			return err
		}
		if err = refreshMutationActions(ctx, mutation, state); err != nil {
			return err
		}
	}

	return nil
}

func prepareHealthRollbackStop(
	ctx context.Context,
	mutation *boundMutation,
) (store.Action, int64, error) {
	preparation := mutation.preparation
	switch preparation.Transaction.Kind {
	case store.TransactionBootstrap:
		journey, err := bootstrapHealthRollbackJourney(preparation)

		return journey.stop, journey.nextSequence, err
	case store.TransactionUpgrade:
		return upgradeHealthStopAction(ctx, mutation)
	case store.TransactionAdopt:
		return store.Action{}, 0, ErrInvalidRequest
	default:
		return store.Action{}, 0, ErrConflictingState
	}
}

func healthRollbackNeedsStop(preparation Preparation) (bool, error) {
	observation := preparation.Plan.Observation
	if observation.State == WorkloadObservationMissing {
		return false, nil
	}
	if observation.State != WorkloadObservationPresent ||
		!healthRollbackObservationMatches(observation, preparation) {
		return false, ErrConflictingState
	}

	switch observation.Lifecycle {
	case WorkloadLifecycleRunning:
		return runningHealthRollbackNeedsStop(preparation)
	case WorkloadLifecycleRestarting:
		return false, ErrHealthPending
	case WorkloadLifecycleCreated, WorkloadLifecycleExited:
		return false, nil
	case WorkloadLifecycleUnknown, WorkloadLifecyclePaused, WorkloadLifecycleRemoving, WorkloadLifecycleDead:
		return false, ErrConflictingState
	default:
		return false, ErrConflictingState
	}
}

func runningHealthRollbackNeedsStop(preparation Preparation) (bool, error) {
	err := requireWorkloadConvergence(
		activeHealthcheck(preparation.Workload),
		preparation.Plan.Observation.Lifecycle,
		preparation.Plan.Observation.Health,
	)
	if err == nil {
		return false, ErrSnapshotStale
	}
	if errors.Is(err, ErrHealthPending) {
		return false, err
	}

	return true, nil
}

func healthRollbackObservationMatches(observation WorkloadObservation, preparation Preparation) bool {
	evidence := WorkloadEffectEvidence{
		ID: observation.ID, Name: preparation.Workload.ContainerName,
		ConfigurationDigest:  observation.ConfigurationDigest,
		StorageDigest:        observation.StorageDigest,
		RuntimeMounts:        observation.RuntimeMounts,
		ConfigurationMatches: observation.ConfigurationMatches,
		Lifecycle:            observation.Lifecycle,
		Health:               observation.Health,
		Ownership:            observation.Ownership,
	}

	return healthRollbackWorkloadMatches(
		evidence,
		preparation.Workload,
		preparation.Transaction.ID.String(),
	)
}

func settleWorkloadHealthStop(
	ctx context.Context,
	mutation *boundMutation,
	runtime workloadHealthStopRuntime,
	action store.Action,
	sequence int64,
) error {
	preparation := mutation.preparation
	intent := workloadHealthStopIntent(
		sequence,
		preparation.Workload,
		preparation.Transaction.ID.String(),
	)
	if action != (store.Action{}) &&
		!actionMatchesExpected(action, preparation.Transaction.ID, intent) {
		return ErrConflictingState
	}

	effect := &workloadHealthStopEffect{
		runtime: runtime, workload: preparation.Workload,
		transaction: preparation.Transaction.ID.String(),
	}
	if action.State == store.ActionStateCompleted {
		return verifyCompletedRuntimeEffect(ctx, action, effect)
	}

	postcondition, err := runWorkloadHealthStop(
		ctx,
		mutation.effectJournal(),
		preparation.Transaction.ID,
		sequence,
		preparation.Workload,
		runtime,
	)

	return requireSatisfiedEffect(postcondition, err)
}

type bootstrapHealthRollback struct {
	stop         store.Action
	discard      store.Action
	nextSequence int64
}

func bootstrapHealthRollbackJourney(preparation Preparation) (bootstrapHealthRollback, error) {
	var empty bootstrapHealthRollback
	actions := preparation.Actions
	coreActions := len(actions)
	for index, action := range actions {
		if action.Kind == workloadHealthStopActionKind || action.Kind == workloadDiscardActionKind {
			coreActions = index

			break
		}
	}
	if !validBootstrapHealthCore(actions[:coreActions]) {
		return empty, ErrConflictingState
	}

	journey := bootstrapHealthRollback{nextSequence: int64(coreActions + 1)}

	return extendBootstrapHealthRollback(preparation, journey, actions[coreActions:])
}

func extendBootstrapHealthRollback(
	preparation Preparation,
	journey bootstrapHealthRollback,
	suffix []store.Action,
) (bootstrapHealthRollback, error) {
	var empty bootstrapHealthRollback
	if len(suffix) > 0 && suffix[0].Kind == workloadHealthStopActionKind {
		intent := workloadHealthStopIntent(
			journey.nextSequence,
			preparation.Workload,
			preparation.Transaction.ID.String(),
		)
		if !actionMatchesExpected(suffix[0], preparation.Transaction.ID, intent) {
			return empty, ErrConflictingState
		}
		journey.stop = suffix[0]
		journey.nextSequence++
		suffix = suffix[1:]
	}
	if len(suffix) > 0 && suffix[0].Kind == workloadDiscardActionKind {
		intent := workloadDiscardIntent(
			journey.nextSequence,
			preparation.Workload,
			preparation.Transaction.ID.String(),
		)
		if !actionMatchesExpected(suffix[0], preparation.Transaction.ID, intent) {
			return empty, ErrConflictingState
		}
		journey.discard = suffix[0]
		journey.nextSequence++
		suffix = suffix[1:]
	}
	if len(suffix) != 0 {
		return empty, ErrConflictingState
	}

	return journey, nil
}

func validBootstrapHealthCore(actions []store.Action) bool {
	if !validBootstrapActions(actions) || len(actions) == 0 ||
		actions[len(actions)-1].Kind != workloadStartActionKind {
		return false
	}
	for _, action := range actions {
		if action.State != store.ActionStateCompleted || action.PostconditionDigest == nil {
			return false
		}
	}

	return true
}

func upgradeHealthStopAction(
	ctx context.Context,
	mutation *boundMutation,
) (store.Action, int64, error) {
	preparation := mutation.preparation
	publication, err := loadRestorePublication(ctx, mutation)
	if err != nil {
		return store.Action{}, 0, err
	}
	intents := upgradeCoreIntents(preparation, newUpgradeJourney(preparation), publication)
	coreActions := restoreCoreActionCount(preparation.Actions, intents)
	if coreActions == 0 ||
		!coreContainsAction(preparation.Actions[:coreActions], workloadStartActionKind) {
		return store.Action{}, 0, ErrConflictingState
	}
	if err = validateCompletedRestoreCore(preparation, intents, coreActions); err != nil {
		return store.Action{}, 0, err
	}

	sequence := int64(coreActions + 1)
	suffix := preparation.Actions[coreActions:]
	if len(suffix) == 0 {
		return store.Action{}, sequence, nil
	}
	if len(suffix) != 1 || suffix[0].Kind != workloadHealthStopActionKind ||
		!actionMatchesExpected(
			suffix[0],
			preparation.Transaction.ID,
			workloadHealthStopIntent(
				sequence,
				preparation.Workload,
				preparation.Transaction.ID.String(),
			),
		) {
		return store.Action{}, 0, ErrConflictingState
	}

	return suffix[0], sequence, nil
}

func rollbackBootstrapHealth(
	ctx context.Context,
	mutation *boundMutation,
	state *store.Store,
	runtime WorkloadDiscardRuntime,
) error {
	journey, err := bootstrapHealthRollbackJourney(mutation.preparation)
	if err != nil {
		return err
	}
	sequence := int64(len(mutation.preparation.Actions) + 1)
	if journey.discard != (store.Action{}) {
		sequence = journey.discard.Sequence
	}
	if err = settleWorkloadDiscard(
		ctx,
		mutation,
		runtime,
		journey.discard,
		sequence,
	); err != nil {
		return err
	}
	if err = refreshMutationActions(ctx, mutation, state); err != nil {
		return err
	}

	return failHealthResolution(ctx, mutation, EventTransactionFailed)
}

func rollbackUpgradeHealth(
	ctx context.Context,
	mutation *boundMutation,
	state *store.Store,
	runtime restoreRuntime,
) error {
	transaction, err := mutation.lock.SetTransactionState(
		ctx,
		mutation.preparation.Transaction.ID,
		store.TransactionDegraded,
	)
	if err != nil {
		return fmt.Errorf("begin health rollback restore: %w", err)
	}
	mutation.preparation.Transaction = transaction
	mutation.preparation.Plan.Kind = PlanRestore
	if err = refreshMutationActions(ctx, mutation, state); err != nil {
		return err
	}
	mutation.publishTransaction(EventTransactionDegraded)

	return runRestore(ctx, mutation, runtime)
}

func refreshMutationActions(
	ctx context.Context,
	mutation *boundMutation,
	state *store.Store,
) error {
	actions, err := state.Actions(ctx, mutation.preparation.Transaction.ID)
	if err != nil {
		return fmt.Errorf("refresh health resolution actions: %w", err)
	}
	mutation.preparation.Actions = actions

	return nil
}

func cancelPendingAdoption(ctx context.Context, mutation *boundMutation) error {
	if mutation == nil || mutation.lock == nil || !activeAdoptTransaction(mutation.preparation) ||
		len(mutation.preparation.Actions) != 0 {
		return ErrInvalidRequest
	}
	observation := mutation.preparation.Plan.Observation
	if observation.State != WorkloadObservationMissing &&
		(observation.State != WorkloadObservationPresent ||
			!cancellableAdoptionObservation(observation, mutation.preparation)) {
		return ErrConflictingState
	}

	return failHealthResolution(ctx, mutation, EventTransactionFailed)
}

func cancellableAdoptionObservation(observation WorkloadObservation, preparation Preparation) bool {
	return observation.ID == preparation.Transaction.PredecessorWorkloadID &&
		observation.ConfigurationDigest != (domain.Digest{}) && observation.ConfigurationMatches &&
		workloadStorageMatches(observation.StorageDigest, observation.RuntimeMounts, preparation.Workload) &&
		observation.Ownership == (domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged})
}

func failHealthResolution(
	ctx context.Context,
	mutation *boundMutation,
	event EventKind,
) error {
	transaction, err := mutation.lock.SetTransactionState(
		ctx,
		mutation.preparation.Transaction.ID,
		store.TransactionFailed,
	)
	if err != nil {
		return fmt.Errorf("complete health resolution: %w", err)
	}
	mutation.preparation.Transaction = transaction
	mutation.publishTransaction(event)

	return nil
}
