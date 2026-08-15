package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
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

		response.Header().Set("Content-Type", jsonContentType)
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

func TestProbeContainerObservesUnmanagedID(t *testing.T) {
	t.Parallel()

	document := validContainerDocument(t, map[string]string{"com.example.owner": "test-owner"}, createdContainerState())
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path != "/v1.55/containers/"+testContainerID+"/json" {
			t.Errorf("path = %s", request.URL.Path)
		}

		response.Header().Set("Content-Type", jsonContentType)
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
		response.Header().Set("Content-Type", jsonContentType)
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
			body:        strings.Replace(validDocument, testContainerID, "short", 1),
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
				response.Header().Set("Content-Type", test.contentType)
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

	for _, reference := range []string{"invalid_name", strings.Repeat("g", containerIDHexBytes), "example-"} {
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
