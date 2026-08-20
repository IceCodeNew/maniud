package application

import (
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

// PlanKind is the next transaction-owned apply journey.
type PlanKind string

const (
	// PlanBootstrap deploys a service whose exact runtime name is absent.
	PlanBootstrap PlanKind = "bootstrap"
	// PlanAdopt records an exact, running, unmanaged workload before changing it.
	PlanAdopt PlanKind = "adopt"
	// PlanUnchanged retains a running managed workload at the exact desired state.
	PlanUnchanged PlanKind = "unchanged"
	// PlanUpgrade replaces a running managed workload with a changed desired state.
	PlanUpgrade PlanKind = "upgrade"
	// PlanResume continues an active transaction that has no unknown effect.
	PlanResume PlanKind = "resume"
	// PlanProbeUnknownEffect permits only the pending action's typed probe.
	PlanProbeUnknownEffect PlanKind = "probe-unknown-effect"
	// PlanRestore recovers the previous workload for a degraded transaction.
	PlanRestore PlanKind = "restore"
)

// RuntimeEvidence binds one operation to a runtime, platform, and opaque
// execution identity.
type RuntimeEvidence struct {
	Kind     domain.RuntimeKind
	Platform domain.Platform
	Digest   domain.Digest
}

// WorkloadObservationState separates proven absence and an observed object
// from the invalid zero value.
type WorkloadObservationState uint8

const (
	// WorkloadObservationUnknown is valid only alongside an adapter error.
	WorkloadObservationUnknown WorkloadObservationState = iota
	// WorkloadObservationMissing proves the exact runtime name is absent.
	WorkloadObservationMissing
	// WorkloadObservationPresent carries one strictly decoded runtime snapshot.
	WorkloadObservationPresent
)

// WorkloadObservation is read-only evidence for one desired workload.
type WorkloadObservation struct {
	ID                   string
	State                WorkloadObservationState
	ConfigurationDigest  domain.Digest
	StorageDigest        domain.Digest
	RuntimeMounts        []domain.RuntimeMount
	ConfigurationMatches bool
	Running              bool
	Ownership            domain.WorkloadOwnership
}

// Plan is the bounded public result of apply preparation.
type Plan struct {
	Kind        PlanKind
	Project     string
	Service     string
	Runtime     domain.RuntimeKind
	Platform    domain.Platform
	Image       domain.ImageIdentity
	Source      domain.Digest
	Desired     domain.Digest
	Observation WorkloadObservation
}

// Preparation retains private evidence needed by a later transaction mutation.
// DryRun returns only its Plan and performs no mutation.
type Preparation struct {
	Plan           Plan
	Workload       domain.DesiredWorkload
	Execution      RuntimeEvidence
	Transaction    store.Transaction
	HasTransaction bool
	Applied        store.AppliedService
	HasApplied     bool
	Actions        []store.Action
}

func classifyNewApply(
	observation WorkloadObservation,
	workload domain.DesiredWorkload,
	execution RuntimeEvidence,
	applied store.AppliedService,
	hasApplied bool,
) (PlanKind, error) {
	if hasApplied {
		return classifyAppliedWorkload(observation, workload, execution, applied)
	}

	switch observation.State {
	case WorkloadObservationUnknown:
		return "", ErrConflictingState
	case WorkloadObservationMissing:
		return PlanBootstrap, nil
	case WorkloadObservationPresent:
		return classifyUnappliedWorkload(observation)
	default:
		return "", ErrConflictingState
	}
}

func classifyUnappliedWorkload(observation WorkloadObservation) (PlanKind, error) {
	if !observation.Running {
		return "", ErrConflictingState
	}

	switch observation.Ownership.Status {
	case domain.OwnershipConflicting:
		return "", ErrConflictingState
	case domain.OwnershipUnmanaged:
		if observation.ConfigurationMatches {
			return PlanAdopt, nil
		}

		return "", ErrConflictingState
	case domain.OwnershipManaged:
		return "", ErrConflictingState
	default:
		return "", ErrConflictingState
	}
}

func classifyAppliedWorkload(
	observation WorkloadObservation,
	workload domain.DesiredWorkload,
	execution RuntimeEvidence,
	applied store.AppliedService,
) (PlanKind, error) {
	if observation.State != WorkloadObservationPresent || !observation.Running ||
		!observationMatchesApplied(observation, workload.ServiceName, applied) ||
		!appliedMatchesExecution(applied, execution) {
		return "", ErrConflictingState
	}

	if observation.ConfigurationMatches && appliedMatchesDesired(applied, workload) {
		return PlanUnchanged, nil
	}

	return PlanUpgrade, nil
}

func appliedMatchesExecution(applied store.AppliedService, execution RuntimeEvidence) bool {
	return applied.Runtime == execution.Kind && applied.ExecutionDigest == execution.Digest
}

func appliedMatchesDesired(applied store.AppliedService, workload domain.DesiredWorkload) bool {
	return applied.SourceDigest == workload.SourceDigest &&
		applied.EffectiveDigest == workload.EffectiveDigest &&
		applied.ReferenceDigest == workload.Image.ReferenceDigest &&
		applied.PlatformManifestDigest == workload.Image.PlatformManifest &&
		applied.ImageConfigDigest == workload.Image.ImageConfig
}

func observationMatchesApplied(
	observation WorkloadObservation,
	service string,
	applied store.AppliedService,
) bool {
	if !observationIdentityMatchesApplied(observation, applied) {
		return false
	}

	if applied.Kind == store.TransactionAdopt {
		return observation.Ownership == (domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged})
	}

	return observation.Ownership.Status == domain.OwnershipManaged &&
		observation.Ownership.Service == service &&
		observation.Ownership.Transaction == applied.TransactionID.String() &&
		observation.Ownership.DesiredState == applied.EffectiveDigest &&
		observation.Ownership.Reference == applied.ReferenceDigest &&
		observation.Ownership.ImageConfig == applied.ImageConfigDigest &&
		observation.Ownership.PlatformManifest == applied.PlatformManifestDigest
}

