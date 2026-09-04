package application

import (
	"context"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const workloadHealthStopActionKind = "workload.health-stop"

type workloadHealthStopRuntime interface {
	WorkloadStartRuntime
	WorkloadTransitionRuntime
}

type workloadHealthStopEffect struct {
	runtime     workloadHealthStopRuntime
	workload    domain.DesiredWorkload
	transaction string
}

func runWorkloadHealthStop(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	workload domain.DesiredWorkload,
	runtime workloadHealthStopRuntime,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	if runtime == nil || sequence <= 0 || identifier == (store.TransactionID{}) ||
		!validWorkloadEffect(workload) {
		return empty, ErrInvalidRequest
	}

	transaction := identifier.String()
	effect := &workloadHealthStopEffect{
		runtime: runtime, workload: workload, transaction: transaction,
	}

	return runRuntimeEffect(
		ctx,
		journal,
		identifier,
		workloadHealthStopIntent(sequence, workload, transaction),
		effect,
	)
}

func workloadHealthStopIntent(
	sequence int64,
	workload domain.DesiredWorkload,
	transaction string,
) store.ActionIntent {
	return workloadIntent(sequence, workloadHealthStopActionKind, workload, transaction)
}

//nolint:cyclop // The closed lifecycle switch is the fail-closed stop effect contract.
func (effect *workloadHealthStopEffect) Apply(ctx context.Context) error {
	probe, err := effect.runtime.ProbeStartedWorkload(ctx, effect.workload, effect.transaction)
	if err != nil {
		return fmt.Errorf("probe health rollback workload: %w", err)
	}
	if probe.State == WorkloadEffectProbeMissing {
		if !workloadEffectEvidenceEmpty(probe.Workload) {
			return ErrConflictingState
		}

		return nil
	}
	if probe.State != WorkloadEffectProbeObserved ||
		!healthRollbackWorkloadMatches(probe.Workload, effect.workload, effect.transaction) {
		return ErrConflictingState
	}

	switch probe.Workload.Lifecycle {
	case WorkloadLifecycleCreated, WorkloadLifecycleExited:
		return nil
	case WorkloadLifecycleRunning:
		transition := healthRollbackStopTransition(probe.Workload, effect.workload.ContainerName)
		if err = effect.runtime.ApplyWorkloadTransition(ctx, transition); err != nil {
			return fmt.Errorf("stop health rollback workload: %w", err)
		}

		return nil
	case WorkloadLifecycleRestarting:
		return ErrHealthPending
	case WorkloadLifecycleUnknown, WorkloadLifecyclePaused, WorkloadLifecycleRemoving, WorkloadLifecycleDead:
		return ErrConflictingState
	default:
		return ErrConflictingState
	}
}

//nolint:cyclop // The closed probe and lifecycle switches reject every untrusted runtime state.
func (effect *workloadHealthStopEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition
	probe, err := effect.runtime.ProbeStartedWorkload(ctx, effect.workload, effect.transaction)
	if err != nil {
		return empty, fmt.Errorf("probe stopped health rollback workload: %w", err)
	}

	switch probe.State {
	case WorkloadEffectProbeMissing:
		if !workloadEffectEvidenceEmpty(probe.Workload) {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest: workloadEffectDigest(
				workloadEffectMissing,
				workloadHealthStopActionKind,
				effect.workload,
				effect.transaction,
				"",
			),
			Satisfied: true,
		}, nil
	case WorkloadEffectProbeObserved:
		if !healthRollbackWorkloadMatches(probe.Workload, effect.workload, effect.transaction) {
			return empty, ErrConflictingState
		}

		switch probe.Workload.Lifecycle {
		case WorkloadLifecycleCreated, WorkloadLifecycleExited:
			return healthStopPostcondition(effect, probe.Workload, true), nil
		case WorkloadLifecycleRunning:
			return healthStopPostcondition(effect, probe.Workload, false), nil
		case WorkloadLifecycleRestarting:
			return empty, ErrHealthPending
		case WorkloadLifecycleUnknown, WorkloadLifecyclePaused, WorkloadLifecycleRemoving, WorkloadLifecycleDead:
			return empty, ErrConflictingState
		default:
			return empty, ErrConflictingState
		}
	case WorkloadEffectProbeUnknown:
		return empty, ErrConflictingState
	default:
		return empty, ErrConflictingState
	}
}

func healthStopPostcondition(
	effect *workloadHealthStopEffect,
	evidence WorkloadEffectEvidence,
	satisfied bool,
) EffectPostcondition {
	state := byte(workloadEffectObserved)
	if satisfied {
		state = workloadEffectStopped
	}

	return EffectPostcondition{
		Digest: workloadObservedEffectDigest(
			state,
			workloadHealthStopActionKind,
			effect.workload,
			effect.transaction,
			evidence.ID,
			evidence.StorageDigest,
		),
		Satisfied: satisfied,
	}
}

func healthRollbackStopTransition(
	evidence WorkloadEffectEvidence,
	name string,
) WorkloadTransition {
	before := ExistingWorkload{
		ID: evidence.ID, Name: name, ConfigurationDigest: evidence.ConfigurationDigest,
		Lifecycle: WorkloadLifecycleRunning, Ownership: evidence.Ownership,
	}
	after := before
	after.Lifecycle = WorkloadLifecycleExited

	return WorkloadTransition{Kind: WorkloadTransitionStop, Before: before, After: after}
}

func healthRollbackWorkloadMatches(
	evidence WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
) bool {
	expectedOwnership := domain.WorkloadOwnership{
		Status:           domain.OwnershipManaged,
		Service:          workload.ServiceName,
		Transaction:      transaction,
		DesiredState:     workload.EffectiveDigest,
		Reference:        workload.Image.ReferenceDigest,
		ImageConfig:      workload.Image.ImageConfig,
		PlatformManifest: workload.Image.PlatformManifest,
	}

	return evidence.ID != "" && evidence.Name == workload.ContainerName &&
		evidence.ConfigurationDigest != (domain.Digest{}) &&
		workloadStorageMatches(evidence.StorageDigest, evidence.RuntimeMounts, workload) &&
		evidence.ConfigurationMatches && evidence.Ownership == expectedOwnership
}
