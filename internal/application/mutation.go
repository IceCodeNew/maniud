package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/registry/credential"
	"github.com/IceCodeNew/maniud/internal/store"
)

type mutationRuntime interface {
	Runtime
	upgradeRuntime
}

type boundMutation struct {
	preparation Preparation
	lock        *store.ServiceLock
	backupRoot  string
	materialize func() error
	events      EventSink
}

// Apply binds one service-scoped transaction and executes or recovers its
// runtime effects while retaining the service lock and writer fence.
func (service *service) Apply(
	ctx context.Context,
	request Request,
	state *store.Store,
	authenticator credential.Provider,
) (plan Plan, err error) {
	if service == nil || authenticator == nil {
		return Plan{}, ErrInvalidRequest
	}
	runtime, valid := service.runtime.(mutationRuntime)
	if !valid {
		return Plan{}, ErrInvalidRequest
	}

	mutation, err := service.bindMutation(ctx, request, state)
	if err != nil {
		return Plan{}, err
	}
	defer func() { //nolint:contextcheck // ServiceLock.Close owns its non-cancelled lease-release context.
		err = errors.Join(err, mutation.close())
	}()
	service.publishPlan(mutation.preparation)
	if err = materializeMutationRuntime(mutation); err != nil {
		return Plan{}, err
	}

	err = runBoundMutation(ctx, mutation, runtime, authenticator)
	if err != nil {
		return Plan{}, err
	}

	return mutation.preparation.Plan, nil
}

func mutationNeedsRepositoryRuntime(preparation Preparation) bool {
	if !preparation.HasTransaction {
		return false
	}

	return preparation.Transaction.Kind == store.TransactionBootstrap ||
		preparation.Transaction.Kind == store.TransactionUpgrade &&
			preparation.Transaction.State != store.TransactionDegraded
}

func materializeMutationRuntime(mutation *boundMutation) error {
	if mutation == nil || mutation.materialize == nil {
		return nil
	}
	if err := mutation.materialize(); err != nil {
		return fmt.Errorf("materialize apply runtime source: %w", err)
	}

	return nil
}

func runBoundMutation(
	ctx context.Context,
	mutation *boundMutation,
	runtime mutationRuntime,
	authenticator credential.Provider,
) error {
	preparation := mutation.preparation
	if !preparation.HasTransaction {
		if preparation.Plan.Kind == PlanUnchanged {
			return nil
		}

		return ErrConflictingState
	}

	switch preparation.Transaction.Kind {
	case store.TransactionBootstrap:
		return runBootstrap(ctx, mutation, runtime, authenticator)
	case store.TransactionAdopt:
		return runAdopt(ctx, mutation, runtime)
	case store.TransactionUpgrade:
		if preparation.Transaction.State == store.TransactionDegraded {
			return runRestore(ctx, mutation, runtime)
		}

		return runUpgrade(ctx, mutation, runtime, authenticator)
	default:
		return ErrConflictingState
	}
}

func (service *service) bindMutation(
	ctx context.Context,
	request Request,
	state *store.Store,
) (*boundMutation, error) {
	if state == nil || !validMutationService(service) {
		return nil, ErrInvalidRequest
	}

	lockedService := newService(service.images, service.runtime, state, service.events)

	initial, err := lockedService.Prepare(ctx, request)
	if err != nil {
		return nil, err
	}

	err = ctx.Err()
	if err != nil {
		return nil, fmt.Errorf("bind apply mutation: %w", err)
	}

	lock, err := state.TryLockService( //nolint:contextcheck // Interactive apply fails fast instead of waiting.
		initial.Plan.Project,
		initial.Plan.Service,
	)
	if err != nil {
		return nil, fmt.Errorf("lock apply mutation: %w", err)
	}

	final, err := lockedService.Prepare(ctx, request)
	if err != nil {
		return closeMutationLock(lock, err) //nolint:contextcheck // Lock release must outlive cancellation.
	}

	if !sameMutationScope(initial, final) {
		return closeMutationLock(lock, ErrConflictingState) //nolint:contextcheck // Lock release must outlive cancellation.
	}

	final, err = bindPreparedTransaction(ctx, lock, final)
	if err != nil {
		return closeMutationLock(lock, err) //nolint:contextcheck // Lock release must outlive cancellation.
	}

	return newBoundMutation( //nolint:contextcheck // Lock release must outlive cancellation.
		lock, state, final, request, service.events,
	)
}

func newBoundMutation(
	lock *store.ServiceLock,
	state *store.Store,
	final Preparation,
	request Request,
	events EventSink,
) (*boundMutation, error) {
	root, err := state.BackupRoot()
	if err != nil {
		return closeMutationLock(lock, err)
	}

	mutation := &boundMutation{
		preparation: final,
		lock:        lock,
		backupRoot:  root,
		events:      events,
	}
	if mutationNeedsRepositoryRuntime(final) {
		mutation.materialize = request.Source.MaterializeRuntime
	}

	return mutation, nil
}

