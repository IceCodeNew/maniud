package docker

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	testRetainedContainerName = "maniud-old-01020304"
	testOtherContainerID      = "ffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffffff"
)

type transitionEngineState struct {
	testing    *testing.T
	transition application.WorkloadTransition
	name       string
	lifecycle  application.WorkloadLifecycle
	present    bool
	mutations  int
	mutex      sync.Mutex
}

func (state *transitionEngineState) ServeHTTP(response http.ResponseWriter, request *http.Request) {
	state.testing.Helper()
	state.mutex.Lock()
	defer state.mutex.Unlock()

	response.Header().Set(contentTypeHeader, jsonContentType)

	if strings.HasSuffix(request.URL.Path, "/json") {
		state.serveProbe(response, request)

		return
	}

	state.serveMutation(response, request)
}

func (state *transitionEngineState) serveProbe(response http.ResponseWriter, request *http.Request) {
	reference := strings.TrimSuffix(strings.TrimPrefix(request.URL.Path, "/v1.54/containers/"), "/json")
	if !state.present || reference != testContainerID && reference != state.name {
		response.WriteHeader(http.StatusNotFound)
		_, _ = io.WriteString(response, `{"message":"No such container"}`)

		return
	}

	_, _ = io.WriteString(response, namedContainerDocument(
		state.testing,
		state.name,
		managedContainerLabels(),
		transitionContainerState(state.lifecycle),
	))
}

func (state *transitionEngineState) serveMutation(response http.ResponseWriter, request *http.Request) {
	wantMethod, wantPath, wantQuery := workloadTransitionRequest(state.transition)
	if request.Method != wantMethod || request.URL.Path != wantPath ||
		request.URL.Query().Encode() != wantQuery.Encode() || request.ContentLength != 0 {
		state.testing.Errorf(
			"transition request = %s %s length %d",
			request.Method,
			request.URL.String(),
			request.ContentLength,
		)
		response.WriteHeader(http.StatusBadRequest)

		return
	}

	state.mutations++
	state.applyTransition()
	response.WriteHeader(http.StatusNoContent)
}

func (state *transitionEngineState) applyTransition() {
	state.testing.Helper()

	switch state.transition.Kind {
	case application.WorkloadTransitionStop:
		state.lifecycle = application.WorkloadLifecycleExited
	case application.WorkloadTransitionRename:
		state.name = state.transition.After.Name
	case application.WorkloadTransitionRemove:
		state.present = false
	case application.WorkloadTransitionRestoreStart:
		state.lifecycle = application.WorkloadLifecycleRunning
	case application.WorkloadTransitionUnknown:
		state.testing.Errorf("unexpected unknown transition kind")
	default:
		state.testing.Errorf("unexpected transition kind %d", state.transition.Kind)
	}
}

func TestDockerAppliesAndProbesExactWorkloadTransitions(t *testing.T) {
	t.Parallel()

	for _, transition := range dockerWorkloadTransitions(t) {
		t.Run(workloadTransitionTestName(transition.Kind), func(t *testing.T) {
			t.Parallel()

			state := &transitionEngineState{
				testing:    t,
				transition: transition,
				name:       transition.Before.Name,
				lifecycle:  transition.Before.Lifecycle,
				present:    true,
			}
			client := connectedTestClient(t, state)

			err := client.ApplyWorkloadTransition(context.Background(), transition)
			if err != nil {
				t.Fatalf("ApplyWorkloadTransition() error = %v", err)
			}

			probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
			if err != nil || !dockerTransitionProbeMatches(probe, transition) || state.mutations != 1 {
				t.Fatalf("ProbeWorkloadTransition() = %#v, %v, mutations %d", probe, err, state.mutations)
			}
		})
	}
}

