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
	"sync/atomic"
	"testing"

	"github.com/google/go-cmp/cmp"
	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testNilClientName           = "nil client"
	testInvalidWorkloadName     = "invalid workload"
	testInvalidTransactionName  = "invalid transaction"
	testInvalidTransactionValue = "_bad"
)

type containerCreateContract struct {
	Image                    string
	Entrypoint               []string
	Command                  []string
	Labels                   map[string]string
	NetworkingConfigPresent  bool
	NetworkMode              string
	RestartPolicyName        string
	RestartMaximumRetryCount int
}

func TestCreateWorkloadSendsStoppedOwnedContainerRequest(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if !assertContainerCreateRequest(t, request, workload, testTransaction) {
			response.WriteHeader(http.StatusInternalServerError)

			return
		}
		response.Header().Set(contentTypeHeader, jsonContentType)

		response.WriteHeader(http.StatusCreated)
		_, _ = io.WriteString(response, `{"Id":"`+testContainerID+`","Warnings":[]}`)
	}))

	identifier, err := client.CreateWorkload(context.Background(), workload, testTransaction, testCreateOptions())
	if err != nil || identifier != testContainerID {
		t.Fatalf("CreateWorkload() = %q, %v", identifier, err)
	}
}

func TestCreateWorkloadScrubsUnsupportedCgroupNamespace(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	request, valid := dockerCreateConfiguration(workload, testTransaction, testCreateOptions())
	if !valid {
		t.Fatal("dockerCreateConfiguration() = false")
	}

	for _, test := range []struct {
		protocol string
		present  bool
	}{
		{protocol: minimumAPIVersion, present: false},
		{protocol: testAPIVersion141, present: true},
	} {
		encoded, valid := encodeDockerCreateRequest(request, testAPIVersion(t, test.protocol))
		if !valid {
			t.Fatalf("encodeDockerCreateRequest(%s) = false", test.protocol)
		}
		var document map[string]json.RawMessage
		var host map[string]json.RawMessage
		if json.Unmarshal(encoded, &document) != nil || json.Unmarshal(document["HostConfig"], &host) != nil {
			t.Fatalf("decode create request for API %s", test.protocol)
		}
		_, present := host["CgroupnsMode"]
		if present != test.present {
			t.Fatalf("CgroupnsMode presence for API %s = %t", test.protocol, present)
		}
	}

	workload.Image.PlatformManifest = workload.Image.ReferenceDigest
	workload.Cgroup = testCgroupPrivate
	workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)
	var requests atomic.Int32
	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests.Add(1)

		return nil, io.ErrUnexpectedEOF
	}))
	setTestClientVersion(t, client, minimumAPIVersion)
	_, err := client.CreateWorkload(context.Background(), workload, testTransaction, testCreateOptions())
	if !errors.Is(err, ErrUnsupportedWorkload) || requests.Load() != 0 {
		t.Fatalf("CreateWorkload(1.40 cgroup) = %v, requests %d", err, requests.Load())
	}
}

func TestEncodeDockerCreateRequestRejectsInvalidLegacyHostConfig(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	request, valid := dockerCreateConfiguration(workload, testTransaction, testCreateOptions())
	if !valid {
		t.Fatal("dockerCreateConfiguration() = false")
	}

	request.HostConfig.CgroupnsMode = testCgroupPrivate
	if _, valid := encodeDockerCreateRequest(request, testAPIVersion(t, minimumAPIVersion)); valid {
		t.Fatal("encodeDockerCreateRequest(1.40, explicit cgroup namespace) = valid")
	}
	request.HostConfig = nil
	encoded, valid := encodeDockerCreateRequest(request, testAPIVersion(t, minimumAPIVersion))
	if !valid {
		t.Fatal("encodeDockerCreateRequest(1.40, nil host config) = false")
	}
	var document map[string]json.RawMessage
	if json.Unmarshal(encoded, &document) != nil {
		t.Fatal("decode create request without host config")
	}
	if _, present := document["HostConfig"]; present {
		t.Fatal("create request contains nil HostConfig")
	}
}

