package docker

import (
	"encoding/json"
	"maps"
	"reflect"
	"slices"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	plainTextContentType  = "text/plain"
	testContainerID       = "aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testImageConfig       = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testPlatformManifest  = "sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testDesiredState      = "sha256:dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testReferenceDigest   = "sha256:eeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeeee"
	testContainerName     = "example-api"
	testContainerImage    = "registry.example/api:1@" + testReferenceDigest
	testContainerService  = "api"
	testTransaction       = "550e8400-e29b-41d4-a716-446655440000"
	testNetworkMode       = "bridge"
	testManifestMediaType = ociManifestMediaType
	testInvalidValue      = "bad"
	testOtherValue        = "other"
)

func testContainerEntrypoint() []string {
	return []string{"/usr/local/bin/api"}
}

func testContainerCommand() []string {
	return []string{"serve", "--port", "8080"}
}

//nolint:tagliatelle // Docker Engine uses exported Go field names in this response.
type containerStateFixture struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	Dead       bool   `json:"Dead"`
}

//nolint:tagliatelle // Docker Engine uses exported Go field names in this response.
type containerConfigFixture struct {
	Image      string            `json:"Image"`
	Labels     map[string]string `json:"Labels"`
	Entrypoint []string          `json:"Entrypoint"`
	Command    []string          `json:"Cmd"`
}

//nolint:tagliatelle // Docker Engine uses exported Go field names in this response.
type containerHostConfigFixture struct {
	NetworkMode string `json:"NetworkMode"`
}

//nolint:tagliatelle // OCI descriptors use lower camel case on the wire.
type descriptorFixture struct {
	MediaType string `json:"mediaType"`
	Digest    string `json:"digest"`
	Size      int64  `json:"size"`
}

//nolint:tagliatelle // Docker Engine uses exported Go field names in this response.
type containerInspectFixture struct {
	ID                      string                      `json:"Id"`
	Name                    string                      `json:"Name"`
	State                   *containerStateFixture      `json:"State"`
	Image                   string                      `json:"Image"`
	Config                  *containerConfigFixture     `json:"Config"`
	HostConfig              *containerHostConfigFixture `json:"HostConfig"`
	ImageManifestDescriptor *descriptorFixture          `json:"ImageManifestDescriptor"`
}

func validContainerDocument(
	t *testing.T,
	labels map[string]string,
	state *containerStateFixture,
) string {
	t.Helper()

	document := containerInspectFixture{
		ID:    testContainerID,
		Name:  "/" + testContainerName,
		State: state,
		Image: testImageConfig,
		Config: &containerConfigFixture{
			Image:      testContainerImage,
			Labels:     labels,
			Entrypoint: testContainerEntrypoint(),
			Command:    testContainerCommand(),
		},
		HostConfig: &containerHostConfigFixture{NetworkMode: testNetworkMode},
		ImageManifestDescriptor: &descriptorFixture{
			MediaType: testManifestMediaType,
			Digest:    testPlatformManifest,
			Size:      512,
		},
	}

	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatalf("json.Marshal(container fixture) error = %v", err)
	}

	return string(encoded)
}

func inspectPayload(t *testing.T, document string) containertypes.InspectResponse {
	t.Helper()

	var payload containertypes.InspectResponse

	err := json.Unmarshal([]byte(document), &payload)
	if err != nil {
		t.Fatalf("json.Unmarshal(container fixture) error = %v", err)
	}

	return payload
}

func managedContainerLabels() map[string]string {
	return map[string]string{
		domain.LabelService:                testContainerService,
		domain.LabelTransaction:            testTransaction,
		domain.LabelDesiredStateDigest:     testDesiredState,
		domain.LabelReferenceDigest:        testReferenceDigest,
		domain.LabelImageConfigDigest:      testImageConfig,
		domain.LabelPlatformManifestDigest: testPlatformManifest,
	}
}

func runningContainerState() *containerStateFixture {
	return &containerStateFixture{
		Status:     string(ContainerRunning),
		Running:    true,
		Paused:     false,
		Restarting: false,
		Dead:       false,
	}
}

func createdContainerState() *containerStateFixture {
	return &containerStateFixture{
		Status:     string(ContainerCreated),
		Running:    false,
		Paused:     false,
		Restarting: false,
		Dead:       false,
	}
}

