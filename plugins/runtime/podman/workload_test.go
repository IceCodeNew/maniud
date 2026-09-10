package podman

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"reflect"
	"strings"
	"testing"
	"time"

	podmanconfig "github.com/IceCodeNew/maniud/containerconfig/podman"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	podmanTestContainerID = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	podmanTestService     = "api"
	podmanTestContainer   = "example-api"
	podmanTestTransaction = "550e8400-e29b-41d4-a716-446655440000"
	podmanTestEntrypoint  = "/usr/local/bin/app"
	podmanTestCommand     = "serve"
	podmanTestTmpfs       = "/tmp"
	podmanTestHealthCMD   = "CMD"
	podmanTestInvalid     = "invalid"
	podmanTestDuplicate   = "duplicate"
	podmanTestNoFile      = "nofile"
	podmanTestBad         = "bad"
	podmanTestBadNUL      = "bad\x00"
	podmanTestPort80TCP   = "80/tcp"
	podmanTestSamePath    = "/same"
	podmanTestStartedAt   = "2026-09-02T10:00:00.123456789Z"
)

func TestPodmanWorkloadHealthMapsEveryBoundedStatus(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status podmanconfig.HealthStatus
		want   application.WorkloadHealthStatus
	}{
		{status: podmanconfig.HealthUnknown, want: application.WorkloadHealthUnknown},
		{status: podmanconfig.HealthAbsent, want: application.WorkloadHealthAbsent},
		{status: podmanconfig.HealthStarting, want: application.WorkloadHealthStarting},
		{status: podmanconfig.HealthHealthy, want: application.WorkloadHealthHealthy},
		{status: podmanconfig.HealthUnhealthy, want: application.WorkloadHealthUnhealthy},
	}
	for _, test := range tests {
		got := podmanWorkloadHealth(podmanconfig.Health{Status: test.status, FailingStreak: 3})
		if got.Status != test.want || got.FailingStreak != 3 {
			t.Fatalf("podmanWorkloadHealth(%d) = %#v", test.status, got)
		}
	}
}

func podmanTestWorkload(t *testing.T) domain.DesiredWorkload {
	t.Helper()
	workload := domain.DesiredWorkload{
		ServiceName:   podmanTestService,
		ContainerName: podmanTestContainer,
		Platform:      domain.Platform{OS: podmanOSLinux, Architecture: podmanArchAMD64},
		Entrypoint:    []string{podmanTestEntrypoint},
		Command:       []string{podmanTestCommand, "--port", "8080"},
		NetworkMode:   podmanNetworkBridge,
		Image:         podmanExpectedImage(t),
		SourceDigest:  domain.Hash([]byte("podman workload source")),
	}
	workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)

	return workload
}

func connectedPodmanWorkloadClient(t *testing.T, handler http.HandlerFunc) *Client {
	t.Helper()
	negotiation := podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion)
	path := startPodmanTestServer(t, podmanTestHandler(func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, "/containers") {
			handler(writer, request)

			return
		}
		negotiation.ServeHTTP(writer, request)
	}))
	client, _, err := Connect(context.Background(), path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(client.CloseIdleConnections)

	return client
}

type podmanWorkloadRuntimeState struct {
	t           *testing.T
	workload    domain.DesiredWorkload
	transaction string
	present     bool
	name        string
	lifecycle   ContainerState
	mutations   int
}

type podmanWorkloadErrorBody struct {
	data     []byte
	readErr  error
	closeErr error
}

func (body *podmanWorkloadErrorBody) Read(buffer []byte) (int, error) {
	if len(body.data) != 0 {
		count := copy(buffer, body.data)
		body.data = body.data[count:]

		return count, nil
	}
	if body.readErr != nil {
		return 0, body.readErr
	}

	return 0, io.EOF
}

func (body *podmanWorkloadErrorBody) Close() error { return body.closeErr }

func failPodmanWorkloadRequests(client *Client, reject func(*http.Request) bool) {
	transport := client.httpClient.Transport
	client.httpClient.Transport = podmanRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if reject(request) {
			return nil, errPodmanClientTest
		}

		return transport.RoundTrip(request)
	})
}

//nolint:cyclop // The fixture dispatches the complete fixed lifecycle route table.
func (state *podmanWorkloadRuntimeState) handler(writer http.ResponseWriter, request *http.Request) {
	state.t.Helper()
	switch {
	case request.Method == http.MethodPost && request.URL.Path == libpodPrefix+"/containers/create":
		state.create(writer, request)
	case request.Method == http.MethodGet && request.URL.Path == libpodPrefix+"/containers/json":
		state.list(writer, request)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/start"):
		state.mutateEmpty(writer, request, ContainerRunning, nil)
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/stop"):
		state.mutateEmpty(writer, request, ContainerExited, map[string]string{"timeout": "10"})
	case request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/rename"):
		state.rename(writer, request)
	case request.Method == http.MethodDelete:
		state.remove(writer, request)
	case request.Method == http.MethodGet && strings.HasSuffix(request.URL.Path, "/json"):
		state.inspect(writer, request)
	default:
		http.NotFound(writer, request)
	}
}

func (state *podmanWorkloadRuntimeState) create(writer http.ResponseWriter, request *http.Request) {
	if state.present || request.Header.Get(podmanContentType) != podmanJSONType {
		state.t.Errorf("invalid create request %s %#v", request.URL.String(), request.Header)
		writer.WriteHeader(http.StatusConflict)

		return
	}
	var configuration podmanCreateSpec
	decoded := decodePodmanJSON(request.Body, maximumControlBytes, &configuration)
	if !decoded || !state.validCreateConfiguration(configuration) {
		state.t.Errorf("invalid create configuration %#v", configuration)
		writer.WriteHeader(http.StatusBadRequest)

		return
	}
	state.present = true
	state.name = state.workload.ContainerName
	state.lifecycle = ContainerCreated
	state.mutations++
	writer.Header().Set(podmanContentType, podmanJSONType)
	writer.WriteHeader(http.StatusCreated)
	_, _ = io.WriteString(writer, `{"Id":"`+podmanTestContainerID+`","Warnings":[]}`)
}

func (state *podmanWorkloadRuntimeState) validCreateConfiguration(configuration podmanCreateSpec) bool {
	return configuration.Name == state.workload.ContainerName &&
		configuration.Image == state.workload.Image.Reference &&
		configuration.RestartPolicy == "no" &&
		validPodmanCreateNamespaces(configuration) &&
		configuration.Labels[domain.LabelTransaction] == state.transaction
}

