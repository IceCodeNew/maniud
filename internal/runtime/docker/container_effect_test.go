package docker

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/url"
	"reflect"
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestCreateWorkloadSendsStoppedOwnedContainerRequest(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		assertContainerCreateRequest(t, request, workload, testTransaction)
		response.Header().Set(contentTypeHeader, jsonContentType)

		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"Id":"`+testContainerID+`","Warnings":[]}`)
	}))

	identifier, err := client.CreateWorkload(context.Background(), workload, testTransaction)
	if err != nil || identifier != testContainerID {
		t.Fatalf("CreateWorkload() = %q, %v", identifier, err)
	}
}

//nolint:cyclop // This assertion checks the complete create request contract in one place.
func assertContainerCreateRequest(
	t *testing.T,
	request *http.Request,
	workload domain.DesiredWorkload,
	transaction string,
) {
	t.Helper()

	if request.Method != http.MethodPost || request.URL.Path != "/v1.54/containers/create" ||
		request.URL.Query().Get(containerNameQueryKey) != workload.ContainerName ||
		request.Header.Get("Accept") != jsonContentType || request.Header.Get(contentTypeHeader) != jsonContentType {
		t.Fatalf("create request = %s %s %#v", request.Method, request.URL.String(), request.Header)
	}

	var body containertypes.CreateRequest

	decoder := json.NewDecoder(request.Body)
	if decoder.Decode(&body) != nil || body.Config == nil || body.HostConfig == nil ||
		body.NetworkingConfig != nil || body.Image != workload.Image.Reference ||
		!reflect.DeepEqual(body.Entrypoint, workload.Entrypoint) ||
		!reflect.DeepEqual(body.Cmd, workload.Command) ||
		!reflect.DeepEqual(body.Labels, workloadOwnershipLabels(workload, transaction)) ||
		body.HostConfig.NetworkMode != dockerNetworkMode ||
		body.HostConfig.RestartPolicy.Name != containertypes.RestartPolicyDisabled ||
		body.HostConfig.RestartPolicy.MaximumRetryCount != 0 {
		t.Fatalf("create body = %#v", body)
	}
}

//nolint:cyclop,funlen // The table and follow-up cases form the strict failure corpus.
func TestCreateWorkloadRejectsInputTransportAndResponseFailures(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	invalid := workload
	invalid.ContainerName = testInvalidContainerName

	for _, test := range []struct {
		name        string
		client      *Client
		workload    domain.DesiredWorkload
		transaction string
		want        error
	}{
		{name: "nil client", client: nil, workload: workload, transaction: testTransaction, want: ErrUnsupportedWorkload},
		{
			name: "invalid workload", client: connectedTestClient(t, nil), workload: invalid,
			transaction: testTransaction, want: ErrUnsupportedWorkload,
		},
		{
			name: "invalid transaction", client: connectedTestClient(t, nil), workload: workload,
			transaction: "_invalid", want: ErrUnsupportedWorkload,
		},
	} {
		identifier, err := test.client.CreateWorkload(
			context.Background(),
			test.workload,
			test.transaction,
		)
		if identifier != "" || !errors.Is(err, test.want) {
			t.Fatalf("CreateWorkload(%s) = %q, %v", test.name, identifier, err)
		}
	}

	transport := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	transport.version = testVersion()

	identifier, err := transport.CreateWorkload(context.Background(), workload, testTransaction)
	if identifier != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CreateWorkload(transport) = %q, %v", identifier, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	identifier, err = transport.CreateWorkload(ctx, workload, testTransaction)
	if identifier != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateWorkload(cancelled) = %q, %v", identifier, err)
	}

	invalidEndpoint := connectedTestClient(t, nil)
	invalidEndpoint.baseURL.Host = "bad\nhost"

	identifier, err = invalidEndpoint.CreateWorkload(context.Background(), workload, testTransaction)
	if identifier != "" || !errors.Is(err, ErrProtocol) {
		t.Fatalf("CreateWorkload(invalid endpoint) = %q, %v", identifier, err)
	}

	invalidVersion := connectedTestClient(t, nil)
	invalidVersion.version.Protocol = ""

	identifier, err = invalidVersion.CreateWorkload(context.Background(), workload, testTransaction)
	if identifier != "" || !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("CreateWorkload(invalid version) = %q, %v", identifier, err)
	}
}

//nolint:funlen // The table and close-failure case form the strict response corpus.
func TestCreateWorkloadStrictlyDecodesResponse(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
		wantID      string
	}{
		{name: testStatusCase, status: http.StatusConflict, contentType: jsonContentType, body: `{}`, wantID: ""},
		{name: testContentTypeCase, status: http.StatusCreated, contentType: plainTextContentType, body: `{}`, wantID: ""},
		{name: testMalformedCase, status: http.StatusCreated, contentType: jsonContentType, body: `{"Id":`, wantID: ""},
		{
			name: testUnknownValue, status: http.StatusCreated, contentType: jsonContentType,
			body: `{"Id":"` + testContainerID + `","Warnings":[],"Other":true}`, wantID: "",
		},
		{
			name: "invalid ID", status: http.StatusCreated, contentType: jsonContentType,
			body: `{"Id":"short","Warnings":[]}`, wantID: "",
		},
		{
			name: "warnings", status: http.StatusCreated, contentType: jsonContentType,
			body: `{"Id":"` + testContainerID + `","Warnings":["private warning"]}`, wantID: testContainerID,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(contentTypeHeader, test.contentType)

				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))

			identifier, err := client.CreateWorkload(
				context.Background(),
				validApplicationWorkload(t),
				testTransaction,
			)
			if identifier != test.wantID || !errors.Is(err, ErrProtocol) ||
				strings.Contains(err.Error(), "private") {
				t.Fatalf("CreateWorkload(%s) = %q, %v", test.name, identifier, err)
			}
		})
	}

	response := &http.Response{ //nolint:exhaustruct // Decoder only consumes status, header, and body.
		StatusCode: http.StatusCreated,
		Header:     http.Header{contentTypeHeader: {jsonContentType}},
		Body: imagePullErrorBody{
			Reader:   strings.NewReader(`{"Id":"` + testContainerID + `","Warnings":[]}`),
			closeErr: errImagePullClose,
		},
	}

	identifier, err := decodeContainerCreateResponse(response)
	if identifier != testContainerID || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("decodeContainerCreateResponse(close) = %q, %v", identifier, err)
	}
}

func TestProbeCreatedWorkloadProvesNamedOwnedStoppedIdentity(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	document := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), createdContainerState())
	client := connectedTestClient(t, createdWorkloadProbeHandler(t, document, document))

	probe, err := client.ProbeCreatedWorkload(
		context.Background(),
		workload,
		testTransaction,
		testContainerID,
	)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved ||
		probe.Workload.ID != testContainerID || probe.Workload.Name != testContainerName ||
		!probe.Workload.ConfigurationMatches || probe.Workload.Lifecycle != application.WorkloadLifecycleCreated ||
		probe.Workload.Ownership.Status != domain.OwnershipManaged {
		t.Fatalf("ProbeCreatedWorkload() = %#v, %v", probe, err)
	}
}

func createdWorkloadProbeHandler(t *testing.T, namedDocument, ownedDocument string) http.Handler {
	t.Helper()

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		switch request.URL.Path {
		case "/v1.54/containers/" + testContainerName + "/json":
			_, _ = io.WriteString(response, namedDocument)
		case testContainerListPath:
			assertOwnedContainerQuery(t, request.URL.Query())
			_, _ = io.WriteString(response, containerSummaryDocument(t, testContainerID))
		case "/v1.54/containers/" + testContainerID + "/json":
			_, _ = io.WriteString(response, ownedDocument)
		default:
			t.Errorf("unexpected probe request path %q", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"message":"missing"}`)
		}
	})
}

