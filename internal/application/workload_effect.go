package application

import (
	"context"
	"encoding/binary"
	"fmt"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	workloadCreateActionKind   = "workload.create"
	workloadStartActionKind    = "workload.start"
	workloadEffectDigestFormat = 1
	workloadEffectIntent       = 0
	workloadEffectObserved     = 1
	workloadEffectMissing      = 2
	workloadEffectUnstarted    = 3
	workloadEffectStopped      = 4
	workloadEffectNilSlice     = 0
	workloadEffectNonNilSlice  = 1
)

// WorkloadEffectProbeState separates proven absence and observation from an
// unknown zero value.
type WorkloadEffectProbeState uint8

const (
	// WorkloadEffectProbeUnknown is valid only alongside an adapter error.
	WorkloadEffectProbeUnknown WorkloadEffectProbeState = iota
	// WorkloadEffectProbeMissing proves both the desired name and transaction
	// ownership selectors are absent.
	WorkloadEffectProbeMissing
	// WorkloadEffectProbeObserved carries one strictly decoded runtime object.
	WorkloadEffectProbeObserved
)

// WorkloadLifecycle is the runtime-neutral lifecycle of an observed workload.
type WorkloadLifecycle uint8

const (
	// WorkloadLifecycleUnknown is the fail-closed zero value.
	WorkloadLifecycleUnknown WorkloadLifecycle = iota
	// WorkloadLifecycleCreated has never started.
	WorkloadLifecycleCreated
	// WorkloadLifecycleRunning is executing normally.
	WorkloadLifecycleRunning
	// WorkloadLifecyclePaused has running processes paused.
	WorkloadLifecyclePaused
	// WorkloadLifecycleRestarting is between an exit and restart attempt.
	WorkloadLifecycleRestarting
	// WorkloadLifecycleRemoving is being deleted.
	WorkloadLifecycleRemoving
	// WorkloadLifecycleExited has stopped after starting.
	WorkloadLifecycleExited
	// WorkloadLifecycleDead cannot be started without removal.
	WorkloadLifecycleDead
)

// WorkloadEffectEvidence is runtime-neutral evidence for one create
// postcondition. ConfigurationMatches covers every supported desired runtime
// field, including the disabled restart policy used before first start.
type WorkloadEffectEvidence struct {
	ID                   string
	Name                 string
	ConfigurationDigest  domain.Digest
	StorageDigest        domain.Digest
	RuntimeMounts        []domain.RuntimeMount
	ConfigurationMatches bool
	Lifecycle            WorkloadLifecycle
	Health               WorkloadHealth
	Ownership            domain.WorkloadOwnership
}

// WorkloadEffectProbe is one read-only workload conclusion.
type WorkloadEffectProbe struct {
	State    WorkloadEffectProbeState
	Workload WorkloadEffectEvidence
}

// WorkloadCreateOptions are create-only runtime instructions. They are not
// part of desired state or configuration digest.
type WorkloadCreateOptions struct {
	CopyImageVolumes bool
}

func defaultWorkloadCreateOptions() WorkloadCreateOptions {
	return WorkloadCreateOptions{CopyImageVolumes: true}
}

// WorkloadEffectRuntime creates and independently observes transaction-owned
// workloads. A create response ID is only a probe constraint, not completion
// evidence.
type WorkloadEffectRuntime interface {
	CreateWorkload(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
		options WorkloadCreateOptions,
	) (string, error)
	ProbeCreatedWorkload(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
		responseID string,
	) (WorkloadEffectProbe, error)
}

// WorkloadStartRuntime starts and independently observes one exact
// transaction-owned workload. Implementations must select the workload by
// both desired identity and transaction ownership before starting it.
type WorkloadStartRuntime interface {
	StartWorkload(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
	) error
	ProbeStartedWorkload(
		ctx context.Context,
		workload domain.DesiredWorkload,
		transaction string,
	) (WorkloadEffectProbe, error)
}

