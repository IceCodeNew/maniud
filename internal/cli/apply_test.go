package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry"
	"github.com/IceCodeNew/maniud/internal/store"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
	dockerruntime "github.com/IceCodeNew/maniud/plugins/runtime/docker"
)

const (
	closeRuntimeEvent   = "close-runtime"
	dockerHostKey       = "DOCKER_HOST"
	dryRunEvent         = "dry-run"
	homeKey             = "HOME"
	inspectEvent        = "inspect"
	invalidPathValue    = "bad\x00path"
	linuxOS             = "linux"
	sourceEvent         = "source"
	testApplyWarning    = "capacity proof unavailable"
	testPlainDockerHost = "tcp://engine.example:2375"
	testProjectName     = "example"
	writeEvent          = "write"
	xdgStateHomeKey     = "XDG_STATE_HOME"
)

var (
	errApplyTest       = errors.New("apply test failure")
	errApplyOutputTest = errors.New("apply output test failure")
)

type applyRuntimeFixture struct {
	events     *[]string
	inspectErr error
	probeErr   error
}

func (runtime *applyRuntimeFixture) ProbeImage(
	_ context.Context,
	expected domain.ImageIdentity,
) (application.ImageProbe, error) {
	return application.ImageProbe{
		State: application.ImageProbeObserved,
		Image: application.ImageEvidence{
			ReferenceDigest:  expected.ReferenceDigest,
			PlatformManifest: expected.PlatformManifest,
			ImageConfig:      expected.ImageConfig,
			Platform:         expected.Platform,
		},
	}, runtime.probeErr
}

func (runtime *applyRuntimeFixture) Inspect(context.Context) (application.RuntimeEvidence, error) {
	*runtime.events = append(*runtime.events, inspectEvent)

	return application.RuntimeEvidence{
		Kind:     domain.RuntimeDocker,
		Platform: domain.Platform{OS: linuxOS, Architecture: testArchitectureAMD64, Variant: ""},
		Digest:   domain.Hash([]byte("runtime")),
	}, runtime.inspectErr
}

func (runtime *applyRuntimeFixture) CheckWorkload(domain.DesiredWorkload) error {
	*runtime.events = append(*runtime.events, "check")

	return nil
}

func (runtime *applyRuntimeFixture) ObserveWorkload(
	context.Context,
	domain.DesiredWorkload,
) (application.WorkloadObservation, error) {
	*runtime.events = append(*runtime.events, "observe")

	return application.WorkloadObservation{
		State:                application.WorkloadObservationMissing,
		ConfigurationMatches: false,
		Running:              false,
		Ownership: domain.WorkloadOwnership{
			Status:           domain.OwnershipConflicting,
			Service:          "",
			Transaction:      "",
			DesiredState:     domain.Digest{},
			Reference:        domain.Digest{},
			ImageConfig:      domain.Digest{},
			PlatformManifest: domain.Digest{},
		},
	}, nil
}

func (runtime *applyRuntimeFixture) CloseIdleConnections() {
	*runtime.events = append(*runtime.events, closeRuntimeEvent)
}

type applyOperationsFixture struct {
	events        *[]string
	dryRun        func(application.Request) (application.Plan, error)
	apply         func(application.Request) (application.Plan, error)
	dryRunPlan    application.Plan
	applyPlan     application.Plan
	dryRunErr     error
	applyErr      error
	dryRunRequest application.Request
	applyRequest  application.Request
}

func (operations *applyOperationsFixture) DryRun(
	_ context.Context,
	request application.Request,
) (application.Plan, error) {
	*operations.events = append(*operations.events, dryRunEvent)
	operations.dryRunRequest = request
	if operations.dryRun != nil {
		return operations.dryRun(request)
	}

	return operations.dryRunPlan, operations.dryRunErr
}

func (operations *applyOperationsFixture) Apply(
	_ context.Context,
	request application.Request,
) (application.Plan, error) {
	*operations.events = append(*operations.events, string(commandApply))
	operations.applyRequest = request
	if operations.apply != nil {
		return operations.apply(request)
	}

	return operations.applyPlan, operations.applyErr
}

type applyBoundaryTest struct {
	name       string
	mutate     func(*applyDependencies, *applyOperationsFixture)
	want       error
	wantEvents []string
	output     io.Writer
}