func validMutationService(service *service) bool {
	return service != nil && service.images != nil && service.runtime != nil
}

func bindPreparedTransaction(
	ctx context.Context,
	lock *store.ServiceLock,
	preparation Preparation,
) (Preparation, error) {
	if lock == nil {
		return Preparation{}, ErrInvalidRequest
	}

	err := lock.Fence(ctx)
	if err != nil {
		return Preparation{}, fmt.Errorf("fence apply transaction: %w", err)
	}

	if preparation.HasTransaction {
		if !boundRecoveryTransaction(preparation) {
			return Preparation{}, ErrConflictingState
		}

		return preparation, nil
	}

	switch preparation.Plan.Kind {
	case PlanUnchanged:
		return preparation, nil
	case PlanBootstrap, PlanAdopt, PlanUpgrade:
		transaction, err := lock.BeginTransaction(ctx, transactionIntent(preparation))
		if err != nil {
			return Preparation{}, fmt.Errorf("begin apply transaction: %w", err)
		}

		preparation.Transaction = transaction
		preparation.HasTransaction = true

		return preparation, nil
	case PlanResume, PlanProbeUnknownEffect, PlanRestore:
		return Preparation{}, ErrConflictingState
	default:
		return Preparation{}, ErrConflictingState
	}
}

func transactionIntent(preparation Preparation) store.TransactionIntent {
	intent := store.TransactionIntent{
		Runtime:         preparation.Execution.Kind,
		SourceDigest:    preparation.Workload.SourceDigest,
		EffectiveDigest: preparation.Workload.EffectiveDigest,
		ExecutionDigest: preparation.Execution.Digest,
	}

	switch preparation.Plan.Kind {
	case PlanBootstrap:
		intent.Kind = store.TransactionBootstrap
	case PlanAdopt:
		intent.Kind = store.TransactionAdopt
		intent.PredecessorWorkloadID = preparation.Plan.Observation.ID
	case PlanUpgrade:
		intent.Kind = store.TransactionUpgrade
		intent.BaseTransactionID = preparation.Applied.TransactionID
		intent.HasBaseTransaction = preparation.HasApplied
		intent.PredecessorWorkloadID = preparation.Applied.WorkloadID
	case PlanUnchanged, PlanResume, PlanProbeUnknownEffect, PlanRestore:
	}

	return intent
}

func recoveryMutationPlan(kind PlanKind) bool {
	return kind == PlanResume || kind == PlanProbeUnknownEffect || kind == PlanRestore
}

func boundRecoveryTransaction(preparation Preparation) bool {
	return recoveryMutationPlan(preparation.Plan.Kind) &&
		transactionMatches(preparation.Transaction, preparation.Workload, preparation.Execution)
}

// The initial preparation selects only the service-scoped lock. The final
// preparation runs under that lock and remains authoritative when observation
// changes the plan or runtime.
func sameMutationScope(initial, final Preparation) bool {
	return initial.Plan.Project == final.Plan.Project && initial.Plan.Service == final.Plan.Service
}

func closeMutationLock(lock *store.ServiceLock, cause error) (*boundMutation, error) {
	closeErr := lock.Close()
	if closeErr != nil {
		closeErr = fmt.Errorf("close apply mutation: %w", closeErr)
	}

	return nil, errors.Join(cause, closeErr)
}

func (mutation *boundMutation) close() error {
	if mutation == nil || mutation.lock == nil {
		return ErrInvalidRequest
	}

	lock := mutation.lock
	mutation.lock = nil

	err := lock.Close()
	if err != nil {
		return fmt.Errorf("close apply mutation: %w", err)
	}

	return nil
}

func (mutation *boundMutation) effectJournal() *observedEffectJournal {
	if mutation == nil || mutation.lock == nil {
		return nil
	}

	preparation := mutation.preparation

	return &observedEffectJournal{
		EffectJournal: mutation.lock,
		events:        mutation.events,
		context: Event{
			Project: preparation.Plan.Project,
			Service: preparation.Plan.Service,
			Runtime: preparation.Plan.Runtime,
		},
	}
}

func (mutation *boundMutation) publishTransaction(kind EventKind) {
	if mutation == nil {
		return
	}

	preparation := mutation.preparation
	tryPublish(mutation.events, Event{
		Kind:        kind,
		Plan:        preparation.Plan.Kind,
		Project:     preparation.Plan.Project,
		Service:     preparation.Plan.Service,
		Runtime:     preparation.Plan.Runtime,
		Transaction: preparation.Transaction.ID.String(),
	})
}