type workloadCreateEffect struct {
	runtime     WorkloadEffectRuntime
	workload    domain.DesiredWorkload
	transaction string
	options     WorkloadCreateOptions
	responseID  string
}

type workloadStartEffect struct {
	runtime     WorkloadStartRuntime
	workload    domain.DesiredWorkload
	transaction string
}

func runWorkloadCreate(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	workload domain.DesiredWorkload,
	runtime WorkloadEffectRuntime,
	options WorkloadCreateOptions,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	if runtime == nil || sequence <= 0 || identifier == (store.TransactionID{}) ||
		!validWorkloadEffect(workload) {
		return empty, ErrInvalidRequest
	}

	transaction := identifier.String()
	intent := workloadCreateIntent(sequence, workload, transaction)
	effect := &workloadCreateEffect{
		runtime:     runtime,
		workload:    workload,
		transaction: transaction,
		options:     options,
		responseID:  "",
	}

	return runRuntimeEffect(ctx, journal, identifier, intent, effect)
}

func runWorkloadStart(
	ctx context.Context,
	journal EffectJournal,
	identifier store.TransactionID,
	sequence int64,
	workload domain.DesiredWorkload,
	runtime WorkloadStartRuntime,
) (EffectPostcondition, error) {
	var empty EffectPostcondition

	if runtime == nil || sequence <= 0 || identifier == (store.TransactionID{}) ||
		!validWorkloadEffect(workload) {
		return empty, ErrInvalidRequest
	}

	transaction := identifier.String()
	intent := workloadStartIntent(sequence, workload, transaction)
	effect := workloadStartEffect{
		runtime:     runtime,
		workload:    workload,
		transaction: transaction,
	}

	return runRuntimeEffect(ctx, journal, identifier, intent, effect)
}

func workloadCreateIntent(
	sequence int64,
	workload domain.DesiredWorkload,
	transaction string,
) store.ActionIntent {
	return workloadIntent(sequence, workloadCreateActionKind, workload, transaction)
}

func workloadStartIntent(
	sequence int64,
	workload domain.DesiredWorkload,
	transaction string,
) store.ActionIntent {
	return workloadIntent(sequence, workloadStartActionKind, workload, transaction)
}

func workloadIntent(
	sequence int64,
	kind string,
	workload domain.DesiredWorkload,
	transaction string,
) store.ActionIntent {
	return store.ActionIntent{
		Sequence: sequence,
		Kind:     kind,
		IntentDigest: workloadEffectDigest(
			workloadEffectIntent,
			kind,
			workload,
			transaction,
			"",
		),
	}
}

func (effect *workloadCreateEffect) Apply(ctx context.Context) error {
	identifier, err := effect.runtime.CreateWorkload(
		ctx, effect.workload, effect.transaction, effect.options,
	)
	effect.responseID = identifier

	if err != nil {
		return fmt.Errorf("create runtime workload: %w", err)
	}

	return nil
}

func (effect *workloadCreateEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition

	probe, err := effect.runtime.ProbeCreatedWorkload(
		ctx,
		effect.workload,
		effect.transaction,
		effect.responseID,
	)
	if err != nil {
		return empty, fmt.Errorf("probe created runtime workload: %w", err)
	}

	switch probe.State {
	case WorkloadEffectProbeMissing:
		if !workloadEffectEvidenceEmpty(probe.Workload) {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest: workloadEffectDigest(
				workloadEffectMissing,
				workloadCreateActionKind,
				effect.workload,
				effect.transaction,
				"",
			),
			Satisfied: false,
		}, nil
	case WorkloadEffectProbeObserved:
		if !createdWorkloadMatches(probe.Workload, effect.workload, effect.transaction, effect.responseID) {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest: workloadObservedEffectDigest(
				workloadEffectObserved,
				workloadCreateActionKind,
				effect.workload,
				effect.transaction,
				probe.Workload.ID,
				probe.Workload.StorageDigest,
			),
			Satisfied: true,
		}, nil
	case WorkloadEffectProbeUnknown:
		return empty, ErrConflictingState
	default:
		return empty, ErrConflictingState
	}
}

