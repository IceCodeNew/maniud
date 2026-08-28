package podman

import (
	"context"
	"errors"
	"net/http"
	"net/url"
	"slices"
	"strings"

	podmanconfig "github.com/IceCodeNew/maniud/containerconfig/podman"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

const (
	podmanExecutionEvidenceVersion = 1
	podmanConfigurationVersion     = 1
	podmanQueryFalse               = "false"
	podmanQueryTrue                = "true"
	maximumContainerNameBytes      = 63
	maximumOwnershipValueBytes     = 128
	containerIDHexBytes            = 64
	maniudLabelPrefix              = "io.maniud."
)

var (
	// ErrInvalidContainerReference reports a name or ID outside maniud's exact lookup grammar.
	ErrInvalidContainerReference = errors.New("podman container reference is invalid")

	_ application.Runtime                   = (*Client)(nil)
	_ application.ImageRuntime              = (*Client)(nil)
	_ application.WorkloadEffectRuntime     = (*Client)(nil)
	_ application.WorkloadStartRuntime      = (*Client)(nil)
	_ application.WorkloadTransitionRuntime = (*Client)(nil)
	_ application.WorkloadDiscardRuntime    = (*Client)(nil)
	_ application.WorkloadArchiveRuntime    = (*Client)(nil)
)

// ContainerState is the normalized native Libpod lifecycle used by typed probes.
type ContainerState = podmanconfig.State

const (
	// ContainerStateUnknown is the fail-closed zero value.
	ContainerStateUnknown = podmanconfig.StateUnknown
	// ContainerCreated has not started.
	ContainerCreated = podmanconfig.StateCreated
	// ContainerRunning is currently executing.
	ContainerRunning = podmanconfig.StateRunning
	// ContainerPaused has suspended processes.
	ContainerPaused = podmanconfig.StatePaused
	// ContainerRemoving is in a transitional delete or stop state.
	ContainerRemoving = podmanconfig.StateRemoving
	// ContainerExited has stopped after starting.
	ContainerExited = podmanconfig.StateExited
)

// Container is runtime-neutral evidence decoded from one native Libpod inspect response.
type Container struct {
	ID               string
	Name             string
	ImageReference   string
	ImageConfig      domain.Digest
	PlatformManifest domain.Digest
	WorkloadSpec     domain.WorkloadSpec
	RuntimeMounts    []domain.RuntimeMount
	State            ContainerState
	Ownership        domain.WorkloadOwnership
}

// ContainerProbeState separates proven absence and observation from an unknown zero value.
type ContainerProbeState uint8

const (
	// ContainerProbeUnknown is returned only with an error.
	ContainerProbeUnknown ContainerProbeState = iota
	// ContainerProbeMissing proves a valid native 404 for the exact reference.
	ContainerProbeMissing
	// ContainerProbeObserved carries one strictly decoded inspect snapshot.
	ContainerProbeObserved
)

// ContainerProbe is one read-only native container conclusion.
type ContainerProbe struct {
	State     ContainerProbeState
	Container Container
}

// Inspect returns the pinned Podman daemon scope and platform.
func (client *Client) Inspect(context.Context) (application.RuntimeEvidence, error) {
	var empty application.RuntimeEvidence

	if !client.negotiated() || client.scope == (domain.Digest{}) {
		return empty, ErrProtocol
	}
	platform, valid := podmanPlatform(client.version.OS, client.version.Architecture)
	if !valid {
		return empty, ErrUnsupportedWorkload
	}

	return application.RuntimeEvidence{
		Kind: domain.RuntimePodman, Platform: platform, Digest: podmanExecutionDigest(client),
	}, nil
}

func podmanExecutionDigest(client *Client) domain.Digest {
	evidence := []byte{podmanExecutionEvidenceVersion}
	evidence = appendPodmanString(evidence, domain.RuntimePodman.String())
	evidence = append(evidence, client.scope[:]...)
	evidence = appendPodmanString(evidence, client.version.Protocol)
	evidence = appendPodmanString(evidence, client.version.OS)
	evidence = appendPodmanString(evidence, client.version.Architecture)

	return domain.Hash(evidence)
}

// CheckWorkload validates one workload against the fixed native Libpod contract.
func (client *Client) CheckWorkload(workload domain.DesiredWorkload) error {
	if !validPodmanWorkload(client, workload) {
		return ErrUnsupportedWorkload
	}

	return nil
}

func validPodmanWorkload(client *Client, workload domain.DesiredWorkload) bool {
	if !client.negotiated() || !validPodmanImage(client, workload.Image) || !validDesiredWorkload(workload) {
		return false
	}
	if client.protocol.major == 4 &&
		(len(workload.Entrypoint) > 1 || len(workload.Entrypoint) == 1 && strings.Contains(workload.Entrypoint[0], " ")) {
		return false
	}

	return podmanconfig.Validate(workload.WorkloadSpec, podmanconfig.CreateOptions{
		ImageReference: workload.Image.Reference, CopyImageVolumes: true,
	}) == nil
}

func validPodmanImage(client *Client, image domain.ImageIdentity) bool {
	if image.Origin != domain.ImageOriginRegistry || image.ReferenceDigest == (domain.Digest{}) ||
		image.PlatformManifest == (domain.Digest{}) || image.ImageConfig == (domain.Digest{}) {
		return false
	}
	reference, err := parseExpectedImageReference(image)
	platform, valid := podmanPlatform(client.version.OS, client.version.Architecture)

	return err == nil && valid && reference.Digest() == image.ReferenceDigest && image.Platform == platform
}

func validDesiredWorkload(workload domain.DesiredWorkload) bool {
	return workload.SourceDigest != (domain.Digest{}) && workload.EffectiveDigest != (domain.Digest{}) &&
		workload.EffectiveDigest == domain.ComputeEffectiveDigest(workload) &&
		workload.Platform == workload.Image.Platform && len(workload.Entrypoint)+len(workload.Command) > 0
}

// ObserveWorkload maps one exact native probe into application planning evidence.
func (client *Client) ObserveWorkload(
	ctx context.Context,
	workload domain.DesiredWorkload,
) (application.WorkloadObservation, error) {
	var empty application.WorkloadObservation

	if err := client.CheckWorkload(workload); err != nil {
		return empty, err
	}
	probe, err := client.ProbeContainer(ctx, workload.ContainerName)
	if err != nil {
		return empty, err
	}

	return podmanWorkloadObservation(probe, workload, client.version.Protocol)
}

func podmanWorkloadObservation(
	probe ContainerProbe,
	workload domain.DesiredWorkload,
	apiVersion string,
) (application.WorkloadObservation, error) {
	var empty application.WorkloadObservation

	switch probe.State {
	case ContainerProbeMissing:
		return application.WorkloadObservation{State: application.WorkloadObservationMissing}, nil
	case ContainerProbeObserved:
		storageDigest, valid := domain.ComputeStorageDigest(workload, probe.Container.RuntimeMounts)
		if !valid {
			return empty, ErrProtocol
		}

		return application.WorkloadObservation{
			ID:                   probe.Container.ID,
			State:                application.WorkloadObservationPresent,
			ConfigurationDigest:  containerConfigurationDigest(probe.Container),
			StorageDigest:        storageDigest,
			RuntimeMounts:        slices.Clone(probe.Container.RuntimeMounts),
			ConfigurationMatches: containerConfigurationMatches(probe.Container, workload, apiVersion),
			Running:              probe.Container.State == ContainerRunning,
			Ownership:            probe.Container.Ownership,
		}, nil
	case ContainerProbeUnknown:
		return empty, ErrProtocol
	default:
		return empty, ErrProtocol
	}
}

// ProbeContainer inspects one exact full ID or supported container name. Only
// a well-formed native ErrorModel 404 proves absence.
func (client *Client) ProbeContainer(ctx context.Context, reference string) (ContainerProbe, error) {
	var unknown ContainerProbe

	if !validContainerReference(reference) {
		return unknown, ErrInvalidContainerReference
	}
	path := client.apiPath("/containers/" + reference + "/json")
	response, err := client.request(
		ctx,
		http.MethodGet,
		path,
		url.Values{"size": {podmanQueryFalse}},
		nil,
		false,
	)
	if err != nil {
		return unknown, err
	}
	defer closePodmanResponse(response)

	if response.StatusCode == http.StatusNotFound {
		if !decodePodmanNotFound(response) {
			return unknown, ErrProtocol
		}

		return ContainerProbe{State: ContainerProbeMissing}, nil
	}
	if response.StatusCode != http.StatusOK || !isPodmanJSON(response.Header.Get(podmanContentType)) {
		return unknown, ErrProtocol
	}

	inspection, decodeErr := podmanconfig.DecodeInspect(
		response.Body,
		maximumControlBytes,
		client.version.Protocol,
	)
	if decodeErr != nil {
		return unknown, ErrProtocol
	}
	container, valid := podmanContainerFromInspection(reference, inspection)
	if !valid {
		return unknown, ErrProtocol
	}

	return ContainerProbe{State: ContainerProbeObserved, Container: container}, nil
}

func validContainerReference(value string) bool {
	return validContainerID(value) || validContainerName(value)
}

func validContainerID(value string) bool {
	if len(value) != containerIDHexBytes {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				return false
			}
		}
	}

	return true
}