func assertApplyBoundaryFailures(
	t *testing.T,
	tests []applyBoundaryTest,
	execute func(context.Context, applyInvocation, io.Writer, applyDependencies) error,
	arguments applyInvocation,
) {
	t.Helper()

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make([]string, 0, 3)
			operations := &applyOperationsFixture{
				events:     &events,
				dryRunPlan: application.Plan{Kind: application.PlanUnchanged},
				applyPlan:  application.Plan{Kind: application.PlanUnchanged},
			}
			dependencies := operationApplyDependencies(t, &events, operations)
			test.mutate(&dependencies, operations)
			if writer, ok := test.output.(eventWriter); ok {
				writer.events = &events
				test.output = writer
			}

			err := execute(context.Background(), arguments, test.output, dependencies)
			if !errors.Is(err, test.want) {
				t.Fatalf("execute apply boundary error = %v, want %v", err, test.want)
			}
			if !reflect.DeepEqual(events, test.wantEvents) {
				t.Fatalf("execute apply boundary events = %q, want %q", events, test.wantEvents)
			}
		})
	}
}

func TestExecuteDryRunLoadsSourceBeforeCallingFacadeAndWriting(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 3)
	operations := &applyOperationsFixture{
		events: &events,
		dryRunPlan: application.Plan{
			Kind: application.PlanBootstrap,
		},
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	output := eventWriter{events: &events, destination: new(bytes.Buffer), err: nil}

	err := executeDryRun(
		context.Background(),
		applyInvocation{compose: composeFileValue, service: applyServiceValue, dryRun: true, json: true},
		output,
		dependencies,
	)
	if err != nil {
		t.Fatalf("executeDryRun() error = %v", err)
	}

	wantEvents := []string{sourceEvent, dryRunEvent, writeEvent}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("executeDryRun() events = %q, want %q", events, wantEvents)
	}
	if operations.dryRunRequest.Source.Content == nil ||
		operations.dryRunRequest.Service != applyServiceValue {
		t.Fatalf("DryRun() request = %#v", operations.dryRunRequest)
	}

	var got applyPlan
	if decodeErr := jsonDecode(output.destination, &got); decodeErr != nil ||
		got.Status != string(application.PlanBootstrap) {
		t.Fatalf("executeDryRun() plan = %#v, %v", got, decodeErr)
	}
}

func TestExecuteDryRunContainsBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := []applyBoundaryTest{
		{
			name: sourceEvent,
			mutate: func(dependencies *applyDependencies, _ *applyOperationsFixture) {
				dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
					return compose.Source{}, errApplyTest
				}
			},
			want:       errApplyTest,
			wantEvents: []string{},
			output:     io.Discard,
		},
		{
			name: "facade",
			mutate: func(_ *applyDependencies, operations *applyOperationsFixture) {
				operations.dryRunErr = errApplyTest
			},
			want:       errApplyTest,
			wantEvents: []string{sourceEvent, dryRunEvent},
			output:     io.Discard,
		},
		{
			name:   "output",
			mutate: func(*applyDependencies, *applyOperationsFixture) {},
			want:   errApplyOutputTest,
			wantEvents: []string{
				sourceEvent, dryRunEvent, writeEvent,
			},
			output: eventWriter{err: errApplyOutputTest},
		},
	}

	assertApplyBoundaryFailures(
		t,
		tests,
		executeDryRun,
		applyInvocation{compose: composeFileValue, service: applyServiceValue, dryRun: true},
	)
}

func TestExecuteMutationLoadsSourceBeforeCallingFacadeAndWriting(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 3)
	operations := &applyOperationsFixture{
		events:    &events,
		applyPlan: application.Plan{Kind: application.PlanBootstrap},
	}
	dependencies := operationApplyDependencies(t, &events, operations)
	output := eventWriter{events: &events, destination: new(bytes.Buffer)}

	err := executeMutation(
		context.Background(),
		applyInvocation{compose: composeFileValue, service: applyServiceValue, dryRun: false, json: true},
		output,
		dependencies,
	)
	if err != nil {
		t.Fatalf("executeMutation() error = %v", err)
	}

	wantEvents := []string{sourceEvent, string(commandApply), writeEvent}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("executeMutation() events = %q, want %q", events, wantEvents)
	}
	if operations.applyRequest.Source.Content == nil ||
		operations.applyRequest.Service != applyServiceValue {
		t.Fatalf("Apply() request = %#v", operations.applyRequest)
	}

	var got applyPlan
	if err = jsonDecode(output.destination, &got); err != nil || got.Status != string(application.PlanBootstrap) {
		t.Fatalf("executeMutation() plan = %#v, %v", got, err)
	}
}

