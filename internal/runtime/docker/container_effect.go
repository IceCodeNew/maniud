package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"net/url"
	"slices"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

// containerCreateRequest is the minimal API 1.54 wire shape maniud supports.
//
//nolint:tagliatelle // Docker Engine API 1.54 uses exported Go field names on the wire.
type containerCreateRequest struct {
	Image      string                    `json:"Image"`
	Entrypoint []string                  `json:"Entrypoint"`
	Command    []string                  `json:"Cmd"`
	Labels     map[string]string         `json:"Labels"`
	HostConfig containerCreateHostConfig `json:"HostConfig"`
}

//nolint:tagliatelle // Docker Engine API 1.54 uses exported Go field names on the wire.
type containerCreateHostConfig struct {
	NetworkMode   string                       `json:"NetworkMode"`
	RestartPolicy containertypes.RestartPolicy `json:"RestartPolicy"`
}

type containerListFilters struct {
	Labels []string `json:"label"`
}

// CreateWorkload creates one stopped transaction-owned container. The returned
// ID remains response evidence until ProbeCreatedWorkload independently proves
// the complete runtime postcondition.
func (client *Client) CreateWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (string, error) {
	if client == nil || client.CheckWorkload(workload) != nil || !validTransaction(transaction) {
		return "", ErrUnsupportedWorkload
	}

	body := containerCreateRequest{
		Image:      workload.Image.Reference,
		Entrypoint: slices.Clone(workload.Entrypoint),
		Command:    slices.Clone(workload.Command),
		Labels:     workloadOwnershipLabels(workload, transaction),
		HostConfig: containerCreateHostConfig{
			NetworkMode: dockerNetworkMode,
			RestartPolicy: containertypes.RestartPolicy{
				Name:              containertypes.RestartPolicyDisabled,
				MaximumRetryCount: 0,
			},
		},
	}
	encoded, _ := json.Marshal(body) //nolint:errchkjson // The wire type contains only JSON-supported values.
	// CheckWorkload proved the immutable client version above.
	path, _ := client.versionedPath("/containers/create")

	response, err := client.containerCreateRequest(ctx, path, workload.ContainerName, encoded)
	if err != nil {
		return "", err
	}

	return decodeContainerCreateResponse(response)
}

func (client *Client) containerCreateRequest(
	ctx context.Context,
	path string,
	name string,
	body []byte,
) (*http.Response, error) {
	endpoint := client.baseURL
	endpoint.Path = path
	endpoint.RawQuery = url.Values{containerNameQueryKey: {name}}.Encode()

	request, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint.String(), bytes.NewReader(body))
	if err != nil {
		return nil, ErrProtocol
	}

	request.Header.Set("Accept", jsonContentType)
	request.Header.Set(contentTypeHeader, jsonContentType)

	response, err := client.httpClient.Do(request)
	if err != nil {
		ctxErr := ctx.Err()
		if ctxErr != nil {
			return nil, fmt.Errorf("docker container create: %w", ctxErr)
		}

		return nil, ErrUnavailable
	}

	return response, nil
}

func decodeContainerCreateResponse(response *http.Response) (string, error) {
	if response.StatusCode != http.StatusCreated || !isJSON(response.Header.Get(contentTypeHeader)) {
		closeResponse(response)

		return "", ErrProtocol
	}

	var payload containertypes.CreateResponse

	valid := decodeStrictJSON(response.Body, &payload)
	closeErr := response.Body.Close()

	if !valid || !validContainerID(payload.ID) {
		return "", ErrProtocol
	}

	if closeErr != nil {
		return payload.ID, ErrUnavailable
	}

	if len(payload.Warnings) != 0 {
		return payload.ID, ErrProtocol
	}

	return payload.ID, nil
}

