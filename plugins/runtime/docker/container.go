package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"reflect"
	"slices"
	"strings"
	"time"
	"unicode"
	"unicode/utf8"

	"github.com/moby/moby/api/types/common"
	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	maximumContainerNameBytes  = 63
	maximumImageReferenceBytes = 2048
	maximumNetworkModeBytes    = 256
	maximumJSONErrorBytes      = 4096
	containerIDHexBytes        = 64
	containerHostnameIDBytes   = 12
	dockerManifestMediaType    = "application/vnd.docker.distribution.manifest.v2+json"
	ociManifestMediaType       = "application/vnd.oci.image.manifest.v1+json"
	containerNameQueryKey      = "name"
	rawJSONNull                = "null"
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
	StartedAt        time.Time
	ImageReference   string
	ImageConfig      domain.Digest
	PlatformManifest domain.Digest
	WorkloadSpec     domain.WorkloadSpec
	RuntimeMounts    []domain.RuntimeMount
	State            ContainerState
	Health           application.WorkloadHealth
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
	WorkloadSpec     domain.WorkloadSpec
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
	if probe.State != ContainerProbeObserved || !probe.Container.matchesConfiguration(expectation) ||
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

func (container Container) matchesConfiguration(expectation ContainerExpectation) bool {
	baseMatches := container.Name == expectation.Name && container.ImageReference == expectation.ImageReference &&
		container.ImageConfig == expectation.ImageConfig &&
		container.PlatformManifest == expectation.PlatformManifest
	if !baseMatches {
		return false
	}

	observed := container.WorkloadSpec.Clone()
	if len(container.ID) < containerHostnameIDBytes {
		return false
	}
	if expectation.WorkloadSpec.Hostname == "" &&
		observed.Hostname == container.ID[:containerHostnameIDBytes] {
		observed.Hostname = ""
	}

	return expectation.WorkloadSpec.ContainerName != "" &&
		dockerConfigurationMatches(observed, expectation.WorkloadSpec)
}

// ProbeContainer inspects one exact full ID or maniud-managed name. A valid 404
// proves absence; transport and protocol failures leave the outcome unknown.
func (client *Client) ProbeContainer(ctx context.Context, reference string) (ContainerProbe, error) {
	var unknown ContainerProbe

	if !validContainerReference(reference) {
		return unknown, ErrInvalidContainerReference
	}

	path, valid := client.apiPath("/containers/" + reference + "/json")
	if !valid {
		return unknown, ErrProtocol
	}

	response, err := client.request(ctx, http.MethodGet, path)
	if err != nil {
		return unknown, err
	}
	defer closeResponse(response)

	if response.StatusCode == http.StatusNotFound {
		return missingContainerProbe(response)
	}

	if response.StatusCode != http.StatusOK || !isJSON(response.Header.Get(contentTypeHeader)) {
		return unknown, ErrProtocol
	}

	var payload containertypes.InspectResponse
	if !decodeContainerInspect(response.Body, &payload) {
		return unknown, ErrProtocol
	}

	observed, valid := decodeContainer(client.protocol, reference, payload)
	if !valid {
		return unknown, ErrProtocol
	}
	if !client.provesContainerImageWithoutDescriptor(ctx, observed, payload.ImageManifestDescriptor != nil) {
		return unknown, ErrProtocol
	}

	return ContainerProbe{State: ContainerProbeObserved, Container: observed}, nil
}

func decodeContainerInspect(reader io.Reader, target *containertypes.InspectResponse) bool {
	encoded, valid := jsonstrict.Read(reader, maximumJSONBytes)
	if !valid || target == nil {
		return false
	}

	var document map[string]json.RawMessage
	if json.Unmarshal(encoded, &document) != nil || !validContainerInspectEnvelope(document) {
		return false
	}

	hostConfig, found := document["HostConfig"]
	if found && !sanitizeContainerHostConfig(hostConfig, document) {
		return false
	}
	if !sanitizeContainerStateHealth(document) {
		return false
	}
	if !validContainerInspectObjects(document) {
		return false
	}

	encoded, err := json.Marshal(document)

	return err == nil && json.Unmarshal(encoded, target) == nil
}

func validContainerInspectEnvelope(document map[string]json.RawMessage) bool {
	if document == nil {
		return false
	}
	for name := range document {
		if !supportedJSONField(reflect.TypeFor[containertypes.InspectResponse](), nil, name) {
			return false
		}
	}

	return true
}