func assertContainerCreateRequest(
	t *testing.T,
	request *http.Request,
	workload domain.DesiredWorkload,
	transaction string,
) bool {
	t.Helper()

	if request.Method != http.MethodPost || request.URL.Path != "/v1.55/containers/create" ||
		request.URL.Query().Get(containerNameQueryKey) != workload.ContainerName ||
		request.Header.Get("Accept") != jsonContentType || request.Header.Get(contentTypeHeader) != jsonContentType {
		t.Errorf("create request = %s %s %#v", request.Method, request.URL.String(), request.Header)

		return false
	}

	var body containertypes.CreateRequest

	decoder := json.NewDecoder(request.Body)
	if err := decoder.Decode(&body); err != nil {
		t.Errorf("decode create body: %v", err)

		return false
	}
	if body.Config == nil || body.HostConfig == nil {
		t.Errorf("create body omits config: %#v", body)

		return false
	}

	want := containerCreateContract{
		Image:                    workload.Image.Reference,
		Entrypoint:               workload.Entrypoint,
		Command:                  workload.Command,
		Labels:                   workloadOwnershipLabels(workload, transaction),
		NetworkingConfigPresent:  false,
		NetworkMode:              string(dockerNetworkMode),
		RestartPolicyName:        string(containertypes.RestartPolicyDisabled),
		RestartMaximumRetryCount: 0,
	}
	got := containerCreateContract{
		Image:                    body.Image,
		Entrypoint:               body.Entrypoint,
		Command:                  body.Cmd,
		Labels:                   body.Labels,
		NetworkingConfigPresent:  body.NetworkingConfig != nil,
		NetworkMode:              string(body.HostConfig.NetworkMode),
		RestartPolicyName:        string(body.HostConfig.RestartPolicy.Name),
		RestartMaximumRetryCount: body.HostConfig.RestartPolicy.MaximumRetryCount,
	}
	if diff := cmp.Diff(want, got); diff != "" {
		t.Errorf("create body mismatch (-want +got):\n%s", diff)

		return false
	}

	return true
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
		{
			name: testNilClientName, client: nil, workload: workload,
			transaction: testTransaction, want: ErrUnsupportedWorkload,
		},
		{
			name: testInvalidWorkloadName, client: connectedTestClient(t, nil), workload: invalid,
			transaction: testTransaction, want: ErrUnsupportedWorkload,
		},
		{
			name: testInvalidTransactionName, client: connectedTestClient(t, nil), workload: workload,
			transaction: testInvalidTransactionValue, want: ErrUnsupportedWorkload,
		},
	} {
		identifier, err := test.client.CreateWorkload(
			context.Background(),
			test.workload,
			test.transaction,
			testCreateOptions(),
		)
		if identifier != "" || !errors.Is(err, test.want) {
			t.Fatalf("CreateWorkload(%s) = %q, %v", test.name, identifier, err)
		}
	}

	transport := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	transport.version = testVersion()

	identifier, err := transport.CreateWorkload(context.Background(), workload, testTransaction, testCreateOptions())
	if identifier != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CreateWorkload(transport) = %q, %v", identifier, err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	identifier, err = transport.CreateWorkload(ctx, workload, testTransaction, testCreateOptions())
	if identifier != "" || !errors.Is(err, context.Canceled) {
		t.Fatalf("CreateWorkload(cancelled) = %q, %v", identifier, err)
	}

	invalidEndpoint := connectedTestClient(t, nil)
	invalidEndpoint.baseURL.Host = "bad\nhost"

	identifier, err = invalidEndpoint.CreateWorkload(context.Background(), workload, testTransaction, testCreateOptions())
	if identifier != "" || !errors.Is(err, ErrProtocol) {
		t.Fatalf("CreateWorkload(invalid endpoint) = %q, %v", identifier, err)
	}

	invalidVersion := connectedTestClient(t, nil)
	invalidVersion.version.Protocol = ""

	identifier, err = invalidVersion.CreateWorkload(context.Background(), workload, testTransaction, testCreateOptions())
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
			body: `{"Id":"` + testShortContainerID + `","Warnings":[]}`, wantID: "",
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
				testCreateOptions(),
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

	drifted := volumeContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), createdContainerState())
	driftedClient := connectedTestClient(t, createdWorkloadProbeHandler(t, drifted, drifted))
	probe, err = driftedClient.ProbeCreatedWorkload(
		context.Background(), workload, testTransaction, testContainerID,
	)
	if !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(storage drift) = %#v, %v", probe, err)
	}
}

func TestStartWorkloadStartsProvenOwnedContainerAndProbesRunning(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	var started atomic.Bool
	client := connectedTestClient(t, startWorkloadHandler(t, workload, &started))

	err := client.StartWorkload(context.Background(), workload, testTransaction)
	if err != nil || !started.Load() {
		t.Fatalf("StartWorkload() = %v, started %t", err, started.Load())
	}

	probe, err := client.ProbeStartedWorkload(context.Background(), workload, testTransaction)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved ||
		!probe.Workload.ConfigurationMatches || probe.Workload.Lifecycle != application.WorkloadLifecycleRunning ||
		!startedWorkloadOwnershipMatches(probe.Workload, workload, testTransaction) {
		t.Fatalf("ProbeStartedWorkload() = %#v, %v", probe, err)
	}
}

