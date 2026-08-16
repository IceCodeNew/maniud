package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/store"
)

type boundMutation struct {
	preparation Preparation
	lock        *store.ServiceLock
}

func (service *Service) bindMutation(
	ctx context.Context,
	request Request,
	state *store.Store,
) (*boundMutation, error) {
	if state == nil || !validMutationService(service) {
		return nil, ErrInvalidRequest
	}

	lockedService := NewService(service.images, service.runtime, state)

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

	return &boundMutation{preparation: final, lock: lock}, nil
}

func validMutationService(service *Service) bool {
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
	return store.TransactionIntent{
		Runtime:         preparation.Execution.Kind,
		SourceDigest:    preparation.Workload.SourceDigest,
		EffectiveDigest: preparation.Workload.EffectiveDigest,
		ExecutionDigest: preparation.Execution.Digest,
	}
}

func recoveryMutationPlan(kind PlanKind) bool {
	return kind == PlanResume || kind == PlanProbeUnknownEffect || kind == PlanRestore
}

func boundRecoveryTransaction(preparation Preparation) bool {
	return recoveryMutationPlan(preparation.Plan.Kind) &&
		transactionMatches(preparation.Transaction, preparation.Workload, preparation.Execution)
}

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