func TestDockerTransitionRecoversAlreadyAppliedPostcondition(t *testing.T) {
	t.Parallel()

	transition := dockerWorkloadTransitions(t)[0]
	state := &transitionEngineState{
		testing:    t,
		transition: transition,
		name:       transition.After.Name,
		lifecycle:  transition.After.Lifecycle,
		present:    true,
	}
	client := connectedTestClient(t, state)

	err := client.ApplyWorkloadTransition(context.Background(), transition)
	if !errors.Is(err, ErrProtocol) || state.mutations != 0 {
		t.Fatalf("ApplyWorkloadTransition(already applied) = %v, mutations %d", err, state.mutations)
	}

	probe, err := client.ProbeWorkloadTransition(context.Background(), transition)
	if err != nil || probe.Workload != transition.After {
		t.Fatalf("ProbeWorkloadTransition(already applied) = %#v, %v", probe, err)
	}
}

func TestDockerTransitionRejectsInvalidInputAndUnavailableRuntime(t *testing.T) {
	t.Parallel()

	transition := dockerWorkloadTransitions(t)[0]
	invalid := transition
	invalid.Before.ID = testInvalidValue

	for _, test := range []struct {
		name       string
		client     *Client
		transition application.WorkloadTransition
	}{
		{name: testNilClientName, client: nil, transition: transition},
		{name: "invalid transition", client: connectedTestClient(t, nil), transition: invalid},
		{name: "invalid version", client: testClient(nil), transition: transition},
	} {
		err := test.client.ApplyWorkloadTransition(context.Background(), test.transition)
		if !errors.Is(err, ErrUnsupportedWorkload) {
			t.Fatalf("ApplyWorkloadTransition(%s) = %v", test.name, err)
		}

		probe, probeErr := test.client.ProbeWorkloadTransition(context.Background(), test.transition)
		if !errors.Is(probeErr, ErrUnsupportedWorkload) || probe != (application.WorkloadTransitionProbe{}) {
			t.Fatalf("ProbeWorkloadTransition(%s) = %#v, %v", test.name, probe, probeErr)
		}
	}

	transport := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	transport.version = testVersion()

	err := transport.ApplyWorkloadTransition(context.Background(), transition)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ApplyWorkloadTransition(transport) = %v", err)
	}

	_, err = transport.ProbeWorkloadTransition(context.Background(), transition)
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ProbeWorkloadTransition(transport) = %v", err)
	}
}

func TestDockerTransitionProbesFailClosedOnAmbiguousNames(t *testing.T) {
	t.Parallel()

	rename := dockerWorkloadTransitions(t)[1]
	renameDocument := namedContainerDocument(t, rename.After.Name, managedContainerLabels(), exitedContainerState())
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		if strings.HasSuffix(request.URL.Path, "/json") {
			_, _ = io.WriteString(response, renameDocument)

			return
		}
		response.WriteHeader(http.StatusNotFound)
	}))

	probe, err := client.ProbeWorkloadTransition(context.Background(), rename)
	if !errors.Is(err, ErrProtocol) || probe != (application.WorkloadTransitionProbe{}) {
		t.Fatalf("ProbeWorkloadTransition(rename ambiguity) = %#v, %v", probe, err)
	}

	remove := dockerWorkloadTransitions(t)[2]
	byID := namedContainerDocument(t, remove.Before.Name, managedContainerLabels(), exitedContainerState())
	byName := strings.Replace(byID, testContainerID, testOtherContainerID, 1)
	client = connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		if strings.Contains(request.URL.Path, testContainerID) {
			_, _ = io.WriteString(response, byID)
		} else {
			_, _ = io.WriteString(response, byName)
		}
	}))

	probe, err = client.ProbeWorkloadTransition(context.Background(), remove)
	if !errors.Is(err, ErrProtocol) || probe != (application.WorkloadTransitionProbe{}) {
		t.Fatalf("ProbeWorkloadTransition(remove ambiguity) = %#v, %v", probe, err)
	}
}