func validPodmanCreateNamespaces(configuration podmanCreateSpec) bool {
	return configuration.NetworkNamespace.Mode == podmanNetworkBridge &&
		configuration.IPCNamespace.Mode == podmanIPCPrivate &&
		configuration.PIDNamespace.Mode == podmanIPCPrivate &&
		configuration.UTSNamespace.Mode == podmanIPCPrivate &&
		configuration.CgroupNamespace.Mode == podmanCgroupPrivate
}

func (state *podmanWorkloadRuntimeState) list(writer http.ResponseWriter, request *http.Request) {
	var filters podmanContainerListFilters
	if request.URL.Query().Get("all") != podmanQueryTrue ||
		json.Unmarshal([]byte(request.URL.Query().Get("filters")), &filters) != nil ||
		!reflect.DeepEqual(filters.Labels, []string{
			domain.LabelService + "=" + state.workload.ServiceName,
			domain.LabelTransaction + "=" + state.transaction,
		}) {
		state.t.Errorf("invalid list query %s", request.URL.RawQuery)
		writer.WriteHeader(http.StatusBadRequest)

		return
	}
	if !state.present {
		writePodmanJSON(writer, `[]`)

		return
	}
	writePodmanJSON(writer, `[{"Id":"`+podmanTestContainerID+`"}]`)
}

func (state *podmanWorkloadRuntimeState) inspect(writer http.ResponseWriter, request *http.Request) {
	reference := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, libpodPrefix+"/containers/"), "/json")
	if request.URL.Query().Get("size") != podmanQueryFalse {
		state.t.Errorf("invalid inspect query %s", request.URL.RawQuery)
	}
	if !state.present || reference != podmanTestContainerID && reference != state.name {
		writePodmanNotFound(writer)

		return
	}
	writePodmanJSON(writer, state.inspectDocument())
}

func (state *podmanWorkloadRuntimeState) inspectDocument() string {
	stateValue := &podmanInspectState{}
	switch state.lifecycle {
	case ContainerCreated:
		stateValue.Status = "created"
	case ContainerRunning:
		stateValue.Status = podmanStateRunning
		stateValue.Running = true
		stateValue.StartedAt = podmanTestStartedAt
	case ContainerPaused:
		stateValue.Status = podmanStatePaused
		stateValue.Paused = true
	case ContainerRemoving:
		stateValue.Status = podmanStateRemoving
	case ContainerExited:
		stateValue.Status = "exited"
	case ContainerStateUnknown:
		stateValue.Status = podmanStateUnknown
	}
	payload := podmanInspectData{
		ID: podmanTestContainerID, Name: state.name, Image: podmanImageConfig,
		ImageName: state.workload.Image.Reference, ImageDigest: podmanManifestDigest,
		State: stateValue, Mounts: []podmanInspectMount{},
		Config: &podmanInspectConfig{
			Image:   state.workload.Image.Reference,
			Command: state.workload.Command, Entrypoint: state.workload.Entrypoint,
			Labels:   workloadOwnershipLabels(state.workload, state.transaction),
			Hostname: podmanTestContainerID[:12], StopSignal: "SIGTERM", StopTimeout: 10,
		},
		HostConfig: &podmanInspectHost{
			NetworkMode: podmanNetworkBridge, IPCMode: podmanIPCPrivate,
			PIDMode: podmanIPCPrivate, UTSMode: podmanIPCPrivate, CgroupMode: podmanCgroupPrivate,
			Cgroups: podmanCgroupsDefault, ShmSize: podmanDefaultSharedMemory,
			RestartPolicy: &podmanInspectRestart{Name: "no"},
		},
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		state.t.Fatal(err)
	}

	return string(encoded)
}

func (state *podmanWorkloadRuntimeState) mutateEmpty(
	writer http.ResponseWriter,
	request *http.Request,
	lifecycle ContainerState,
	wantQuery map[string]string,
) {
	if !state.matchesIDPath(request.URL.Path) {
		writePodmanNotFound(writer)

		return
	}
	for key, value := range wantQuery {
		if request.URL.Query().Get(key) != value {
			state.t.Errorf("invalid mutation query %s", request.URL.RawQuery)
		}
	}
	state.lifecycle = lifecycle
	state.mutations++
	writer.WriteHeader(http.StatusNoContent)
}

func (state *podmanWorkloadRuntimeState) rename(writer http.ResponseWriter, request *http.Request) {
	wantPath := libpodPrefix + "/containers/" + podmanTestContainerID + "/rename"
	if !state.present || request.URL.Path != wantPath || request.URL.Query().Get("name") == "" {
		writePodmanNotFound(writer)

		return
	}
	state.name = request.URL.Query().Get("name")
	state.mutations++
	writer.WriteHeader(http.StatusNoContent)
}

func (state *podmanWorkloadRuntimeState) remove(writer http.ResponseWriter, request *http.Request) {
	if !state.matchesIDPath(request.URL.Path) || request.URL.Query().Get("volumes") != podmanQueryFalse ||
		request.URL.Query().Get("force") == "" {
		writePodmanNotFound(writer)

		return
	}
	state.present = false
	state.mutations++
	writePodmanJSON(writer, `[{"Id":"`+podmanTestContainerID+`"}]`)
}

func (state *podmanWorkloadRuntimeState) matchesIDPath(path string) bool {
	return state.present && path == libpodPrefix+"/containers/"+podmanTestContainerID ||
		state.present && strings.HasPrefix(path, libpodPrefix+"/containers/"+podmanTestContainerID+"/")
}

func writePodmanNotFound(writer http.ResponseWriter) {
	writer.Header().Set(podmanContentType, podmanJSONType)
	writer.WriteHeader(http.StatusNotFound)
	_, _ = io.WriteString(writer, `{"cause":"container absent","message":"container absent","response":404}`)
}

