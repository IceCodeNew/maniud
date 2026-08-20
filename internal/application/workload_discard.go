package application

import (
	"context"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const workloadDiscardActionKind = "workload.discard"

// WorkloadDiscardRuntime removes and independently proves absence of the
// exact workload selected by desired name and transaction ownership.
type WorkloadDiscardRuntime interface {
	DiscardWorkload(ctx context.Context, workload domain.DesiredWorkload, transaction string) error
	ProbeDiscardedWorkload(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
	) (WorkloadEffectProbe, error)
}

type workloadDiscardEffect struct {
	runtime     WorkloadDiscardRuntime
	workload    domain.DesiredWorkload
	transaction string
}

func runWorkloadDiscard(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	workload domain.DesiredWorkload,
	runtime WorkloadDiscardRuntime,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	if runtime == nil || sequence <= 0 || identifier == (store.TransactionID{}) ||
		!validWorkloadEffect(workload) {
		return empty, ErrInvalidRequest
	}

	transaction := identifier.String()
	intent := workloadDiscardIntent(sequence, workload, transaction)
	effect := workloadDiscardEffect{runtime: runtime, workload: workload, transaction: transaction}

	return runRuntimeEffect(ctx, journal, identifier, intent, effect)
}

func workloadDiscardIntent(
	sequence int64,
	workload domain.DesiredWorkload,
	transaction string,
) store.ActionIntent {
	return workloadIntent(sequence, workloadDiscardActionKind, workload, transaction)
}

func (effect workloadDiscardEffect) Apply(ctx context.Context) error {
	err := effect.runtime.DiscardWorkload(ctx, effect.workload, effect.transaction)
	if err != nil {
		return fmt.Errorf("discard runtime workload: %w", err)
	}

	return nil
}

func (effect workloadDiscardEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition

	probe, err := effect.runtime.ProbeDiscardedWorkload(ctx, effect.workload, effect.transaction)
	if err != nil {
		return empty, fmt.Errorf("probe discarded runtime workload: %w", err)
	}

	switch probe.State {
	case WorkloadEffectProbeMissing:
		if !workloadEffectEvidenceEmpty(probe.Workload) {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest: workloadEffectDigest(
				workloadEffectMissing,
				workloadDiscardActionKind,
				effect.workload,
				effect.transaction,
				"",
			),
			Satisfied: true,
		}, nil
	case WorkloadEffectProbeObserved:
		if !discardWorkloadMatches(probe.Workload, effect.workload, effect.transaction) {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest: workloadObservedEffectDigest(
				workloadEffectObserved,
				workloadDiscardActionKind,
				effect.workload,
				effect.transaction,
				probe.Workload.ID,
				probe.Workload.StorageDigest,
			),
			Satisfied: false,
		}, nil
	case WorkloadEffectProbeUnknown:
		return empty, ErrConflictingState
	default:
		return empty, ErrConflictingState
	}
}

func discardWorkloadMatches(
	evidence WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
) bool {
	if !evidence.ConfigurationMatches || !discardLifecycle(evidence.Lifecycle) {
		return false
	}

	return evidence.ID != "" && evidence.Name == workload.ContainerName &&
		evidence.ConfigurationDigest != (domain.Digest{}) &&
		workloadStorageMatches(evidence.StorageDigest, evidence.RuntimeMounts, workload) &&
		evidence.Ownership.Matches(
			workload.ServiceName,
			transaction,
			workload.EffectiveDigest,
			workload.Image.ReferenceDigest,
		) && evidence.Ownership.ImageConfig == workload.Image.ImageConfig &&
		evidence.Ownership.PlatformManifest == workload.Image.PlatformManifest
}

func discardLifecycle(lifecycle WorkloadLifecycle) bool {
	return lifecycle == WorkloadLifecycleCreated || lifecycle == WorkloadLifecycleRunning ||
		lifecycle == WorkloadLifecycleExited
}