func TestDockerTransitionRejectsObservedOldNameAfterRename(t *testing.T) {
	t.Parallel()

	rename := dockerWorkloadTransitions(t)[1]
	afterDocument := namedContainerDocument(t, rename.After.Name, managedContainerLabels(), exitedContainerState())
	beforeDocument := namedContainerDocument(t, rename.Before.Name, managedContainerLabels(), exitedContainerState())
	oldNameObserved := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		if strings.Contains(request.URL.Path, rename.Before.Name) {
			_, _ = io.WriteString(response, beforeDocument)
		} else {
			_, _ = io.WriteString(response, afterDocument)
		}
	}))

	probe, err := oldNameObserved.ProbeWorkloadTransition(context.Background(), rename)
	if !errors.Is(err, ErrProtocol) || probe != (application.WorkloadTransitionProbe{}) {
		t.Fatalf("ProbeWorkloadTransition(old name observed) = %#v, %v", probe, err)
	}
}

func TestDockerRemoveTransitionReportsProbeFailures(t *testing.T) {
	t.Parallel()

	remove := dockerWorkloadTransitions(t)[2]
	probe, err := probeTransitionWithResponses(
		t,
		remove,
		dockerResponseFixture(http.StatusNotFound, `{"message":"No such container"}`),
		nil,
	)
	if !errors.Is(err, ErrUnavailable) || probe != (application.WorkloadTransitionProbe{}) {
		t.Fatalf("ProbeWorkloadTransition(name transport) = %#v, %v", probe, err)
	}

	probe, err = probeTransitionWithResponses(t, remove, nil)
	if !errors.Is(err, ErrUnavailable) || probe != (application.WorkloadTransitionProbe{}) {
		t.Fatalf("ProbeWorkloadTransition(ID transport) = %#v, %v", probe, err)
	}
}

func TestDockerRemoveTransitionReturnsExactObservedWorkload(t *testing.T) {
	t.Parallel()

	remove := dockerWorkloadTransitions(t)[2]
	removedDocument := namedContainerDocument(t, remove.Before.Name, managedContainerLabels(), exitedContainerState())
	probe, err := probeTransitionWithResponses(
		t,
		remove,
		dockerResponseFixture(http.StatusOK, removedDocument),
		dockerResponseFixture(http.StatusOK, removedDocument),
	)
	if err != nil || probe.State != application.WorkloadEffectProbeObserved || probe.Workload != remove.Before {
		t.Fatalf("ProbeWorkloadTransition(still observed) = %#v, %v", probe, err)
	}
}

func TestDockerTransitionReturnsTypedMissingWorkload(t *testing.T) {
	t.Parallel()

	stop := dockerWorkloadTransitions(t)[0]
	probe, err := probeTransitionWithResponses(
		t,
		stop,
		dockerResponseFixture(http.StatusNotFound, `{"message":"No such container"}`),
	)
	if err != nil || probe.State != application.WorkloadEffectProbeMissing {
		t.Fatalf("ProbeWorkloadTransition(missing) = %#v, %v", probe, err)
	}
}

func TestDockerTransitionReportsMutationFailures(t *testing.T) {
	t.Parallel()

	transition := dockerWorkloadTransitions(t)[0]
	document := namedContainerDocument(
		t,
		transition.Before.Name,
		managedContainerLabels(),
		runningContainerState(),
	)
	requests := 0
	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		requests++
		if requests == 1 {
			return dockerHTTPResponse(http.StatusOK, document), nil
		}

		return nil, io.ErrUnexpectedEOF
	}))
	client.version = testVersion()

	err := client.ApplyWorkloadTransition(context.Background(), transition)
	if !errors.Is(err, ErrUnavailable) || requests != 2 {
		t.Fatalf("ApplyWorkloadTransition(mutation transport) = %v, requests %d", err, requests)
	}

	state := &transitionEngineState{
		testing:    t,
		transition: transition,
		name:       transition.Before.Name,
		lifecycle:  transition.Before.Lifecycle,
		present:    true,
	}
	unexpectedStatus := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		if strings.HasSuffix(request.URL.Path, "/json") {
			state.serveProbe(response, request)

			return
		}

		response.Header().Set(contentTypeHeader, jsonContentType)
		response.WriteHeader(http.StatusConflict)
		_, _ = io.WriteString(response, `{"message":"conflict"}`)
	}))

	err = unexpectedStatus.ApplyWorkloadTransition(context.Background(), transition)
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("ApplyWorkloadTransition(unexpected status) = %v", err)
	}
}

