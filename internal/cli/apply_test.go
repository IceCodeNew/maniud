package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1" //nolint:depguard // This composition test serves the adapter's gRPC probe boundary.
	versionapi "github.com/containerd/containerd/api/services/version/v1"             //nolint:depguard // This composition test serves the adapter's gRPC probe boundary.
	"google.golang.org/grpc"                                                          //nolint:depguard // This composition test serves the adapter's gRPC probe boundary.
	"google.golang.org/protobuf/types/known/emptypb"                                  //nolint:depguard // This composition test serves the adapter's gRPC probe boundary.

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
	containerdruntime "github.com/IceCodeNew/maniud/internal/runtime/containerd"
	dockerruntime "github.com/IceCodeNew/maniud/internal/runtime/docker"
	podmanruntime "github.com/IceCodeNew/maniud/internal/runtime/podman"
	"github.com/IceCodeNew/maniud/internal/store"
)

const (
	closeRuntimeEvent   = "close-runtime"
	closeReaderEvent    = "close-reader"
	dockerHostKey       = "DOCKER_HOST"
	homeKey             = "HOME"
	inspectEvent        = "inspect"
	invalidPathValue    = "bad\x00path"
	linuxOS             = "linux"
	mutationEvent       = "mutation"
	readerEvent         = "reader"
	runtimeEvent        = "runtime"
	sourceEvent         = "source"
	stateEvent          = "state"
	testApplyWarning    = "capacity proof unavailable"
	testPlainDockerHost = "tcp://engine.example:2375"
	xdgStateHomeKey     = "XDG_STATE_HOME"
)

var (
	errApplyTest       = errors.New("apply test failure")
	errApplyCloseTest  = errors.New("apply close test failure")
	errApplyOutputTest = errors.New("apply output test failure")
	errStateOpenTest   = errors.New("apply state remained open")
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

type applyReaderFixture struct {
	events   *[]string
	closeErr error
}

func (reader *applyReaderFixture) UnresolvedTransaction(
	context.Context,
	string,
	string,
) (store.Transaction, bool, error) {
	*reader.events = append(*reader.events, "journal")

	return store.Transaction{
		ID:              store.TransactionID{},
		State:           "",
		Runtime:         "",
		SourceDigest:    domain.Digest{},
		EffectiveDigest: domain.Digest{},
		ExecutionDigest: domain.Digest{},
	}, false, nil
}

func (*applyReaderFixture) AppliedService(
	context.Context,
	string,
	string,
) (store.AppliedService, bool, error) {
	return store.AppliedService{}, false, nil
}

func (*applyReaderFixture) Actions(context.Context, store.TransactionID) ([]store.Action, error) {
	return nil, errApplyTest
}

func (reader *applyReaderFixture) Close() error {
	*reader.events = append(*reader.events, closeReaderEvent)

	return reader.closeErr
}

type applyImageResolverFixture struct {
	events *[]string
}

func (resolver applyImageResolverFixture) Resolve(
	_ context.Context,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	*resolver.events = append(*resolver.events, "resolve")
	referenceDigest := domain.Hash([]byte("reference"))

	reference, err := source.Pin(referenceDigest)
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("pin test image: %w", err)
	}

	return domain.ImageIdentity{
		Origin:           domain.ImageOriginRegistry,
		Reference:        reference.String(),
		ReferenceDigest:  referenceDigest,
		Platform:         platform,
		PlatformManifest: domain.Hash([]byte("manifest")),
		ImageConfig:      domain.Hash([]byte("config")),
		Entrypoint:       []string{"/usr/local/bin/api"},
		Command:          []string{"serve"},
	}, nil
}

