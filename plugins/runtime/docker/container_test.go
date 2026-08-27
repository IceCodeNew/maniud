package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

type containerResponseTest struct {
	name        string
	status      int
	contentType string
	body        string
}

func TestProbeContainerObservesManagedIdentity(t *testing.T) {
	t.Parallel()

	document := validContainerDocument(t, managedContainerLabels(), runningContainerState())
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodGet || request.URL.Path != "/v1.55/containers/"+testContainerName+"/json" {
			t.Errorf("request = %s %s", request.Method, request.URL.Path)
		}

		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, document)
	}))

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if err != nil {
		t.Fatalf("ProbeContainer() error = %v", err)
	}

	assertManagedContainerProbe(t, probe)

	expectation := ContainerExpectation{
		ID:               "",
		Name:             testContainerName,
		ImageReference:   testContainerImage,
		ImageConfig:      mustTestDigest(t, testImageConfig),
		PlatformManifest: mustTestDigest(t, testPlatformManifest),
		WorkloadSpec:     expectedTestContainerWorkloadSpec(),
		Service:          testContainerService,
		Transaction:      testTransaction,
		DesiredState:     mustTestDigest(t, testDesiredState),
		Reference:        mustTestDigest(t, testReferenceDigest),
		AllowedStates:    []ContainerState{ContainerCreated, ContainerRunning},
	}
	if !probe.Matches(expectation) {
		t.Fatal("ContainerProbe.Matches(name recovery) = false")
	}

	expectation.ID = testContainerID
	if !probe.Matches(expectation) {
		t.Fatal("ContainerProbe.Matches(exact ID) = false")
	}
}

func TestProbeContainerProvesLegacyRegistryIdentity(t *testing.T) {
	t.Parallel()
	expected := testRegistrySingleManifestIdentity(t)
	validImage := strings.Replace(
		imageInspectDocument(expected, false),
		"example.com/team/api",
		"registry.example/api",
		1,
	)
	tests := []struct {
		name           string
		containerImage string
		imageDocument  string
		valid          bool
		requests       int32
	}{
		{name: "valid", containerImage: testContainerImage, imageDocument: validImage, valid: true, requests: 2},
		{
			name:           "image config drift",
			containerImage: testContainerImage,
			imageDocument:  strings.Replace(validImage, testImageConfig, domain.Hash([]byte("drift")).String(), 1),
			valid:          false,
			requests:       2,
		},
		{
			name: "container reference spoof",
			containerImage: "registry.example/other:1@" +
				domain.Hash([]byte("other reference")).String(),
			imageDocument: validImage,
			valid:         false,
			requests:      1,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var requests atomic.Int32
			client := registryIdentityClientWithoutDescriptor(
				t, minimumAPIVersion, test.containerImage, test.imageDocument, &requests,
			)

			probe, err := client.ProbeContainer(context.Background(), testContainerName)
			if test.valid {
				if err != nil || probe.State != ContainerProbeObserved ||
					probe.Container.Ownership.Status != domain.OwnershipManaged {
					t.Fatalf("ProbeContainer(legacy) = %#v, %v", probe, err)
				}
			} else if !errors.Is(err, ErrProtocol) || probe.State != ContainerProbeUnknown {
				t.Fatalf("ProbeContainer(legacy conflict) = %#v, %v", probe, err)
			}
			if requests.Load() != test.requests {
				t.Fatalf("legacy identity requests = %d, want %d", requests.Load(), test.requests)
			}
		})
	}
}

func TestProbeContainerRejectsNonCanonicalArchiveReference(t *testing.T) {
	t.Parallel()

	var requests atomic.Int32
	client := registryIdentityClientWithoutDescriptor(
		t,
		minimumAPIVersion,
		"api:latest",
		"",
		&requests,
	)

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if !errors.Is(err, ErrProtocol) || probe.State != ContainerProbeUnknown || requests.Load() != 1 {
		t.Fatalf("ProbeContainer(non-canonical archive) = %#v, %v, requests %d", probe, err, requests.Load())
	}
}

func TestProbeContainerProvesClassicStoreIdentityOnModernAPI(t *testing.T) {
	t.Parallel()

	expected := testRegistrySingleManifestIdentity(t)
	imageDocument := strings.Replace(
		imageInspectDocument(expected, false),
		"example.com/team/api",
		"registry.example/api",
		1,
	)
	var requests atomic.Int32
	client := registryIdentityClientWithoutDescriptor(
		t,
		maximumAPIVersion,
		testContainerImage,
		imageDocument,
		&requests,
	)

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if err != nil || probe.State != ContainerProbeObserved ||
		probe.Container.Ownership.Status != domain.OwnershipManaged || requests.Load() != 2 {
		t.Fatalf("ProbeContainer(classic store) = %#v, %v, requests %d", probe, err, requests.Load())
	}
}