func (effect workloadStartEffect) Apply(ctx context.Context) error {
	err := effect.runtime.StartWorkload(ctx, effect.workload, effect.transaction)
	if err != nil {
		return fmt.Errorf("start runtime workload: %w", err)
	}

	return nil
}

func (effect workloadStartEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition

	probe, err := effect.runtime.ProbeStartedWorkload(ctx, effect.workload, effect.transaction)
	if err != nil {
		return empty, fmt.Errorf("probe started runtime workload: %w", err)
	}

	switch probe.State {
	case WorkloadEffectProbeMissing:
		if !workloadEffectEvidenceEmpty(probe.Workload) {
			return empty, ErrConflictingState
		}

		return EffectPostcondition{
			Digest: workloadEffectDigest(
				workloadEffectMissing,
				workloadStartActionKind,
				effect.workload,
				effect.transaction,
				"",
			),
			Satisfied: false,
		}, nil
	case WorkloadEffectProbeObserved:
		if startedWorkloadMatches(probe.Workload, effect.workload, effect.transaction) {
			return EffectPostcondition{
				Digest: workloadObservedEffectDigest(
					workloadEffectObserved,
					workloadStartActionKind,
					effect.workload,
					effect.transaction,
					probe.Workload.ID,
					probe.Workload.StorageDigest,
				),
				Satisfied: true,
			}, nil
		}

		if createdWorkloadMatches(probe.Workload, effect.workload, effect.transaction, "") {
			return EffectPostcondition{
				Digest: workloadObservedEffectDigest(
					workloadEffectUnstarted,
					workloadStartActionKind,
					effect.workload,
					effect.transaction,
					probe.Workload.ID,
					probe.Workload.StorageDigest,
				),
				Satisfied: false,
			}, nil
		}

		return empty, ErrConflictingState
	case WorkloadEffectProbeUnknown:
		return empty, ErrConflictingState
	default:
		return empty, ErrConflictingState
	}
}

func createdWorkloadMatches(
	evidence WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
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
		evidence.ConfigurationMatches && evidence.Lifecycle == WorkloadLifecycleCreated &&
		evidence.Ownership == expectedOwnership && (responseID == "" || evidence.ID == responseID)
}

func startedWorkloadMatches(
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
		evidence.ConfigurationMatches &&
		(evidence.Lifecycle == WorkloadLifecycleRunning ||
			evidence.Lifecycle == WorkloadLifecycleRestarting) &&
		evidence.Ownership == expectedOwnership
}

func validWorkloadEffect(workload domain.DesiredWorkload) bool {
	if workload.ServiceName == "" || workload.ContainerName == "" ||
		workload.SourceDigest == (domain.Digest{}) || workload.EffectiveDigest == (domain.Digest{}) ||
		!validRuntimeImageIdentity(workload.Image) || !validWorkloadEffectProcess(workload) {
		return false
	}

	return true
}

func validWorkloadEffectProcess(workload domain.DesiredWorkload) bool {
	if len(workload.Entrypoint)+len(workload.Command) == 0 {
		return false
	}

	for _, values := range [][]string{workload.Entrypoint, workload.Command} {
		for _, value := range values {
			if !utf8.ValidString(value) {
				return false
			}

			for index := range value {
				if value[index] == 0 {
					return false
				}
			}
		}
	}

	return true
}

