package podman

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/url"
	"slices"
	"strconv"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

type podmanContainerListFilters struct {
	Labels []string `json:"label"`
}

type podmanContainerListEntry struct {
	ID string `json:"Id"` //nolint:tagliatelle // Native Libpod wire field.
}

type podmanContainerCreateResponse struct {
	ID       string   `json:"Id"`       //nolint:tagliatelle // Native Libpod wire field.
	Warnings []string `json:"Warnings"` //nolint:tagliatelle // Native Libpod wire field.
}

// CreateWorkload creates one stopped transaction-owned container. The response
// ID remains untrusted until ProbeCreatedWorkload independently observes it.
func (client *Client) CreateWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	options application.WorkloadCreateOptions,
) (string, error) {
	if client == nil || client.CheckWorkload(workload) != nil || !validOwnershipName(transaction) {
		return "", ErrUnsupportedWorkload
	}
	body, valid := encodePodmanWorkload(workload, transaction, options)
	if !valid {
		return "", ErrProtocol
	}
	path := client.apiPath("/containers/create")
	response, err := client.requestWithBody( //nolint:bodyclose // decodePodmanCreateResponse consumes and closes it.
		ctx,
		http.MethodPost,
		path,
		nil,
		http.Header{podmanContentType: {podmanJSONType}},
		body,
		false,
	)
	if err != nil {
		return "", err
	}

	return decodePodmanCreateResponse(response)
}

func decodePodmanCreateResponse(response *http.Response) (string, error) {
	document, err := readPodmanJSONResponse(response, http.StatusCreated)
	if err != nil {
		return "", err
	}
	var fields map[string]json.RawMessage
	var payload podmanContainerCreateResponse
	if json.Unmarshal(document, &fields) != nil || json.Unmarshal(document, &payload) != nil {
		return "", ErrProtocol
	}
	if _, present := fields["Id"]; !present || !validContainerID(payload.ID) || len(payload.Warnings) != 0 {
		return "", ErrProtocol
	}

	return payload.ID, nil
}

// ProbeCreatedWorkload proves the create result through both the desired name
// and the transaction ownership selector.
func (client *Client) ProbeCreatedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) (application.WorkloadEffectProbe, error) {
	return client.probeWorkloadEffect(ctx, workload, transaction, responseID)
}

// StartWorkload starts the exact stopped workload owned by this transaction.
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
		!validStartedWorkload(probe.Workload, workload, transaction, application.WorkloadLifecycleCreated) {
		return ErrProtocol
	}
	path := client.apiPath("/containers/" + probe.Workload.ID + "/start")
	response, err := client.request(
		ctx,
		http.MethodPost,
		path,
		nil,
		nil,
		false,
	)
	if err != nil {
		return err
	}

	return decodePodmanEmptyResponse(response, http.StatusNoContent, http.StatusNotModified)
}

// ProbeStartedWorkload independently observes the exact transaction-owned workload.
func (client *Client) ProbeStartedWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
) (application.WorkloadEffectProbe, error) {
	return client.probeWorkloadEffect(ctx, workload, transaction, "")
}

func (client *Client) probeWorkloadEffect(
	ctx context.Context,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) (application.WorkloadEffectProbe, error) {
	var empty application.WorkloadEffectProbe
	if !validWorkloadEffectProbeInput(client, workload, transaction, responseID) {
		return empty, ErrUnsupportedWorkload
	}
	named, owned, err := client.workloadCandidates(ctx, workload, transaction)
	if err != nil {
		return empty, err
	}
	selected, found, consistent := selectPodmanContainer(named, owned)
	if !consistent {
		return empty, ErrProtocol
	}
	if !found {
		return application.WorkloadEffectProbe{State: application.WorkloadEffectProbeMissing}, nil
	}

	return application.WorkloadEffectProbe{
		State:    application.WorkloadEffectProbeObserved,
		Workload: podmanWorkloadEffectEvidence(selected, workload, client.version.Protocol),
	}, nil
}

func validWorkloadEffectProbeInput(
	client *Client,
	workload domain.DesiredWorkload,
	transaction string,
	responseID string,
) bool {
	return client != nil && client.CheckWorkload(workload) == nil && validOwnershipName(transaction) &&
		(responseID == "" || validContainerID(responseID))
}

func (client *Client) workloadCandidates(
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
	var empty ContainerProbe
	identifier, found, err := client.ownedContainerID(ctx, service, transaction)
	if err != nil {
		return empty, err
	}
	if !found {
		return ContainerProbe{State: ContainerProbeMissing}, nil
	}
	probe, err := client.ProbeContainer(ctx, identifier)
	if err != nil {
		return empty, err
	}
	if probe.State != ContainerProbeObserved ||
		probe.Container.Ownership.Service != service || probe.Container.Ownership.Transaction != transaction {
		return empty, ErrProtocol
	}

	return probe, nil
}