func TestProbeContainerProvesClassicStoreArchiveIdentity(t *testing.T) {
	t.Parallel()

	fixture := newArchiveProbeFixture(t)
	labels := managedContainerLabels()
	labels[domain.LabelReferenceDigest] = fixture.expected.ReferenceDigest.String()
	labels[domain.LabelImageConfigDigest] = fixture.expected.ImageConfig.String()
	labels[domain.LabelPlatformManifestDigest] = fixture.expected.PlatformManifest.String()
	containerDocument := containerDocumentWithoutDescriptor(
		t,
		fixture.expected.Reference,
		fixture.expected.ImageConfig.String(),
		labels,
	)
	var requests atomic.Int32
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch requests.Add(1) {
		case 1:
			if request.URL.Path != "/v1.55/containers/"+testContainerName+"/json" {
				t.Errorf("classic archive container request = %s", request.URL.String())
			}
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, containerDocument)
		case 2, 4:
			assertArchiveInspectRequest(t, request, fixture.expected)
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, archiveInspectDocument(fixture.expected, false))
		case 3:
			assertArchiveSaveRequest(t, request, fixture.expected)
			response.Header().Set(contentTypeHeader, dockerArchiveContentType)
			_, _ = response.Write(fixture.raw)
		default:
			t.Errorf("unexpected classic archive request = %s", request.URL.String())
		}
	}))

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if err != nil || probe.State != ContainerProbeObserved ||
		probe.Container.Ownership.Status != domain.OwnershipManaged || requests.Load() != 4 {
		t.Fatalf("ProbeContainer(classic archive) = %#v, %v, requests %d", probe, err, requests.Load())
	}
}

func TestProbeContainerRejectsUnprovableClassicStoreIndexIdentity(t *testing.T) {
	t.Parallel()

	expected := testImageIdentity(t)
	labels := managedContainerLabels()
	labels[domain.LabelReferenceDigest] = expected.ReferenceDigest.String()
	labels[domain.LabelImageConfigDigest] = expected.ImageConfig.String()
	labels[domain.LabelPlatformManifestDigest] = expected.PlatformManifest.String()
	containerDocument := containerDocumentWithoutDescriptor(
		t,
		expected.Reference,
		expected.ImageConfig.String(),
		labels,
	)
	var requests atomic.Int32
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		switch requests.Add(1) {
		case 1:
			_, _ = io.WriteString(response, containerDocument)
		case 2:
			_, _ = io.WriteString(response, imageInspectDocument(expected, false))
		default:
			t.Errorf("unexpected classic index request = %s", request.URL.String())
		}
	}))

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if !errors.Is(err, ErrProtocol) || probe.State != ContainerProbeUnknown || requests.Load() != 2 {
		t.Fatalf("ProbeContainer(classic index) = %#v, %v, requests %d", probe, err, requests.Load())
	}
}

func testRegistrySingleManifestIdentity(t *testing.T) domain.ImageIdentity {
	t.Helper()

	return domain.ImageIdentity{
		Origin:           domain.ImageOriginRegistry,
		Reference:        testContainerImage,
		ReferenceDigest:  mustTestDigest(t, testReferenceDigest),
		PlatformManifest: mustTestDigest(t, testReferenceDigest),
		ImageConfig:      mustTestDigest(t, testImageConfig),
		Platform:         domain.Platform{OS: testOS, Architecture: testArchitecture, Variant: ""},
	}
}

func registryIdentityClientWithoutDescriptor(
	t *testing.T,
	protocol string,
	containerImage string,
	imageDocument string,
	requests *atomic.Int32,
) *Client {
	t.Helper()

	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		requestNumber := requests.Add(1)
		response.Header().Set(contentTypeHeader, jsonContentType)
		switch requestNumber {
		case 1:
			if request.URL.Path != "/v"+protocol+"/containers/"+testContainerName+"/json" {
				t.Errorf("legacy container request = %s", request.URL.String())
			}
			_, _ = io.WriteString(response, legacyContainerDocument(t, containerImage))
		case 2:
			query := request.URL.Query()
			queryValid := len(query) == 0
			if supportsImageInspectPlatform(testAPIVersion(t, protocol)) {
				platform := domain.Platform{OS: testOS, Architecture: testArchitecture, Variant: ""}
				queryValid = len(query) == 1 && query.Get(imagePullPlatformQuery) == imagePlatform(platform)
			}
			if request.URL.Path != "/v"+protocol+"/images/registry.example/api@"+testReferenceDigest+"/json" ||
				!queryValid {
				t.Errorf("legacy identity request = %s", request.URL.String())
			}
			_, _ = io.WriteString(response, imageDocument)
		default:
			t.Errorf("unexpected legacy identity request = %s", request.URL.String())
		}
	}))
	setTestClientVersion(t, client, protocol)

	return client
}