// ProbeCreatedWorkload proves one stopped create result using both the desired
// name and the transaction ownership selectors. This prevents a lost create
// response followed by an external rename from being misclassified as absence.
func (client *Client) ProbeCreatedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) (application.WorkloadEffectProbe, error) {
	var empty application.WorkloadEffectProbe

	if !validCreatedWorkloadProbeInput(client, workload, transaction, responseID) {
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

	expectation := applicationContainerExpectation(workload, transaction, responseID)

	return application.WorkloadEffectProbe{
		State: application.WorkloadEffectProbeObserved,
		Workload: application.WorkloadEffectEvidence{
			ID:                   selected.ID,
			Name:                 selected.Name,
			ConfigurationMatches: selected.matchesConfiguration(expectation),
			Lifecycle:            applicationWorkloadLifecycle(selected.State),
			Ownership:            selected.Ownership,
		},
	}, nil
}

func validCreatedWorkloadProbeInput(
	client *Client,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) bool {
	return client != nil && client.CheckWorkload(workload) == nil && validTransaction(transaction) &&
		(responseID == "" || validContainerID(responseID))
}

func (client *Client) createdContainerCandidates(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (ContainerProbe, ContainerProbe, error) {
	var empty ContainerProbe

	named, err := client.ProbeContainer(ctx, workload.ContainerName)
	if err != nil {
		return empty, empty, err
	}

	owned, err := client.probeOwnedContainer(ctx, workload.ServiceName, transaction)
	if err != nil {
		return empty, empty, err
	}

	if named.State == ContainerProbeObserved && owned.State == ContainerProbeMissing &&
		named.Container.Ownership.Service == workload.ServiceName &&
		named.Container.Ownership.Transaction == transaction {
		return empty, empty, ErrProtocol
	}

	return named, owned, nil
}

func (client *Client) probeOwnedContainer(
	ctx context.Context,
	service string,
	transaction string,
) (ContainerProbe, error) {
	var unknown ContainerProbe

	identifier, found, err := client.ownedContainerID(ctx, service, transaction)
	if err != nil {
		return unknown, err
	}

	if !found {
		var emptyContainer Container

		return ContainerProbe{State: ContainerProbeMissing, Container: emptyContainer}, nil
	}

	probe, err := client.ProbeContainer(ctx, identifier)
	if err != nil {
		return unknown, err
	}

	if probe.State != ContainerProbeObserved ||
		probe.Container.Ownership.Service != service || probe.Container.Ownership.Transaction != transaction {
		return unknown, ErrProtocol
	}

	return probe, nil
}

func (client *Client) ownedContainerID(
	ctx context.Context,
	service string,
	transaction string,
) (string, bool, error) {
	filter, _ := json.Marshal(containerListFilters{ //nolint:errchkjson // The wire type contains only strings.
		Labels: []string{
			domain.LabelService + "=" + service,
			domain.LabelTransaction + "=" + transaction,
		},
	})

	path, valid := client.versionedPath("/containers/json")
	if !valid {
		return "", false, ErrProtocol
	}

	response, err := client.requestQuery(ctx, http.MethodGet, path, url.Values{
		"all":     {"true"},
		"filters": {string(filter)},
	})
	if err != nil {
		return "", false, err
	}
	defer closeResponse(response)

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get(contentTypeHeader)) {
		return "", false, ErrProtocol
	}

	var summaries []containertypes.Summary
	if !decodeStrictJSON(response.Body, &summaries) || len(summaries) > 1 {
		return "", false, ErrProtocol
	}

	if len(summaries) == 0 {
		return "", false, nil
	}

	if !validContainerID(summaries[0].ID) {
		return "", false, ErrProtocol
	}

	return summaries[0].ID, true, nil
}

func selectCreatedContainer(named, owned ContainerProbe) (Container, bool, bool) {
	var empty Container

	if named.State == ContainerProbeMissing && owned.State == ContainerProbeMissing {
		return empty, false, true
	}

	if named.State == ContainerProbeObserved && owned.State == ContainerProbeMissing {
		return named.Container, true, true
	}

	if named.State == ContainerProbeMissing && owned.State == ContainerProbeObserved {
		return owned.Container, true, true
	}

	if named.State == ContainerProbeObserved && owned.State == ContainerProbeObserved &&
		named.Container.ID == owned.Container.ID {
		return named.Container, true, true
	}

	return empty, false, false
}

func workloadOwnershipLabels(workload domain.DesiredWorkload, transaction string) map[string]string {
	return map[string]string{
		domain.LabelService:                workload.ServiceName,
		domain.LabelTransaction:            transaction,
		domain.LabelDesiredStateDigest:     workload.EffectiveDigest.String(),
		domain.LabelReferenceDigest:        workload.Image.ReferenceDigest.String(),
		domain.LabelImageConfigDigest:      workload.Image.ImageConfig.String(),
		domain.LabelPlatformManifestDigest: workload.Image.PlatformManifest.String(),
	}
}

func applicationContainerExpectation(
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) ContainerExpectation {
	return ContainerExpectation{
		ID:               responseID,
		Name:             workload.ContainerName,
		ImageReference:   workload.Image.Reference,
		ImageConfig:      workload.Image.ImageConfig,
		PlatformManifest: workload.Image.PlatformManifest,
		Entrypoint:       workload.Entrypoint,
		Command:          workload.Command,
		NetworkMode:      dockerNetworkMode,
		RestartPolicy: containertypes.RestartPolicy{
			Name:              containertypes.RestartPolicyDisabled,
			MaximumRetryCount: 0,
		},
		Service:       workload.ServiceName,
		Transaction:   transaction,
		DesiredState:  workload.EffectiveDigest,
		Reference:     workload.Image.ReferenceDigest,
		AllowedStates: []ContainerState{ContainerCreated},
	}
}

func applicationWorkloadLifecycle(state ContainerState) application.WorkloadLifecycle {
	switch state {
	case ContainerCreated:
		return application.WorkloadLifecycleCreated
	case ContainerRunning:
		return application.WorkloadLifecycleRunning
	case ContainerPaused:
		return application.WorkloadLifecyclePaused
	case ContainerRestarting:
		return application.WorkloadLifecycleRestarting
	case ContainerRemoving:
		return application.WorkloadLifecycleRemoving
	case ContainerExited:
		return application.WorkloadLifecycleExited
	case ContainerDead:
		return application.WorkloadLifecycleDead
	default:
		return application.WorkloadLifecycleUnknown
	}
}

func emptyApplicationWorkloadEffectEvidence() application.WorkloadEffectEvidence {
	return application.WorkloadEffectEvidence{
		ID:                   "",
		Name:                 "",
		ConfigurationMatches: false,
		Lifecycle:            application.WorkloadLifecycleUnknown,
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
