package podman

import (
	"context"
	"encoding/json"
	"net/http"
	"net/url"
	"strconv"

	"github.com/IceCodeNew/maniud/internal/application"
)

const podmanStopTimeoutSeconds = 10

type podmanRemovalReport struct {
	ID string `json:"Id"` //nolint:tagliatelle // Native Libpod wire field.
}

// ApplyWorkloadTransition applies one exact stop, rename, remove, or recovery start.
func (client *Client) ApplyWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) error {
	if !validPodmanWorkloadTransition(client, transition) {
		return ErrUnsupportedWorkload
	}
	probe, err := client.probeExistingWorkload(ctx, transition.Before)
	if err != nil {
		return err
	}
	if probe.State != application.WorkloadEffectProbeObserved || probe.Workload != transition.Before {
		return ErrProtocol
	}
	method, path, query := client.podmanWorkloadTransitionRequest(transition)
	response, err := client.request(ctx, method, path, query, nil, false)
	if err != nil {
		return err
	}
	if transition.Kind == application.WorkloadTransitionRemove {
		return decodePodmanRemovalResponse(response, transition.Before.ID)
	}
	if transition.Kind == application.WorkloadTransitionStop ||
		transition.Kind == application.WorkloadTransitionRestoreStart {
		return decodePodmanEmptyResponse(response, http.StatusNoContent, http.StatusNotModified)
	}

	return decodePodmanEmptyResponse(response, http.StatusNoContent)
}

// ProbeWorkloadTransition independently proves the exact postcondition.
func (client *Client) ProbeWorkloadTransition(
	ctx context.Context,
	transition application.WorkloadTransition,
) (application.WorkloadTransitionProbe, error) {
	var empty application.WorkloadTransitionProbe
	if !validPodmanWorkloadTransition(client, transition) {
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
		return application.WorkloadTransitionProbe{State: application.WorkloadEffectProbeMissing}, nil
	}
	if byID.State == ContainerProbeObserved && byName.State == ContainerProbeObserved &&
		byID.Container.ID == byName.Container.ID {
		return existingPodmanWorkloadProbe(byID), nil
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
		return application.WorkloadTransitionProbe{State: application.WorkloadEffectProbeMissing}, nil
	}

	return existingPodmanWorkloadProbe(probe), nil
}

func existingPodmanWorkloadProbe(probe ContainerProbe) application.WorkloadTransitionProbe {
	container := probe.Container

	return application.WorkloadTransitionProbe{
		State: application.WorkloadEffectProbeObserved,
		Workload: application.ExistingWorkload{
			ID:                  container.ID,
			Name:                container.Name,
			ConfigurationDigest: containerConfigurationDigest(container),
			Lifecycle:           podmanWorkloadLifecycle(container.State),
			Ownership:           container.Ownership,
		},
		Health: container.Health,
	}
}

func validPodmanWorkloadTransition(client *Client, transition application.WorkloadTransition) bool {
	if !client.negotiated() || !transition.Valid() ||
		!validContainerID(transition.Before.ID) || !validContainerName(transition.Before.Name) {
		return false
	}
	if transition.Kind == application.WorkloadTransitionRemove {
		return true
	}

	return validContainerID(transition.After.ID) && validContainerName(transition.After.Name)
}

func (client *Client) podmanWorkloadTransitionRequest(
	transition application.WorkloadTransition,
) (string, string, url.Values) {
	path := client.apiPath("/containers/" + transition.Before.ID)
	if path == "" {
		return "", "", nil
	}
	//nolint:exhaustive // Unknown is rejected by validation before request construction.
	switch transition.Kind {
	case application.WorkloadTransitionStop:
		return http.MethodPost, path + "/stop", url.Values{
			"timeout": {strconv.Itoa(podmanStopTimeoutSeconds)},
		}
	case application.WorkloadTransitionRename:
		return http.MethodPost, path + "/rename", url.Values{"name": {transition.After.Name}}
	case application.WorkloadTransitionRemove:
		return http.MethodDelete, path, url.Values{
			"force": {podmanQueryFalse}, "volumes": {podmanQueryFalse},
		}
	case application.WorkloadTransitionRestoreStart:
		return http.MethodPost, path + "/start", nil
	}

	return "", "", nil
}

func decodePodmanRemovalResponse(response *http.Response, expectedID string) error {
	document, err := readPodmanJSONResponse(response, http.StatusOK)
	if err != nil {
		return err
	}
	var fields []map[string]json.RawMessage
	var reports []podmanRemovalReport
	if json.Unmarshal(document, &fields) != nil || json.Unmarshal(document, &reports) != nil ||
		len(fields) != 1 || len(reports) != 1 {
		return ErrProtocol
	}
	_, hasID := fields[0]["Id"]
	_, hasError := fields[0]["Err"]
	if !hasID || hasError || reports[0].ID != expectedID {
		return ErrProtocol
	}

	return nil
}
