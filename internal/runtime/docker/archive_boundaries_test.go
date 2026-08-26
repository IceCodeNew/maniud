package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

var errDockerArchiveTestTransport = errors.New("docker archive test transport failed")

func TestDockerArchiveMethodsRejectInvalidInputsAndTransportFailures(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	if _, err := (*Client)(nil).ProbeWorkloadArchivePath(
		context.Background(), workload, testTransaction, "relative",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadArchivePath(path) = %v", err)
	}
	if _, err := (*Client)(nil).ProbeWorkloadArchivePath(
		context.Background(), workload, testTransaction, "/data",
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeWorkloadArchivePath(client) = %v", err)
	}
	if _, err := (*Client)(nil).GetWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", io.Discard, 1,
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("GetWorkloadArchive(client) = %v", err)
	}
	if err := (*Client)(nil).PutWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", strings.NewReader("tar"),
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("PutWorkloadArchive(client) = %v", err)
	}
	if err := (*Client)(nil).PutWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", nil,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("PutWorkloadArchive(source) = %v", err)
	}

	client := dockerOwnedArchiveClient(t, workload, nil)
	dockerFailArchiveRequests(client)
	if _, err := client.ProbeWorkloadArchivePath(
		context.Background(), workload, testTransaction, "/data",
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeWorkloadArchivePath(transport) = %v", err)
	}
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", io.Discard, 1,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GetWorkloadArchive(transport) = %v", err)
	}
	if err := client.PutWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", strings.NewReader("tar"),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("PutWorkloadArchive(transport) = %v", err)
	}
}

func TestDockerArchiveContainerRejectsMissingAndInvalidEvidence(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	missing := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == testContainerListPath {
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, `[]`)
		}
	}))
	if _, err := missing.archiveContainer(
		context.Background(), workload, testTransaction,
	); !errors.Is(err, application.ErrArchiveConflict) {
		t.Fatalf("archiveContainer(missing) = %v", err)
	}

	invalidList := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == testContainerListPath {
			response.WriteHeader(http.StatusInternalServerError)
		}
	}))
	if _, err := invalidList.archiveContainer(
		context.Background(), workload, testTransaction,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("archiveContainer(list) = %v", err)
	}
}

func TestDockerArchiveMethodsRejectMalformedResponses(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	probeStatus := dockerOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusConflict)
	})
	if _, err := probeStatus.ProbeWorkloadArchivePath(
		context.Background(), workload, testTransaction, "/data",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadArchivePath(status) = %v", err)
	}

	probeStat := dockerOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})
	if _, err := probeStat.ProbeWorkloadArchivePath(
		context.Background(), workload, testTransaction, "/data",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadArchivePath(stat) = %v", err)
	}

	get := dockerOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, "text/plain")
		response.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(response, "tar")
	})
	if _, err := get.GetWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", io.Discard, 10,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkloadArchive(response) = %v", err)
	}

	put := dockerOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		response.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(response, `{"message":"conflict"}`)
	})
	if err := put.PutWorkloadArchive(
		context.Background(), workload, testTransaction, "/data", strings.NewReader("tar"),
	); !errors.Is(err, application.ErrArchiveConflict) {
		t.Fatalf("PutWorkloadArchive(response) = %v", err)
	}
}

func TestDockerArchiveResponseBoundaries(t *testing.T) {
	t.Parallel()

	if _, err := validateDockerArchiveResponse(nil, "/data"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("validateDockerArchiveResponse(nil) = %v", err)
	}
	errorResponse := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{contentTypeHeader: {jsonContentType}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"missing"}`)),
	}
	_, err := validateDockerArchiveResponse(errorResponse, "/data")
	if !errors.Is(err, application.ErrArchivePathMissing) {
		t.Fatalf("validateDockerArchiveResponse(status) = %v", err)
	}
	invalidStat := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			contentTypeHeader:              {dockerArchiveContentType},
			"X-Docker-Container-Path-Stat": {dockerArchiveStatHeader(t, "/other", 1)},
		},
		Body: io.NopCloser(strings.NewReader("x")),
	}
	if _, err := validateDockerArchiveResponse(invalidStat, "/data"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("validateDockerArchiveResponse(stat) = %v", err)
	}
	if err := decodeDockerArchivePutResponse(&http.Response{StatusCode: http.StatusOK}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodeDockerArchivePutResponse(body) = %v", err)
	}
	unexpected := &http.Response{
		StatusCode: http.StatusTeapot,
		Header:     http.Header{contentTypeHeader: {jsonContentType}},
		Body:       io.NopCloser(strings.NewReader(`{"message":"unexpected"}`)),
	}
	if err := dockerArchiveResponseError(unexpected); !errors.Is(err, ErrProtocol) {
		t.Fatalf("dockerArchiveResponseError(default) = %v", err)
	}
}

func TestDockerArchiveRequestBoundaries(t *testing.T) {
	t.Parallel()

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, errDockerArchiveTestTransport
	}))
	response, err := client.archiveRequest(
		context.Background(), "bad\nmethod", "/archive", nil, nil,
	)
	closeResponse(response)
	if response != nil || !errors.Is(err, ErrProtocol) {
		t.Fatalf("archiveRequest(method) = %#v, %v", response, err)
	}
	response, err = client.archiveRequest(
		context.Background(), http.MethodGet, "/archive", nil, nil,
	)
	closeResponse(response)
	if response != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("archiveRequest(transport) = %#v, %v", response, err)
	}
	cancelled, cancel := context.WithCancel(context.Background())
	cancel()
	response, err = client.archiveRequest(cancelled, http.MethodGet, "/archive", nil, nil)
	closeResponse(response)
	if response != nil || !errors.Is(err, context.Canceled) {
		t.Fatalf("archiveRequest(cancelled) = %#v, %v", response, err)
	}
}

func dockerOwnedArchiveClient(
	t *testing.T,
	workload domain.DesiredWorkload,
	archiveHandler http.HandlerFunc,
) *Client {
	t.Helper()

	return connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case testContainerListPath:
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, `[{"Id":"`+testContainerID+`"}]`)
		case "/v1.55/containers/" + testContainerID + "/json":
			response.Header().Set(contentTypeHeader, jsonContentType)
			_, _ = io.WriteString(response, validContainerDocument(
				t, workloadOwnershipLabels(workload, testTransaction), runningContainerState(),
			))
		case "/v1.55/containers/" + testContainerID + "/archive":
			if archiveHandler != nil {
				archiveHandler(response, request)
			}
		default:
			http.NotFound(response, request)
		}
	}))
}

func dockerFailArchiveRequests(client *Client) {
	base := client.httpClient.Transport
	client.httpClient.Transport = roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if strings.HasSuffix(request.URL.Path, "/archive") {
			return nil, errDockerArchiveTestTransport
		}

		return base.RoundTrip(request)
	})
}