//nolint:cyclop // The event assertions keep one full lifecycle and every unexpected call in one test.
func TestExecuteDryRunEmitsPlanAfterClosingEvidence(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 10)
	reader := &applyReaderFixture{events: &events, closeErr: nil}
	runtime := &applyRuntimeFixture{events: &events, inspectErr: nil}
	dependencies := validApplyDependencies(t, &events, reader, runtime)
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

	wantEvents := []string{
		sourceEvent, readerEvent, runtimeEvent, inspectEvent, "resolve", "check", "journal", "observe",
		closeRuntimeEvent, closeReaderEvent, "write",
	}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("executeDryRun() events = %q, want %q", events, wantEvents)
	}

	var got applyPlan

	decodeErr := jsonDecode(output.destination, &got)
	if decodeErr != nil {
		t.Fatalf("decode dry-run plan: %v", decodeErr)
	}

	valid := got.Project == "example" && got.Service == applyServiceValue && got.Status == "bootstrap" &&
		got.Runtime == testDockerRuntime && got.Platform == testPlatformAMD64 &&
		strings.HasPrefix(got.Image, "example.com/team/api:1@sha256:") &&
		strings.HasPrefix(got.SourceDigest, "sha256:") && strings.HasPrefix(got.DesiredDigest, "sha256:")
	if !valid {
		t.Fatalf("executeDryRun() plan = %#v", got)
	}
}

//nolint:funlen // The table keeps error precedence and lifecycle evidence adjacent.
func TestExecuteDryRunContainsOpenAndCloseFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		mutate       func(*applyDependencies, *applyReaderFixture, *applyRuntimeFixture)
		want         []error
		wantEvents   []string
		outputWriter io.Writer
	}{
		{
			name: sourceEvent,
			mutate: func(dependencies *applyDependencies, _ *applyReaderFixture, _ *applyRuntimeFixture) {
				dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
					return compose.Source{}, errApplyTest
				}
			},
			want:         []error{errApplyTest},
			wantEvents:   []string{},
			outputWriter: io.Discard,
		},
		{
			name: "runtime selection",
			mutate: func(dependencies *applyDependencies, _ *applyReaderFixture, _ *applyRuntimeFixture) {
				loadSource := dependencies.loadSource
				dependencies.loadSource = func(ctx context.Context, path string) (compose.Source, error) {
					source, err := loadSource(ctx, path)
					source.Content = []byte("invalid: [")

					return source, err
				}
			},
			want:         []error{compose.ErrInvalidSource},
			wantEvents:   []string{sourceEvent},
			outputWriter: io.Discard,
		},
		{
			name: "reader",
			mutate: func(dependencies *applyDependencies, _ *applyReaderFixture, _ *applyRuntimeFixture) {
				dependencies.openReader = func(context.Context) (applyTransactionReader, error) {
					return nil, errApplyTest
				}
			},
			want:         []error{errApplyTest},
			wantEvents:   []string{sourceEvent},
			outputWriter: io.Discard,
		},
		{
			name: "runtime and reader close",
			mutate: func(dependencies *applyDependencies, reader *applyReaderFixture, _ *applyRuntimeFixture) {
				reader.closeErr = errApplyCloseTest
				dependencies.openRuntime = func(context.Context, domain.RuntimeKind) (applyRuntime, error) {
					return nil, errApplyTest
				}
			},
			want:         []error{errApplyTest, errApplyCloseTest},
			wantEvents:   []string{sourceEvent, readerEvent, closeReaderEvent},
			outputWriter: io.Discard,
		},
		{
			name: "operation and reader close",
			mutate: func(_ *applyDependencies, reader *applyReaderFixture, runtime *applyRuntimeFixture) {
				reader.closeErr = errApplyCloseTest
				runtime.inspectErr = errApplyTest
			},
			want:         []error{errApplyTest, errApplyCloseTest},
			wantEvents:   []string{sourceEvent, readerEvent, runtimeEvent, inspectEvent, closeRuntimeEvent, closeReaderEvent},
			outputWriter: io.Discard,
		},
		{
			name: "output after close",
			mutate: func(_ *applyDependencies, _ *applyReaderFixture, _ *applyRuntimeFixture) {
			},
			want: []error{errApplyOutputTest},
			wantEvents: []string{
				sourceEvent, readerEvent, runtimeEvent, inspectEvent, "resolve", "check", "journal", "observe",
				closeRuntimeEvent, closeReaderEvent,
			},
			outputWriter: failingWriterWithError{err: errApplyOutputTest},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make([]string, 0, 10)
			reader := &applyReaderFixture{events: &events, closeErr: nil}
			runtime := &applyRuntimeFixture{events: &events, inspectErr: nil}
			dependencies := validApplyDependencies(t, &events, reader, runtime)
			test.mutate(&dependencies, reader, runtime)

			err := executeDryRun(
				context.Background(),
				applyInvocation{compose: composeFileValue, service: applyServiceValue, dryRun: true},
				test.outputWriter,
				dependencies,
			)
			for _, want := range test.want {
				if !errors.Is(err, want) {
					t.Fatalf("executeDryRun() error = %v, want %v", err, want)
				}
			}

			if !reflect.DeepEqual(events, test.wantEvents) {
				t.Fatalf("executeDryRun() events = %q, want %q", events, test.wantEvents)
			}
		})
	}
}

