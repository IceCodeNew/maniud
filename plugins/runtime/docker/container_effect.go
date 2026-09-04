package docker

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"net/url"
	"slices"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	dockerQueryFalse = "false"
	dockerQueryTrue  = "true"
)

type containerListFilters struct {
	Labels []string `json:"label"`
}

//nolint:tagliatelle // Docker Engine wire fields use exported Go names.
type createRequestWithoutCgroupNamespace struct {
	containertypes.CreateRequest

	HostConfig *hostConfigWithoutCgroupNamespace `json:"HostConfig,omitempty"`
}

//nolint:tagliatelle // Docker Engine wire fields use exported Go names.
type hostConfigWithoutCgroupNamespace struct {
	*containertypes.HostConfig

	CgroupnsMode containertypes.CgroupnsMode `json:"CgroupnsMode,omitempty"`
}

// CreateWorkload creates one stopped transaction-owned container. The returned
// ID remains response evidence until ProbeCreatedWorkload independently proves
// the complete runtime postcondition.
func (client *Client) CreateWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	options application.WorkloadCreateOptions,
) (string, error) {
	if client == nil || !validTransaction(transaction) {
		return "", ErrUnsupportedWorkload
	}

	body, valid := dockerCreateConfiguration(workload, transaction, options)
	if !valid {
		return "", ErrUnsupportedWorkload
	}
	encoded, valid := encodeDockerCreateRequest(body, client.protocol)
	if !valid || client.CheckWorkload(workload) != nil {
		return "", ErrUnsupportedWorkload
	}
	// CheckWorkload proved the immutable client version above.
	path, _ := client.apiPath("/containers/create")

	response, err := client.containerCreateRequest(ctx, path, workload.ContainerName, encoded)
	if err != nil {
		return "", err
	}

	return decodeContainerCreateResponse(response)
}

func encodeDockerCreateRequest(request containertypes.CreateRequest, protocol apiVersion) ([]byte, bool) {
	if supportsCgroupNamespace(protocol) {
		encoded, _ := json.Marshal(request) //nolint:errchkjson // The Docker wire type contains only JSON-supported values.

		return encoded, true
	}
	if request.HostConfig != nil && request.HostConfig.CgroupnsMode != "" {
		return nil, false
	}

	wireRequest := createRequestWithoutCgroupNamespace{
		CreateRequest: request,
	}
	if request.HostConfig != nil {
		wireRequest.HostConfig = &hostConfigWithoutCgroupNamespace{
			HostConfig:   request.HostConfig,
			CgroupnsMode: "",
		}
	}
	encoded, _ := json.Marshal(wireRequest) //nolint:errchkjson // The Docker wire type has only JSON-supported values.

	return encoded, true
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
	return client.probeWorkloadEffect(
		ctx,
		workload,
		transaction,
		responseID,
	)
}

// StartWorkload starts the exact stopped container owned by transaction.
func (client *Client) StartWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) error {
	probe, err := client.ProbeCreatedWorkload(ctx, workload, transaction, "")
	if err != nil {
		return err
	}

	if probe.State != application.WorkloadEffectProbeObserved ||
		!probe.Workload.ConfigurationMatches ||
		probe.Workload.Lifecycle != application.WorkloadLifecycleCreated ||
		!startedWorkloadOwnershipMatches(probe.Workload, workload, transaction) {
		return ErrProtocol
	}

	// ProbeCreatedWorkload proved the immutable client version above.
	path, _ := client.apiPath("/containers/" + probe.Workload.ID + "/start")

	response, err := client.request(ctx, http.MethodPost, path)
	if err != nil {
		return err
	}

	return decodeContainerNoContentResponse(response)
}

// ProbeStartedWorkload proves the exact transaction-owned workload is running.
func (client *Client) ProbeStartedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (application.WorkloadEffectProbe, error) {
	return client.probeWorkloadEffect(
		ctx,
		workload,
		transaction,
		"",
	)
}

func (client *Client) probeWorkloadEffect(
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

	expectation := applicationContainerExpectation(workload, transaction, responseID, selected.State)
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
			Health:               selected.Health,
			Ownership:            selected.Ownership,
		},
	}, nil
}

func decodeContainerNoContentResponse(response *http.Response) error {
	if response == nil || response.Body == nil {
		return ErrProtocol
	}

	if response.StatusCode != http.StatusNoContent || response.ContentLength > 0 {
		closeResponse(response)

		return ErrProtocol
	}

	body, readErr := io.ReadAll(io.LimitReader(response.Body, 1))
	closeErr := response.Body.Close()
	if readErr != nil || closeErr != nil {
		return ErrUnavailable
	}

	if len(body) != 0 {
		return ErrProtocol
	}

	return nil
}

func startedWorkloadOwnershipMatches(
	evidence application.WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
) bool {
	return evidence.Ownership.Matches(
		workload.ServiceName,
		transaction,
		workload.EffectiveDigest,
		workload.Image.ReferenceDigest,
	) && evidence.Ownership.ImageConfig == workload.Image.ImageConfig &&
		evidence.Ownership.PlatformManifest == workload.Image.PlatformManifest
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

	path, valid := client.apiPath("/containers/json")
	if !valid {
		return "", false, ErrProtocol
	}

	response, err := client.requestQuery(ctx, http.MethodGet, path, url.Values{
		"all":     {dockerQueryTrue},
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
	allowedState ContainerState,
) ContainerExpectation {
	return ContainerExpectation{
		ID:               responseID,
		Name:             workload.ContainerName,
		ImageReference:   workload.Image.Reference,
		ImageConfig:      workload.Image.ImageConfig,
		PlatformManifest: workload.Image.PlatformManifest,
		WorkloadSpec:     workload.Clone(),
		Service:          workload.ServiceName,
		Transaction:      transaction,
		DesiredState:     workload.EffectiveDigest,
		Reference:        workload.Image.ReferenceDigest,
		AllowedStates:    []ContainerState{allowedState},
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
		ConfigurationDigest:  domain.Digest{},
		StorageDigest:        domain.Digest{},
		RuntimeMounts:        nil,
		ConfigurationMatches: false,
		Lifecycle:            application.WorkloadLifecycleUnknown,
		Health:               application.WorkloadHealth{},
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