func TestWriteApplyPlanEmitsStableWarnings(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	plan := application.Plan{
		Kind: application.PlanUpgrade,
		Warnings: []application.Warning{{
			Code: application.WarningDaemonMountProbeUnavailable, Message: testApplyWarning,
		}},
	}
	if err := writeApplyPlan(output, plan, false, true); err != nil {
		t.Fatalf("writeApplyPlan() error = %v", err)
	}

	var got applyPlan
	if err := jsonDecode(output, &got); err != nil || len(got.Warnings) != 1 ||
		got.Warnings[0].Code != application.WarningDaemonMountProbeUnavailable ||
		got.Warnings[0].Message != testApplyWarning {
		t.Fatalf("writeApplyPlan() = %#v, %v", got, err)
	}
}

func TestWriteHumanApplyPlanExplainsEveryAction(t *testing.T) {
	t.Parallel()

	tests := []struct {
		kind application.PlanKind
		want string
	}{
		{application.PlanBootstrap, "create a new workload"},
		{application.PlanAdopt, "adopt the matching running workload"},
		{application.PlanUnchanged, "keep the matching workload unchanged"},
		{application.PlanUpgrade, "upgrade the managed workload"},
		{application.PlanResume, "resume the interrupted operation"},
		{application.PlanProbeUnknownEffect, "verify an interrupted runtime operation before continuing"},
		{application.PlanRestore, "restore the previous workload"},
		{application.PlanKind("future"), "process the workload"},
	}
	for _, test := range tests {
		output := new(bytes.Buffer)
		plan := applyPlanTestFixture(test.kind)
		if err := writeApplyPlan(output, plan, true, false); err != nil {
			t.Fatalf("writeApplyPlan(%s) error = %v", test.kind, err)
		}
		assertHumanApplyPlan(t, output.String(), test.kind, test.want)
	}
}

func TestWriteHumanApplyPlanReportsCompletedApply(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)
	if err := writeApplyPlan(output, application.Plan{Kind: application.PlanUnchanged}, false, false); err != nil {
		t.Fatalf("writeApplyPlan(apply) error = %v", err)
	}
	if !strings.HasPrefix(output.String(), "Apply completed") || strings.Contains(output.String(), "No changes") {
		t.Fatalf("writeApplyPlan(apply) = %q", output.String())
	}
}

func applyPlanTestFixture(kind application.PlanKind) application.Plan {
	return application.Plan{
		Kind: kind, Project: testProjectName, Service: "api", Runtime: domain.RuntimeDocker,
		Platform: domain.Platform{OS: linuxOS, Architecture: testArchitectureAMD64},
		Image:    domain.ImageIdentity{Reference: "example.invalid/api:1"},
		Warnings: []application.Warning{{
			Code: application.WarningDaemonMountProbeUnavailable, Message: testApplyWarning,
		}},
	}
}

func assertHumanApplyPlan(t *testing.T, got string, kind application.PlanKind, action string) {
	t.Helper()

	want := []string{
		"Dry run passed for example/api.\n",
		"Action: " + action + " (" + string(kind) + ").\n",
		"Runtime: docker on linux/amd64.\n",
		"Image: example.invalid/api:1.\n",
		"Warning: " + testApplyWarning + " (daemon_mount_probe_unavailable).\n",
	}
	for _, fragment := range want {
		if !strings.Contains(got, fragment) {
			t.Fatalf("writeApplyPlan(%s) = %q; missing %q", kind, got, fragment)
		}
	}
	if !strings.HasSuffix(got, "Ready to apply. No changes were made.\n") {
		t.Fatalf("writeApplyPlan(%s) = %q; missing final dry-run confirmation", kind, got)
	}
}

