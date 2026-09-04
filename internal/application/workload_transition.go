package application

import (
	"context"
	"encoding/binary"
	"fmt"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	workloadStopActionKind         = "workload.stop"
	workloadRenameActionKind       = "workload.rename"
	workloadRemoveActionKind       = "workload.remove"
	workloadRestoreStartActionKind = "workload.restore-start"
	workloadTransitionDigestFormat = 1
	workloadTransitionBefore       = 0
	workloadTransitionAfter        = 1
)

// WorkloadTransitionKind identifies one exact existing-workload operation.
type WorkloadTransitionKind uint8

const (
	// WorkloadTransitionUnknown is the invalid zero value.
	WorkloadTransitionUnknown WorkloadTransitionKind = iota
	// WorkloadTransitionStop changes a running workload to exited.
	WorkloadTransitionStop
	// WorkloadTransitionRename changes only the runtime name of an exited workload.
	WorkloadTransitionRename
	// WorkloadTransitionRemove deletes an exited workload.
	WorkloadTransitionRemove
	// WorkloadTransitionRestoreStart restarts an exited predecessor during recovery.
	WorkloadTransitionRestoreStart
)

// ExistingWorkload identifies one exact runtime object before or after a
// lifecycle transition.
type ExistingWorkload struct {
	ID                  string
	Name                string
	ConfigurationDigest domain.Digest
	Lifecycle           WorkloadLifecycle
	Ownership           domain.WorkloadOwnership
}

// WorkloadTransition contains the complete typed precondition and
// postcondition for one runtime operation. Remove has an empty After value.
type WorkloadTransition struct {
	Kind   WorkloadTransitionKind
	Before ExistingWorkload
	After  ExistingWorkload
}

// Valid reports whether the transition expresses one supported exact state
// change with complete identity evidence.
func (transition WorkloadTransition) Valid() bool {
	return validWorkloadTransition(transition)
}

// WorkloadTransitionRuntime applies and independently probes exact existing
// workload transitions.
type WorkloadTransitionRuntime interface {
	ApplyWorkloadTransition(ctx context.Context, transition WorkloadTransition) error
	ProbeWorkloadTransition(ctx context.Context, transition WorkloadTransition) (WorkloadTransitionProbe, error)
}

type incompleteWorkloadTransitionRuntime interface {
	ResumeIncompleteWorkloadTransition(ctx context.Context, transition WorkloadTransition) error
}

// WorkloadTransitionProbe is either a proven missing postcondition or one
// exact observed workload.
type WorkloadTransitionProbe struct {
	State    WorkloadEffectProbeState
	Workload ExistingWorkload
	Health   WorkloadHealth
}

type workloadTransitionEffect struct {
	runtime      WorkloadTransitionRuntime
	transition   WorkloadTransition
	resumeNeeded bool
}

func runWorkloadTransition(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	transition WorkloadTransition,
	runtime WorkloadTransitionRuntime,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	if runtime == nil || sequence <= 0 || identifier == (store.TransactionID{}) ||
		!validWorkloadTransition(transition) {
		return empty, ErrInvalidRequest
	}

	intent := workloadTransitionIntent(sequence, transition)
	effect := &workloadTransitionEffect{runtime: runtime, transition: transition}

	return runRuntimeEffect(ctx, journal, identifier, intent, effect)
}

func workloadTransitionIntent(sequence int64, transition WorkloadTransition) store.ActionIntent {
	return store.ActionIntent{
		Sequence:     sequence,
		Kind:         workloadTransitionActionKind(transition.Kind),
		IntentDigest: workloadTransitionDigest(workloadTransitionBefore, transition, transition.Before),
	}
}

func (effect *workloadTransitionEffect) Apply(ctx context.Context) error {
	err := effect.runtime.ApplyWorkloadTransition(ctx, effect.transition)
	if err != nil {
		return fmt.Errorf("apply workload transition: %w", err)
	}

	return nil
}

func (effect *workloadTransitionEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition
	effect.resumeNeeded = false

	probe, err := effect.runtime.ProbeWorkloadTransition(ctx, effect.transition)
	if err != nil {
		return empty, fmt.Errorf("probe workload transition: %w", err)
	}

	if probe.State == WorkloadEffectProbeMissing {
		if probe.Workload != (ExistingWorkload{}) || effect.transition.Kind != WorkloadTransitionRemove {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest: workloadTransitionDigest(
				workloadTransitionAfter,
				effect.transition,
				ExistingWorkload{},
			),
			Satisfied: true,
		}, nil
	}
	if probe.State != WorkloadEffectProbeObserved {
		return empty, ErrConflictingState
	}
	postcondition, incomplete, valid := observedWorkloadTransition(effect.transition, probe.Workload)
	if !valid {
		return empty, ErrConflictingState
	}
	effect.resumeNeeded = incomplete

	return postcondition, nil
}

func (effect *workloadTransitionEffect) incomplete() bool {
	return effect.resumeNeeded
}

func (effect *workloadTransitionEffect) resumeIncomplete(ctx context.Context) error {
	runtime, ok := effect.runtime.(incompleteWorkloadTransitionRuntime)
	if !ok {
		return ErrConflictingState
	}
	if err := runtime.ResumeIncompleteWorkloadTransition(ctx, effect.transition); err != nil {
		return fmt.Errorf("resume incomplete workload transition: %w", err)
	}

	return nil
}