func (client *Client) ownedContainerID(
	ctx context.Context,
	service string,
	transaction string,
) (string, bool, error) {
	if !validOwnershipName(service) || !validOwnershipName(transaction) {
		return "", false, ErrUnsupportedWorkload
	}
	filter := []byte(`{` + strconv.Quote("label") + `:[` + strconv.Quote(domain.LabelService+"="+service) + `,` +
		strconv.Quote(domain.LabelTransaction+"="+transaction) + `]}`)
	path := client.apiPath("/containers/json")
	response, err := client.request(
		ctx,
		http.MethodGet,
		path,
		url.Values{"all": {podmanQueryTrue}, "filters": {string(filter)}},
		nil,
		false,
	)
	if err != nil {
		return "", false, err
	}
	document, err := readPodmanJSONResponse(response, http.StatusOK)
	if err != nil {
		return "", false, err
	}
	entries, valid := decodePodmanContainerList(document)
	if !valid || len(entries) > 1 {
		return "", false, ErrProtocol
	}
	if len(entries) == 0 {
		return "", false, nil
	}

	return entries[0].ID, true, nil
}

func decodePodmanContainerList(document []byte) ([]podmanContainerListEntry, bool) {
	var fields []map[string]json.RawMessage
	var entries []podmanContainerListEntry
	if json.Unmarshal(document, &fields) != nil || json.Unmarshal(document, &entries) != nil ||
		len(fields) != len(entries) {
		return nil, false
	}
	for index := range entries {
		if _, present := fields[index]["Id"]; !present || !validContainerID(entries[index].ID) {
			return nil, false
		}
	}

	return entries, true
}

//nolint:cyclop // The two independent selectors form a small exhaustive state table.
func selectPodmanContainer(named, owned ContainerProbe) (Container, bool, bool) {
	var empty Container
	switch {
	case named.State == ContainerProbeMissing && owned.State == ContainerProbeMissing:
		return empty, false, true
	case named.State == ContainerProbeObserved && owned.State == ContainerProbeMissing:
		return named.Container, true, true
	case named.State == ContainerProbeMissing && owned.State == ContainerProbeObserved:
		return owned.Container, true, true
	case named.State == ContainerProbeObserved && owned.State == ContainerProbeObserved &&
		named.Container.ID == owned.Container.ID:
		return named.Container, true, true
	default:
		return empty, false, false
	}
}

func podmanWorkloadEffectEvidence(
	container Container,
	workload domain.DesiredWorkload,
	apiVersion string,
) application.WorkloadEffectEvidence {
	storageDigest, _ := domain.ComputeStorageDigest(workload, container.RuntimeMounts)

	return application.WorkloadEffectEvidence{
		ID:                   container.ID,
		Name:                 container.Name,
		ConfigurationDigest:  containerConfigurationDigest(container),
		StorageDigest:        storageDigest,
		RuntimeMounts:        slices.Clone(container.RuntimeMounts),
		ConfigurationMatches: containerConfigurationMatches(container, workload, apiVersion),
		Lifecycle:            podmanWorkloadLifecycle(container.State),
		Ownership:            container.Ownership,
	}
}

func podmanWorkloadLifecycle(state ContainerState) application.WorkloadLifecycle {
	switch state {
	case ContainerCreated:
		return application.WorkloadLifecycleCreated
	case ContainerRunning:
		return application.WorkloadLifecycleRunning
	case ContainerPaused:
		return application.WorkloadLifecyclePaused
	case ContainerRemoving:
		return application.WorkloadLifecycleRemoving
	case ContainerExited:
		return application.WorkloadLifecycleExited
	case ContainerStateUnknown:
	}

	return application.WorkloadLifecycleUnknown
}

func validStartedWorkload(
	evidence application.WorkloadEffectEvidence,
	workload domain.DesiredWorkload,
	transaction string,
	lifecycle application.WorkloadLifecycle,
) bool {
	storageDigest, storageValid := domain.ComputeStorageDigest(workload, evidence.RuntimeMounts)

	return storageValid && evidence.StorageDigest == storageDigest &&
		evidence.ConfigurationMatches && evidence.Lifecycle == lifecycle &&
		evidence.Ownership.Matches(
			workload.ServiceName,
			transaction,
			workload.EffectiveDigest,
			workload.Image.ReferenceDigest,
		) && evidence.Ownership.ImageConfig == workload.Image.ImageConfig &&
		evidence.Ownership.PlatformManifest == workload.Image.PlatformManifest
}

func readPodmanJSONResponse(response *http.Response, status int) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, ErrProtocol
	}
	if response.StatusCode != status || !isPodmanJSON(response.Header.Get(podmanContentType)) {
		closePodmanResponse(response)

		return nil, ErrProtocol
	}
	document, valid := jsonstrict.Read(response.Body, maximumControlBytes)
	closeErr := response.Body.Close()
	if !valid {
		return nil, ErrProtocol
	}
	if closeErr != nil {
		return nil, ErrUnavailable
	}

	return document, nil
}

func decodePodmanEmptyResponse(response *http.Response, statuses ...int) error {
	if response == nil || response.Body == nil {
		return ErrProtocol
	}
	if !slices.Contains(statuses, response.StatusCode) || response.ContentLength > 0 {
		closePodmanResponse(response)

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