//nolint:cyclop,funlen // This test follows one complete create, transition, and removal lifecycle.
func TestPodmanWorkloadLifecycleUsesIndependentProbes(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{t: t, workload: workload, transaction: podmanTestTransaction}
	client := connectedPodmanWorkloadClient(t, state.handler)

	evidence, err := client.Inspect(context.Background())
	if err != nil || evidence.Kind != domain.RuntimePodman || evidence.Digest == (domain.Digest{}) {
		t.Fatalf("Inspect() = %#v, %v", evidence, err)
	}
	if err := client.CheckWorkload(workload); err != nil {
		t.Fatalf("CheckWorkload() = %v", err)
	}
	observation, err := client.ObserveWorkload(context.Background(), workload)
	if err != nil || observation.State != application.WorkloadObservationMissing {
		t.Fatalf("ObserveWorkload(missing) = %#v, %v", observation, err)
	}

	identifier, err := client.CreateWorkload(context.Background(), workload, podmanTestTransaction, testCreateOptions())
	if err != nil || identifier != podmanTestContainerID || state.mutations != 1 {
		t.Fatalf("CreateWorkload() = %q, %v, mutations=%d", identifier, err, state.mutations)
	}
	created, err := client.ProbeCreatedWorkload(
		context.Background(), workload, podmanTestTransaction, identifier,
	)
	if err != nil || created.State != application.WorkloadEffectProbeObserved ||
		!created.Workload.ConfigurationMatches || created.Workload.Lifecycle != application.WorkloadLifecycleCreated {
		t.Fatalf("ProbeCreatedWorkload() = %#v, %v", created, err)
	}
	if err := client.StartWorkload(context.Background(), workload, podmanTestTransaction); err != nil {
		t.Fatalf("StartWorkload() = %v", err)
	}
	started, err := client.ProbeStartedWorkload(context.Background(), workload, podmanTestTransaction)
	if err != nil || started.Workload.Lifecycle != application.WorkloadLifecycleRunning {
		t.Fatalf("ProbeStartedWorkload() = %#v, %v", started, err)
	}

	current := existingPodmanWorkloadProbe(ContainerProbe{
		State: ContainerProbeObserved,
		Container: Container{
			ID: podmanTestContainerID, Name: state.name, ImageReference: workload.Image.Reference,
			ImageConfig: workload.Image.ImageConfig, PlatformManifest: workload.Image.PlatformManifest,
			WorkloadSpec: workload.WorkloadSpec, State: ContainerRunning,
			Ownership: started.Workload.Ownership,
		},
	}).Workload
	current.ConfigurationDigest = started.Workload.ConfigurationDigest
	exited := current
	exited.Lifecycle = application.WorkloadLifecycleExited
	stop := application.WorkloadTransition{Kind: application.WorkloadTransitionStop, Before: current, After: exited}
	if err := client.ApplyWorkloadTransition(context.Background(), stop); err != nil {
		t.Fatalf("ApplyWorkloadTransition(stop) = %v", err)
	}
	assertPodmanTransition(t, client, stop, exited)

	renamed := exited
	renamed.Name = "example-api-old"
	rename := application.WorkloadTransition{Kind: application.WorkloadTransitionRename, Before: exited, After: renamed}
	if err := client.ApplyWorkloadTransition(context.Background(), rename); err != nil {
		t.Fatalf("ApplyWorkloadTransition(rename) = %v", err)
	}
	assertPodmanTransition(t, client, rename, renamed)

	runningAgain := renamed
	runningAgain.Lifecycle = application.WorkloadLifecycleRunning
	restore := application.WorkloadTransition{
		Kind: application.WorkloadTransitionRestoreStart, Before: renamed, After: runningAgain,
	}
	if err := client.ApplyWorkloadTransition(context.Background(), restore); err != nil {
		t.Fatalf("ApplyWorkloadTransition(restore) = %v", err)
	}
	assertPodmanTransition(t, client, restore, runningAgain)

	exitedAgain := runningAgain
	exitedAgain.Lifecycle = application.WorkloadLifecycleExited
	stopAgain := application.WorkloadTransition{
		Kind: application.WorkloadTransitionStop, Before: runningAgain, After: exitedAgain,
	}
	if err := client.ApplyWorkloadTransition(context.Background(), stopAgain); err != nil {
		t.Fatalf("ApplyWorkloadTransition(stop again) = %v", err)
	}
	remove := application.WorkloadTransition{
		Kind: application.WorkloadTransitionRemove, Before: exitedAgain,
	}
	if err := client.ApplyWorkloadTransition(context.Background(), remove); err != nil {
		t.Fatalf("ApplyWorkloadTransition(remove) = %v", err)
	}
	probe, err := client.ProbeWorkloadTransition(context.Background(), remove)
	if err != nil || probe.State != application.WorkloadEffectProbeMissing {
		t.Fatalf("ProbeWorkloadTransition(remove) = %#v, %v", probe, err)
	}
}

func TestPodmanWorkloadRejectsLossyPodman4Entrypoints(t *testing.T) {
	t.Parallel()

	state := &podmanWorkloadRuntimeState{t: t}
	client := connectedPodmanWorkloadClient(t, state.handler)
	client.version.Protocol = minimumLibpodAPIVersion
	client.version.Minimum = testLibpodServerMinimum
	client.version.Maximum = minimumLibpodAPIVersion
	client.protocol, _ = parseSemanticVersion(minimumLibpodAPIVersion)

	for _, entrypoint := range [][]string{{"/bin/sh", "-c"}, {"/path with space"}} {
		workload := podmanTestWorkload(t)
		workload.Entrypoint = entrypoint
		workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)
		if err := client.CheckWorkload(workload); !errors.Is(err, ErrUnsupportedWorkload) {
			t.Fatalf("CheckWorkload(Podman 4 entrypoint %q) = %v", entrypoint, err)
		}
	}
}

func assertPodmanTransition(
	t *testing.T,
	client *Client,
	transition application.WorkloadTransition,
	want application.ExistingWorkload,
) {
	t.Helper()
	probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved || probe.Workload != want {
		t.Fatalf("ProbeWorkloadTransition(%d) = %#v, %v, want %#v", transition.Kind, probe, err, want)
	}
}

func TestPodmanDiscardForceRemovesOnlyOwnedWorkload(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}
	client := connectedPodmanWorkloadClient(t, state.handler)
	if err := client.DiscardWorkload(context.Background(), workload, podmanTestTransaction); err != nil {
		t.Fatalf("DiscardWorkload() = %v", err)
	}
	probe, err := client.ProbeDiscardedWorkload(context.Background(), workload, podmanTestTransaction)
	if err != nil || probe.State != application.WorkloadEffectProbeMissing || state.present {
		t.Fatalf("ProbeDiscardedWorkload() = %#v, %v, present=%t", probe, err, state.present)
	}
}