func TestWriteApplyPlanContainsOutputFailures(t *testing.T) {
	t.Parallel()

	plan := application.Plan{
		Kind: application.PlanUpgrade,
		Warnings: []application.Warning{{
			Code: application.WarningDaemonMountProbeUnavailable, Message: "warning",
		}},
	}
	if err := writeApplyPlan(
		failingWriterWithError{err: errApplyOutputTest}, plan, false, true,
	); !errors.Is(err, errApplyOutputTest) {
		t.Fatalf("writeApplyPlan(JSON failure) = %v", err)
	}
	for _, failAt := range []int{2, 3} {
		writer := &failAtWriter{failAt: failAt, err: errApplyOutputTest}
		if err := writeApplyPlan(writer, plan, true, false); !errors.Is(err, errApplyOutputTest) {
			t.Fatalf("writeApplyPlan(human failure %d) = %v", failAt, err)
		}
	}
}

type failAtWriter struct {
	writes int
	failAt int
	err    error
}

func (writer *failAtWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, writer.err
	}

	return len(value), nil
}

func TestExecuteMutationContainsBoundaryFailures(t *testing.T) {
	t.Parallel()

	tests := []applyBoundaryTest{
		{
			name: "source",
			mutate: func(dependencies *applyDependencies, _ *applyOperationsFixture) {
				dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
					return compose.Source{}, errApplyTest
				}
			},
			want:       errApplyTest,
			wantEvents: []string{},
			output:     io.Discard,
		},
		{
			name: "facade",
			mutate: func(_ *applyDependencies, operations *applyOperationsFixture) {
				operations.applyErr = errApplyTest
			},
			want:       errApplyTest,
			wantEvents: []string{sourceEvent, string(commandApply)},
			output:     io.Discard,
		},
		{
			name:   "output",
			mutate: func(*applyDependencies, *applyOperationsFixture) {},
			want:   errApplyOutputTest,
			wantEvents: []string{
				sourceEvent, string(commandApply), writeEvent,
			},
			output: eventWriter{err: errApplyOutputTest},
		},
	}

	assertApplyBoundaryFailures(
		t,
		tests,
		executeMutation,
		applyInvocation{compose: composeFileValue, service: applyServiceValue, dryRun: false},
	)
}

func operationApplyDependencies(
	t *testing.T,
	events *[]string,
	operations applyOperations,
) applyDependencies {
	t.Helper()

	return applyDependencies{
		loadSource: func(context.Context, string) (compose.Source, error) {
			*events = append(*events, sourceEvent)

			return testComposeSource(t), nil
		},
		operations: operations,
	}
}