func observationIdentityMatchesApplied(
	observation WorkloadObservation,
	applied store.AppliedService,
) bool {
	return observation.ID == applied.WorkloadID &&
		observation.ConfigurationDigest == applied.ConfigurationDigest &&
		observation.StorageDigest == applied.StorageDigest
}

func classifyRecovery(transaction store.Transaction, actions []store.Action) (PlanKind, error) {
	pending, err := recoveryPendingAction(transaction, actions)
	if err != nil {
		return "", err
	}

	switch transaction.State {
	case store.TransactionActive:
		return classifyActiveRecovery(pending)
	case store.TransactionDegraded:
		if pending != nil && !validRestorePendingAction(actions, *pending) {
			return "", ErrConflictingState
		}

		return PlanRestore, nil
	case store.TransactionFailed, store.TransactionSucceeded:
		return "", ErrConflictingState
	default:
		return "", ErrConflictingState
	}
}

func validRestorePendingAction(actions []store.Action, pending store.Action) bool {
	switch pending.Kind {
	case workloadDiscardActionKind, workloadRestoreStartActionKind:
		return true
	case workloadRenameActionKind:
		for _, action := range actions {
			if action.Kind == workloadDiscardActionKind && action.State == store.ActionStateCompleted {
				return true
			}
		}

		return false
	default:
		return false
	}
}

func recoveryPendingAction(transaction store.Transaction, actions []store.Action) (*store.Action, error) {
	var pending *store.Action

	for index, action := range actions {
		if action.TransactionID != transaction.ID || action.Sequence != int64(index+1) ||
			!validActionState(action) || pending != nil {
			return nil, ErrConflictingState
		}

		if action.State == store.ActionStateCompleted {
			continue
		}

		current := action
		pending = &current
	}

	return pending, nil
}

func classifyActiveRecovery(pending *store.Action) (PlanKind, error) {
	if pending == nil {
		return PlanResume, nil
	}

	switch pending.State {
	case store.ActionStateIntent:
		return PlanResume, nil
	case store.ActionStateEffectOutcomeUnknown:
		return PlanProbeUnknownEffect, nil
	case store.ActionStateCompleted:
		return "", ErrConflictingState
	default:
		return "", ErrConflictingState
	}
}

func validActionState(action store.Action) bool {
	switch action.State {
	case store.ActionStateIntent, store.ActionStateEffectOutcomeUnknown:
		return action.PostconditionDigest == nil
	case store.ActionStateCompleted:
		return action.PostconditionDigest != nil
	default:
		return false
	}
}