func TestPodmanWorkloadProbeRejectsMalformedContainer(t *testing.T) {
	t.Parallel()

	client := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/json") {
			writePodmanJSON(writer, `{"Id":"short"}`)

			return
		}
		writePodmanJSON(writer, `[]`)
	})
	probe, err := client.ProbeContainer(context.Background(), podmanTestContainer)
	if !errors.Is(err, ErrProtocol) || !reflect.DeepEqual(probe, ContainerProbe{}) {
		t.Fatalf("ProbeContainer(malformed) = %#v, %v", probe, err)
	}
}

func TestPodmanCreateAndResponseHelpersFailClosed(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	invalid := workload
	invalid.NetworkMode = podmanCgroupHost
	client := &Client{}
	if identifier, err := client.CreateWorkload(
		context.Background(), invalid, podmanTestTransaction,
		testCreateOptions(),
	); !errors.Is(err, ErrUnsupportedWorkload) || identifier != "" {
		t.Fatalf("CreateWorkload(invalid) = %q, %v", identifier, err)
	}

	responses := []*http.Response{
		nil,
		{StatusCode: http.StatusCreated, Header: http.Header{}, Body: io.NopCloser(strings.NewReader(`{}`))},
		{
			StatusCode: http.StatusCreated, Header: http.Header{podmanContentType: {podmanJSONType}},
			Body: io.NopCloser(strings.NewReader(`{"Id":"short"}`)),
		},
	}
	for _, response := range responses {
		identifier, err := decodePodmanCreateResponse(response)
		if !errors.Is(err, ErrProtocol) || identifier != "" {
			t.Fatalf("decodePodmanCreateResponse(%#v) = %q, %v", response, identifier, err)
		}
	}
	if err := decodePodmanEmptyResponse(nil, http.StatusNoContent); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanEmptyResponse(nil) = %v", err)
	}
	if err := decodePodmanRemovalResponse(&http.Response{
		StatusCode: http.StatusOK, Header: http.Header{podmanContentType: {podmanJSONType}},
		Body: io.NopCloser(strings.NewReader(`[{"Id":"wrong"}]`)),
	}, podmanTestContainerID); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanRemovalResponse(wrong) = %v", err)
	}
}

func TestPodmanContainerReferenceGrammar(t *testing.T) {
	t.Parallel()

	validID := podmanTestContainerID
	for _, value := range []string{"", "-bad", "bad/name", strings.Repeat("a", 64) + "x"} {
		if validContainerReference(value) {
			t.Fatalf("validContainerReference(%q) = true", value)
		}
	}
	if !validContainerReference(validID) || !validContainerReference("A.b_c-9") {
		t.Fatal("validContainerReference() rejected supported identities")
	}
	if got := fmt.Sprint(podmanWorkloadLifecycle(ContainerStateUnknown)); got != "0" {
		t.Fatalf("podmanWorkloadLifecycle(unknown) = %s", got)
	}
}

//nolint:cyclop,funlen // The table-driven test exhausts independent fail-closed state enums.
func TestPodmanWorkloadPureFailClosedBranches(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	if evidence, err := (*Client)(nil).Inspect(context.Background()); !errors.Is(err, ErrProtocol) ||
		evidence != (application.RuntimeEvidence{}) {
		t.Fatalf("Inspect(nil) = %#v, %v", evidence, err)
	}
	invalidPlatform := &Client{
		version: Version{
			Protocol: libpodAPIVersion, Minimum: testLibpodServerMinimum, Maximum: libpodAPIVersion,
			OS: "plan9", Architecture: "mips",
		},
		protocol: semanticVersion{major: 6, minor: 1},
		scope:    domain.Hash([]byte("scope")),
	}
	if _, err := invalidPlatform.Inspect(context.Background()); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("Inspect(unsupported platform) = %v", err)
	}
	for _, state := range []ContainerProbeState{ContainerProbeUnknown, 99} {
		observation, err := podmanWorkloadObservation(ContainerProbe{State: state}, workload, libpodAPIVersion)
		if !errors.Is(err, ErrProtocol) || !reflect.DeepEqual(observation, application.WorkloadObservation{}) {
			t.Fatalf("podmanWorkloadObservation(%d) = %#v, %v", state, observation, err)
		}
	}

	first := Container{ID: podmanTestContainerID}
	second := Container{ID: strings.Repeat("e", containerIDHexBytes)}
	missing := ContainerProbe{State: ContainerProbeMissing}
	observedFirst := ContainerProbe{State: ContainerProbeObserved, Container: first}
	observedSecond := ContainerProbe{State: ContainerProbeObserved, Container: second}
	cases := []struct {
		named, owned ContainerProbe
		id           string
		found, valid bool
	}{
		{missing, missing, "", false, true},
		{observedFirst, missing, first.ID, true, true},
		{missing, observedSecond, second.ID, true, true},
		{observedFirst, observedFirst, first.ID, true, true},
		{observedFirst, observedSecond, "", false, false},
		{ContainerProbe{}, missing, "", false, false},
	}
	for _, testCase := range cases {
		selected, found, valid := selectPodmanContainer(testCase.named, testCase.owned)
		if selected.ID != testCase.id || found != testCase.found || valid != testCase.valid {
			t.Fatalf("selectPodmanContainer(%#v, %#v) = %#v, %t, %t", testCase.named, testCase.owned, selected, found, valid)
		}
	}

	lifecycles := map[ContainerState]application.WorkloadLifecycle{
		ContainerStateUnknown: application.WorkloadLifecycleUnknown,
		ContainerCreated:      application.WorkloadLifecycleCreated,
		ContainerRunning:      application.WorkloadLifecycleRunning,
		ContainerPaused:       application.WorkloadLifecyclePaused,
		ContainerRemoving:     application.WorkloadLifecycleRemoving,
		ContainerExited:       application.WorkloadLifecycleExited,
		99:                    application.WorkloadLifecycleUnknown,
	}
	for state, want := range lifecycles {
		if got := podmanWorkloadLifecycle(state); got != want {
			t.Fatalf("podmanWorkloadLifecycle(%d) = %d, want %d", state, got, want)
		}
	}

	if validContainerID(strings.Repeat("g", containerIDHexBytes)) {
		t.Fatal("validContainerID accepted non-hex ID")
	}
	if !validContainerID(strings.Repeat("0", containerIDHexBytes)) {
		t.Fatal("validContainerID rejected numeric ID")
	}
	if hasManiudLabel(map[string]string{"team": "platform"}) {
		t.Fatal("hasManiudLabel accepted unrelated label")
	}
	protocol, _ := parseSemanticVersion(libpodAPIVersion)
	client := &Client{
		version: Version{
			Protocol: libpodAPIVersion, Minimum: testLibpodServerMinimum, Maximum: libpodAPIVersion,
		},
		protocol: protocol,
	}
	method, requestPath, query := client.podmanWorkloadTransitionRequest(
		application.WorkloadTransition{Kind: 99},
	)
	if method != "" || requestPath != "" || query != nil {
		t.Fatalf("podmanWorkloadTransitionRequest(unknown) = %q, %q, %#v", method, requestPath, query)
	}
	if containerConfigurationMatches(Container{}, workload, libpodAPIVersion) {
		t.Fatal("containerConfigurationMatches accepted empty container")
	}
	if validOwnershipName("bad/name") || validOwnershipName(strings.Repeat("a", maximumOwnershipValueBytes+1)) {
		t.Fatal("validOwnershipName accepted invalid value")
	}
}

