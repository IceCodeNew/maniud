package application

import (
	"context"
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

	return nil
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
		preparation.Transaction.State == store.TransactionActive && !preparation.HasApplied &&
		len(preparation.Actions) == 0
}

func adoptPlan(kind PlanKind) bool {
	return kind == PlanAdopt || kind == PlanResume
}

func validAdoptedObservation(observation WorkloadObservation, preparation Preparation) bool {
	return observation.State == WorkloadObservationPresent && observation.ID != "" &&
		observation.ID == preparation.Transaction.PredecessorWorkloadID &&
		observation.ConfigurationDigest != (domain.Digest{}) &&
		observation.ConfigurationDigest == preparation.Plan.Observation.ConfigurationDigest &&
		observation.ConfigurationMatches && observation.Running &&
		observation.Ownership == (domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged})
}
