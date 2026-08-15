package docker

import (
	"context"
	"errors"
	"net/http"
	"slices"
	"strings"
	"unicode"
	"unicode/utf8"

	"github.com/moby/moby/api/types/common"
	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	maximumContainerNameBytes  = 63
	maximumImageReferenceBytes = 2048
	maximumJSONErrorBytes      = 4096
	containerIDHexBytes        = 64
	dockerManifestMediaType    = "application/vnd.docker.distribution.manifest.v2+json"
	ociManifestMediaType       = "application/vnd.oci.image.manifest.v1+json"
)

var (
	// ErrInvalidContainerReference reports a name or ID outside maniud's exact lookup grammar.
	ErrInvalidContainerReference = errors.New("docker container reference is invalid")
)

// ContainerProbeState separates proven absence and observation from an unknown zero value.
type ContainerProbeState uint8

const (
	// ContainerProbeUnknown is the fail-closed zero value used with an error.
	ContainerProbeUnknown ContainerProbeState = iota
	// ContainerProbeMissing proves the exact reference was absent.
	ContainerProbeMissing
	// ContainerProbeObserved carries one strictly decoded inspect snapshot.
	ContainerProbeObserved
)

// Container is runtime-neutral evidence decoded from one Docker inspect response.
type Container struct {
	ID               string
	Name             string
	ImageReference   string
	ImageConfig      domain.Digest
	PlatformManifest domain.Digest
	State            ContainerState
	Running          bool
	Ownership        domain.WorkloadOwnership
}

// ContainerExpectation is the immutable postcondition for a transaction-owned container.
// ID may be empty when a create response was lost and the unique name is the recovery key.
type ContainerExpectation struct {
	ID               string
	Name             string
	ImageReference   string
	ImageConfig      domain.Digest
	PlatformManifest domain.Digest
	Service          string
	Transaction      string
	DesiredState     domain.Digest
	Reference        domain.Digest
	AllowedStates    []ContainerState
}

// ContainerProbe is a read-only conclusion. Unknown is returned with an error.
type ContainerProbe struct {
	State     ContainerProbeState
	Container Container
}

// Matches reports whether an observed container proves the complete expected identity.
func (probe ContainerProbe) Matches(expectation ContainerExpectation) bool {
	if probe.State != ContainerProbeObserved || probe.Container.Name != expectation.Name ||
		probe.Container.ImageReference != expectation.ImageReference ||
		probe.Container.ImageConfig != expectation.ImageConfig ||
		probe.Container.PlatformManifest != expectation.PlatformManifest ||
		expectation.ID != "" && probe.Container.ID != expectation.ID ||
		!slices.Contains(expectation.AllowedStates, probe.Container.State) {
		return false
	}

	return probe.Container.Ownership.Matches(
		expectation.Service,
		expectation.Transaction,
		expectation.DesiredState,
		expectation.Reference,
	)
}

// ProbeContainer inspects one exact full ID or maniud-managed name. A valid 404
// proves absence; transport and protocol failures leave the outcome unknown.
func (client *Client) ProbeContainer(ctx context.Context, reference string) (ContainerProbe, error) {
	var unknown ContainerProbe

	if !validContainerReference(reference) {
		return unknown, ErrInvalidContainerReference
	}

	path, valid := client.versionedPath("/containers/" + reference + "/json")
	if !valid {
		return unknown, ErrProtocol
	}

	response, err := client.request(ctx, http.MethodGet, path)
	if err != nil {
		return unknown, err
	}
	defer closeResponse(response)

	if response.StatusCode == http.StatusNotFound {
		if !validNotFoundResponse(response) {
			return unknown, ErrProtocol
		}

		var emptyContainer Container

		return ContainerProbe{State: ContainerProbeMissing, Container: emptyContainer}, nil
	}

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get("Content-Type")) {
		return unknown, ErrProtocol
	}

	var payload containertypes.InspectResponse
	if !decodeStrictJSON(response.Body, &payload) {
		return unknown, ErrProtocol
	}

	observed, valid := decodeContainer(reference, payload)
	if !valid {
		return unknown, ErrProtocol
	}

	return ContainerProbe{State: ContainerProbeObserved, Container: observed}, nil
}

func validNotFoundResponse(response *http.Response) bool {
	if !isJSON(response.Header.Get("Content-Type")) {
		return false
	}

	var payload common.ErrorResponse

	return decodeStrictJSON(response.Body, &payload) && validErrorMessage(payload.Message)
}

func validErrorMessage(message string) bool {
	if message == "" || len(message) > maximumJSONErrorBytes || !utf8.ValidString(message) {
		return false
	}

	for _, character := range message {
		if unicode.IsControl(character) {
			return false
		}
	}

	return true
}

func decodeContainer(reference string, payload containertypes.InspectResponse) (Container, bool) {
	var emptyContainer Container

	if !validContainerPayload(reference, payload) {
		return emptyContainer, false
	}

	imageConfig, imageErr := domain.ParseDigest(payload.Image)
	platformManifest, manifestErr := domain.ParseDigest(payload.ImageManifestDescriptor.Digest.String())

	state, stateValid := decodeContainerState(payload.State)
	if imageErr != nil || manifestErr != nil ||
		!validManifestDescriptor(payload.ImageManifestDescriptor.MediaType, payload.ImageManifestDescriptor.Size) ||
		!stateValid {
		return emptyContainer, false
	}

	ownership := decodeOwnership(payload.Config.Labels, imageConfig, platformManifest)

	return Container{
		ID:               payload.ID,
		Name:             strings.TrimPrefix(payload.Name, "/"),
		ImageReference:   payload.Config.Image,
		ImageConfig:      imageConfig,
		PlatformManifest: platformManifest,
		State:            state,
		Running:          payload.State.Running,
		Ownership:        ownership,
	}, true
}

func validManifestDescriptor(mediaType string, size int64) bool {
	return size > 0 && (mediaType == ociManifestMediaType || mediaType == dockerManifestMediaType)
}

func validContainerPayload(reference string, payload containertypes.InspectResponse) bool {
	if payload.State == nil || payload.Config == nil || payload.ImageManifestDescriptor == nil {
		return false
	}

	name := strings.TrimPrefix(payload.Name, "/")

	return validContainerID(payload.ID) && validContainerName(name) && payload.Name == "/"+name &&
		matchesContainerReference(reference, payload) && validOpaqueValue(payload.Config.Image, maximumImageReferenceBytes)
}

func validContainerReference(reference string) bool {
	return validContainerID(reference) || validContainerName(reference)
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
	if len(value) == 0 || len(value) > maximumContainerNameBytes || !lowerAlphaNumeric(value[0]) ||
		!lowerAlphaNumeric(value[len(value)-1]) {
		return false
	}

	for index := 1; index < len(value)-1; index++ {
		if value[index] != '-' && !lowerAlphaNumeric(value[index]) {
			return false
		}
	}

	return true
}

func matchesContainerReference(reference string, payload containertypes.InspectResponse) bool {
	if validContainerID(reference) {
		return payload.ID == reference
	}

	return payload.Name == "/"+reference
}

func lowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}