//nolint:cyclop // The table-driven test exhausts independent wire decoder failures.
func TestPodmanWireDecodersFailClosed(t *testing.T) {
	t.Parallel()

	jsonResponse := func(status int, document string) *http.Response {
		return &http.Response{
			StatusCode: status, Header: http.Header{podmanContentType: {podmanJSONType}},
			Body: io.NopCloser(strings.NewReader(document)),
		}
	}
	for _, document := range []string{
		`{`, `{}`, `{"Warnings":[]}`, `{"Id":"` + podmanTestContainerID + `","Warnings":["warning"]}`,
	} {
		response := jsonResponse(http.StatusCreated, document) //nolint:bodyclose // Decoder closes it.
		identifier, err := decodePodmanCreateResponse(response)
		if identifier != "" || !errors.Is(err, ErrProtocol) {
			t.Fatalf("decodePodmanCreateResponse(%q) = %q, %v", document, identifier, err)
		}
	}
	for _, document := range []string{`{`, `[{}]`, `[{"Id":"short"}]`, `[{"Id":"` + podmanTestContainerID + `"},{}]`} {
		if entries, valid := decodePodmanContainerList([]byte(document)); valid || entries != nil {
			t.Fatalf("decodePodmanContainerList(%q) = %#v, %t", document, entries, valid)
		}
	}
	for _, document := range []string{`{`, `[]`, `[{"Id":"` + podmanTestContainerID + `","Err":"failed"}]`} {
		response := jsonResponse(http.StatusOK, document) //nolint:bodyclose // Decoder closes it.
		if err := decodePodmanRemovalResponse(response, podmanTestContainerID); !errors.Is(err, ErrProtocol) {
			t.Fatalf("decodePodmanRemovalResponse(%q) = %v", document, err)
		}
	}
	statusResponse := jsonResponse(http.StatusOK, "x") //nolint:bodyclose // Decoder closes it.
	if err := decodePodmanEmptyResponse(statusResponse, http.StatusNoContent); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanEmptyResponse(status) = %v", err)
	}
	response := jsonResponse(http.StatusNoContent, "x")
	response.ContentLength = -1
	if err := decodePodmanEmptyResponse(response, http.StatusNoContent); !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanEmptyResponse(body) = %v", err)
	}
}

func TestPodmanProbeContainerHTTPFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		status      int
		contentType string
		body        string
	}{
		{"malformed not found", http.StatusNotFound, podmanJSONType, `{}`},
		{"wrong status", http.StatusInternalServerError, podmanJSONType, `{}`},
		{"wrong content type", http.StatusOK, "text/plain", `{}`},
		{"malformed inspect", http.StatusOK, podmanJSONType, `{}`},
	}
	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			t.Parallel()
			client := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, _ *http.Request) {
				writer.Header().Set(podmanContentType, testCase.contentType)
				writer.WriteHeader(testCase.status)
				_, _ = io.WriteString(writer, testCase.body)
			})
			probe, err := client.ProbeContainer(context.Background(), podmanTestContainer)
			if !reflect.DeepEqual(probe, ContainerProbe{}) || !errors.Is(err, ErrProtocol) {
				t.Fatalf("ProbeContainer() = %#v, %v", probe, err)
			}
		})
	}
	client := connectedPodmanWorkloadClient(t, func(http.ResponseWriter, *http.Request) {})
	probe, err := client.ProbeContainer(context.Background(), "bad/name")
	if !reflect.DeepEqual(probe, ContainerProbe{}) ||
		!errors.Is(err, ErrInvalidContainerReference) {
		t.Fatalf("ProbeContainer(invalid) = %#v, %v", probe, err)
	}
}

//nolint:cyclop // Each assertion covers an independent public fail-closed operation.
func TestPodmanWorkloadOperationsRejectUnprovenEffects(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{t: t, workload: workload, transaction: podmanTestTransaction}
	client := connectedPodmanWorkloadClient(t, state.handler)
	ctx := context.Background()

	observation, err := client.ObserveWorkload(ctx, domain.DesiredWorkload{})
	if !reflect.DeepEqual(observation, application.WorkloadObservation{}) ||
		!errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ObserveWorkload(invalid) = %#v, %v", observation, err)
	}
	probe, err := client.ProbeCreatedWorkload(ctx, workload, "bad/name", "")
	if !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) || !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeCreatedWorkload(invalid) = %#v, %v", probe, err)
	}
	probe, err = client.ProbeCreatedWorkload(ctx, workload, podmanTestTransaction, "short")
	if !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) || !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeCreatedWorkload(response ID) = %#v, %v", probe, err)
	}
	if err := client.StartWorkload(ctx, workload, podmanTestTransaction); !errors.Is(err, ErrProtocol) {
		t.Fatalf("StartWorkload(missing) = %v", err)
	}
	if err := client.DiscardWorkload(ctx, workload, podmanTestTransaction); !errors.Is(err, ErrProtocol) {
		t.Fatalf("DiscardWorkload(missing) = %v", err)
	}
	err = client.ApplyWorkloadTransition(ctx, application.WorkloadTransition{})
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ApplyWorkloadTransition(invalid) = %v", err)
	}
	transitionProbe, err := client.ProbeWorkloadTransition(ctx, application.WorkloadTransition{})
	if transitionProbe != (application.WorkloadTransitionProbe{}) || !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ProbeWorkloadTransition(invalid) = %#v, %v", transitionProbe, err)
	}
}