func TestExecuteMutationEmitsPlanAfterClosingResources(t *testing.T) {
	t.Parallel()

	events := make([]string, 0, 6)
	runtime := &applyRuntimeFixture{events: &events, inspectErr: nil}
	dependencies := mutationApplyDependencies(t, &events, runtime)
	var opened *store.Store
	openState := dependencies.openState
	dependencies.openState = func(ctx context.Context) (*store.Store, error) {
		state, err := openState(ctx)
		opened = state

		return state, err
	}
	output := &closedStateWriter{
		state:       &opened,
		events:      &events,
		destination: new(bytes.Buffer),
	}

	err := executeMutation(
		context.Background(),
		applyInvocation{compose: composeFileValue, service: applyServiceValue, dryRun: false, json: true},
		output,
		dependencies,
	)
	if err != nil {
		t.Fatalf("executeMutation() error = %v", err)
	}

	wantEvents := []string{sourceEvent, runtimeEvent, stateEvent, mutationEvent, closeRuntimeEvent, "write"}
	if !reflect.DeepEqual(events, wantEvents) {
		t.Fatalf("executeMutation() events = %q, want %q", events, wantEvents)
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
		Kind: kind, Project: "example", Service: "api", Runtime: domain.RuntimeDocker,
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

//nolint:funlen // The table keeps mutation resource ownership and error precedence together.
func TestExecuteMutationContainsOpenRunAndOutputFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		mutate     func(*applyDependencies, *applyRuntimeFixture)
		want       error
		wantEvents []string
		output     io.Writer
	}{
		{
			name: "source",
			mutate: func(dependencies *applyDependencies, _ *applyRuntimeFixture) {
				dependencies.loadSource = func(context.Context, string) (compose.Source, error) {
					return compose.Source{}, errApplyTest
				}
			},
			want:       errApplyTest,
			wantEvents: []string{},
			output:     io.Discard,
		},
		{
			name: "runtime selection",
			mutate: func(dependencies *applyDependencies, _ *applyRuntimeFixture) {
				loadSource := dependencies.loadSource
				dependencies.loadSource = func(ctx context.Context, path string) (compose.Source, error) {
					source, err := loadSource(ctx, path)
					source.Content = []byte("invalid: [")

					return source, err
				}
			},
			want:       compose.ErrInvalidSource,
			wantEvents: []string{sourceEvent},
			output:     io.Discard,
		},
		{
			name: "runtime",
			mutate: func(dependencies *applyDependencies, _ *applyRuntimeFixture) {
				dependencies.openRuntime = func(context.Context, domain.RuntimeKind) (applyRuntime, error) {
					return nil, errApplyTest
				}
			},
			want:       errApplyTest,
			wantEvents: []string{sourceEvent},
			output:     io.Discard,
		},
		{
			name: "state",
			mutate: func(dependencies *applyDependencies, _ *applyRuntimeFixture) {
				dependencies.openState = func(context.Context) (*store.Store, error) {
					return nil, errApplyTest
				}
			},
			want:       errApplyTest,
			wantEvents: []string{sourceEvent, runtimeEvent, closeRuntimeEvent},
			output:     io.Discard,
		},
		{
			name: "mutation",
			mutate: func(dependencies *applyDependencies, _ *applyRuntimeFixture) {
				dependencies.mutate = func(
					context.Context,
					application.Request,
					*store.Store,
					applyRuntime,
				) (application.Plan, error) {
					return application.Plan{}, errApplyTest
				}
			},
			want:       errApplyTest,
			wantEvents: []string{sourceEvent, runtimeEvent, stateEvent, closeRuntimeEvent},
			output:     io.Discard,
		},
		{
			name:       "output",
			mutate:     func(*applyDependencies, *applyRuntimeFixture) {},
			want:       errApplyOutputTest,
			wantEvents: []string{sourceEvent, runtimeEvent, stateEvent, mutationEvent, closeRuntimeEvent},
			output:     failingWriterWithError{err: errApplyOutputTest},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			events := make([]string, 0, 6)
			runtime := &applyRuntimeFixture{events: &events, inspectErr: nil}
			dependencies := mutationApplyDependencies(t, &events, runtime)
			test.mutate(&dependencies, runtime)

			err := executeMutation(
				context.Background(),
				applyInvocation{compose: composeFileValue, service: applyServiceValue, dryRun: false},
				test.output,
				dependencies,
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("executeMutation() error = %v, want %v", err, test.want)
			}
			if !reflect.DeepEqual(events, test.wantEvents) {
				t.Fatalf("executeMutation() events = %q, want %q", events, test.wantEvents)
			}
		})
	}
}

