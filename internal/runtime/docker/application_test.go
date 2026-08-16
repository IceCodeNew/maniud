package docker

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net/http"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestInspectReturnsApplicationRuntimeEvidence(t *testing.T) {
	t.Parallel()

	daemon := Daemon{
		ID:           "engine-id",
		Driver:       "overlay2",
		OS:           testOS,
		Architecture: testArchitecture,
		Rootless:     true,
	}
	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, daemonDocument(
			daemon.ID,
			daemon.Driver,
			daemon.OS,
			daemon.Architecture,
			testProduct,
			daemon.Rootless,
		))
	}))

	got, err := client.Inspect(context.Background())
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}

	want := application.RuntimeEvidence{
		Kind: domain.RuntimeDocker,
		Platform: domain.Platform{
			OS:           testOS,
			Architecture: testArchitecture,
			Variant:      "",
		},
		Digest: dockerExecutionDigest(daemon),
	}
	if got != want {
		t.Fatalf("Inspect() = %#v, want %#v", got, want)
	}

	daemon.Rootless = false
	if dockerExecutionDigest(daemon) == got.Digest {
		t.Fatal("dockerExecutionDigest(rootless drift) did not change")
	}
}

func TestInspectRejectsUnsupportedDaemonPlatform(t *testing.T) {
	t.Parallel()

	client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, _ *http.Request) {
		response.Header().Set(contentTypeHeader, jsonContentType)
		_, _ = io.WriteString(response, daemonDocument(
			"engine-id",
			"overlay2",
			testOS,
			"s390x",
			testProduct,
			false,
		))
	}))
	client.version.Architecture = "s390x"

	got, err := client.Inspect(context.Background())

	var empty application.RuntimeEvidence

	if got != empty || !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("Inspect(unsupported platform) = %#v, %v", got, err)
	}
}

func TestInspectContainsDaemonFailure(t *testing.T) {
	t.Parallel()

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	client.version = testVersion()

	got, err := client.Inspect(context.Background())

	var empty application.RuntimeEvidence

	if got != empty || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Inspect() = %#v, %v", got, err)
	}
}

func TestCheckWorkloadAcceptsSupportedDesiredState(t *testing.T) {
	t.Parallel()

	client := testClient(http.DefaultTransport)
	client.version = testVersion()

	err := client.CheckWorkload(validApplicationWorkload(t))
	if err != nil {
		t.Fatalf("CheckWorkload() error = %v", err)
	}
}

func TestCheckWorkloadRejectsUnsupportedDesiredState(t *testing.T) {
	t.Parallel()

	client := testClient(http.DefaultTransport)
	client.version = testVersion()

	for _, workload := range unsupportedApplicationWorkloads(t) {
		err := client.CheckWorkload(workload)
		if !errors.Is(err, ErrUnsupportedWorkload) {
			t.Fatalf("CheckWorkload(%#v) error = %v", workload, err)
		}
	}

	client.version.Protocol = testUnsupportedAPIVersion

	err := client.CheckWorkload(validApplicationWorkload(t))
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("CheckWorkload(invalid version) error = %v", err)
	}

	var nilClient *Client

	err = nilClient.CheckWorkload(validApplicationWorkload(t))
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("CheckWorkload(nil client) error = %v", err)
	}
}

type applicationObservationTest struct {
	name       string
	status     int
	body       string
	mutate     func(*domain.DesiredWorkload)
	wantState  application.WorkloadObservationState
	wantConfig bool
	wantOwner  domain.OwnershipStatus
}

func TestObserveWorkloadMapsDockerProbe(t *testing.T) {
	t.Parallel()

	for _, test := range applicationObservationTests(t) {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			client := connectedTestClient(t, http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
				if request.URL.Path != "/v1.54/containers/example-api/json" {
					t.Errorf("request path = %q", request.URL.Path)
				}

				response.Header().Set(contentTypeHeader, jsonContentType)
				response.WriteHeader(test.status)
				_, _ = io.WriteString(response, test.body)
			}))
			workload := validApplicationWorkload(t)
			test.mutate(&workload)

			got, err := client.ObserveWorkload(context.Background(), workload)
			if err != nil {
				t.Fatalf("ObserveWorkload() error = %v", err)
			}

			valid := got.State == test.wantState && got.ConfigurationMatches == test.wantConfig &&
				got.Ownership.Status == test.wantOwner && got.Running == (test.wantState == application.WorkloadObservationPresent)
			if !valid {
				t.Fatalf("ObserveWorkload() = %#v", got)
			}
		})
	}
}

func applicationObservationTests(t *testing.T) []applicationObservationTest {
	t.Helper()

	observed := validContainerDocument(t, managedContainerLabels(), runningContainerState())

	return []applicationObservationTest{
		{
			name:       testMissingValue,
			status:     http.StatusNotFound,
			body:       `{"message":"No such container: example-api"}`,
			mutate:     func(*domain.DesiredWorkload) {},
			wantState:  application.WorkloadObservationMissing,
			wantConfig: false,
			wantOwner:  domain.OwnershipConflicting,
		},
		{
			name:       "managed matching",
			status:     http.StatusOK,
			body:       observed,
			mutate:     func(*domain.DesiredWorkload) {},
			wantState:  application.WorkloadObservationPresent,
			wantConfig: true,
			wantOwner:  domain.OwnershipManaged,
		},
		{
			name:   "configuration drift",
			status: http.StatusOK,
			body:   observed,
			mutate: func(workload *domain.DesiredWorkload) {
				workload.Command = []string{"changed"}
			},
			wantState:  application.WorkloadObservationPresent,
			wantConfig: false,
			wantOwner:  domain.OwnershipManaged,
		},
	}
}

