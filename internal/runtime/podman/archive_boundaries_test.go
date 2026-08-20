package podman

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

func TestPodmanArchiveMethodsRejectInvalidInputsAndTransportFailures(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	if _, err := (*Client)(nil).ProbeWorkloadArchivePath(
		context.Background(), workload, podmanTestTransaction, "relative",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadArchivePath(path) = %v", err)
	}
	if _, err := (*Client)(nil).ProbeWorkloadArchivePath(
		context.Background(), workload, podmanTestTransaction, "/data",
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeWorkloadArchivePath(client) = %v", err)
	}
	if _, err := (*Client)(nil).GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", io.Discard, 1,
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("GetWorkloadArchive(client) = %v", err)
	}
	if err := (*Client)(nil).PutWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", strings.NewReader("tar"),
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("PutWorkloadArchive(client) = %v", err)
	}
	if err := (*Client)(nil).PutWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", nil,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("PutWorkloadArchive(source) = %v", err)
	}

	client := podmanOwnedArchiveClient(t, workload, nil)
	failPodmanWorkloadRequests(client, func(request *http.Request) bool {
		return strings.HasSuffix(request.URL.Path, "/archive")
	})
	if _, err := client.ProbeWorkloadArchivePath(
		context.Background(), workload, podmanTestTransaction, "/data",
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeWorkloadArchivePath(transport) = %v", err)
	}
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", io.Discard, 1,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("GetWorkloadArchive(transport) = %v", err)
	}
	if err := client.PutWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", strings.NewReader("tar"),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("PutWorkloadArchive(transport) = %v", err)
	}
}

func TestPodmanArchiveContainerRejectsMissingAndInvalidEvidence(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	missing := connectedPodmanWorkloadClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == libpodPrefix+"/containers/json" {
			writePodmanJSON(response, `[]`)
		}
	})
	if _, err := missing.archiveContainer(
		context.Background(), workload, podmanTestTransaction,
	); !errors.Is(err, application.ErrArchiveConflict) {
		t.Fatalf("archiveContainer(missing) = %v", err)
	}

	invalidList := connectedPodmanWorkloadClient(t, func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == libpodPrefix+"/containers/json" {
			response.WriteHeader(http.StatusInternalServerError)
		}
	})
	if _, err := invalidList.archiveContainer(
		context.Background(), workload, podmanTestTransaction,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("archiveContainer(list) = %v", err)
	}
}

func TestPodmanArchiveMethodsRejectMalformedResponses(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	probeStatus := podmanOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusConflict)
	})
	if _, err := probeStatus.ProbeWorkloadArchivePath(
		context.Background(), workload, podmanTestTransaction, "/data",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadArchivePath(status) = %v", err)
	}

	probeStat := podmanOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.WriteHeader(http.StatusOK)
	})
	if _, err := probeStat.ProbeWorkloadArchivePath(
		context.Background(), workload, podmanTestTransaction, "/data",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadArchivePath(stat) = %v", err)
	}

	get := podmanOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(podmanContentType, "text/plain")
		response.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(response, "tar")
	})
	if _, err := get.GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", io.Discard, 10,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("GetWorkloadArchive(response) = %v", err)
	}

	put := podmanOwnedArchiveClient(t, workload, func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(podmanContentType, podmanJSONType)
		response.WriteHeader(http.StatusBadRequest)
		_, _ = io.WriteString(response,
			`{"cause":"conflict","message":"conflict","response":400}`)
	})
	if err := put.PutWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", strings.NewReader("tar"),
	); !errors.Is(err, application.ErrArchiveConflict) {
		t.Fatalf("PutWorkloadArchive(response) = %v", err)
	}
}