func TestPodmanTransitionProbesRejectInconsistentIdentity(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerExited,
	}
	client := connectedPodmanWorkloadClient(t, state.handler)
	expected := application.ExistingWorkload{ID: podmanTestContainerID, Name: "different-name"}
	probe, err := client.probeRemovedWorkload(context.Background(), expected)
	if probe != (application.WorkloadTransitionProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("probeRemovedWorkload(inconsistent) = %#v, %v", probe, err)
	}

	missing := expected
	missing.ID = strings.Repeat("e", containerIDHexBytes)
	probe, err = client.probeExistingWorkload(context.Background(), missing)
	if err != nil || probe.State != application.WorkloadEffectProbeMissing {
		t.Fatalf("probeExistingWorkload(missing) = %#v, %v", probe, err)
	}
}

func TestPodmanOwnedContainerListFailsClosed(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	for _, document := range []string{`{}`, `[{"Id":"short"}]`, `[
		{"Id":"` + podmanTestContainerID + `"},
		{"Id":"` + strings.Repeat("e", containerIDHexBytes) + `"}
	]`} {
		t.Run(document, func(t *testing.T) {
			t.Parallel()
			client := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, request *http.Request) {
				if request.URL.Path == libpodPrefix+"/containers/json" {
					writePodmanJSON(writer, document)

					return
				}
				writePodmanNotFound(writer)
			})
			probe, err := client.ProbeCreatedWorkload(
				context.Background(), workload, podmanTestTransaction, "",
			)
			if !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) || !errors.Is(err, ErrProtocol) {
				t.Fatalf("ProbeCreatedWorkload() = %#v, %v", probe, err)
			}
		})
	}
}

func TestPodmanOwnershipBranches(t *testing.T) {
	t.Parallel()

	imageConfig := domain.Hash([]byte("image config"))
	reference := domain.Hash([]byte("reference"))
	manifest := domain.Hash([]byte("manifest"))
	if ownership := decodeOwnership(nil, imageConfig, reference, manifest); ownership.Status != domain.OwnershipUnmanaged {
		t.Fatalf("decodeOwnership(unmanaged) = %#v", ownership)
	}
	labels := workloadOwnershipLabels(podmanTestWorkload(t), podmanTestTransaction)
	labels[maniudLabelPrefix+"unknown"] = "value"
	if ownership := decodeOwnership(labels, imageConfig, reference, manifest); ownership != (domain.WorkloadOwnership{}) {
		t.Fatalf("decodeOwnership(unknown label) = %#v", ownership)
	}
	delete(labels, maniudLabelPrefix+"unknown")
	delete(labels, domain.LabelService)
	if supportedOwnershipLabels(labels) {
		t.Fatal("supportedOwnershipLabels(missing) = true")
	}
	labels[domain.LabelService] = podmanTestService
	labels[domain.LabelImageConfigDigest] = podmanTestInvalid
	if ownership := decodeOwnership(labels, imageConfig, reference, manifest); ownership != (domain.WorkloadOwnership{}) {
		t.Fatalf("decodeOwnership(invalid digest) = %#v", ownership)
	}
}

//nolint:cyclop // The test keeps independent protocol rejection paths in one fixture matrix.
func TestPodmanObservedWorkloadAndInspectMappingBranches(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	container := Container{
		ID: podmanTestContainerID, Name: workload.ContainerName, ImageReference: workload.Image.Reference,
		StartedAt:   time.Date(2026, 9, 2, 10, 0, 0, 123456789, time.UTC),
		ImageConfig: workload.Image.ImageConfig, PlatformManifest: workload.Image.PlatformManifest,
		WorkloadSpec: workload.WorkloadSpec, State: ContainerRunning,
		Health: application.WorkloadHealth{Status: application.WorkloadHealthAbsent},
	}
	observation, err := podmanWorkloadObservation(
		ContainerProbe{State: ContainerProbeObserved, Container: container}, workload, libpodAPIVersion,
	)
	if err != nil || observation.State != application.WorkloadObservationPresent ||
		observation.Lifecycle != application.WorkloadLifecycleRunning ||
		observation.StartedAt != container.StartedAt || !observation.ConfigurationMatches {
		t.Fatalf("podmanWorkloadObservation(observed) = %#v, %v", observation, err)
	}
	invalidStorage := workload
	invalidStorage.SourceDigest = domain.Digest{}
	observation, err = podmanWorkloadObservation(
		ContainerProbe{State: ContainerProbeObserved, Container: container}, invalidStorage, libpodAPIVersion,
	)
	if !reflect.DeepEqual(observation, application.WorkloadObservation{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("podmanWorkloadObservation(invalid storage) = %#v, %v", observation, err)
	}

	runtimeState := podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}
	var payload map[string]any
	if unmarshalErr := json.Unmarshal([]byte(runtimeState.inspectDocument()), &payload); unmarshalErr != nil {
		t.Fatal(unmarshalErr)
	}
	payload["Id"] = strings.Repeat("e", containerIDHexBytes)
	document, marshalErr := json.Marshal(payload)
	if marshalErr != nil {
		t.Fatal(marshalErr)
	}
	client := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, _ *http.Request) {
		writePodmanJSON(writer, string(document))
	})
	probe, err := client.ProbeContainer(context.Background(), podmanTestContainerID)
	if !reflect.DeepEqual(probe, ContainerProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeContainer(identity mismatch) = %#v, %v", probe, err)
	}
}

func TestPodmanWorkloadTransportErrorsPropagate(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	client := connectedPodmanWorkloadClient(t, func(http.ResponseWriter, *http.Request) {})
	failPodmanWorkloadRequests(client, func(*http.Request) bool { return true })
	probe, err := client.ProbeContainer(context.Background(), workload.ContainerName)
	if !reflect.DeepEqual(probe, ContainerProbe{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeContainer(transport) = %#v, %v", probe, err)
	}
	observation, err := client.ObserveWorkload(context.Background(), workload)
	if !reflect.DeepEqual(observation, application.WorkloadObservation{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ObserveWorkload(transport) = %#v, %v", observation, err)
	}
	err = client.StartWorkload(context.Background(), workload, podmanTestTransaction)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("StartWorkload(probe transport) = %v", err)
	}
	err = client.DiscardWorkload(context.Background(), workload, podmanTestTransaction)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("DiscardWorkload(probe transport) = %v", err)
	}
}

func TestPodmanMutationTransportErrorsPropagate(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	ctx := context.Background()
	createdState := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerCreated,
	}
	createdClient := connectedPodmanWorkloadClient(t, createdState.handler)
	failPodmanWorkloadRequests(createdClient, func(request *http.Request) bool {
		return request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/start")
	})
	if err := createdClient.StartWorkload(ctx, workload, podmanTestTransaction); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("StartWorkload(mutation transport) = %v", err)
	}

	runningState := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}
	runningClient := connectedPodmanWorkloadClient(t, runningState.handler)
	failPodmanWorkloadRequests(runningClient, func(request *http.Request) bool {
		return request.Method == http.MethodDelete
	})
	if err := runningClient.DiscardWorkload(ctx, workload, podmanTestTransaction); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("DiscardWorkload(mutation transport) = %v", err)
	}
}

