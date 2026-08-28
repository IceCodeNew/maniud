package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

type discardEngineState struct {
	testing  *testing.T
	document string
	present  bool
	deletes  int
	mutex    sync.Mutex
}

func (state *discardEngineState) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	state.testing.Helper()
	state.mutex.Lock()
	defer state.mutex.Unlock()

	response.Header().Set(contentTypeHeader, jsonContentType)
	switch request.URL.Path {
	case "/v1.55/containers/" + testContainerName + "/json":
		state.serveInspect(response)
	case testContainerListPath:
		if state.present {
			_, _ = io.WriteString(response, containerSummaryDocument(state.testing, testContainerID))
		} else {
			_, _ = io.WriteString(response, `[]`)
		}
	case "/v1.55/containers/" + testContainerID + "/json":
		state.serveInspect(response)
	case "/v1.55/containers/" + testContainerID:
		if request.Method != http.MethodDelete || request.URL.Query().Get("force") != dockerQueryTrue ||
			request.URL.Query().Get("v") != dockerQueryFalse {
			state.testing.Errorf("discard request = %s %s", request.Method, request.URL.String())
		}

		state.deletes++
		state.present = false
		response.WriteHeader(http.StatusNoContent)
	default:
		state.testing.Errorf("unexpected discard path %q", request.URL.Path)
		response.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(response, `{"message":"missing"}`)
	}
}

func (state *discardEngineState) serveInspect(response http.ResponseWriter) {
	if !state.present {
		response.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(response, `{"message":"missing"}`)

		return
	}

	_, _ = io.WriteString(response, state.document)
}

func TestDockerDiscardsExactTransactionOwnedWorkload(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	for _, containerState := range []*containerStateFixture{
		createdContainerState(),
		runningContainerState(),
		exitedContainerState(),
	} {
		state := &discardEngineState{
			testing: t,
			document: validContainerDocument(
				t,
				workloadOwnershipLabels(workload, testTransaction),
				containerState,
			),
			present: true,
		}
		client := connectedTestClient(t, state)

		err := client.DiscardWorkload(context.Background(), workload, testTransaction)
		if err != nil || state.deletes != 1 {
			t.Fatalf("DiscardWorkload() = %v, deletes %d", err, state.deletes)
		}

		probe, err := client.ProbeDiscardedWorkload(context.Background(), workload, testTransaction)
		if err != nil || probe.State != application.WorkloadEffectProbeMissing {
			t.Fatalf("ProbeDiscardedWorkload() = %#v, %v", probe, err)
		}
	}
}

func TestDockerDiscardRejectsInvalidAndUnprovenWorkloads(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	invalid := workload
	invalid.ContainerName = testInvalidContainerName

	for _, test := range []struct {
		name        string
		client      *Client
		workload    domain.DesiredWorkload
		transaction string
	}{
		{name: testNilClientName, client: nil, workload: workload, transaction: testTransaction},
		{name: testInvalidWorkloadName, client: connectedTestClient(t, nil), workload: invalid, transaction: testTransaction},
		{
			name:        testInvalidTransactionName,
			client:      connectedTestClient(t, nil),
			workload:    workload,
			transaction: testInvalidTransactionValue,
		},
	} {
		probe, err := test.client.ProbeDiscardedWorkload(
			context.Background(),
			test.workload,
			test.transaction,
		)
		if !errors.Is(err, ErrUnsupportedWorkload) || !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) {
			t.Fatalf("ProbeDiscardedWorkload(%s) = %#v, %v", test.name, probe, err)
		}
	}

	missing := connectedTestClient(t, createdWorkloadProbeHandler(
		t,
		`{"message":"missing"}`,
		`{"message":"missing"}`,
	))
	err := missing.DiscardWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("DiscardWorkload(missing) = %v", err)
	}

	paused := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), &containerStateFixture{
		Status:     string(ContainerPaused),
		Running:    true,
		Paused:     true,
		Restarting: false,
		Dead:       false,
	})
	invalidState := connectedTestClient(t, createdWorkloadProbeHandler(t, paused, paused))
	err = invalidState.DiscardWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("DiscardWorkload(paused) = %v", err)
	}
}

func TestDockerDiscardRejectsInconsistentWorkloadIdentity(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	document := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), runningContainerState())
	other := strings.Replace(document, testContainerID, testOtherContainerID, 1)
	inconsistent := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		switch request.URL.Path {
		case "/v1.55/containers/" + testContainerName + "/json":
			_, _ = io.WriteString(response, document)
		case testContainerListPath:
			_, _ = io.WriteString(response, containerSummaryDocument(t, testOtherContainerID))
		case "/v1.55/containers/" + testOtherContainerID + "/json":
			_, _ = io.WriteString(response, other)
		default:
			t.Errorf("unexpected inconsistent probe path %q", request.URL.Path)
		}
	}))
	probe, err := inconsistent.ProbeDiscardedWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrProtocol) || !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) {
		t.Fatalf("ProbeDiscardedWorkload(inconsistent) = %#v, %v", probe, err)
	}

	drifted := volumeContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), runningContainerState())
	driftedClient := connectedTestClient(t, createdWorkloadProbeHandler(t, drifted, drifted))
	probe, err = driftedClient.ProbeDiscardedWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrProtocol) || !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) {
		t.Fatalf("ProbeDiscardedWorkload(storage drift) = %#v, %v", probe, err)
	}
}

func TestDockerDiscardContainsTransportAndProtocolFailures(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	transport := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	transport.version = testVersion()

	probe, err := transport.ProbeDiscardedWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrUnavailable) || !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) {
		t.Fatalf("ProbeDiscardedWorkload(transport) = %#v, %v", probe, err)
	}

	document := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), runningContainerState())
	requests := 0
	mutationFailure := testClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		requests++
		if request.Method == http.MethodDelete {
			return nil, io.ErrUnexpectedEOF
		}

		return discardProbeResponse(t, request, document), nil
	}))
	mutationFailure.version = testVersion()

	err = mutationFailure.DiscardWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrUnavailable) || requests != 4 {
		t.Fatalf("DiscardWorkload(transport) = %v, requests %d", err, requests)
	}

	unexpectedStatus := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.Method == http.MethodDelete {
			response.WriteHeader(http.StatusConflict)

			return
		}

		createdWorkloadProbeHandler(t, document, document).ServeHTTP(response, request)
	}))
	err = unexpectedStatus.DiscardWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("DiscardWorkload(status) = %v", err)
	}
}

func discardProbeResponse(t *testing.T, request *http.Request, document string) *http.Response {
	t.Helper()

	body := document
	if request.URL.Path == testContainerListPath {
		body = containerSummaryDocument(t, testContainerID)
	}

	return &http.Response{ //nolint:exhaustruct // Test transport needs status, header, and body.
		StatusCode: http.StatusOK,
		Header:     http.Header{contentTypeHeader: {jsonContentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