func TestDockerTransitionRequestRejectsUnexpectedKind(t *testing.T) {
	t.Parallel()

	method, path, query := workloadTransitionRequest(application.WorkloadTransition{})
	if method != "" || path != "" || query != nil {
		t.Fatalf("workloadTransitionRequest(invalid) = %q, %q, %#v", method, path, query)
	}
}

func dockerWorkloadTransitions(t *testing.T) []application.WorkloadTransition {
	t.Helper()

	container := observedContainerProbe(t).Container
	before := application.ExistingWorkload{
		ID:                  container.ID,
		Name:                container.Name,
		ConfigurationDigest: containerConfigurationDigest(container),
		Lifecycle:           application.WorkloadLifecycleRunning,
		Ownership:           container.Ownership,
	}
	stopped := before
	stopped.Lifecycle = application.WorkloadLifecycleExited
	stop := application.WorkloadTransition{
		Kind:   application.WorkloadTransitionStop,
		Before: before,
		After:  stopped,
	}
	renamed := stopped
	renamed.Name = testRetainedContainerName
	rename := application.WorkloadTransition{
		Kind:   application.WorkloadTransitionRename,
		Before: stopped,
		After:  renamed,
	}
	remove := application.WorkloadTransition{
		Kind:   application.WorkloadTransitionRemove,
		Before: renamed,
	}
	restore := application.WorkloadTransition{
		Kind:   application.WorkloadTransitionRestoreStart,
		Before: stopped,
		After:  before,
	}

	return []application.WorkloadTransition{stop, rename, remove, restore}
}

func transitionContainerState(lifecycle application.WorkloadLifecycle) *containerStateFixture {
	if lifecycle == application.WorkloadLifecycleRunning {
		return runningContainerState()
	}

	return exitedContainerState()
}

func dockerTransitionProbeMatches(
	probe application.WorkloadTransitionProbe,
	transition application.WorkloadTransition,
) bool {
	if transition.Kind == application.WorkloadTransitionRemove {
		return probe.State == application.WorkloadEffectProbeMissing &&
			probe.Workload == (application.ExistingWorkload{})
	}

	return probe.State == application.WorkloadEffectProbeObserved && probe.Workload == transition.After
}

func workloadTransitionTestName(kind application.WorkloadTransitionKind) string {
	switch kind {
	case application.WorkloadTransitionStop:
		return "stop"
	case application.WorkloadTransitionRename:
		return "rename"
	case application.WorkloadTransitionRemove:
		return "remove"
	case application.WorkloadTransitionRestoreStart:
		return "restore start"
	case application.WorkloadTransitionUnknown:
		return "unknown"
	default:
		return "unknown"
	}
}

func probeTransitionWithResponses(
	t *testing.T,
	transition application.WorkloadTransition,
	responses ...*transitionResponseFixture,
) (application.WorkloadTransitionProbe, error) {
	t.Helper()

	next := 0
	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		if next >= len(responses) || responses[next] == nil {
			return nil, io.ErrUnexpectedEOF
		}

		fixture := responses[next]
		next++

		return dockerHTTPResponse(fixture.status, fixture.body), nil
	}))
	client.version = testVersion()

	return client.ProbeWorkloadTransition(context.Background(), transition)
}

type transitionResponseFixture struct {
	status int
	body   string
}

func dockerResponseFixture(status int, body string) *transitionResponseFixture {
	return &transitionResponseFixture{status: status, body: body}
}

func dockerHTTPResponse(status int, body string) *http.Response {
	return &http.Response{ //nolint:exhaustruct // Transition tests need status, header, and body.
		StatusCode: status,
		Header:     http.Header{contentTypeHeader: {jsonContentType}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}