func mutationApplyDependencies(
	t *testing.T,
	events *[]string,
	runtime applyRuntime,
) applyDependencies {
	t.Helper()

	return applyDependencies{
		loadSource: func(context.Context, string) (compose.Source, error) {
			*events = append(*events, sourceEvent)

			return testComposeSource(t), nil
		},
		openRuntime: func(_ context.Context, runtimeKind domain.RuntimeKind) (applyRuntime, error) {
			*events = append(*events, runtimeEvent)
			if runtimeKind != domain.RuntimeDocker {
				return nil, errApplyTest
			}

			return runtime, nil
		},
		openReader: nil,
		openState: func(ctx context.Context) (*store.Store, error) {
			*events = append(*events, stateEvent)
			directory, err := filepath.EvalSymlinks(t.TempDir())
			if err != nil {
				return nil, fmt.Errorf("resolve state test directory: %w", err)
			}

			return store.Open(ctx, filepath.Join(directory, "maniud", stateDatabaseName))
		},
		mutate: func(
			_ context.Context,
			request application.Request,
			state *store.Store,
			gotRuntime applyRuntime,
		) (application.Plan, error) {
			*events = append(*events, mutationEvent)
			if request.Source.Content == nil || request.Service != applyServiceValue ||
				state == nil || gotRuntime != runtime {
				return application.Plan{}, errApplyTest
			}

			return application.Plan{Kind: application.PlanBootstrap}, nil
		},
		images: nil,
	}
}

type closedStateWriter struct {
	state       **store.Store
	events      *[]string
	destination *bytes.Buffer
}

func (writer *closedStateWriter) Write(value []byte) (int, error) {
	if writer.state == nil || *writer.state == nil {
		return 0, errStateOpenTest
	}

	_, _, err := (*writer.state).AppliedService(context.Background(), "example", applyServiceValue)
	if err == nil {
		return 0, errStateOpenTest
	}

	*writer.events = append(*writer.events, "write")

	written, err := writer.destination.Write(value)
	if err != nil {
		return written, fmt.Errorf("write state-close test output: %w", err)
	}

	return written, nil
}

