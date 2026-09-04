package docker

import (
	"context"
	"net/http"
	"net/url"
	"strconv"

	"github.com/IceCodeNew/maniud/internal/application"
)

const dockerStopTimeoutSeconds = 10

// ApplyWorkloadTransition applies one exact stop, rename, remove, or recovery
// start operation. The API response remains untrusted until the caller runs
// ProbeWorkloadTransition.
func (client *Client) ApplyWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) error {
	if client == nil || !validNegotiatedVersion(client.version) ||
		!validDockerWorkloadTransition(transition) {
		return ErrUnsupportedWorkload
	}

	probe, err := client.probeExistingWorkload(ctx, transition.Before)
	if err != nil {
		return err
	}

	if probe.State != application.WorkloadEffectProbeObserved || probe.Workload != transition.Before {
		return ErrProtocol
	}

	method, endpoint, query := workloadTransitionRequest(transition)
	path, valid := client.apiPath(endpoint)
	if !valid {
		return ErrProtocol
	}
	response, err := client.requestQuery(ctx, method, path, query)
	if err != nil {
		return err
	}

	return decodeContainerNoContentResponse(response)
}

// ProbeWorkloadTransition proves either the exact post-transition workload or
// complete absence for remove. Rename also proves the old name is free.
func (client *Client) ProbeWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) (application.WorkloadTransitionProbe, error) {
	var empty application.WorkloadTransitionProbe

	if client == nil || !validNegotiatedVersion(client.version) ||
		!validDockerWorkloadTransition(transition) {
		return empty, ErrUnsupportedWorkload
	}

	if transition.Kind == application.WorkloadTransitionRemove {
		return client.probeRemovedWorkload(ctx, transition.Before)
	}

	probe, err := client.probeExistingWorkload(ctx, transition.After)
	if err != nil {
		return empty, err
	}

	if transition.Kind == application.WorkloadTransitionRename &&
		probe.State == application.WorkloadEffectProbeObserved {
		oldName, oldErr := client.ProbeContainer(ctx, transition.Before.Name)
		if oldErr != nil {
			return empty, oldErr
		}

		if oldName.State != ContainerProbeMissing {
			return empty, ErrProtocol
		}
	}

	return probe, nil
}

func (client *Client) probeRemovedWorkload(
	ctx context.Context,
	expected application.ExistingWorkload,
) (application.WorkloadTransitionProbe, error) {
	var empty application.WorkloadTransitionProbe

	byID, err := client.ProbeContainer(ctx, expected.ID)
	if err != nil {
		return empty, err
	}

	byName, err := client.ProbeContainer(ctx, expected.Name)
	if err != nil {
		return empty, err
	}

	if byID.State == ContainerProbeMissing && byName.State == ContainerProbeMissing {
		return application.WorkloadTransitionProbe{
			State:    application.WorkloadEffectProbeMissing,
			Workload: application.ExistingWorkload{},
		}, nil
	}

	if byID.State == ContainerProbeObserved && byName.State == ContainerProbeObserved &&
		byID.Container.ID == byName.Container.ID {
		return existingWorkloadProbe(byID), nil
	}

	return empty, ErrProtocol
}

func (client *Client) probeExistingWorkload(
	ctx context.Context,
	expected application.ExistingWorkload,
) (application.WorkloadTransitionProbe, error) {
	var empty application.WorkloadTransitionProbe

	probe, err := client.ProbeContainer(ctx, expected.ID)
	if err != nil {
		return empty, err
	}

	if probe.State == ContainerProbeMissing {
		return application.WorkloadTransitionProbe{
			State:    application.WorkloadEffectProbeMissing,
			Workload: application.ExistingWorkload{},
		}, nil
	}

	return existingWorkloadProbe(probe), nil
}

func existingWorkloadProbe(probe ContainerProbe) application.WorkloadTransitionProbe {
	container := probe.Container

	return application.WorkloadTransitionProbe{
		State: application.WorkloadEffectProbeObserved,
		Workload: application.ExistingWorkload{
			ID:                  container.ID,
			Name:                container.Name,
			ConfigurationDigest: containerConfigurationDigest(container),
			Lifecycle:           applicationWorkloadLifecycle(container.State),
			Ownership:           container.Ownership,
		},
		Health: container.Health,
	}
}

func validDockerWorkloadTransition(transition application.WorkloadTransition) bool {
	if !transition.Valid() || !validContainerID(transition.Before.ID) ||
		!validContainerName(transition.Before.Name) {
		return false
	}

	if transition.Kind == application.WorkloadTransitionRemove {
		return true
	}

	return validContainerID(transition.After.ID) && validContainerName(transition.After.Name)
}

func workloadTransitionRequest(transition application.WorkloadTransition) (string, string, url.Values) {
	path := "/containers/" + transition.Before.ID

	switch transition.Kind {
	case application.WorkloadTransitionStop:
		return http.MethodPost, path + "/stop", url.Values{
			"t": {strconv.Itoa(dockerStopTimeoutSeconds)},
		}
	case application.WorkloadTransitionRename:
		return http.MethodPost, path + "/rename", url.Values{
			"name": {transition.After.Name},
		}
	case application.WorkloadTransitionRemove:
		return http.MethodDelete, path, url.Values{
			"force": {dockerQueryFalse},
			"v":     {dockerQueryFalse},
		}
	case application.WorkloadTransitionRestoreStart:
		return http.MethodPost, path + "/start", nil
	case application.WorkloadTransitionUnknown:
	}

	return "", "", nil
}