func testComposeSource(t *testing.T) compose.Source {
	t.Helper()

	return compose.Source{
		Content: []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`),
		WorkingDir:  t.TempDir(),
		Environment: nil,
		Profiles:    nil,
	}
}

type eventWriter struct {
	events      *[]string
	destination *bytes.Buffer
	err         error
}

func (writer eventWriter) Write(value []byte) (int, error) {
	*writer.events = append(*writer.events, writeEvent)
	if writer.err != nil {
		return 0, writer.err
	}

	written, err := writer.destination.Write(value)
	if err != nil {
		return written, fmt.Errorf("write test output: %w", err)
	}

	return written, nil
}

type failingWriterWithError struct {
	err error
}

func (writer failingWriterWithError) Write([]byte) (int, error) {
	return 0, writer.err
}

func jsonDecode(source *bytes.Buffer, destination any) error {
	decoder := json.NewDecoder(source)

	err := decoder.Decode(destination)
	if err != nil {
		return fmt.Errorf("decode test output: %w", err)
	}

	return nil
}

func TestApplyEnvironmentDefaults(t *testing.T) {
	t.Parallel()

	home := filepath.Join(string(filepath.Separator), "home", "operator")
	tests := []struct {
		name        string
		environment map[string]string
		wantState   string
		wantConfig  string
		wantErr     bool
	}{
		{
			name:        "XDG state and Docker config",
			environment: map[string]string{xdgStateHomeKey: "/state", "DOCKER_CONFIG": "/config"},
			wantState:   "/state/maniud/state.db",
			wantConfig:  "/config/config.json",
			wantErr:     false,
		},
		{
			name:        "home defaults",
			environment: map[string]string{homeKey: home},
			wantState:   filepath.Join(home, ".local", "state", "maniud", "state.db"),
			wantConfig:  filepath.Join(home, ".docker", "config.json"),
			wantErr:     false,
		},
		{name: "missing home", environment: map[string]string{}, wantState: "", wantConfig: "", wantErr: true},
		{
			name:        "relative XDG state",
			environment: map[string]string{xdgStateHomeKey: "state", homeKey: home},
			wantState:   "",
			wantConfig:  filepath.Join(home, ".docker", "config.json"),
			wantErr:     true,
		},
		{
			name:        "unclean XDG state",
			environment: map[string]string{xdgStateHomeKey: "/state/../other", homeKey: home},
			wantState:   "",
			wantConfig:  filepath.Join(home, ".docker", "config.json"),
			wantErr:     true,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			statePath, err := defaultStatePath(test.environment)
			if (err != nil) != test.wantErr || statePath != test.wantState {
				t.Fatalf("defaultStatePath() = %q, %v", statePath, err)
			}

			if got := dockerConfigPath(test.environment); got != test.wantConfig {
				t.Fatalf("dockerConfigPath() = %q, want %q", got, test.wantConfig)
			}
		})
	}
}

func TestEnvironmentMapUsesLastCompleteValue(t *testing.T) {
	t.Parallel()

	got := environmentMap([]string{"A=one", "ignored", "EMPTY=", "A=two=tail"})
	want := map[string]string{"A": "two=tail", "EMPTY": ""}

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("environmentMap() = %#v, want %#v", got, want)
	}
}

func TestRuntimeWarningSinkUsesStableJSON(t *testing.T) {
	t.Parallel()

	warnings := new(bytes.Buffer)
	if err := runtimeWarningSink(warnings)(runtimeplugin.Warning{
		Code: "insecure_remote_engine", Message: "plain TCP endpoint",
	}); err != nil || warnings.String() !=
		"{\"code\":\"insecure_remote_engine\",\"message\":\"plain TCP endpoint\"}\n" {
		t.Fatalf("runtimeWarningSink() = %v, %q", err, warnings.String())
	}
	if runtimeWarningSink(nil) != nil {
		t.Fatal("runtimeWarningSink(nil) is configured")
	}
	if err := runtimeWarningSink(failingWriterWithError{err: errApplyTest})(runtimeplugin.Warning{
		Code: "warning", Message: "message",
	}); !errors.Is(err, errApplyTest) {
		t.Fatalf("runtimeWarningSink(write failure) = %v", err)
	}
}

func TestLoadComposeSourcePinsCommittedEnvironment(t *testing.T) {
	t.Parallel()

	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve source test directory: %v", err)
	}
	path := filepath.Join(directory, "compose.yaml")
	content := []byte("services: {}\n")

	err = os.WriteFile(path, content, 0o600)
	if err != nil {
		t.Fatal(err)
	}
	if err = os.WriteFile(filepath.Join(directory, ".env"), []byte("VALUE=committed\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitApplyTestRepository(t, directory, "compose.yaml", ".env")

	environment := map[string]string{"VALUE": "private"}

	source, err := loadTrackedComposeSource(t.Context(), composeFileValue, directory, environment, t.TempDir())
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}

	environment["VALUE"] = "changed"

	if !bytes.Equal(source.Content, content) || source.WorkingDir != directory ||
		source.Environment["VALUE"] != "committed" || source.Profiles != nil {
		t.Fatalf("loadTrackedComposeSource() = %#v", source)
	}
}

func TestLoadTrackedComposeSourceRejectsDirtyRepository(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	path := filepath.Join(directory, "compose.yaml")
	if err := os.WriteFile(path, []byte("services: {}\n"), 0o600); err != nil {
		t.Fatal(err)
	}
	commitApplyTestRepository(t, directory, "compose.yaml")
	if err := os.WriteFile(filepath.Join(directory, "untracked"), []byte("dirty"), 0o600); err != nil {
		t.Fatal(err)
	}

	_, err := loadTrackedComposeSource(t.Context(), path, directory, nil, t.TempDir())
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadTrackedComposeSource(dirty) error = %v", err)
	}
}

func TestDefaultApplyDependenciesBuildsApplicationFacade(t *testing.T) {
	t.Parallel()

	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatalf("resolve apply test directory: %v", err)
	}

	composePath := filepath.Join(directory, "compose.yaml")

	err = os.WriteFile(composePath, []byte("services: {}\n"), 0o600)
	if err != nil {
		t.Fatal(err)
	}
	commitApplyTestRepository(t, directory, "compose.yaml")
	server := startApplyDockerServer(t)

	dependencies, err := defaultApplyDependencies(map[string]string{
		homeKey:         directory,
		xdgStateHomeKey: directory,
		dockerHostKey:   "tcp" + strings.TrimPrefix(server.URL, "http"),
	}, io.Discard, os.Getwd, nil, testRuntimePlugins(t))
	if err != nil {
		t.Fatalf("defaultApplyDependencies() error = %v", err)
	}

	source, err := dependencies.loadSource(t.Context(), composePath)
	if err != nil || !bytes.Equal(source.Content, []byte("services: {}\n")) {
		t.Fatalf("loadSource() = %#v, %v", source, err)
	}
	if dependencies.operations == nil {
		t.Fatal("defaultApplyDependencies() has no application facade")
	}
	if _, err = dependencies.operations.Apply(
		context.Background(),
		application.Request{Source: testComposeSource(t), Service: applyServiceValue},
	); err == nil {
		t.Fatal("Apply(runtime probe failure) succeeded")
	}
}

func TestDefaultApplyDependenciesRejectsInvalidConfiguration(t *testing.T) {
	t.Parallel()

	_, err := defaultApplyDependencies(
		map[string]string{}, io.Discard, os.Getwd, nil, testRuntimePlugins(t),
	)
	if err == nil {
		t.Fatal("defaultApplyDependencies(missing home) succeeded")
	}

	directory := t.TempDir()
	invalidDependencies, err := defaultApplyDependencies(
		map[string]string{homeKey: directory, dockerHostKey: "invalid://engine"},
		io.Discard,
		os.Getwd,
		nil,
		testRuntimePlugins(t),
	)
	if err == nil {
		_, err = invalidDependencies.operations.DryRun(
			context.Background(),
			application.Request{Source: testComposeSource(t), Service: applyServiceValue},
		)
	}
	if !errors.Is(err, dockerruntime.ErrInvalidEndpoint) {
		t.Fatalf("defaultApplyDependencies(invalid endpoint) error = %v", err)
	}
}

func commitApplyTestRepository(t *testing.T, directory string, paths ...string) {
	t.Helper()

	commands := [][]string{
		{"init", "--quiet"},
		append([]string{"add", "--"}, paths...),
		{"-c", "user.name=Maniud Tests", "-c", "user.email=maniud@example.invalid",
			"-c", "commit.gpgsign=false", "commit", "--quiet", "-m", "test source"},
	}
	for _, arguments := range commands {
		if _, err := runGit(t.Context(), directory, arguments...); err != nil {
			t.Fatalf("git %v error = %v", arguments, err)
		}
	}
}

func startApplyDockerServer(t *testing.T) *httptest.Server {
	t.Helper()
	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_ping":
			response.Header().Set("Api-Version", "1.54")
			response.WriteHeader(http.StatusOK)
		case "/v1.54/version":
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"Version":"29.0.0","ApiVersion":"1.54",`+
				`"MinAPIVersion":"1.54","Os":"`+linuxOS+`","Arch":"`+testArchitectureAMD64+`"}`)
		case "/v1.54/info":
			response.WriteHeader(http.StatusInternalServerError)
		default:
			t.Errorf("unexpected Docker request = %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	return server
}

func TestDefaultApplyDependenciesRejectsMissingWorkingDirectory(t *testing.T) {
	t.Parallel()

	dependencies, err := defaultApplyDependencies(
		map[string]string{homeKey: "/tmp"},
		io.Discard,
		func() (string, error) { return "", io.ErrUnexpectedEOF },
		nil,
		testRuntimePlugins(t),
	)
	if err == nil || dependencies.loadSource != nil || dependencies.operations != nil {
		t.Fatalf("defaultApplyDependencies() = (%+v, %v), want empty dependencies and error", dependencies, err)
	}
}

func TestRunDispatchesApplyAndMapsFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		execute    func(applyInvocation) error
		wantStatus int
		wantOutput string
	}{
		{
			name: "success",
			execute: func(arguments applyInvocation) error {
				if arguments.compose != composeFileValue || arguments.service != applyServiceValue {
					return errApplyTest
				}

				return nil
			},
			wantStatus: 0,
			wantOutput: "",
		},
		{
			name:       "retryable failure",
			execute:    func(applyInvocation) error { return runtimeplugin.ErrUnavailable },
			wantStatus: 1,
			wantOutput: retryableApplyFailureJSON,
		},
		{
			name:       "runtime not built",
			execute:    func(applyInvocation) error { return runtimeplugin.ErrNotBuilt },
			wantStatus: 1,
			wantOutput: "{\"code\":\"runtime_not_built\",\"message\":" +
				"\"selected container runtime is not included in this build\",\"retryable\":false}\n",
		},
		{
			name:       "missing executor",
			execute:    nil,
			wantStatus: 1,
			wantOutput: internalErrorJSON,
		},
	}

	for _, test := range tests {
		for _, dryRun := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/dry-run=%t", test.name, dryRun), func(t *testing.T) {
				t.Parallel()

				output := new(bytes.Buffer)
				args := []string{string(commandApply), composeFileValue, applyServiceValue}
				if dryRun {
					args = append(args, dryRunOption)
				}

				status := run(context.Background(), args, output, nil, test.execute)
				if status != test.wantStatus || output.String() != test.wantOutput {
					t.Fatalf("run() = %d, %q", status, output.String())
				}
			})
		}
	}
}