func validContainerInspectObjects(document map[string]json.RawMessage) bool {
	objects := []struct {
		name          string
		schema        reflect.Type
		compatibility []string
	}{
		{name: "Config", schema: reflect.TypeFor[containertypes.Config](), compatibility: []string{"MacAddress"}},
		{name: "State", schema: reflect.TypeFor[containertypes.State]()},
		{
			name:   "NetworkSettings",
			schema: reflect.TypeFor[containertypes.NetworkSettings](),
			compatibility: []string{
				"Bridge", "HairpinMode", "LinkLocalIPv6Address", "LinkLocalIPv6PrefixLen",
				"SecondaryIPAddresses", "SecondaryIPv6Addresses", "EndpointID", "Gateway",
				"GlobalIPv6Address", "GlobalIPv6PrefixLen", "IPAddress", "IPPrefixLen",
				"IPv6Gateway", "MacAddress",
			},
		},
	}
	for _, object := range objects {
		encoded, found := document[object.name]
		if found && !validJSONFields(encoded, object.schema, object.compatibility) {
			return false
		}
	}

	return validJSONArrayFields(document["Mounts"], reflect.TypeFor[containertypes.MountPoint]())
}

func sanitizeContainerStateHealth(document map[string]json.RawMessage) bool {
	encoded, found := document["State"]
	if !found || strings.TrimSpace(string(encoded)) == rawJSONNull {
		return true
	}

	var state map[string]json.RawMessage
	if json.Unmarshal(encoded, &state) != nil || state == nil {
		return false
	}
	health, found := state["Health"]
	if !found || strings.TrimSpace(string(health)) == rawJSONNull {
		return true
	}
	health, valid := sanitizedContainerHealth(health)
	if !valid {
		return false
	}
	state["Health"] = health
	encoded, _ = json.Marshal(state) //nolint:errchkjson // Values came from successfully decoded valid JSON.
	document["State"] = encoded

	return true
}

func sanitizedContainerHealth(encoded json.RawMessage) (json.RawMessage, bool) {
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || fields == nil {
		return nil, false
	}
	for name := range fields {
		if name != "Status" && name != "FailingStreak" && name != "Log" {
			return nil, false
		}
	}
	if log, present := fields["Log"]; present {
		var entries []json.RawMessage
		if json.Unmarshal(log, &entries) != nil {
			return nil, false
		}
		delete(fields, "Log")
	}

	sanitized, _ := json.Marshal(fields) //nolint:errchkjson // Values came from successfully decoded valid JSON.

	return sanitized, true
}

func validJSONFields(encoded json.RawMessage, schema reflect.Type, compatibility []string) bool {
	if encoded == nil || strings.TrimSpace(string(encoded)) == rawJSONNull {
		return true
	}

	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || fields == nil {
		return false
	}
	for name := range fields {
		if !supportedJSONField(schema, compatibility, name) {
			return false
		}
	}

	return true
}

func validJSONArrayFields(encoded json.RawMessage, schema reflect.Type) bool {
	if encoded == nil || strings.TrimSpace(string(encoded)) == rawJSONNull {
		return true
	}

	var values []json.RawMessage
	if json.Unmarshal(encoded, &values) != nil || values == nil {
		return false
	}
	for _, value := range values {
		if !validJSONFields(value, schema, nil) {
			return false
		}
	}

	return true
}

func sanitizeContainerHostConfig(encoded json.RawMessage, document map[string]json.RawMessage) bool {
	var fields map[string]json.RawMessage
	if json.Unmarshal(encoded, &fields) != nil || fields == nil {
		return false
	}

	compatibilityFields := []string{"Capabilities", "KernelMemory", "KernelMemoryTCP"}
	for name := range fields {
		if !supportedJSONField(reflect.TypeFor[containertypes.HostConfig](), compatibilityFields, name) {
			return false
		}
	}
	for _, name := range compatibilityFields {
		delete(fields, name)
	}

	var err error
	document["HostConfig"], err = json.Marshal(fields)

	return err == nil
}

func missingContainerProbe(response *http.Response) (ContainerProbe, error) {
	var unknown ContainerProbe
	if !validErrorResponse(response) {
		return unknown, ErrProtocol
	}

	var emptyContainer Container

	return ContainerProbe{State: ContainerProbeMissing, Container: emptyContainer}, nil
}

func (client *Client) provesContainerImageWithoutDescriptor(
	ctx context.Context,
	observed Container,
	hasDescriptor bool,
) bool {
	if hasDescriptor || observed.Ownership.Status != domain.OwnershipManaged {
		return true
	}

	platform, platformValid := dockerPlatform(client.version.OS, client.version.Architecture)
	expected, valid := containerImageWithoutDescriptor(observed, platform)
	if !platformValid || !valid {
		return false
	}

	probe, err := client.ProbeImage(ctx, expected)

	return observedImageMatches(probe, expected, err)
}

