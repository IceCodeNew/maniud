package containerd

import (
	"context"
	"slices"
	"time"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const containerdStopTimeout = 10 * time.Second

// CreateWorkload creates one stopped transaction-owned native container. The
// returned ID remains response evidence until the independent probe succeeds.
func (client *Client) CreateWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	options application.WorkloadCreateOptions,
) (string, error) {
	configuration, err := containerdconfig.Encode(workload.WorkloadSpec)
	if err != nil || client.CheckWorkload(workload) != nil || !validTransaction(transaction) {
		return "", ErrUnsupportedWorkload
	}
	snapshotParent, err := client.snapshotParent(ctx, workload.Image)
	if err != nil {
		return "", err
	}

	var identifier string
	err = client.checked(ctx, func(requestContext context.Context) error {
		var createErr error
		identifier, createErr = client.workloads.Create(requestContext, createWorkloadRequest{
			Workload: workload, Transaction: transaction, Configuration: configuration,
			SnapshotParent: snapshotParent, CopyImageVolumes: options.CopyImageVolumes,
		})

		return wrapWorkloadBackendError("creation", createErr)
	})

	return identifier, workloadError(err)
}

// ProbeCreatedWorkload proves one create result by logical name and complete
// transaction ownership.
func (client *Client) ProbeCreatedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) (application.WorkloadEffectProbe, error) {
	return client.probeWorkloadEffect(ctx, workload, transaction, responseID)
}

// StartWorkload starts the exact stopped transaction-owned container.
func (client *Client) StartWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	probe, err := client.ProbeCreatedWorkload(ctx, workload, transaction, "")
	if err != nil {
		return err
	}
	if probe.State != application.WorkloadEffectProbeObserved || !validEffectWorkload(
		probe.Workload, workload, transaction,
	) || probe.Workload.Lifecycle != application.WorkloadLifecycleCreated {
		return ErrProtocol
	}

	err = client.checked(ctx, func(requestContext context.Context) error {
		return client.workloads.Start(requestContext, probe.Workload.ID)
	})

	return workloadError(err)
}

// ProbeStartedWorkload proves the exact transaction-owned container is
// running and network-connected.
func (client *Client) ProbeStartedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (application.WorkloadEffectProbe, error) {
	return client.probeWorkloadEffect(ctx, workload, transaction, "")
}

// DiscardWorkload force-removes one exact transaction-owned container while
// retaining fail-closed selector checks.
func (client *Client) DiscardWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	probe, err := client.ProbeDiscardedWorkload(ctx, workload, transaction)
	if err != nil {
		return err
	}
	if probe.State != application.WorkloadEffectProbeObserved ||
		!validEffectWorkload(probe.Workload, workload, transaction) {
		return ErrProtocol
	}

	err = client.checked(ctx, func(requestContext context.Context) error {
		return client.workloads.Remove(requestContext, probe.Workload.ID, true)
	})

	return workloadError(err)
}

// ProbeDiscardedWorkload proves either complete selector absence or one exact
// discard candidate.
func (client *Client) ProbeDiscardedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (application.WorkloadEffectProbe, error) {
	return client.probeWorkloadEffect(ctx, workload, transaction, "")
}

//nolint:cyclop // Effect probing resolves ambiguous name, ownership, and response evidence fail closed.
func (client *Client) probeWorkloadEffect(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) (application.WorkloadEffectProbe, error) {
	if client.CheckWorkload(workload) != nil || !validTransaction(transaction) ||
		(responseID != "" && !validContainerdID(responseID)) {
		return application.WorkloadEffectProbe{}, ErrUnsupportedWorkload
	}

	var candidates workloadCandidates
	err := client.checked(ctx, func(requestContext context.Context) error {
		var err error
		candidates, err = client.workloads.Candidates(
			requestContext, workload.ContainerName, workload.ServiceName, transaction,
		)

		return wrapWorkloadBackendError("candidate probe", err)
	})
	if err != nil {
		return application.WorkloadEffectProbe{}, workloadError(err)
	}
	selected, found, consistent := selectWorkloadCandidate(candidates)
	if !consistent {
		return application.WorkloadEffectProbe{}, ErrProtocol
	}
	if !found {
		return application.WorkloadEffectProbe{
			State: application.WorkloadEffectProbeMissing, Workload: emptyWorkloadEvidence(),
		}, nil
	}
	evidence, err := workloadEffectEvidence(selected, workload)
	if err != nil || responseID != "" && evidence.ID != responseID {
		return application.WorkloadEffectProbe{}, ErrProtocol
	}

	return application.WorkloadEffectProbe{
		State: application.WorkloadEffectProbeObserved, Workload: evidence,
	}, nil
}