func TestProbeContainerObservesContainerdImageStoreIdentity(t *testing.T) {
	t.Parallel()

	payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
	payload.Image = testReferenceDigest
	document, err := json.Marshal(payload)
	if err != nil {
		t.Fatal(err)
	}
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = response.Write(document)
	}))

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if err != nil {
		t.Fatalf("ProbeContainer(containerd image store) error = %v", err)
	}
	assertManagedContainerProbe(t, probe)
}

func TestProbeContainerObservesUnmanagedID(t *testing.T) {
	t.Parallel()

	document := validContainerDocument(
		t,
		map[string]string{testForeignOwnerLabel: testContainerOwner},
		createdContainerState(),
	)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1.55/containers/"+testContainerID+"/json" {
			t.Errorf("path = %s", request.URL.Path)
		}

		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, document)
	}))

	probe, err := client.ProbeContainer(context.Background(), testContainerID)
	if err != nil || probe.State != ContainerProbeObserved ||
		probe.Container.Ownership.Status != domain.OwnershipUnmanaged || probe.Container.Running {
		t.Fatalf("ProbeContainer(unmanaged) = %#v, %v", probe, err)
	}
}

func TestProbeContainerProvesMissing(t *testing.T) {
	t.Parallel()

	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		response.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(response, `{"message":"No such container: example-api"}`)
	}))

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if err != nil || probe.State != ContainerProbeMissing {
		t.Fatalf("ProbeContainer(missing) = %#v, %v", probe, err)
	}
}

func TestProbeContainerReturnsUnknownForProtocolFailures(t *testing.T) {
	t.Parallel()

	validDocument := validContainerDocument(t, managedContainerLabels(), runningContainerState())
	tests := []containerResponseTest{
		{name: "server failure", status: http.StatusInternalServerError, contentType: jsonContentType, body: `{}`},
		{name: "success content type", status: http.StatusOK, contentType: plainTextContentType, body: validDocument},
		{name: "not found content type", status: http.StatusNotFound, contentType: plainTextContentType, body: `{}`},
		{name: "not found malformed", status: http.StatusNotFound, contentType: jsonContentType, body: `{}`},
		{
			name:        "not found control message",
			status:      http.StatusNotFound,
			contentType: jsonContentType,
			body:        `{"message":"missing\ncontainer"}`,
		},
		{
			name:        "not found unknown field",
			status:      http.StatusNotFound,
			contentType: jsonContentType,
			body:        `{"message":"missing","code":404}`,
		},
		{name: "malformed inspect", status: http.StatusOK, contentType: jsonContentType, body: `{"Id":`},
		{name: "trailing inspect", status: http.StatusOK, contentType: jsonContentType, body: validDocument + `{}`},
		{
			name:        "invalid inspect semantics",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        strings.Replace(validDocument, testContainerID, testShortContainerID, 1),
		},
		{
			name:        "duplicate inspect field",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        strings.Replace(validDocument, `{"Id":`, `{"Id":"`+testContainerID+`","Id":`, 1),
		},
		{
			name:        "unknown inspect field",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        strings.TrimSuffix(validDocument, "}") + `,"Unknown":true}`,
		},
		{
			name:        "oversized inspect",
			status:      http.StatusOK,
			contentType: jsonContentType,
			body:        `{"Id":"` + strings.Repeat("a", maximumJSONBytes) + `"}`,
		},
	}
	assertContainerResponsesRejected(t, tests)
}

func assertContainerResponsesRejected(t *testing.T, tests []containerResponseTest) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(contentTypeHeader, test.contentType)
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))

			probe, err := client.ProbeContainer(context.Background(), testContainerName)
			if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrProtocol) {
				t.Fatalf("ProbeContainer() = %#v, %v; want unknown, ErrProtocol", probe, err)
			}
		})
	}
}

func TestProbeContainerContainsInputAndTransportFailures(t *testing.T) {
	t.Parallel()

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	client.version = testVersion()

	for _, reference := range []string{testInvalidContainerName, strings.Repeat("g", containerIDHexBytes), "example-"} {
		probe, err := client.ProbeContainer(context.Background(), reference)
		if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrInvalidContainerReference) {
			t.Fatalf("ProbeContainer(%q) = %#v, %v", reference, probe, err)
		}
	}

	probe, err := client.ProbeContainer(context.Background(), testContainerName)
	if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeContainer(transport) = %#v, %v", probe, err)
	}

	client.version.Protocol = ""

	probe, err = client.ProbeContainer(context.Background(), testContainerName)
	if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeContainer(invalid client) = %#v, %v", probe, err)
	}
}