func mustTestDigest(t *testing.T, value string) domain.Digest {
	t.Helper()

	digest, err := domain.ParseDigest(value)
	if err != nil {
		t.Fatalf("ParseDigest(%q) error = %v", value, err)
	}

	return digest
}

func dockerContainerState(
	status ContainerState,
	running bool,
	paused bool,
	restarting bool,
	dead bool,
) *containertypes.State {
	return &containertypes.State{ //nolint:exhaustruct // The fixture sets every field used by lifecycle validation.
		Status:     containertypes.ContainerState(status),
		Running:    running,
		Paused:     paused,
		Restarting: restarting,
		Dead:       dead,
	}
}

func cloneContainerState(state *containertypes.State) *containertypes.State {
	cloned := *state

	return &cloned
}

func cloneContainerConfig(config *containertypes.Config) *containertypes.Config {
	cloned := *config

	cloned.Labels = make(map[string]string, len(config.Labels))
	maps.Copy(cloned.Labels, config.Labels)
	cloned.Entrypoint = slices.Clone(config.Entrypoint)
	cloned.Cmd = slices.Clone(config.Cmd)

	return &cloned
}

func cloneContainerHostConfig(config *containertypes.HostConfig) *containertypes.HostConfig {
	cloned := *config

	return &cloned
}

func observedContainerProbe(t *testing.T) ContainerProbe {
	t.Helper()

	payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
	container, valid := decodeContainer(testContainerName, payload)

	if !valid {
		t.Fatal("decodeContainer(valid fixture) = false")
	}

	return ContainerProbe{State: ContainerProbeObserved, Container: container}
}

func assertManagedContainerProbe(t *testing.T, probe ContainerProbe) {
	t.Helper()

	imageConfig := mustTestDigest(t, testImageConfig)
	manifest := mustTestDigest(t, testPlatformManifest)

	want := ContainerProbe{
		State: ContainerProbeObserved,
		Container: Container{
			ID:               testContainerID,
			Name:             testContainerName,
			ImageReference:   testContainerImage,
			ImageConfig:      imageConfig,
			PlatformManifest: manifest,
			Entrypoint:       testContainerEntrypoint(),
			Command:          testContainerCommand(),
			NetworkMode:      testNetworkMode,
			State:            ContainerRunning,
			Running:          true,
			Ownership: domain.WorkloadOwnership{
				Status:           domain.OwnershipManaged,
				Service:          testContainerService,
				Transaction:      testTransaction,
				DesiredState:     mustTestDigest(t, testDesiredState),
				Reference:        mustTestDigest(t, testReferenceDigest),
				ImageConfig:      imageConfig,
				PlatformManifest: manifest,
			},
		},
	}
	if !reflect.DeepEqual(probe, want) {
		t.Fatalf("ProbeContainer() = %#v, want %#v", probe, want)
	}
}

func assertOwnershipBaseline(t *testing.T, imageConfig, manifest domain.Digest) {
	t.Helper()

	unmanaged := decodeOwnership(map[string]string{"com.example.owner": "test-owner"}, imageConfig, manifest)
	if unmanaged.Status != domain.OwnershipUnmanaged {
		t.Fatalf("decodeOwnership(unmanaged) = %#v", unmanaged)
	}

	managed := decodeOwnership(managedContainerLabels(), imageConfig, manifest)
	if managed.Status != domain.OwnershipManaged || managed.PlatformManifest != manifest {
		t.Fatalf("decodeOwnership(managed) = %#v", managed)
	}
}

func matchingContainerExpectation(t *testing.T) ContainerExpectation {
	t.Helper()

	return ContainerExpectation{
		ID:               testContainerID,
		Name:             testContainerName,
		ImageReference:   testContainerImage,
		ImageConfig:      mustTestDigest(t, testImageConfig),
		PlatformManifest: mustTestDigest(t, testPlatformManifest),
		Entrypoint:       testContainerEntrypoint(),
		Command:          testContainerCommand(),
		NetworkMode:      testNetworkMode,
		Service:          testContainerService,
		Transaction:      testTransaction,
		DesiredState:     mustTestDigest(t, testDesiredState),
		Reference:        mustTestDigest(t, testReferenceDigest),
		AllowedStates:    []ContainerState{ContainerRunning},
	}
}