func selectWorkloadCandidate(candidates workloadCandidates) (*nativeWorkload, bool, bool) {
	if candidates.Named == nil && candidates.Owned == nil {
		return nil, false, true
	}
	if candidates.Named == nil {
		return candidates.Owned, true, true
	}
	if candidates.Owned == nil {
		if candidates.Named.Ownership.Status == domain.OwnershipManaged {
			return nil, false, false
		}

		return candidates.Named, true, true
	}
	if candidates.Named.ID != candidates.Owned.ID {
		return nil, false, false
	}

	return candidates.Named, true, true
}

func validEffectWorkload(
	evidence application.WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
) bool {
	return evidence.ID != "" && evidence.Name == workload.ContainerName &&
		evidence.ConfigurationDigest != (domain.Digest{}) && evidence.ConfigurationMatches &&
		validEffectStorage(evidence, workload) && evidence.Ownership.Matches(
		workload.ServiceName, transaction, workload.EffectiveDigest, workload.Image.ReferenceDigest,
	) && evidence.Ownership.ImageConfig == workload.Image.ImageConfig &&
		evidence.Ownership.PlatformManifest == workload.Image.PlatformManifest
}

func validEffectStorage(evidence application.WorkloadEffectEvidence, workload domain.DesiredWorkload) bool {
	digest, valid := domain.ComputeStorageDigest(workload, evidence.RuntimeMounts)

	return valid && digest == evidence.StorageDigest
}

func emptyWorkloadEvidence() application.WorkloadEffectEvidence {
	return application.WorkloadEffectEvidence{
		ID: "", Name: "", ConfigurationDigest: domain.Digest{}, StorageDigest: domain.Digest{},
		RuntimeMounts: nil, ConfigurationMatches: false,
		Lifecycle: application.WorkloadLifecycleUnknown,
		Ownership: domain.WorkloadOwnership{Status: domain.OwnershipConflicting},
	}
}

// ApplyWorkloadTransition performs one exact existing-workload operation.
func (client *Client) ApplyWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) error {
	if client == nil || client.workloads == nil || !validContainerdTransition(transition) {
		return ErrUnsupportedWorkload
	}
	probe, err := client.probeExistingWorkload(ctx, transition.Before)
	if err != nil {
		return err
	}
	if probe.State != application.WorkloadEffectProbeObserved || probe.Workload != transition.Before {
		return ErrProtocol
	}

	err = client.checked(ctx, func(requestContext context.Context) error {
		return client.applyWorkloadTransition(requestContext, transition)
	})

	return workloadError(err)
}

// ResumeIncompleteWorkloadTransition continues an independently observed,
// transaction-owned native removal without requiring resources already deleted
// by the interrupted attempt.
func (client *Client) ResumeIncompleteWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) error {
	if client == nil || client.workloads == nil || transition.Kind != application.WorkloadTransitionRemove ||
		!validContainerdTransition(transition) {
		return ErrUnsupportedWorkload
	}
	probe, err := client.probeRemovedWorkload(ctx, transition.Before)
	removing := transition.Before
	removing.Lifecycle = application.WorkloadLifecycleRemoving
	if err != nil {
		return err
	}
	if probe.State != application.WorkloadEffectProbeObserved || probe.Workload != removing {
		return ErrProtocol
	}

	err = client.checked(ctx, func(requestContext context.Context) error {
		return wrapWorkloadBackendError(
			"resumed remove", client.workloads.Remove(requestContext, transition.Before.ID, false),
		)
	})

	return workloadError(err)
}

func (client *Client) applyWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) error {
	switch transition.Kind {
	case application.WorkloadTransitionStop:
		return wrapWorkloadBackendError(
			"stop", client.workloads.Stop(ctx, transition.Before.ID, containerdStopTimeout),
		)
	case application.WorkloadTransitionRename:
		available, err := client.workloads.NameAvailable(
			ctx, transition.After.Name, transition.Before.ID,
		)
		if err != nil {
			return wrapWorkloadBackendError("rename name probe", err)
		}
		if !available {
			return ErrProtocol
		}

		return wrapWorkloadBackendError(
			"rename", client.workloads.Rename(ctx, transition.Before.ID, transition.After.Name),
		)
	case application.WorkloadTransitionRemove:
		return wrapWorkloadBackendError(
			"remove", client.workloads.Remove(ctx, transition.Before.ID, false),
		)
	case application.WorkloadTransitionRestoreStart:
		return wrapWorkloadBackendError(
			"restore start", client.workloads.Start(ctx, transition.Before.ID),
		)
	case application.WorkloadTransitionUnknown:
	}

	return ErrProtocol
}