func validContainerName(value string) bool {
	if len(value) == 0 || len(value) > maximumContainerNameBytes || !alphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !alphaNumeric(value[index]) && value[index] != '.' && value[index] != '_' && value[index] != '-' {
			return false
		}
	}

	return true
}

func alphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func containerConfigurationMatches(
	container Container,
	workload domain.DesiredWorkload,
	apiVersion string,
) bool {
	observedReference, observedErr := imageref.Parse(container.ImageReference)
	expectedReference, expectedErr := parseExpectedImageReference(workload.Image)
	if observedErr != nil || expectedErr != nil ||
		observedReference.DigestReference() != expectedReference.DigestReference() ||
		container.Name != workload.ContainerName ||
		container.ImageConfig != workload.Image.ImageConfig ||
		container.PlatformManifest != workload.Image.PlatformManifest {
		return false
	}

	return podmanconfig.Equivalent(container.WorkloadSpec, workload.WorkloadSpec, apiVersion)
}

func containerConfigurationDigest(container Container) domain.Digest {
	evidence := []byte{podmanConfigurationVersion}
	evidence = appendPodmanString(evidence, container.ImageReference)
	evidence = append(evidence, container.ImageConfig[:]...)
	evidence = append(evidence, container.PlatformManifest[:]...)
	configuration := domain.ComputeWorkloadSpecDigest(container.WorkloadSpec)
	evidence = append(evidence, configuration[:]...)

	return domain.Hash(evidence)
}

