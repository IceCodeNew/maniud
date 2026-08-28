package docker

import (
	"context"
	"net/http"
	"net/url"
	"slices"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

// DiscardWorkload force-removes the exact desired-name and transaction-owned
// container selected by a strict probe. It never removes an unmanaged object.
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
		!validDiscardWorkload(probe.Workload, workload, transaction) {
		return ErrProtocol
	}

	// The strict probe proved the immutable client version and full container ID.
	path, _ := client.apiPath("/containers/" + probe.Workload.ID)

	response, err := client.requestQuery(ctx, http.MethodDelete, path, url.Values{
		"force": {dockerQueryTrue},
		"v":     {dockerQueryFalse},
	})
	if err != nil {
		return err
	}

	return decodeContainerNoContentResponse(response)
}

// ProbeDiscardedWorkload proves either complete absence from the desired name
// and transaction ownership selector or one exact discard candidate.
func (client *Client) ProbeDiscardedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (application.WorkloadEffectProbe, error) {
	var empty application.WorkloadEffectProbe

	if !validCreatedWorkloadProbeInput(client, workload, transaction, "") {
		return empty, ErrUnsupportedWorkload
	}

	named, owned, err := client.createdContainerCandidates(ctx, workload, transaction)
	if err != nil {
		return empty, err
	}

	selected, found, consistent := selectCreatedContainer(named, owned)
	if !consistent {
		return empty, ErrProtocol
	}

	if !found {
		return application.WorkloadEffectProbe{
			State:    application.WorkloadEffectProbeMissing,
			Workload: emptyApplicationWorkloadEffectEvidence(),
		}, nil
	}

	expectation := applicationContainerExpectation(workload, transaction, "", selected.State)
	storageDigest, valid := domain.ComputeStorageDigest(workload, selected.RuntimeMounts)
	if !valid {
		return empty, ErrProtocol
	}

	return application.WorkloadEffectProbe{
		State: application.WorkloadEffectProbeObserved,
		Workload: application.WorkloadEffectEvidence{
			ID:                   selected.ID,
			Name:                 selected.Name,
			ConfigurationDigest:  containerConfigurationDigest(selected),
			StorageDigest:        storageDigest,
			RuntimeMounts:        slices.Clone(selected.RuntimeMounts),
			ConfigurationMatches: selected.matchesConfiguration(expectation),
			Lifecycle:            applicationWorkloadLifecycle(selected.State),
			Ownership:            selected.Ownership,
		},
	}, nil
}

func validDiscardWorkload(
	evidence application.WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
) bool {
	if !evidence.ConfigurationMatches ||
		!validDockerStorageEvidence(evidence, workload) ||
		(evidence.Lifecycle != application.WorkloadLifecycleCreated &&
			evidence.Lifecycle != application.WorkloadLifecycleRunning &&
			evidence.Lifecycle != application.WorkloadLifecycleExited) {
		return false
	}

	return evidence.Ownership.Matches(
		workload.ServiceName,
		transaction,
		workload.EffectiveDigest,
		workload.Image.ReferenceDigest,
	) && evidence.Ownership.ImageConfig == workload.Image.ImageConfig &&
		evidence.Ownership.PlatformManifest == workload.Image.PlatformManifest
}

func validDockerStorageEvidence(
	evidence application.WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
) bool {
	digest, valid := domain.ComputeStorageDigest(workload, evidence.RuntimeMounts)

	return valid && digest == evidence.StorageDigest
}