func validApplyDependencies(
	t *testing.T,
	events *[]string,
	reader applyTransactionReader,
	runtime applyRuntime,
) applyDependencies {
	t.Helper()

	return applyDependencies{
		loadSource: func(context.Context, string) (compose.Source, error) {
			*events = append(*events, sourceEvent)

			return testComposeSource(t), nil
		},
		openRuntime: func(_ context.Context, runtimeKind domain.RuntimeKind) (applyRuntime, error) {
			*events = append(*events, runtimeEvent)
			if runtimeKind != domain.RuntimeDocker {
				return nil, errApplyTest
			}

			return runtime, nil
		},
		openReader: func(context.Context) (applyTransactionReader, error) {
			*events = append(*events, readerEvent)

			return reader, nil
		},
		openState: nil,
		mutate:    nil,
		images:    applyImageResolverFixture{events: events},
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
	*writer.events = append(*writer.events, "write")
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

//nolint:funlen // The table keeps every supported and rejected endpoint form together.
func TestDockerEndpointSelection(t *testing.T) {
	t.Parallel()

	for _, environment := range []map[string]string{
		{},
		{dockerHostKey: "unix:///tmp/docker.sock"},
	} {
		endpoint, err := dockerEndpoint(environment, io.Discard)
		if err != nil || reflect.ValueOf(endpoint).IsZero() {
			t.Fatalf("dockerEndpoint(%v) = %#v, %v", environment, endpoint, err)
		}
	}

	warnings := new(bytes.Buffer)

	endpoint, err := dockerEndpoint(map[string]string{dockerHostKey: testPlainDockerHost}, warnings)
	if err != nil || reflect.ValueOf(endpoint).IsZero() ||
		!strings.Contains(warnings.String(), `"code":"insecure_remote_engine"`) {
		t.Fatalf("dockerEndpoint(VPN) = %#v, %v, warning %q", endpoint, err, warnings.String())
	}

	tests := []struct {
		name        string
		environment map[string]string
		stderr      io.Writer
		want        error
	}{
		{
			name:        "invalid Unix",
			environment: map[string]string{dockerHostKey: "unix://relative.sock"},
			stderr:      io.Discard,
			want:        dockerruntime.ErrInvalidEndpoint,
		},
		{
			name:        "SSH without explicit authentication",
			environment: map[string]string{dockerHostKey: "ssh://engine.example"},
			stderr:      io.Discard,
			want:        dockerruntime.ErrInvalidEndpoint,
		},
		{
			name:        "VPN without warning transport",
			environment: map[string]string{dockerHostKey: testPlainDockerHost},
			stderr:      nil,
			want:        dockerruntime.ErrWarningDelivery,
		},
		{
			name:        "VPN warning failure",
			environment: map[string]string{dockerHostKey: testPlainDockerHost},
			stderr:      failingWriterWithError{err: errApplyOutputTest},
			want:        dockerruntime.ErrWarningDelivery,
		},
		{
			name: "TLS not configured",
			environment: map[string]string{
				dockerHostKey:       "tcp://engine.example:2376",
				"DOCKER_TLS_VERIFY": "1",
			},
			stderr: io.Discard,
			want:   dockerruntime.ErrInvalidEndpoint,
		},
		{
			name:        "unknown scheme",
			environment: map[string]string{dockerHostKey: "http://engine.example:2375"},
			stderr:      io.Discard,
			want:        dockerruntime.ErrInvalidEndpoint,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := dockerEndpoint(test.environment, test.stderr)
			if !errors.Is(err, test.want) {
				t.Fatalf("dockerEndpoint() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestApplyRuntimeKindUsesValidatedComposeMetadata(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		extension string
		want      domain.RuntimeKind
		wantErr   error
	}{
		{name: "Docker default", want: domain.RuntimeDocker},
		{
			name: "Podman", extension: "x-maniud:\n  services:\n    api:\n      runtime: podman\n",
			want: domain.RuntimePodman,
		},
		{
			name:      "containerd",
			extension: "x-maniud:\n  services:\n    api:\n      runtime: containerd\n",
			want:      domain.RuntimeContainerd,
		},
		{name: "invalid source", extension: "x-maniud: []\n", wantErr: compose.ErrInvalidSource},
		{
			name:      "missing service metadata",
			extension: "x-maniud:\n  services:\n    worker:\n      runtime: podman\n",
			wantErr:   compose.ErrInvalidSource,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			source := testComposeSource(t)
			source.Content = append(source.Content, test.extension...)
			got, err := applyRuntimeKind(context.Background(), source, applyServiceValue)
			if !errors.Is(err, test.wantErr) || got != test.want {
				t.Fatalf("applyRuntimeKind() = %q, %v", got, err)
			}
		})
	}
}

func TestPodmanSocketPathUsesOnlyLocalUnixEndpoints(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		environment map[string]string
		want        string
		wantErr     bool
	}{
		{
			name: "explicit", environment: map[string]string{containerHostEnvironment: "unix:///tmp/podman.sock"},
			want: "/tmp/podman.sock",
		},
		{
			name: "XDG runtime", environment: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
			want: "/run/user/1000/podman/podman.sock",
		},
		{
			name: "remote", environment: map[string]string{containerHostEnvironment: "ssh://host/run/podman.sock"},
			wantErr: true,
		},
		{
			name: testRelativePath, environment: map[string]string{containerHostEnvironment: "unix://podman.sock"},
			wantErr: true,
		},
		{name: "unclean", environment: map[string]string{"XDG_RUNTIME_DIR": "/run/../tmp"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := podmanSocketPath(test.environment, 1000)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("podmanSocketPath() = %q, %v", got, err)
			}
		})
	}

	got, err := podmanSocketPath(map[string]string{}, 0)
	if err != nil || got != defaultRootfulPodmanSocket {
		t.Fatalf("podmanSocketPath(root) = %q, %v", got, err)
	}
	got, err = podmanSocketPath(map[string]string{}, 1000)
	if err != nil || got != "/run/user/1000/podman/podman.sock" {
		t.Fatalf("podmanSocketPath(rootless default) = %q, %v", got, err)
	}
	got, err = podmanSocketPath(map[string]string{}, -1)
	if got != "" || !errors.Is(err, podmanruntime.ErrInvalidEndpoint) {
		t.Fatalf("podmanSocketPath(invalid user) = %q, %v", got, err)
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

//nolint:cyclop,funlen // The assertions audit each default factory and its construction failures together.
func TestDefaultApplyDependenciesOwnLifecycleFactories(t *testing.T) {
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

	dependencies, err := defaultApplyDependencies(map[string]string{
		homeKey:         directory,
		xdgStateHomeKey: directory,
		dockerHostKey:   "unix:///missing/docker.sock",
	}, io.Discard, os.Getwd)
	if err != nil {
		t.Fatalf("defaultApplyDependencies() error = %v", err)
	}

	source, err := dependencies.loadSource(t.Context(), composePath)
	if err != nil || !bytes.Equal(source.Content, []byte("services: {}\n")) {
		t.Fatalf("loadSource() = %#v, %v", source, err)
	}

	reader, err := dependencies.openReader(context.Background())
	if err != nil {
		t.Fatalf("openReader() error = %v", err)
	}

	err = reader.Close()
	if err != nil {
		t.Fatalf("reader.Close() error = %v", err)
	}

	state, err := dependencies.openState(context.Background())
	if err != nil {
		t.Fatalf("openState() error = %v", err)
	}

	events := make([]string, 0, 1)
	_, err = dependencies.mutate(
		context.Background(),
		application.Request{},
		state,
		&applyRuntimeFixture{events: &events, inspectErr: nil},
	)
	if !errors.Is(err, application.ErrInvalidRequest) {
		t.Fatalf("mutate(read-only runtime) error = %v", err)
	}
	if err = state.Close(); err != nil {
		t.Fatalf("state.Close() error = %v", err)
	}

	runtime, err := dependencies.openRuntime(context.Background(), domain.RuntimeDocker)
	if runtime != nil || !errors.Is(err, dockerruntime.ErrUnavailable) {
		t.Fatalf("openRuntime() = %#v, %v", runtime, err)
	}

	if dependencies.images == nil {
		t.Fatal("defaultApplyDependencies() has no image resolver")
	}

	_, err = defaultApplyDependencies(map[string]string{}, io.Discard, os.Getwd)
	if err == nil {
		t.Fatal("defaultApplyDependencies(missing home) succeeded")
	}

	invalidDependencies, err := defaultApplyDependencies(
		map[string]string{homeKey: directory, dockerHostKey: "invalid://engine"},
		io.Discard,
		os.Getwd,
	)
	if err == nil {
		_, err = invalidDependencies.openRuntime(context.Background(), domain.RuntimeDocker)
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

func TestDefaultApplyDependenciesOpenRuntime(t *testing.T) {
	t.Parallel()

	server := httptest.NewServer(http.HandlerFunc(func(response http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_ping":
			response.Header().Set("Api-Version", "1.54")
			response.WriteHeader(http.StatusOK)
		case "/v1.54/version":
			response.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(response, `{"Version":"29.0.0","ApiVersion":"1.54",`+
				`"MinAPIVersion":"1.54","Os":"`+linuxOS+`","Arch":"`+testArchitectureAMD64+`"}`)
		default:
			t.Errorf("unexpected Docker request = %s %s", request.Method, request.URL.Path)
			response.WriteHeader(http.StatusNotFound)
		}
	}))
	t.Cleanup(server.Close)

	dependencies, err := defaultApplyDependencies(map[string]string{
		homeKey:       t.TempDir(),
		dockerHostKey: "tcp" + strings.TrimPrefix(server.URL, "http"),
	}, io.Discard, os.Getwd)
	if err != nil {
		t.Fatalf("defaultApplyDependencies() error = %v", err)
	}

	runtimeAdapter, err := dependencies.openRuntime(context.Background(), domain.RuntimeDocker)
	if err != nil {
		t.Fatalf("openRuntime() error = %v", err)
	}

	if runtimeAdapter == nil {
		t.Fatal("openRuntime() returned a nil adapter")
	}

	runtimeAdapter.CloseIdleConnections()
}

func TestDefaultApplyDependenciesOpenPodmanRuntime(t *testing.T) {
	t.Parallel()

	socketPath := startApplyPodmanServer(t)
	dependencies, err := defaultApplyDependencies(map[string]string{
		homeKey:                  t.TempDir(),
		xdgStateHomeKey:          t.TempDir(),
		dockerHostKey:            "invalid://must-not-be-read",
		containerHostEnvironment: "unix://" + socketPath,
	}, io.Discard, os.Getwd)
	if err != nil {
		t.Fatalf("defaultApplyDependencies() error = %v", err)
	}

	runtimeAdapter, err := dependencies.openRuntime(context.Background(), domain.RuntimePodman)
	if err != nil {
		t.Fatalf("openRuntime(Podman) error = %v", err)
	}
	evidence, err := runtimeAdapter.Inspect(context.Background())
	if err != nil || evidence.Kind != domain.RuntimePodman {
		t.Fatalf("Inspect(Podman) = %#v, %v", evidence, err)
	}
	runtimeAdapter.CloseIdleConnections()
}

func TestDefaultApplyDependenciesOpenContainerdRuntime(t *testing.T) {
	t.Parallel()

	socketPath := startApplyContainerdServer(t)
	dependencies, err := defaultApplyDependencies(map[string]string{
		homeKey:                        t.TempDir(),
		xdgStateHomeKey:                t.TempDir(),
		containerdAddressEnvironment:   "unix://" + socketPath,
		containerdNamespaceEnvironment: "maniud-test",
	}, io.Discard, os.Getwd)
	if err != nil {
		t.Fatalf("defaultApplyDependencies() error = %v", err)
	}

	runtimeAdapter, err := dependencies.openRuntime(context.Background(), domain.RuntimeContainerd)
	if err != nil {
		t.Fatalf("openRuntime(containerd) error = %v", err)
	}
	if runtimeAdapter == nil {
		t.Fatal("openRuntime(containerd) returned a nil adapter")
	}
	runtimeAdapter.CloseIdleConnections()
}

func TestDefaultApplyDependenciesRejectsInvalidRuntimes(t *testing.T) {
	t.Parallel()

	dependencies, err := defaultApplyDependencies(map[string]string{homeKey: t.TempDir()}, io.Discard, os.Getwd)
	if err != nil {
		t.Fatalf("defaultApplyDependencies() error = %v", err)
	}
	var runtimeAdapter applyRuntime
	runtimeAdapter, err = dependencies.openRuntime(context.Background(), domain.RuntimeContainerd)
	if runtimeAdapter != nil || err == nil {
		t.Fatalf("openRuntime(containerd) = %#v, %v", runtimeAdapter, err)
	}
	runtimeAdapter, err = dependencies.openRuntime(context.Background(), domain.RuntimeKind("invalid"))
	if runtimeAdapter != nil || !errors.Is(err, application.ErrInvalidRequest) {
		t.Fatalf("openRuntime(invalid) = %#v, %v", runtimeAdapter, err)
	}

	for _, host := range []string{"invalid://podman", "unix:///missing/podman.sock"} {
		invalidDependencies, dependencyErr := defaultApplyDependencies(map[string]string{
			homeKey: t.TempDir(), containerHostEnvironment: host,
		}, io.Discard, os.Getwd)
		if dependencyErr != nil {
			t.Fatalf("defaultApplyDependencies(%q) error = %v", host, dependencyErr)
		}
		runtimeAdapter, err = invalidDependencies.openRuntime(context.Background(), domain.RuntimePodman)
		if runtimeAdapter != nil || err == nil {
			t.Fatalf("openRuntime(Podman %q) = %#v, %v", host, runtimeAdapter, err)
		}
	}
}

type applyContainerdVersionServer struct {
	versionapi.UnimplementedVersionServer
}

func (*applyContainerdVersionServer) Version(
	context.Context,
	*emptypb.Empty,
) (*versionapi.VersionResponse, error) {
	return &versionapi.VersionResponse{Version: "2.3.4", Revision: "test"}, nil
}

type applyContainerdIntrospectionServer struct {
	introspectionapi.UnimplementedIntrospectionServer
}

func (*applyContainerdIntrospectionServer) Server(
	context.Context,
	*emptypb.Empty,
) (*introspectionapi.ServerResponse, error) {
	return &introspectionapi.ServerResponse{UUID: "maniud-test", Pid: 42, Pidns: 84}, nil
}

func startApplyContainerdServer(t *testing.T) string {
	t.Helper()

	directory, err := os.MkdirTemp("/tmp", "maniud-") //nolint:usetesting // Darwin test paths must fit sockaddr_un.
	if err != nil {
		t.Fatalf("create containerd test socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "containerd.sock")
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("listen on containerd test socket: %v", err)
	}
	if err := os.Chmod(path, 0o600); err != nil {
		_ = listener.Close()
		t.Fatalf("protect containerd test socket: %v", err)
	}
	server := grpc.NewServer()
	versionapi.RegisterVersionServer(server, &applyContainerdVersionServer{})
	introspectionapi.RegisterIntrospectionServer(server, &applyContainerdIntrospectionServer{})
	done := make(chan struct{})
	go func() {
		_ = server.Serve(listener)
		close(done)
	}()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
		<-done
	})

	return path
}

func startApplyPodmanServer(t *testing.T) string {
	t.Helper()

	directory, err := os.MkdirTemp("/tmp", "maniud-") //nolint:usetesting // Darwin test paths must fit sockaddr_un.
	if err != nil {
		t.Fatalf("create Podman test socket directory: %v", err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "podman.sock")
	listener, err := (&net.ListenConfig{}).Listen(context.Background(), "unix", path)
	if err != nil {
		t.Fatalf("listen Podman test socket: %v", err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // The test socket is private to the current user.
		_ = listener.Close()
		t.Fatalf("protect Podman test socket: %v", err)
	}
	handler := http.HandlerFunc(func(writer http.ResponseWriter, request *http.Request) {
		switch request.URL.Path {
		case "/_ping":
			writer.Header().Set("Libpod-Api-Version", "6.1.0")
			_, _ = io.WriteString(writer, "OK")
		case "/version":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer, `{"Version":"6.1.0","Components":[{"Name":"Podman Engine",`+
				`"Version":"6.1.0","Details":{"APIVersion":"6.1.0","MinAPIVersion":"5.0.0"}}]}`)
		case "/v6.1.0/libpod/info":
			writer.Header().Set("Content-Type", "application/json")
			_, _ = io.WriteString(writer,
				`{"host":{"os":"linux","arch":"amd64"},`+
					`"store":{"graphRoot":"/var/lib/containers/storage"}}`,
			)
		default:
			t.Errorf("unexpected Podman request = %s %s", request.Method, request.URL.Path)
			http.NotFound(writer, request)
		}
	})
	server := &http.Server{Handler: handler} //nolint:gosec // The test server binds only the private Unix socket.
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		_ = server.Close()
		_ = listener.Close()
	})

	return path
}

func TestDefaultApplyDependenciesRejectsMissingWorkingDirectory(t *testing.T) {
	t.Parallel()

	dependencies, err := defaultApplyDependencies(
		map[string]string{homeKey: "/tmp"},
		io.Discard,
		func() (string, error) { return "", io.ErrUnexpectedEOF },
	)
	if err == nil || dependencies.loadSource != nil || dependencies.openRuntime != nil ||
		dependencies.openReader != nil || dependencies.openState != nil || dependencies.mutate != nil ||
		dependencies.images != nil {
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
			execute:    func(applyInvocation) error { return dockerruntime.ErrUnavailable },
			wantStatus: 1,
			wantOutput: "{\"code\":\"apply_failed\",\"message\":\"apply validation failed\",\"retryable\":true}\n",
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
		{err: containerdruntime.ErrUnavailable, code: domain.ErrorApplyFailed, retryable: true},
		{err: fmt.Errorf("wrapped: %w", containerdruntime.ErrUnavailable), code: domain.ErrorApplyFailed, retryable: true},
		{err: dockerruntime.ErrUnavailable, code: domain.ErrorApplyFailed, retryable: true},
		{err: podmanruntime.ErrUnavailable, code: domain.ErrorApplyFailed, retryable: true},
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