func TestPodmanArchiveProtocolBoundaries(t *testing.T) {
	t.Parallel()

	if _, err := validatePodmanArchiveResponse(nil, "/data"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("validatePodmanArchiveResponse(nil) = %v", err)
	}
	errorResponse := &http.Response{
		StatusCode: http.StatusNotFound,
		Header:     http.Header{podmanContentType: {podmanJSONType}},
		Body: io.NopCloser(strings.NewReader(
			`{"cause":"missing","message":"missing","response":404}`)),
	}
	_, err := validatePodmanArchiveResponse(errorResponse, "/data")
	if !errors.Is(err, application.ErrArchivePathMissing) {
		t.Fatalf("validatePodmanArchiveResponse(status) = %v", err)
	}
	invalidStat := &http.Response{
		StatusCode: http.StatusOK,
		Header: http.Header{
			podmanContentType:              {podmanArchiveContentType},
			"X-Docker-Container-Path-Stat": {podmanArchiveStatHeader(t, "/other", 1)},
		},
		Body: io.NopCloser(strings.NewReader("x")),
	}
	if _, err := validatePodmanArchiveResponse(invalidStat, "/data"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("validatePodmanArchiveResponse(stat) = %v", err)
	}

	for _, response := range []*http.Response{
		nil,
		{StatusCode: http.StatusBadRequest},
		{
			StatusCode: http.StatusBadRequest,
			Header:     http.Header{podmanContentType: {podmanJSONType}},
			Body:       io.NopCloser(strings.NewReader(`{"response":400}`)),
		},
	} {
		if err := podmanArchiveResponseError(response); !errors.Is(err, ErrProtocol) {
			t.Fatalf("podmanArchiveResponseError(invalid) = %v", err)
		}
	}
	unexpected := &http.Response{
		StatusCode: http.StatusTeapot,
		Header:     http.Header{podmanContentType: {podmanJSONType}},
		Body: io.NopCloser(strings.NewReader(
			`{"cause":"unexpected","message":"unexpected","response":418}`)),
	}
	if err := podmanArchiveResponseError(unexpected); !errors.Is(err, ErrProtocol) {
		t.Fatalf("podmanArchiveResponseError(default) = %v", err)
	}
}

func TestPodmanGetWorkloadArchiveRejectsSocketMutationDuringStream(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	client := podmanOwnedArchiveClient(t, workload, nil)
	base := client.httpClient.Transport
	client.httpClient.Transport = podmanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if !strings.HasSuffix(request.URL.Path, "/archive") {
			return base.RoundTrip(request)
		}

		return &http.Response{
			StatusCode:    http.StatusOK,
			ContentLength: -1,
			Header: http.Header{
				podmanContentType:              {podmanArchiveContentType},
				"X-Docker-Container-Path-Stat": {podmanArchiveStatHeader(t, "/data", 3)},
			},
			Body: &podmanMutatingArchiveBody{
				client: client,
				data:   []byte("tar"),
			},
			Request: request,
		}, nil
	})
	if _, err := client.GetWorkloadArchive(
		context.Background(), workload, podmanTestTransaction, "/data", io.Discard, 10,
	); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("GetWorkloadArchive(socket mutation) = %v", err)
	}
}

type podmanMutatingArchiveBody struct {
	client  *Client
	data    []byte
	mutated bool
}

func (body *podmanMutatingArchiveBody) Read(value []byte) (int, error) {
	if !body.mutated {
		body.client.socket.inode++
		body.mutated = true
	}

	if len(body.data) == 0 {
		return 0, io.EOF
	}
	count := copy(value, body.data)
	body.data = body.data[count:]

	return count, nil
}

func (*podmanMutatingArchiveBody) Close() error { return nil }

func podmanOwnedArchiveClient(
	t *testing.T,
	workload domain.DesiredWorkload,
	archiveHandler http.HandlerFunc,
) *Client {
	t.Helper()

	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}

	return connectedPodmanWorkloadClient(t, func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/archive") {
			if archiveHandler != nil {
				archiveHandler(response, request)
			}

			return
		}
		state.handler(response, request)
	})
}