func TestRunRejectsUnavailableCommand(t *testing.T) {
	t.Parallel()

	output := new(bytes.Buffer)

	status := run(
		context.Background(),
		[]string{string(commandDoctor), reindexBackupsOption},
		output,
		nil,
		nil,
	)
	if status != 1 || output.String() != internalErrorJSON {
		t.Fatalf("run() = %d, %q", status, output.String())
	}
}

func TestRunBuildsDefaultDryRunDependencies(t *testing.T) {
	directory := t.TempDir()
	t.Setenv(homeKey, directory)
	t.Setenv(xdgStateHomeKey, directory)
	t.Setenv(dockerHostKey, "unix:///missing/docker.sock")

	output := new(bytes.Buffer)

	status := Run(
		context.Background(),
		[]string{string(commandApply), filepath.Join(directory, "missing.yaml"), dryRunOption},
		nil,
		output,
		io.Discard,
		testRuntimePlugins(t),
	)
	if status != 1 || output.String() !=
		"{\"code\":\"apply_failed\",\"message\":\"apply validation failed\",\"retryable\":false}\n" {
		t.Fatalf("Run(default dry-run) = %d, %q", status, output.String())
	}

	t.Setenv(homeKey, "")
	t.Setenv(xdgStateHomeKey, "")
	output.Reset()

	status = Run(
		context.Background(),
		[]string{string(commandApply), composeFileValue, dryRunOption},
		nil,
		output,
		io.Discard,
		testRuntimePlugins(t),
	)
	if status != 1 || !strings.Contains(output.String(), `"code":"apply_failed"`) {
		t.Fatalf("Run(invalid defaults) = %d, %q", status, output.String())
	}
}