// ProbeWorkloadTransition proves the exact postcondition or complete removal.
func (client *Client) ProbeWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) (application.WorkloadTransitionProbe, error) {
	if client == nil || client.workloads == nil || !validContainerdTransition(transition) {
		return application.WorkloadTransitionProbe{}, ErrUnsupportedWorkload
	}
	if transition.Kind == application.WorkloadTransitionRemove {
		return client.probeRemovedWorkload(ctx, transition.Before)
	}

	probe, err := client.probeExistingWorkload(ctx, transition.After)
	if err != nil {
		return application.WorkloadTransitionProbe{}, err
	}
	if transition.Kind == application.WorkloadTransitionRename &&
		probe.State == application.WorkloadEffectProbeObserved {
		var available bool
		err = client.checked(ctx, func(requestContext context.Context) error {
			var err error
			available, err = client.workloads.NameAvailable(requestContext, transition.Before.Name, "")

			return wrapWorkloadBackendError("rename name probe", err)
		})
		if err != nil || !available {
			return application.WorkloadTransitionProbe{}, ErrProtocol
		}
	}

	return probe, nil
}

func (client *Client) probeRemovedWorkload(
	ctx context.Context,
	before application.ExistingWorkload,
) (application.WorkloadTransitionProbe, error) {
	var byID *nativeWorkload
	var removalComplete bool
	err := client.checked(ctx, func(requestContext context.Context) error {
		var err error
		byID, removalComplete, err = client.readRemovalState(requestContext, before)

		return err
	})
	if err != nil {
		return application.WorkloadTransitionProbe{}, workloadError(err)
	}
	if byID != nil {
		probe := existingWorkloadProbe(byID)
		if probe.Workload.Lifecycle == application.WorkloadLifecycleCreated ||
			probe.Workload.Lifecycle == application.WorkloadLifecycleExited {
			probe.Workload.Lifecycle = application.WorkloadLifecycleRemoving
		}

		return probe, nil
	}
	if removalComplete {
		return application.WorkloadTransitionProbe{State: application.WorkloadEffectProbeMissing}, nil
	}

	return application.WorkloadTransitionProbe{}, ErrProtocol
}

func (client *Client) readRemovalState(
	ctx context.Context,
	before application.ExistingWorkload,
) (*nativeWorkload, bool, error) {
	byID, err := client.workloads.RemovalCandidate(ctx, before.ID)
	if err != nil {
		return nil, false, wrapWorkloadBackendError("removal ID probe", err)
	}
	if byID != nil {
		return byID, false, nil
	}
	available, err := client.workloads.NameAvailable(ctx, before.Name, "")
	if err != nil {
		return nil, false, wrapWorkloadBackendError("removal name probe", err)
	}
	if !available {
		return nil, false, nil
	}
	complete, err := client.workloads.RemovalComplete(ctx, before.ID)

	return nil, complete, wrapWorkloadBackendError("removal cleanup probe", err)
}

func (client *Client) probeExistingWorkload(
	ctx context.Context,
	expected application.ExistingWorkload,
) (application.WorkloadTransitionProbe, error) {
	var workload *nativeWorkload
	err := client.checked(ctx, func(requestContext context.Context) error {
		var err error
		workload, err = client.workloads.Workload(requestContext, expected.ID)

		return wrapWorkloadBackendError("existing workload probe", err)
	})
	if err != nil {
		return application.WorkloadTransitionProbe{}, workloadError(err)
	}
	if workload == nil {
		return application.WorkloadTransitionProbe{State: application.WorkloadEffectProbeMissing}, nil
	}

	return existingWorkloadProbe(workload), nil
}

func existingWorkloadProbe(workload *nativeWorkload) application.WorkloadTransitionProbe {
	return application.WorkloadTransitionProbe{
		State: application.WorkloadEffectProbeObserved,
		Workload: application.ExistingWorkload{
			ID: workload.ID, Name: workload.Name, ConfigurationDigest: workload.ConfigurationDigest,
			Lifecycle: workload.Lifecycle, Ownership: workload.Ownership,
		},
	}
}

func validContainerdTransition(transition application.WorkloadTransition) bool {
	return transition.Valid() && validContainerdID(transition.Before.ID) &&
		validContainerdName(transition.Before.Name) &&
		(transition.Kind == application.WorkloadTransitionRemove ||
			validContainerdID(transition.After.ID) && validContainerdName(transition.After.Name))
}

func validTransaction(value string) bool {
	return value != "" && len(value) <= 256 && validContainerdText(value)
}

func validContainerdID(value string) bool {
	return validContainerdName(value) && len(value) <= 64
}

func validContainerdName(value string) bool {
	if value == "" || len(value) > 255 || !asciiAlphaNumeric(value[0]) ||
		!asciiAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for index := 1; index+1 < len(value); index++ {
		selected := value[index]
		if !asciiAlphaNumeric(selected) && selected != '.' && selected != '-' && selected != '_' {
			return false
		}
	}

	return true
}

func validContainerdText(value string) bool {
	return !slices.Contains([]byte(value), byte(0))
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}