func startWorkloadHandler(
	t *testing.T,
	workload domain.DesiredWorkload,
	started *atomic.Bool,
) http.Handler {
	t.Helper()

	created := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), createdContainerState())
	running := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), runningContainerState())
	summary := containerSummaryDocument(t, testContainerID)

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		switch request.URL.Path {
		case "/v1.55/containers/" + testContainerName + "/json",
			"/v1.55/containers/" + testContainerID + "/json":
			if started.Load() {
				_, _ = io.WriteString(response, running)
			} else {
				_, _ = io.WriteString(response, created)
			}
		case testContainerListPath:
			if !assertOwnedContainerQuery(t, request.URL.Query()) {
				response.WriteHeader(http.StatusInternalServerError)

				return
			}
			_, _ = io.WriteString(response, summary)
		case "/v1.55/containers/" + testContainerID + "/start":
			if request.Method != http.MethodPost || request.ContentLength != 0 || started.Swap(true) {
				t.Errorf("start request = %s %s, length %d", request.Method, request.URL.Path, request.ContentLength)
			}

			response.WriteHeader(http.StatusNoContent)
		default:
			t.Errorf("unexpected start request path %q", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	})
}

func TestStartWorkloadRejectsInvalidStateAndTransportFailure(t *testing.T) {
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
		err := test.client.StartWorkload(context.Background(), test.workload, test.transaction)
		if !errors.Is(err, ErrUnsupportedWorkload) {
			t.Fatalf("StartWorkload(%s) = %v", test.name, err)
		}
	}

	running := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), runningContainerState())
	conflict := connectedTestClient(t, createdWorkloadProbeHandler(t, running, running))

	err := conflict.StartWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("StartWorkload(running) = %v", err)
	}

	created := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), createdContainerState())
	transport := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if request.URL.Path == "/v1.55/containers/"+testContainerID+"/start" {
			panic(http.ErrAbortHandler)
		}

		createdWorkloadProbeHandler(t, created, created).ServeHTTP(response, request)
	}))

	err = transport.StartWorkload(context.Background(), workload, testTransaction)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("StartWorkload(transport) = %v", err)
	}
}

//nolint:bodyclose // The decoder's response-body ownership is the contract under test.
func TestDecodeContainerNoContentResponseRejectsInvalidResponses(t *testing.T) {
	t.Parallel()

	readFailure := imagePullReaderFunc(func([]byte) (int, error) {
		return 0, io.ErrUnexpectedEOF
	})
	closeFailure := func() *http.Response {
		response := containerNoContentResponse(http.StatusNoContent, 0, "", nil)
		response.Body = imagePullErrorBody{Reader: strings.NewReader(""), closeErr: errImagePullClose}

		return response
	}

	responses := []struct {
		name     string
		response func() *http.Response
		want     error
	}{
		{name: "nil response", response: func() *http.Response { return nil }, want: ErrProtocol},
		{
			name: "nil body",
			response: func() *http.Response {
				return &http.Response{StatusCode: http.StatusNoContent}
			},
			want: ErrProtocol,
		},
		{
			name: testStatusCase, response: containerNoContentResponseFactory(http.StatusConflict, 0, "", nil),
			want: ErrProtocol,
		},
		{
			name: "declared body", response: containerNoContentResponseFactory(http.StatusNoContent, 1, "", nil),
			want: ErrProtocol,
		},
		{
			name: "body", response: containerNoContentResponseFactory(http.StatusNoContent, -1, "x", nil),
			want: ErrProtocol,
		},
		{
			name: "read", response: containerNoContentResponseFactory(http.StatusNoContent, -1, "", readFailure),
			want: ErrUnavailable,
		},
		{
			name: "close", response: closeFailure,
			want: ErrUnavailable,
		},
		{
			name: "valid", response: containerNoContentResponseFactory(http.StatusNoContent, 0, "", nil),
			want: nil,
		},
	}

	for _, test := range responses {
		err := decodeContainerNoContentResponse(test.response())
		if !errors.Is(err, test.want) {
			t.Fatalf("decodeContainerNoContentResponse(%s) = %v, want %v", test.name, err, test.want)
		}
	}
}

func containerNoContentResponseFactory(
	status int,
	contentLength int64,
	body string,
	reader io.Reader,
) func() *http.Response {
	return func() *http.Response {
		return containerNoContentResponse(status, contentLength, body, reader)
	}
}