func emptyWorkloadEffectEvidence() WorkloadEffectEvidence {
	return WorkloadEffectEvidence{
		ID:                   "",
		Name:                 "",
		ConfigurationDigest:  domain.Digest{},
		StorageDigest:        domain.Digest{},
		RuntimeMounts:        nil,
		ConfigurationMatches: false,
		Lifecycle:            WorkloadLifecycleUnknown,
		Health:               WorkloadHealth{},
		Ownership: domain.WorkloadOwnership{
			Status:           domain.OwnershipConflicting,
			Service:          "",
			Transaction:      "",
			DesiredState:     domain.Digest{},
			Reference:        domain.Digest{},
			ImageConfig:      domain.Digest{},
			PlatformManifest: domain.Digest{},
		},
	}
}

func workloadEffectEvidenceEmpty(evidence WorkloadEffectEvidence) bool {
	empty := emptyWorkloadEffectEvidence()

	return evidence.ID == empty.ID && evidence.Name == empty.Name &&
		evidence.ConfigurationDigest == empty.ConfigurationDigest &&
		evidence.StorageDigest == empty.StorageDigest && evidence.RuntimeMounts == nil &&
		evidence.ConfigurationMatches == empty.ConfigurationMatches &&
		evidence.Lifecycle == empty.Lifecycle && evidence.Health == empty.Health &&
		evidence.Ownership == empty.Ownership
}

func workloadStorageMatches(
	digest domain.Digest,
	mounts []domain.RuntimeMount,
	workload domain.DesiredWorkload,
) bool {
	if len(workload.Mounts) == 0 && mounts != nil {
		return false
	}
	expected, valid := domain.ComputeStorageDigest(workload, mounts)

	return valid && digest == expected
}

func workloadObservedEffectDigest(
	state byte,
	actionKind string,
	workload domain.DesiredWorkload,
	transaction string,
	observedID string,
	storage domain.Digest,
) domain.Digest {
	base := workloadEffectDigest(state, actionKind, workload, transaction, observedID)
	encoded := make([]byte, 0, 1+len(base)+len(storage))
	encoded = append(encoded, workloadEffectDigestFormat)
	encoded = append(encoded, base[:]...)
	encoded = append(encoded, storage[:]...)

	return domain.Hash(encoded)
}

func workloadEffectDigest(
	state byte,
	actionKind string,
	workload domain.DesiredWorkload,
	transaction string,
	observedID string,
) domain.Digest {
	value := []byte{workloadEffectDigestFormat, state}
	value = appendWorkloadEffectString(value, actionKind)
	value = appendWorkloadEffectString(value, workload.ServiceName)
	value = appendWorkloadEffectString(value, workload.ContainerName)
	value = appendWorkloadEffectString(value, workload.Image.Reference)
	value = append(value, workload.Image.ReferenceDigest[:]...)
	value = appendWorkloadEffectString(value, workload.Image.Platform.OS)
	value = appendWorkloadEffectString(value, workload.Image.Platform.Architecture)
	value = appendWorkloadEffectString(value, workload.Image.Platform.Variant)
	value = append(value, workload.Image.PlatformManifest[:]...)
	value = append(value, workload.Image.ImageConfig[:]...)
	value = appendWorkloadEffectStrings(value, workload.Entrypoint)
	value = appendWorkloadEffectStrings(value, workload.Command)
	value = append(value, workload.SourceDigest[:]...)
	value = append(value, workload.EffectiveDigest[:]...)
	value = appendWorkloadEffectString(value, transaction)
	value = appendWorkloadEffectString(value, observedID)

	return domain.Hash(value)
}

func appendWorkloadEffectStrings(encoded []byte, values []string) []byte {
	if values == nil {
		return append(encoded, workloadEffectNilSlice)
	}

	encoded = append(encoded, workloadEffectNonNilSlice)

	encoded = binary.AppendUvarint(encoded, uint64(len(values)))
	for _, value := range values {
		encoded = appendWorkloadEffectString(encoded, value)
	}

	return encoded
}

func appendWorkloadEffectString(encoded []byte, value string) []byte {
	encoded = binary.AppendUvarint(encoded, uint64(len(value)))

	return append(encoded, value...)
}