//nolint:cyclop // Every ownership label is required and verified independently.
func decodeOwnership(
	labels map[string]string,
	imageConfig domain.Digest,
	referenceDigest domain.Digest,
	observedDigest domain.Digest,
) domain.WorkloadOwnership {
	if !hasManiudLabel(labels) {
		return domain.WorkloadOwnership{Status: domain.OwnershipUnmanaged}
	}
	if !supportedOwnershipLabels(labels) {
		return domain.WorkloadOwnership{}
	}
	desired, desiredErr := domain.ParseDigest(labels[domain.LabelDesiredStateDigest])
	labeledReference, referenceErr := domain.ParseDigest(labels[domain.LabelReferenceDigest])
	labeledImage, imageErr := domain.ParseDigest(labels[domain.LabelImageConfigDigest])
	labeledManifest, manifestErr := domain.ParseDigest(labels[domain.LabelPlatformManifestDigest])
	service := labels[domain.LabelService]
	transaction := labels[domain.LabelTransaction]
	if desiredErr != nil || referenceErr != nil || imageErr != nil || manifestErr != nil ||
		labeledImage != imageConfig || labeledReference != referenceDigest ||
		(observedDigest != labeledReference && observedDigest != labeledManifest) ||
		!validOwnershipName(service) || !validOwnershipName(transaction) {
		return domain.WorkloadOwnership{}
	}

	return domain.WorkloadOwnership{
		Status: domain.OwnershipManaged, Service: service, Transaction: transaction,
		DesiredState: desired, Reference: labeledReference,
		ImageConfig: labeledImage, PlatformManifest: labeledManifest,
	}
}

func hasManiudLabel(labels map[string]string) bool {
	for key := range labels {
		if strings.HasPrefix(key, maniudLabelPrefix) {
			return true
		}
	}

	return false
}

func supportedOwnershipLabels(labels map[string]string) bool {
	required := map[string]bool{
		domain.LabelService: false, domain.LabelTransaction: false,
		domain.LabelDesiredStateDigest: false, domain.LabelReferenceDigest: false,
		domain.LabelImageConfigDigest: false, domain.LabelPlatformManifestDigest: false,
	}
	for key := range labels {
		if _, found := required[key]; found {
			required[key] = true

			continue
		}
		if strings.HasPrefix(key, maniudLabelPrefix) {
			return false
		}
	}
	for _, present := range required {
		if !present {
			return false
		}
	}

	return true
}

func validOwnershipName(value string) bool {
	if len(value) == 0 || len(value) > maximumOwnershipValueBytes || !alphaNumeric(value[0]) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !alphaNumeric(value[index]) && value[index] != '.' && value[index] != '_' && value[index] != '-' {
			return false
		}
	}

	return true
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