func containerNoContentResponse(
	status int,
	contentLength int64,
	body string,
	reader io.Reader,
) *http.Response {
	if reader == nil {
		reader = strings.NewReader(body)
	}

	return &http.Response{ //nolint:exhaustruct // Decoder consumes only status, length, and body.
		StatusCode:    status,
		ContentLength: contentLength,
		Body:          io.NopCloser(reader),
	}
}

func createdWorkloadProbeHandler(t *testing.T, namedDocument, ownedDocument string) http.Handler {
	t.Helper()
	summary := containerSummaryDocument(t, testContainerID)

	return http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		switch request.URL.Path {
		case "/v1.55/containers/" + testContainerName + "/json":
			_, _ = io.WriteString(response, namedDocument)
		case testContainerListPath:
			if !assertOwnedContainerQuery(t, request.URL.Query()) {
				response.WriteHeader(http.StatusInternalServerError)

				return
			}
			_, _ = io.WriteString(response, summary)
		case "/v1.55/containers/" + testContainerID + "/json":
			_, _ = io.WriteString(response, ownedDocument)
		default:
			t.Errorf("unexpected probe request path %q", request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
			_, _ = io.WriteString(response, `{"message":"missing"}`)
		}
	})
}

func assertOwnedContainerQuery(t *testing.T, query url.Values) bool {
	t.Helper()

	var filters map[string][]string
	if query.Get("all") != dockerQueryTrue || json.Unmarshal([]byte(query.Get("filters")), &filters) != nil {
		t.Errorf("owned query = %#v", query)

		return false
	}

	want := []string{
		domain.LabelService + "=" + testContainerService,
		domain.LabelTransaction + "=" + testTransaction,
	}
	if !reflect.DeepEqual(filters["label"], want) {
		t.Errorf("owned filters = %#v, want %#v", filters, want)

		return false
	}

	return true
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
			!reflect.DeepEqual(probe.Workload, emptyApplicationWorkloadEffectEvidence()) {
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
			case "/v1.55/containers/" + testContainerName + "/json":
				response.WriteHeader(http.StatusNotFound)
				_, _ = io.WriteString(response, `{"message":"No such container"}`)
			case testContainerListPath:
				_, _ = io.WriteString(response, containerSummaryDocument(t, testContainerID))
			case "/v1.55/containers/" + testContainerID + "/json":
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
		{name: testNilClientName, client: nil, workload: workload, transaction: testTransaction, responseID: ""},
		{name: "workload", client: client, workload: invalid, transaction: testTransaction, responseID: ""},
		{name: "transaction", client: client, workload: workload, transaction: testInvalidTransactionValue, responseID: ""},
		{
			name: "response ID", client: client, workload: workload,
			transaction: testTransaction, responseID: testShortContainerID,
		},
	} {
		probe, err := test.client.ProbeCreatedWorkload(
			context.Background(),
			test.workload,
			test.transaction,
			test.responseID,
		)
		if !reflect.DeepEqual(probe, emptyProbe) || !errors.Is(err, ErrUnsupportedWorkload) {
			t.Fatalf("ProbeCreatedWorkload(%s) = %#v, %v", test.name, probe, err)
		}
	}

	transport := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	transport.version = testVersion()

	probe, err := transport.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
	if !reflect.DeepEqual(probe, emptyProbe) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeCreatedWorkload(transport) = %#v, %v", probe, err)
	}

	document := validContainerDocument(t, workloadOwnershipLabels(workload, testTransaction), createdContainerState())
	conflict := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)

		switch request.URL.Path {
		case "/v1.55/containers/" + testContainerName + "/json":
			_, _ = io.WriteString(response, document)
		case testContainerListPath:
			_, _ = io.WriteString(response, containerSummaryDocument(t, strings.Repeat("b", containerIDHexBytes)))
		case "/v1.55/containers/" + strings.Repeat("b", containerIDHexBytes) + "/json":
			other := strings.Replace(document, testContainerID, strings.Repeat("b", containerIDHexBytes), 1)
			_, _ = io.WriteString(response, other)
		}
	}))

	probe, err = conflict.ProbeCreatedWorkload(context.Background(), workload, testTransaction, "")
	if !reflect.DeepEqual(probe, emptyProbe) || !errors.Is(err, ErrProtocol) {
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
	if !reflect.DeepEqual(probe, emptyProbe) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(missing ownership selector) = %#v, %v", probe, err)
	}

	ownedTransport := testClient(roundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.URL.Path == "/v1.55/containers/"+testContainerName+"/json" {
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
	if !reflect.DeepEqual(probe, emptyProbe) || !errors.Is(err, ErrUnavailable) {
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
		{
			name: "invalid ID", status: http.StatusOK, contentType: jsonContentType,
			body: containerSummaryDocument(t, testShortContainerID),
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