func validErrorResponse(response *http.Response) bool {
	if !isJSON(response.Header.Get(contentTypeHeader)) {
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

func decodeContainer(
	protocol apiVersion,
	reference string,
	payload containertypes.InspectResponse,
) (Container, bool) {
	var emptyContainer Container

	if !validContainerPayload(protocol, reference, payload) {
		return emptyContainer, false
	}

	imageTarget, imageErr := domain.ParseDigest(payload.Image)
	platformManifest, manifestValid := containerPlatformManifest(protocol, payload)

	state, stateValid := decodeContainerState(payload.State)
	workloadSpec, workloadValid := dockerWorkloadFromInspect(
		strings.TrimPrefix(payload.Name, "/"),
		payload.Config,
		payload.HostConfig,
	)
	runtimeMounts, runtimeMountsValid := dockerRuntimeMounts(payload.Mounts, workloadSpec)
	health, healthValid := dockerWorkloadHealth(payload.State.Health, workloadSpec.Healthcheck)
	startedAt, startedAtValid := dockerStartedAt(payload.State.StartedAt)
	if imageErr != nil || !manifestValid ||
		!stateValid || !workloadValid || !runtimeMountsValid || !healthValid || !startedAtValid {
		return emptyContainer, false
	}

	ownership, imageConfig := decodeContainerOwnership(
		payload.Config.Labels,
		payload.Config.Image,
		imageTarget,
		platformManifest,
	)

	return Container{
		ID:               payload.ID,
		Name:             strings.TrimPrefix(payload.Name, "/"),
		StartedAt:        startedAt,
		ImageReference:   payload.Config.Image,
		ImageConfig:      imageConfig,
		PlatformManifest: platformManifest,
		WorkloadSpec:     workloadSpec,
		RuntimeMounts:    runtimeMounts,
		State:            state,
		Health:           health,
		Ownership:        ownership,
	}, true
}

func dockerStartedAt(value string) (time.Time, bool) {
	if value == "" {
		return time.Time{}, true
	}
	startedAt, err := time.Parse(time.RFC3339Nano, value)
	if err != nil {
		return time.Time{}, false
	}

	return startedAt.UTC(), true
}

func containerPlatformManifest(
	protocol apiVersion,
	payload containertypes.InspectResponse,
) (domain.Digest, bool) {
	if payload.ImageManifestDescriptor == nil {
		manifest, err := domain.ParseDigest(payload.Config.Labels[domain.LabelPlatformManifestDigest])
		if err != nil {
			return domain.Digest{}, true
		}

		return manifest, true
	}

	if !supportsImageDescriptors(protocol) {
		return domain.Digest{}, false
	}

	manifest, err := domain.ParseDigest(payload.ImageManifestDescriptor.Digest.String())

	return manifest, err == nil &&
		validManifestDescriptor(payload.ImageManifestDescriptor.MediaType, payload.ImageManifestDescriptor.Size)
}

func containerImageWithoutDescriptor(
	container Container,
	platform domain.Platform,
) (domain.ImageIdentity, bool) {
	var empty domain.ImageIdentity
	origin := domain.ImageOriginDockerArchive
	reference, err := imageref.Parse(container.ImageReference)
	if err == nil {
		if reference.Digest() != container.Ownership.Reference {
			return empty, false
		}
		origin = domain.ImageOriginRegistry
	} else {
		source, sourceErr := imageref.Normalize(container.ImageReference)
		if sourceErr != nil || source.String() != container.ImageReference || source.IsPinned() {
			return empty, false
		}
	}

	return domain.ImageIdentity{
		Origin:           origin,
		Reference:        container.ImageReference,
		ReferenceDigest:  container.Ownership.Reference,
		PlatformManifest: container.Ownership.PlatformManifest,
		ImageConfig:      container.Ownership.ImageConfig,
		Platform:         platform,
	}, true
}

func validManifestDescriptor(mediaType string, size int64) bool {
	return size > 0 && (mediaType == ociManifestMediaType || mediaType == dockerManifestMediaType)
}

func validContainerPayload(
	protocol apiVersion,
	reference string,
	payload containertypes.InspectResponse,
) bool {
	if payload.State == nil || payload.Config == nil || payload.HostConfig == nil ||
		!validContainerDescriptor(protocol, payload.ImageManifestDescriptor != nil) {
		return false
	}

	name := strings.TrimPrefix(payload.Name, "/")

	return validContainerID(payload.ID) && validContainerName(name) && payload.Name == "/"+name &&
		matchesContainerReference(reference, payload) &&
		validOpaqueValue(payload.Config.Image, maximumImageReferenceBytes) &&
		validContainerHostConfig(protocol, payload.HostConfig)
}

func validContainerDescriptor(protocol apiVersion, present bool) bool {
	return !present || supportsImageDescriptors(protocol)
}

func validContainerHostConfig(protocol apiVersion, config *containertypes.HostConfig) bool {
	return validOpaqueValue(string(config.NetworkMode), maximumNetworkModeBytes) &&
		(supportsCgroupNamespace(protocol) || config.CgroupnsMode == "" ||
			config.CgroupnsMode == containertypes.CgroupnsModePrivate) &&
		validRestartPolicy(config.RestartPolicy)
}

func validRestartPolicy(policy containertypes.RestartPolicy) bool {
	return containertypes.ValidateRestartPolicy(policy) == nil
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