func assertOwnedContainerQuery(t *testing.T, query url.Values) {
	t.Helper()

	var filters map[string][]string
	if query.Get("all") != "true" || json.Unmarshal([]byte(query.Get("filters")), &filters) != nil {
		t.Fatalf("owned query = %#v", query)
	}

	want := []string{
		domain.LabelService + "=" + testContainerService,
		domain.LabelTransaction + "=" + testTransaction,
	}
	if !reflect.DeepEqual(filters["label"], want) {
		t.Fatalf("owned filters = %#v, want %#v", filters, want)
	}
}

func containerSummaryDocument(t *testing.T, identifiers ...string) string {
	t.Helper()

	summaries := make([]containertypes.Summary, 0, len(identifiers))
	for _, identifier := range identifiers {
		summaries = append(summaries, containertypes.Summary{ //nolint:exhaustruct // Probe validates IDs before inspect.
			ID: identifier,
		})
	}

	encoded, err := json.Marshal(summaries)
	if err != nil {
		t.Fatalf("json.Marshal(container summaries) error = %v", err)
	}

	return string(encoded)
}

//nolint:cyclop // The subtests verify both absence and renamed ownership proof paths.
func TestProbeCreatedWorkloadProvesAbsenceAndDetectsRenamedOwnership(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)

	t.Run("absent", func(t *testing.T) {
		t.Parallel()

		client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set(contentTypeHeader, jsonContentType)

			if request.URL.Path == testContainerListPath {
				_, _ = io.WriteString(response, `[]`)

				return
			}

			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"message":"No such container"}`)
		}))

		probe, err := client.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
		if err != nil || probe.State != application.WorkloadEffectProbeMissing ||
			probe.Workload != emptyApplicationWorkloadEffectEvidence() {
			t.Fatalf("ProbeCreatedWorkload(absent) = %#v, %v", probe, err)
		}
	})

	t.Run("renamed", func(t *testing.T) {
		t.Parallel()

		document := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), createdContainerState())
		document = strings.Replace(document, `"Name":"/`+testContainerName+`"`, `"Name":"/renamed-api"`, 1)

		client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
			response.Header().Set(contentTypeHeader, jsonContentType)

			switch request.URL.Path {
			case "/v1.54/containers/" + testContainerName + "/json":
				response.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(response, `{"message":"No such container"}`)
			case testContainerListPath:
				_, _ = io.WriteString(response, containerSummaryDocument(t, testContainerID))
			case "/v1.54/containers/" + testContainerID + "/json":
				_, _ = io.WriteString(response, document)
			}
		}))

		probe, err := client.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
		if err != nil || probe.State != application.WorkloadEffectProbeObserved ||
			probe.Workload.Name != "renamed-api" || probe.Workload.ConfigurationMatches {
			t.Fatalf("ProbeCreatedWorkload(renamed) = %#v, %v", probe, err)
		}
	})
}

func TestProbeCreatedWorkloadReturnsForeignNameAsConflictingEvidence(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	foreign := validContainerDocument(
		t,
		map[string]string{testForeignOwnerLabel: "foreign"},
		createdContainerState(),
	)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		if request.URL.Path == testContainerListPath {
			_, _ = io.WriteString(response, `[]`)

			return
		}

		_, _ = io.WriteString(response, foreign)
	}))

	probe, err := client.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
	if err != nil || probe.State != application.WorkloadEffectProbeObserved ||
		probe.Workload.Ownership.Status != domain.OwnershipUnmanaged {
		t.Fatalf("ProbeCreatedWorkload(foreign) = %#v, %v", probe, err)
	}
}

//nolint:cyclop,funlen // The table and conflict cases form the strict rejection corpus.
func TestProbeCreatedWorkloadRejectsInvalidAndInconsistentEvidence(t *testing.T) {
	t.Parallel()

	var emptyProbe application.WorkloadEffectProbe

	workload := validApplicationWorkload(t)
	client := connectedTestClient(t, nil)
	invalid := workload
	invalid.ContainerName = testInvalidContainerName

	for _, test := range []struct {
		name        string
		client      *Client
		workload    domain.DesiredWorkload
		transaction string
		responseID  string
	}{
		{name: "nil client", client: nil, workload: workload, transaction: testTransaction, responseID: ""},
		{name: "workload", client: client, workload: invalid, transaction: testTransaction, responseID: ""},
		{name: "transaction", client: client, workload: workload, transaction: "_bad", responseID: ""},
		{name: "response ID", client: client, workload: workload, transaction: testTransaction, responseID: "short"},
	} {
		probe, err := test.client.ProbeCreatedWorkload(
			context.Background(),
			test.workload,
			test.transaction,
			test.responseID,
		)
		if probe != emptyProbe || !errors.Is(err, ErrUnsupportedWorkload) {
			t.Fatalf("ProbeCreatedWorkload(%s) = %#v, %v", test.name, probe, err)
		}
	}

	transport := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	transport.version = testVersion()

	probe, err := transport.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
	if probe != emptyProbe || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeCreatedWorkload(transport) = %#v, %v", probe, err)
	}

	document := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), createdContainerState())
	conflict := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		switch request.URL.Path {
		case "/v1.54/containers/" + testContainerName + "/json":
			_, _ = io.WriteString(response, document)
		case testContainerListPath:
			_, _ = io.WriteString(response, containerSummaryDocument(t, strings.Repeat("b", containerIDHexBytes)))
		case "/v1.54/containers/" + strings.Repeat("b", containerIDHexBytes) + "/json":
			other := strings.Replace(document, testContainerID, strings.Repeat("b", containerIDHexBytes), 1)
			_, _ = io.WriteString(response, other)
		}
	}))

	probe, err = conflict.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
	if probe != emptyProbe || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(conflict) = %#v, %v", probe, err)
	}

	missingOwned := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		if request.URL.Path == testContainerListPath {
			_, _ = io.WriteString(response, `[]`)

			return
		}

		_, _ = io.WriteString(response, document)
	}))

	probe, err = missingOwned.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
	if probe != emptyProbe || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(missing ownership selector) = %#v, %v", probe, err)
	}

	ownedTransport := testClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1.54/containers/"+testContainerName+"/json" {
			return &http.Response{ //nolint:exhaustruct // Probe needs status, header, and body.
				StatusCode: http.StatusOK,
				Header:     http.Header{contentTypeHeader: {jsonContentType}},
				Body:       io.NopCloser(strings.NewReader(document)),
			}, nil
		}

		return nil, io.ErrUnexpectedEOF
	}))
	ownedTransport.version = testVersion()

	probe, err = ownedTransport.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
	if probe != emptyProbe || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeCreatedWorkload(ownership transport) = %#v, %v", probe, err)
	}
}

//nolint:cyclop,funlen // The table and transport cases form the strict list rejection corpus.
func TestProbeOwnedContainerRejectsInvalidListEvidence(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{name: testStatusCase, status: http.StatusInternalServerError, contentType: jsonContentType, body: `{}`},
		{name: testContentTypeCase, status: http.StatusOK, contentType: plainTextContentType, body: `[]`},
		{name: testMalformedCase, status: http.StatusOK, contentType: jsonContentType, body: `[`},
		{
			name: "duplicate", status: http.StatusOK, contentType: jsonContentType,
			body: containerSummaryDocument(t, testContainerID, strings.Repeat("b", containerIDHexBytes)),
		},
		{name: "invalid ID", status: http.StatusOK, contentType: jsonContentType, body: containerSummaryDocument(t, "short")},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
				response.Header().Set(contentTypeHeader, test.contentType)

				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))

			probe, err := client.probeOwnedContainer(context.Background(), testContainerService, testTransaction)
			if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrProtocol) {
				t.Fatalf("probeOwnedContainer(%s) = %#v, %v", test.name, probe, err)
			}
		})
	}

	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		if request.URL.Path == testContainerListPath {
			_, _ = io.WriteString(response, containerSummaryDocument(t, testContainerID))

			return
		}

		response.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(response, `{"message":"missing"}`)
	}))

	probe, err := client.probeOwnedContainer(context.Background(), testContainerService, testTransaction)
	if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrProtocol) {
		t.Fatalf("probeOwnedContainer(disappeared) = %#v, %v", probe, err)
	}

	client.version.Protocol = ""

	probe, err = client.probeOwnedContainer(context.Background(), testContainerService, testTransaction)
	if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrProtocol) {
		t.Fatalf("probeOwnedContainer(invalid client) = %#v, %v", probe, err)
	}

	transport := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	transport.version = testVersion()

	probe, err = transport.probeOwnedContainer(context.Background(), testContainerService, testTransaction)
	if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("probeOwnedContainer(transport) = %#v, %v", probe, err)
	}

	inspectTransport := testClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path != testContainerListPath {
			return nil, io.ErrUnexpectedEOF
		}

		return &http.Response{ //nolint:exhaustruct // List decoding needs only status, header, and body.
			StatusCode: http.StatusOK,
			Header:     http.Header{contentTypeHeader: {jsonContentType}},
			Body:       io.NopCloser(strings.NewReader(containerSummaryDocument(t, testContainerID))),
		}, nil
	}))
	inspectTransport.version = testVersion()

	probe, err = inspectTransport.probeOwnedContainer(context.Background(), testContainerService, testTransaction)
	if probe.State != ContainerProbeUnknown || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("probeOwnedContainer(inspect transport) = %#v, %v", probe, err)
	}
}

func TestSelectCreatedContainerAndLifecycleMapping(t *testing.T) {
	t.Parallel()

	var (
		emptyContainer Container
		unknown        ContainerProbe
	)

	missing := ContainerProbe{State: ContainerProbeMissing, Container: emptyContainer}
	firstContainer := emptyContainer
	firstContainer.ID = testContainerID
	first := ContainerProbe{State: ContainerProbeObserved, Container: firstContainer}
	otherContainer := emptyContainer
	otherContainer.ID = strings.Repeat("b", containerIDHexBytes)

	other := ContainerProbe{
		State:     ContainerProbeObserved,
		Container: otherContainer,
	}
	for _, test := range []struct {
		name       string
		named      ContainerProbe
		owned      ContainerProbe
		wantFound  bool
		consistent bool
	}{
		{name: testMissingValue, named: missing, owned: missing, wantFound: false, consistent: true},
		{name: "named", named: first, owned: missing, wantFound: true, consistent: true},
		{name: "owned", named: missing, owned: first, wantFound: true, consistent: true},
		{name: "same", named: first, owned: first, wantFound: true, consistent: true},
		{name: "different", named: first, owned: other, wantFound: false, consistent: false},
		{name: testUnknownValue, named: unknown, owned: missing, wantFound: false, consistent: false},
	} {
		_, found, consistent := selectCreatedContainer(test.named, test.owned)
		if found != test.wantFound || consistent != test.consistent {
			t.Fatalf("selectCreatedContainer(%s) = %t, %t", test.name, found, consistent)
		}
	}

	states := map[ContainerState]application.WorkloadLifecycle{
		ContainerCreated:                 application.WorkloadLifecycleCreated,
		ContainerRunning:                 application.WorkloadLifecycleRunning,
		ContainerPaused:                  application.WorkloadLifecyclePaused,
		ContainerRestarting:              application.WorkloadLifecycleRestarting,
		ContainerRemoving:                application.WorkloadLifecycleRemoving,
		ContainerExited:                  application.WorkloadLifecycleExited,
		ContainerDead:                    application.WorkloadLifecycleDead,
		ContainerState(testUnknownValue): application.WorkloadLifecycleUnknown,
	}
	for state, want := range states {
		if got := applicationWorkloadLifecycle(state); got != want {
			t.Fatalf("applicationWorkloadLifecycle(%q) = %d, want %d", state, got, want)
		}
	}
}