func TestPodmanCreateAndBodyErrorsPropagate(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	oversized := workload
	oversized.Labels = make([]string, 5000)
	for index := range oversized.Labels {
		oversized.Labels[index] = fmt.Sprintf("label-%d=%s", index, strings.Repeat("x", maximumTextBytes-20))
	}
	oversized.EffectiveDigest = domain.ComputeEffectiveDigest(oversized)
	client := connectedPodmanWorkloadClient(t, func(http.ResponseWriter, *http.Request) {})
	if identifier, err := client.CreateWorkload(
		context.Background(), oversized, podmanTestTransaction,
		testCreateOptions(),
	); identifier != "" || !errors.Is(err, ErrProtocol) {
		t.Fatalf("CreateWorkload(oversized) = %q, %v", identifier, err)
	}
	failPodmanWorkloadRequests(client, func(request *http.Request) bool {
		return request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/containers/create")
	})
	if identifier, err := client.CreateWorkload(
		context.Background(), workload, podmanTestTransaction,
		testCreateOptions(),
	); identifier != "" || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("CreateWorkload(transport) = %q, %v", identifier, err)
	}

	closeFailure := &http.Response{
		StatusCode: http.StatusOK, Header: http.Header{podmanContentType: {podmanJSONType}},
		Body: &podmanWorkloadErrorBody{data: []byte(`{}`), closeErr: errPodmanClientTest},
	}
	if _, err := readPodmanJSONResponse(closeFailure, http.StatusOK); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("readPodmanJSONResponse(close) = %v", err)
	}
	for _, body := range []podmanWorkloadErrorBody{
		{readErr: errPodmanClientTest},
		{closeErr: errPodmanClientTest},
	} {
		response := &http.Response{StatusCode: http.StatusNoContent, Body: &body}
		if err := decodePodmanEmptyResponse(response, http.StatusNoContent); !errors.Is(err, ErrUnavailable) {
			t.Fatalf("decodePodmanEmptyResponse(IO) = %v", err)
		}
	}
}

func TestPodmanEffectCandidateIntegrityBranches(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerCreated,
	}
	missingOwned := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == libpodPrefix+"/containers/json" {
			writePodmanJSON(writer, `[]`)

			return
		}
		state.handler(writer, request)
	})
	probe, err := missingOwned.ProbeCreatedWorkload(
		context.Background(), workload, podmanTestTransaction, "",
	)
	if !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(unlisted owned name) = %#v, %v", probe, err)
	}

	otherID := strings.Repeat("e", containerIDHexBytes)
	inconsistent := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, request *http.Request) {
		switch {
		case request.URL.Path == libpodPrefix+"/containers/json":
			writePodmanJSON(writer, `[{"Id":"`+otherID+`"}]`)
		case strings.Contains(request.URL.Path, otherID):
			writePodmanJSON(writer, strings.ReplaceAll(state.inspectDocument(), podmanTestContainerID, otherID))
		default:
			state.handler(writer, request)
		}
	})
	probe, err = inconsistent.ProbeCreatedWorkload(
		context.Background(), workload, podmanTestTransaction, "",
	)
	if !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(inconsistent selectors) = %#v, %v", probe, err)
	}
}

func TestPodmanEffectProbeRejectsInvalidStorageEvidence(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	workload.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: "/state"}}
	workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerCreated,
	}
	client := connectedPodmanWorkloadClient(t, state.handler)
	probe, err := client.ProbeCreatedWorkload(
		context.Background(), workload, podmanTestTransaction, "",
	)
	if !reflect.DeepEqual(probe, application.WorkloadEffectProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeCreatedWorkload(invalid storage) = %#v, %v", probe, err)
	}
}

func TestPodmanOwnedContainerProbeErrors(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	ctx := context.Background()
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerCreated,
	}
	probeFailure := connectedPodmanWorkloadClient(t, state.handler)
	failPodmanWorkloadRequests(probeFailure, func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, podmanTestContainerID+"/json")
	})
	probe, err := probeFailure.probeOwnedContainer(ctx, workload.ServiceName, podmanTestTransaction)
	if !reflect.DeepEqual(probe, ContainerProbe{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("probeOwnedContainer(inspect transport) = %#v, %v", probe, err)
	}

	foreign := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: "foreign-transaction",
		present: true, name: workload.ContainerName, lifecycle: ContainerCreated,
	}
	foreignClient := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == libpodPrefix+"/containers/json" {
			writePodmanJSON(writer, `[{"Id":"`+podmanTestContainerID+`"}]`)

			return
		}
		foreign.handler(writer, request)
	})
	probe, err = foreignClient.probeOwnedContainer(ctx, workload.ServiceName, podmanTestTransaction)
	if !reflect.DeepEqual(probe, ContainerProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("probeOwnedContainer(foreign ownership) = %#v, %v", probe, err)
	}
}

func TestPodmanOwnedContainerListErrors(t *testing.T) {
	t.Parallel()

	client := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if request.URL.Path == libpodPrefix+"/containers/json" {
			writer.WriteHeader(http.StatusInternalServerError)

			return
		}
		writePodmanNotFound(writer)
	})
	identifier, found, err := client.ownedContainerID(context.Background(), "bad/name", podmanTestTransaction)
	if identifier != "" || found || !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ownedContainerID(invalid) = %q, %t, %v", identifier, found, err)
	}
	identifier, found, err = client.ownedContainerID(
		context.Background(), podmanTestService, podmanTestTransaction,
	)
	if identifier != "" || found || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ownedContainerID(status) = %q, %t, %v", identifier, found, err)
	}
	podmanAssertOwnedContainerTransportError(t, client)
}

func podmanAssertOwnedContainerTransportError(t *testing.T, client *Client) {
	t.Helper()
	failPodmanWorkloadRequests(client, func(*http.Request) bool { return true })
	identifier, found, err := client.ownedContainerID(
		context.Background(), podmanTestService, podmanTestTransaction,
	)
	if identifier != "" || found || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ownedContainerID(transport) = %q, %t, %v", identifier, found, err)
	}
}

