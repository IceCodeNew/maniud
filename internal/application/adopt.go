package application

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func runAdopt(ctx context.Context, mutation *boundMutation, runtime Runtime) error {
	if mutation == nil || runtime == nil || !validAdoptMutation(mutation) {
		return ErrInvalidRequest
	}

	preparation := mutation.preparation
	observation, err := runtime.ObserveWorkload(ctx, preparation.Workload)
	if err != nil {
		return fmt.Errorf("prove adopted workload: %w", err)
	}

	if !validAdoptedObservation(observation, preparation) {
		return ErrConflictingState
	}
	err = requireWorkloadConvergence(
		activeHealthcheck(preparation.Workload),
		observation.Lifecycle,
		observation.Health,
	)
	if errors.Is(err, ErrHealthPending) {
		return err
	}
	if err != nil {
		return settleAdoptionHealthDegraded(ctx, mutation, err)
	}

	applied, err := mutation.lock.CommitAppliedService(
		ctx,
		preparation.Transaction.ID,
		store.AppliedServiceIntent{
			WorkloadID:             observation.ID,
			ConfigurationDigest:    observation.ConfigurationDigest,
			StorageDigest:          observation.StorageDigest,
			ReferenceDigest:        preparation.Workload.Image.ReferenceDigest,
			PlatformManifestDigest: preparation.Workload.Image.PlatformManifest,
			ImageConfigDigest:      preparation.Workload.Image.ImageConfig,
			Healthcheck:            activeHealthcheck(preparation.Workload),
		},
	)
	if err != nil {
		return fmt.Errorf("complete adopt transaction: %w", err)
	}

	completed := preparation.Transaction
	completed.State = store.TransactionSucceeded
	mutation.preparation.Transaction = completed
	mutation.preparation.Plan.Observation = observation
	mutation.preparation.Applied = applied
	mutation.preparation.HasApplied = true
	mutation.publishTransaction(EventTransactionSucceeded)

	return nil
}

func settleAdoptionHealthDegraded(ctx context.Context, mutation *boundMutation, cause error) error {
	if mutation.preparation.Transaction.State == store.TransactionHealthDegraded {
		return cause
	}

	transaction, err := mutation.lock.SetTransactionState(
		context.WithoutCancel(ctx),
		mutation.preparation.Transaction.ID,
		store.TransactionHealthDegraded,
	)
	if err != nil {
		return errors.Join(cause, fmt.Errorf("record degraded adoption health: %w", err))
	}
	mutation.preparation.Transaction = transaction
	mutation.publishTransaction(EventTransactionDegraded)

	return cause
}

func validAdoptMutation(mutation *boundMutation) bool {
	if mutation == nil || mutation.lock == nil {
		return false
	}

	preparation := mutation.preparation
	if !activeAdoptTransaction(preparation) || !adoptPlan(preparation.Plan.Kind) {
		return false
	}

	return preparation.Transaction.PredecessorWorkloadID == preparation.Plan.Observation.ID &&
		validAdoptedObservation(preparation.Plan.Observation, preparation)
}

func activeAdoptTransaction(preparation Preparation) bool {
	return preparation.HasTransaction && preparation.Transaction.Kind == store.TransactionAdopt &&
		(preparation.Transaction.State == store.TransactionActive ||
			preparation.Transaction.State == store.TransactionHealthDegraded) && !preparation.HasApplied &&
		len(preparation.Actions) == 0
}

func adoptPlan(kind PlanKind) bool {
	return kind == PlanAdopt || kind == PlanResume || kind == PlanHealthDegraded
}

func validAdoptedObservation(observation WorkloadObservation, preparation Preparation) bool {
	return observation.State == WorkloadObservationPresent && observation.ID != "" &&
		observation.ID == preparation.Transaction.PredecessorWorkloadID &&
		observation.ConfigurationDigest != (domain.Digest{}) &&
		observation.ConfigurationDigest == preparation.Plan.Observation.ConfigurationDigest &&
		observation.StorageDigest == preparation.Plan.Observation.StorageDigest &&
		workloadStorageMatches(observation.StorageDigest, observation.RuntimeMounts, preparation.Workload) &&
		observation.ConfigurationMatches && observation.Lifecycle == WorkloadLifecycleRunning &&
		observation.Ownership == (domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged})
}