func observedWorkloadTransition(
	transition WorkloadTransition,
	workload ExistingWorkload,
) (EffectPostcondition, bool, bool) {
	removing := transition.Before
	removing.Lifecycle = WorkloadLifecycleRemoving
	if transition.Kind == WorkloadTransitionRemove && workload == removing {
		return workloadTransitionPostcondition(transition, false), true, true
	}
	if workload != transition.Before && workload != transition.After {
		return EffectPostcondition{}, false, false
	}

	satisfied := transition.Kind != WorkloadTransitionRemove && workload == transition.After
	state := byte(workloadTransitionBefore)
	if satisfied {
		state = workloadTransitionAfter
	}

	return EffectPostcondition{
		Digest:    workloadTransitionDigest(state, transition, workload),
		Satisfied: satisfied,
	}, false, true
}

func validWorkloadTransition(transition WorkloadTransition) bool {
	if !validExistingWorkload(transition.Before) {
		return false
	}

	switch transition.Kind {
	case WorkloadTransitionStop:
		return validWorkloadStop(transition.Before, transition.After)
	case WorkloadTransitionRename:
		return validWorkloadRename(transition.Before, transition.After)
	case WorkloadTransitionRemove:
		return validWorkloadRemove(transition.Before, transition.After)
	case WorkloadTransitionRestoreStart:
		return validWorkloadRestoreStart(transition.Before, transition.After)
	case WorkloadTransitionUnknown:
	}

	return false
}

func validWorkloadStop(before, after ExistingWorkload) bool {
	return sameExistingWorkloadIdentity(before, after) &&
		before.Lifecycle == WorkloadLifecycleRunning && after.Lifecycle == WorkloadLifecycleExited
}

func validWorkloadRename(before, after ExistingWorkload) bool {
	return before.Name != after.Name && sameExistingWorkloadExceptName(before, after) &&
		before.Lifecycle == WorkloadLifecycleExited
}

func validWorkloadRemove(before, after ExistingWorkload) bool {
	return after == (ExistingWorkload{}) && before.Lifecycle == WorkloadLifecycleExited
}

func validWorkloadRestoreStart(before, after ExistingWorkload) bool {
	return sameExistingWorkloadIdentity(before, after) &&
		before.Lifecycle == WorkloadLifecycleExited && after.Lifecycle == WorkloadLifecycleRunning
}

func sameExistingWorkloadIdentity(left, right ExistingWorkload) bool {
	return left.ID == right.ID && left.Name == right.Name &&
		left.ConfigurationDigest == right.ConfigurationDigest && left.Ownership == right.Ownership
}

func sameExistingWorkloadExceptName(left, right ExistingWorkload) bool {
	return left.ID == right.ID && left.ConfigurationDigest == right.ConfigurationDigest &&
		left.Lifecycle == right.Lifecycle && left.Ownership == right.Ownership
}

func validExistingWorkload(workload ExistingWorkload) bool {
	return validWorkloadTransitionString(workload.ID) && validWorkloadTransitionString(workload.Name) &&
		workload.ConfigurationDigest != (domain.Digest{}) &&
		(workload.Lifecycle == WorkloadLifecycleCreated || workload.Lifecycle == WorkloadLifecycleRunning ||
			workload.Lifecycle == WorkloadLifecycleExited) && validTransitionOwnership(workload.Ownership)
}

func validWorkloadLifecycle(lifecycle WorkloadLifecycle) bool {
	return lifecycle >= WorkloadLifecycleCreated && lifecycle <= WorkloadLifecycleDead
}

func validTransitionOwnership(ownership domain.WorkloadOwnership) bool {
	if ownership.Status == domain.OwnershipUnmanaged {
		return ownership == (domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged})
	}

	return ownership.Status == domain.OwnershipManaged && ownership.Service != "" &&
		ownership.Transaction != "" && ownership.DesiredState != (domain.Digest{}) &&
		ownership.Reference != (domain.Digest{}) && ownership.ImageConfig != (domain.Digest{}) &&
		ownership.PlatformManifest != (domain.Digest{})
}

func validWorkloadTransitionString(value string) bool {
	return value != "" && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func workloadTransitionActionKind(kind WorkloadTransitionKind) string {
	switch kind {
	case WorkloadTransitionStop:
		return workloadStopActionKind
	case WorkloadTransitionRename:
		return workloadRenameActionKind
	case WorkloadTransitionRemove:
		return workloadRemoveActionKind
	case WorkloadTransitionRestoreStart:
		return workloadRestoreStartActionKind
	case WorkloadTransitionUnknown:
	}

	return ""
}

func workloadTransitionDigest(
	state byte,
	transition WorkloadTransition,
	observed ExistingWorkload,
) domain.Digest {
	value := []byte{workloadTransitionDigestFormat, state, byte(transition.Kind)}
	value = appendExistingWorkload(value, transition.Before)
	value = appendExistingWorkload(value, transition.After)
	value = appendExistingWorkload(value, observed)

	return domain.Hash(value)
}

func appendExistingWorkload(encoded []byte, workload ExistingWorkload) []byte {
	encoded = appendWorkloadEffectString(encoded, workload.ID)
	encoded = appendWorkloadEffectString(encoded, workload.Name)
	encoded = append(encoded, workload.ConfigurationDigest[:]...)
	encoded = binary.AppendUvarint(encoded, uint64(workload.Lifecycle))
	encoded = binary.AppendUvarint(encoded, uint64(workload.Ownership.Status))
	encoded = appendWorkloadEffectString(encoded, workload.Ownership.Service)
	encoded = appendWorkloadEffectString(encoded, workload.Ownership.Transaction)
	encoded = append(encoded, workload.Ownership.DesiredState[:]...)
	encoded = append(encoded, workload.Ownership.Reference[:]...)
	encoded = append(encoded, workload.Ownership.ImageConfig[:]...)
	encoded = append(encoded, workload.Ownership.PlatformManifest[:]...)

	return encoded
}