func TestPodmanCreateResponseRejectsTypedMismatch(t *testing.T) {
	t.Parallel()

	response := &http.Response{
		StatusCode: http.StatusCreated, Header: http.Header{podmanContentType: {podmanJSONType}},
		Body: io.NopCloser(strings.NewReader(`{"Id":1,"Warnings":[]}`)),
	}
	identifier, err := decodePodmanCreateResponse(response)
	if identifier != "" || !errors.Is(err, ErrProtocol) {
		t.Fatalf("decodePodmanCreateResponse(type mismatch) = %q, %v", identifier, err)
	}
}

func podmanExistingWorkloadFromProbe(
	t *testing.T,
	client *Client,
	workload domain.DesiredWorkload,
) application.ExistingWorkload {
	t.Helper()
	probe, err := client.ProbeCreatedWorkload(
		context.Background(), workload, podmanTestTransaction, "",
	)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved {
		t.Fatalf("ProbeCreatedWorkload() = %#v, %v", probe, err)
	}

	return application.ExistingWorkload{
		ID: probe.Workload.ID, Name: probe.Workload.Name,
		ConfigurationDigest: probe.Workload.ConfigurationDigest,
		Lifecycle:           probe.Workload.Lifecycle,
		Ownership:           probe.Workload.Ownership,
	}
}

func TestPodmanApplyTransitionErrorsPropagate(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerRunning,
	}
	client := connectedPodmanWorkloadClient(t, state.handler)
	current := podmanExistingWorkloadFromProbe(t, client, workload)
	after := current
	after.Lifecycle = application.WorkloadLifecycleExited
	transition := application.WorkloadTransition{
		Kind: application.WorkloadTransitionStop, Before: current, After: after,
	}
	mismatch := transition
	mismatch.Before.ConfigurationDigest = domain.Hash([]byte("mismatch"))
	mismatch.After.ConfigurationDigest = mismatch.Before.ConfigurationDigest
	if err := client.ApplyWorkloadTransition(context.Background(), mismatch); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ApplyWorkloadTransition(mismatch) = %v", err)
	}
	failPodmanWorkloadRequests(client, func(request *http.Request) bool {
		return request.Method == http.MethodPost && strings.HasSuffix(request.URL.Path, "/stop")
	})
	if err := client.ApplyWorkloadTransition(context.Background(), transition); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ApplyWorkloadTransition(mutation transport) = %v", err)
	}

	probeFailure := connectedPodmanWorkloadClient(t, state.handler)
	failPodmanWorkloadRequests(probeFailure, func(*http.Request) bool { return true })
	if err := probeFailure.ApplyWorkloadTransition(
		context.Background(), transition,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ApplyWorkloadTransition(probe transport) = %v", err)
	}
}

func TestPodmanTransitionProbeErrorsPropagate(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerExited,
	}
	client := connectedPodmanWorkloadClient(t, state.handler)
	current := podmanExistingWorkloadFromProbe(t, client, workload)
	after := current
	after.Lifecycle = application.WorkloadLifecycleRunning
	transition := application.WorkloadTransition{
		Kind: application.WorkloadTransitionRestoreStart, Before: current, After: after,
	}
	failPodmanWorkloadRequests(client, func(*http.Request) bool { return true })
	probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
	if probe != (application.WorkloadTransitionProbe{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeWorkloadTransition(transport) = %#v, %v", probe, err)
	}

	client.version.Protocol = "5.0.0"
	method, path, query := client.podmanWorkloadTransitionRequest(application.WorkloadTransition{})
	if method != "" || path != "" || query != nil {
		t.Fatalf("podmanWorkloadTransitionRequest(drift) = %q, %q, %#v", method, path, query)
	}
}

func TestPodmanRemovedWorkloadProbeBranches(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: workload.ContainerName, lifecycle: ContainerExited,
	}
	client := connectedPodmanWorkloadClient(t, state.handler)
	current := podmanExistingWorkloadFromProbe(t, client, workload)
	probe, err := client.probeRemovedWorkload(context.Background(), current)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved || probe.Workload != current {
		t.Fatalf("probeRemovedWorkload(observed) = %#v, %v", probe, err)
	}

	idFailure := connectedPodmanWorkloadClient(t, state.handler)
	failPodmanWorkloadRequests(idFailure, func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, current.ID+"/json")
	})
	probe, err = idFailure.probeRemovedWorkload(context.Background(), current)
	if probe != (application.WorkloadTransitionProbe{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("probeRemovedWorkload(ID transport) = %#v, %v", probe, err)
	}

	nameFailure := connectedPodmanWorkloadClient(t, state.handler)
	failPodmanWorkloadRequests(nameFailure, func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, current.Name+"/json")
	})
	probe, err = nameFailure.probeRemovedWorkload(context.Background(), current)
	if probe != (application.WorkloadTransitionProbe{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("probeRemovedWorkload(name transport) = %#v, %v", probe, err)
	}
}

func TestPodmanRenameProbeRejectsOldName(t *testing.T) {
	t.Parallel()

	workload := podmanTestWorkload(t)
	oldName := workload.ContainerName
	state := &podmanWorkloadRuntimeState{
		t: t, workload: workload, transaction: podmanTestTransaction,
		present: true, name: "renamed-api", lifecycle: ContainerExited,
	}
	client := connectedPodmanWorkloadClient(t, state.handler)
	after := podmanExistingWorkloadFromProbe(t, client, workload)
	before := after
	before.Name = oldName
	transition := application.WorkloadTransition{
		Kind: application.WorkloadTransitionRename, Before: before, After: after,
	}
	failPodmanWorkloadRequests(client, func(request *http.Request) bool {
		return strings.Contains(request.URL.Path, oldName+"/json")
	})
	probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
	if probe != (application.WorkloadTransitionProbe{}) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeWorkloadTransition(old-name transport) = %#v, %v", probe, err)
	}

	oldObserved := connectedPodmanWorkloadClient(t, func(writer http.ResponseWriter, request *http.Request) {
		if strings.Contains(request.URL.Path, oldName+"/json") {
			oldState := *state
			oldState.name = oldName
			oldState.inspect(writer, request)

			return
		}
		state.handler(writer, request)
	})
	probe, err = oldObserved.ProbeWorkloadTransition(context.Background(), transition)
	if probe != (application.WorkloadTransitionProbe{}) || !errors.Is(err, ErrProtocol) {
		t.Fatalf("ProbeWorkloadTransition(old name observed) = %#v, %v", probe, err)
	}
}