func TestPlatformString(t *testing.T) {
	t.Parallel()

	if got := platformString(domain.Platform{
		OS: linuxOS, Architecture: testArchitectureAMD64, Variant: "",
	}); got != testPlatformAMD64 {
		t.Fatalf("platformString(amd64) = %q", got)
	}

	if got := platformString(domain.Platform{OS: linuxOS, Architecture: "arm64", Variant: "v8"}); got != "linux/arm64/v8" {
		t.Fatalf("platformString(arm64) = %q", got)
	}
}

func TestClassifyApplyFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err       error
		code      domain.ErrorCode
		retryable bool
	}{
		{err: context.Canceled, code: domain.ErrorOperationCancelled, retryable: false},
		{err: registry.ErrCancelled, code: domain.ErrorOperationCancelled, retryable: false},
		{err: runtimeplugin.ErrUnavailable, code: domain.ErrorApplyFailed, retryable: true},
		{err: fmt.Errorf("wrapped: %w", runtimeplugin.ErrUnavailable), code: domain.ErrorApplyFailed, retryable: true},
		{err: runtimeplugin.ErrNotBuilt, code: domain.ErrorRuntimeNotBuilt, retryable: false},
		{err: registry.ErrUnavailable, code: domain.ErrorApplyFailed, retryable: true},
		{err: registry.ErrRateLimited, code: domain.ErrorApplyFailed, retryable: true},
		{err: store.ErrUnavailable, code: domain.ErrorApplyFailed, retryable: true},
		{err: errApplyTest, code: domain.ErrorApplyFailed, retryable: false},
	}

	for _, test := range tests {
		failure := classifyApplyFailure(test.err)
		if failure.Code() != test.code || failure.Retryable() != test.retryable {
			t.Fatalf("classifyApplyFailure(%v) = %q, %t", test.err, failure.Code(), failure.Retryable())
		}
	}
}