func TestObserveWorkloadContainsValidationAndProbeFailures(t *testing.T) {
	t.Parallel()

	client := testClient(roundTripFunc(func(*http.Request) (*http.Response, error) {
		return nil, io.ErrUnexpectedEOF
	}))
	client.version = testVersion()

	invalid := validApplicationWorkload(t)
	invalid.ContainerName = testInvalidContainerName

	got, err := client.ObserveWorkload(context.Background(), invalid)

	var empty application.WorkloadObservation

	if got != empty || !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("ObserveWorkload(invalid) = %#v, %v", got, err)
	}

	got, err = client.ObserveWorkload(context.Background(), validApplicationWorkload(t))
	if got != empty || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ObserveWorkload(transport) = %#v, %v", got, err)
	}

	var emptyContainer Container

	for _, state := range []ContainerProbeState{ContainerProbeUnknown, ContainerProbeState(255)} {
		got, err = workloadObservation(ContainerProbe{State: state, Container: emptyContainer}, validApplicationWorkload(t))
		if got != empty || !errors.Is(err, ErrProtocol) {
			t.Fatalf("workloadObservation(%d) = %#v, %v", state, got, err)
		}
	}
}

func validApplicationWorkload(t *testing.T) domain.DesiredWorkload {
	t.Helper()

	return domain.DesiredWorkload{
		ServiceName:   testContainerService,
		ContainerName: testContainerName,
		Image: domain.ImageIdentity{
			Reference:        testContainerImage,
			ReferenceDigest:  mustTestDigest(t, testReferenceDigest),
			Platform:         domain.Platform{OS: testOS, Architecture: testArchitecture, Variant: ""},
			PlatformManifest: mustTestDigest(t, testPlatformManifest),
			ImageConfig:      mustTestDigest(t, testImageConfig),
			Entrypoint:       testContainerEntrypoint(),
			Command:          testContainerCommand(),
		},
		Entrypoint:      testContainerEntrypoint(),
		Command:         testContainerCommand(),
		SourceDigest:    domain.Hash([]byte("source")),
		EffectiveDigest: mustTestDigest(t, testDesiredState),
	}
}

func unsupportedApplicationWorkloads(t *testing.T) []domain.DesiredWorkload {
	t.Helper()

	base := validApplicationWorkload(t)
	empty := domain.Digest{}

	mutations := []func(*domain.DesiredWorkload){
		func(value *domain.DesiredWorkload) { value.ServiceName = "invalid service" },
		func(value *domain.DesiredWorkload) { value.ContainerName = testInvalidContainerName },
		func(value *domain.DesiredWorkload) { value.Image.Reference = "registry.example/api:1" },
		func(value *domain.DesiredWorkload) { value.Image.ReferenceDigest = domain.Hash(nil) },
		func(value *domain.DesiredWorkload) { value.Image.Platform.OS = "windows" },
		func(value *domain.DesiredWorkload) { value.Image.Platform.Architecture = "arm64" },
		func(value *domain.DesiredWorkload) { value.Image.Platform.Variant = "v8" },
		func(value *domain.DesiredWorkload) { value.Image.PlatformManifest = empty },
		func(value *domain.DesiredWorkload) { value.Image.ImageConfig = empty },
		func(value *domain.DesiredWorkload) { value.SourceDigest = empty },
		func(value *domain.DesiredWorkload) { value.EffectiveDigest = empty },
		func(value *domain.DesiredWorkload) { value.Entrypoint = nil; value.Command = nil },
		func(value *domain.DesiredWorkload) { value.Entrypoint = []string{"\xff"} },
		func(value *domain.DesiredWorkload) { value.Command = []string{"bad\x00argument"} },
	}

	workloads := make([]domain.DesiredWorkload, 0, len(mutations))
	for _, mutate := range mutations {
		workload := base
		mutate(&workload)
		workloads = append(workloads, workload)
	}

	return workloads
}

func TestExecutionDigestEncodingSeparatesFields(t *testing.T) {
	t.Parallel()

	left := appendExecutionString(nil, "ab")
	left = appendExecutionString(left, "c")
	right := appendExecutionString(nil, "a")
	right = appendExecutionString(right, "bc")

	if bytes.Equal(left, right) {
		t.Fatal("appendExecutionString() did not separate fields")
	}
}

func TestDockerPlatformSupportsPhaseOneArchitectures(t *testing.T) {
	t.Parallel()

	var empty domain.Platform

	tests := []struct {
		osName       string
		architecture string
		want         domain.Platform
		valid        bool
	}{
		{
			osName:       dockerOperatingSystem,
			architecture: dockerArchitectureAMD64,
			want: domain.Platform{
				OS:           dockerOperatingSystem,
				Architecture: dockerArchitectureAMD64,
				Variant:      "",
			},
			valid: true,
		},
		{
			osName:       dockerOperatingSystem,
			architecture: dockerArchitectureARM64,
			want: domain.Platform{
				OS:           dockerOperatingSystem,
				Architecture: dockerArchitectureARM64,
				Variant:      dockerARM64Variant,
			},
			valid: true,
		},
		{osName: "windows", architecture: dockerArchitectureAMD64, want: empty, valid: false},
		{osName: dockerOperatingSystem, architecture: "s390x", want: empty, valid: false},
	}

	for _, test := range tests {
		got, valid := dockerPlatform(test.osName, test.architecture)
		if valid != test.valid || got != test.want {
			t.Fatalf("dockerPlatform(%q, %q) = %#v, %t", test.osName, test.architecture, got, valid)
		}
	}
}
