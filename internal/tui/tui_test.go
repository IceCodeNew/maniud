package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

var (
	errTestTUI    = errors.New("TUI test failure")
	errTestSecret = errors.New("BARK_DEVICE_KEY=secret")
)

const (
	testProject          = "project"
	testService          = "service"
	testRuntime          = "docker"
	testPlatform         = "linux/amd64"
	testUpgrade          = "upgrade"
	testAPI              = "api"
	testWorker           = "worker"
	testOldProjection    = "old"
	testNewProjection    = "new"
	testUnknownValue     = "unknown"
	testSecretValue      = "secret"
	testOtherValue       = "other"
	applyCall            = "apply"
	dryRunCall           = "dry-run"
	snapshotCall         = "snapshot"
	stageCall            = "stage"
	evidenceCall         = "evidence"
	registeredAPIID      = "services/api.yaml"
	testServicePath      = "services/service.yaml"
	testComposePath      = "compose.yaml"
	testImage            = "image"
	testRuntimeCommand   = "docker run image"
	testRepositoryPath   = "/tmp/repository"
	testGitHubRepository = "owner/desired-state"
	testExistingRemote   = "https://example.com/desired-state.git"
	testApplyCompleted   = "Apply completed"
	testBlockerMessage   = "Compose source did not pass validation"
	testCommitMessage    = "message"
	testStableStatus     = "stable"
	testDiff             = "diff"
	testPlaceholderPath  = "path"
	renderSyncTimeout    = 5 * time.Second
)

type catalogFixture struct {
	mu               sync.Mutex
	snapshot         CatalogSnapshot
	registeredResult OpenResult
	pathResult       OpenResult
	registration     RegistrationResult
	calls            []string
	ready            chan struct{}
	readyOnce        sync.Once
}

func (fixture *catalogFixture) Snapshot(context.Context) CatalogSnapshot {
	fixture.record(snapshotCall)
	if fixture.ready != nil {
		fixture.readyOnce.Do(func() { close(fixture.ready) })
	}

	return fixture.snapshot
}

func (fixture *catalogFixture) OpenRegistered(_ context.Context, id string) OpenResult {
	fixture.record("registered:" + id)

	return fixture.registeredResult
}

func (fixture *catalogFixture) OpenPath(_ context.Context, path string) OpenResult {
	fixture.record("path:" + path)

	return fixture.pathResult
}

func (fixture *catalogFixture) Register(_ context.Context, request RepositorySetupRequest) RegistrationResult {
	fixture.record(fmt.Sprintf("register:%d:%s:%s", request.Mode, request.Remote, request.Checkout))

	return fixture.registration
}

func (fixture *catalogFixture) record(call string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	fixture.calls = append(fixture.calls, call)
}

func (fixture *catalogFixture) recordedCalls() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	return slices.Clone(fixture.calls)
}

type operationsFixture struct {
	mu             sync.Mutex
	calls          []string
	requests       []application.Request
	resolutions    []application.HealthResolution
	dryRunPlan     application.Plan
	snapshot       application.OperationSnapshot
	evidence       application.EvidenceBundle
	applyErr       error
	resolutionErr  error
	dryRunErr      error
	snapshotErr    error
	evidenceErr    error
	blockApply     bool
	cancelObserved chan struct{}
	cancelOnce     sync.Once
	evidenceReady  chan struct{}
	evidenceOnce   sync.Once
}

type workspaceFixture struct {
	mu                   sync.Mutex
	draft                ServiceDraft
	staged               StagedService
	commit               CommitResult
	previewErr           error
	stageErr             error
	commitErr            error
	suspendErr           error
	suspendHasDeadline   bool
	suspendContextErr    error
	suspendTimeRemaining time.Duration
	calls                []string
}

func (fixture *workspaceFixture) Preview(_ context.Context, input string) (ServiceDraft, error) {
	fixture.record("preview:" + input)

	return fixture.draft, fixture.previewErr
}

func (fixture *workspaceFixture) Stage(context.Context) (StagedService, error) {
	fixture.record(stageCall)

	return fixture.staged, fixture.stageErr
}

func (fixture *workspaceFixture) Commit(
	_ context.Context,
	message string,
	unsigned bool,
) (CommitResult, error) {
	fixture.record(fmt.Sprintf("commit:%t:%s", unsigned, message))

	return fixture.commit, fixture.commitErr
}

func (fixture *workspaceFixture) Suspend(ctx context.Context) error {
	deadline, hasDeadline := ctx.Deadline()
	fixture.mu.Lock()
	fixture.suspendHasDeadline = hasDeadline
	fixture.suspendContextErr = ctx.Err()
	fixture.suspendTimeRemaining = time.Until(deadline)
	fixture.mu.Unlock()
	fixture.record("suspend")

	return fixture.suspendErr
}

func (fixture *workspaceFixture) record(call string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.calls = append(fixture.calls, call)
}

func (fixture *workspaceFixture) recordedCalls() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	return slices.Clone(fixture.calls)
}

func (fixture *workspaceFixture) recordedSuspendContext() (bool, time.Duration, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	return fixture.suspendHasDeadline, fixture.suspendTimeRemaining, fixture.suspendContextErr
}

func newWorkspaceFixture() *workspaceFixture {
	return &workspaceFixture{
		draft: ServiceDraft{
			Runtime: testRuntime, Image: "registry.example/api@sha256:aaaaaaaa", Service: testAPI,
			ComposePath: "services/api.yaml",
		},
		staged: StagedService{
			Diff:        "diff --git a/services/api.yaml b/services/api.yaml\n+image: example\n",
			ComposePath: "services/api.yaml", CommitMessage: "Add api service",
		},
	}
}

type deploymentWorkspaceFixture struct{}

func (*deploymentWorkspaceFixture) Fields(
	context.Context,
	application.Request,
) ([]DeploymentFieldState, error) {
	return nil, nil
}

func (*deploymentWorkspaceFixture) Preview(
	context.Context,
	application.Request,
	string,
	string,
	bool,
) (DeploymentEditPreview, error) {
	return DeploymentEditPreview{}, nil
}

func (*deploymentWorkspaceFixture) PreviewRestore(
	context.Context,
	application.Request,
	string,
) (DeploymentEditPreview, error) {
	return DeploymentEditPreview{}, nil
}

func (*deploymentWorkspaceFixture) Stage(context.Context) (StagedDeploymentEdit, error) {
	return StagedDeploymentEdit{}, nil
}

func (*deploymentWorkspaceFixture) Commit(
	context.Context,
	string,
	bool,
) (CommitResult, error) {
	return CommitResult{}, nil
}

func (*deploymentWorkspaceFixture) Discard(context.Context) error {
	return nil
}

func (*deploymentWorkspaceFixture) History(
	context.Context,
	application.Request,
) ([]DeploymentHistoryEntry, error) {
	return nil, nil
}

func (fixture *operationsFixture) DryRun(
	_ context.Context,
	request application.Request,
) (application.Plan, error) {
	fixture.record(dryRunCall, request)

	return fixture.dryRunPlan, fixture.dryRunErr
}

func (fixture *operationsFixture) Apply(
	ctx context.Context,
	request application.Request,
) (application.Plan, error) {
	fixture.record(applyCall, request)
	if fixture.blockApply {
		<-ctx.Done()
		fixture.cancelOnce.Do(func() { close(fixture.cancelObserved) })

		return application.Plan{}, fmt.Errorf("wait for cancellation: %w", ctx.Err())
	}

	return fixture.snapshot.Plan, fixture.applyErr
}

func (fixture *operationsFixture) ResolveHealth(
	_ context.Context,
	request application.Request,
	resolution application.HealthResolution,
) (application.Plan, error) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()
	fixture.calls = append(fixture.calls, "resolve-health")
	fixture.requests = append(fixture.requests, request)
	fixture.resolutions = append(fixture.resolutions, resolution)

	return fixture.snapshot.Plan, fixture.resolutionErr
}

func (fixture *operationsFixture) Snapshot(
	_ context.Context,
	request application.Request,
) (application.OperationSnapshot, error) {
	fixture.record(snapshotCall, request)

	return fixture.snapshot, fixture.snapshotErr
}

func (fixture *operationsFixture) Evidence(
	application.OperationSnapshot,
) (application.EvidenceBundle, error) {
	fixture.record(evidenceCall, application.Request{})
	if fixture.evidenceReady != nil {
		fixture.evidenceOnce.Do(func() { close(fixture.evidenceReady) })
	}

	return fixture.evidence, fixture.evidenceErr
}

func (fixture *operationsFixture) record(call string, request application.Request) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	fixture.calls = append(fixture.calls, call)
	fixture.requests = append(fixture.requests, request)
}

func (fixture *operationsFixture) recordedCalls() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	return slices.Clone(fixture.calls)
}

func (fixture *operationsFixture) recordedResolutions() []application.HealthResolution {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	return slices.Clone(fixture.resolutions)
}

func newOperationsFixture() *operationsFixture {
	plan := testPlan()

	return &operationsFixture{
		dryRunPlan: plan,
		snapshot: application.OperationSnapshot{
			Plan:       plan,
			HasApplied: true,
			Applied: application.SnapshotAppliedService{
				Reference: "registry.example/team/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa",
			},
		},
		evidence: application.EvidenceBundle{Version: application.EvidenceBundleVersion},
	}
}

func testPlan() application.Plan {
	return application.Plan{
		Kind:     application.PlanUpgrade,
		Project:  testProject,
		Service:  testService,
		Runtime:  domain.RuntimeDocker,
		Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
		Image: domain.ImageIdentity{
			Reference: "registry.example/team/api@sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb",
		},
	}
}

func testTarget() Target {
	return Target{
		Project: testProject, Service: testService, Runtime: testRuntime,
		Request: application.Request{Service: testService},
	}
}

func newTestModel(t *testing.T) (*model, *catalogFixture, *operationsFixture) {
	t.Helper()

	target := testTarget()
	catalog := &catalogFixture{
		snapshot: CatalogSnapshot{
			State: CatalogReady,
			Services: []Service{{
				ID: registeredAPIID, Location: registeredAPIID, Project: testProject,
				Name: testService, Runtime: testRuntime,
			}},
		},
		registeredResult: OpenResult{Targets: []Target{target}},
		pathResult:       OpenResult{Targets: []Target{target}},
	}
	operations := newOperationsFixture()
	state := newModelWithCapabilities(
		t.Context(), catalog, newWorkspaceFixture(), nil, nil, operations, NewEventStream(), Options{},
	)
	state.resize(defaultWidth, defaultHeight)

	return state, catalog, operations
}

func deliver(t *testing.T, state *model, command tea.Cmd) {
	t.Helper()
	if command == nil {
		return
	}
	message := command()
	if batch, valid := message.(tea.BatchMsg); valid {
		for _, nested := range batch {
			deliver(t, state, nested)
		}

		return
	}
	_, next := state.Update(message)
	deliver(t, state, next)
}

func openPathPageValue(t *testing.T, state *model) openPathPage {
	t.Helper()

	value, valid := state.page.(openPathPage)
	if !valid {
		t.Fatalf("page = %T, want openPathPage", state.page)
	}

	return value
}

func reviewPageValue(t *testing.T, state *model) reviewPage {
	t.Helper()

	value, valid := state.page.(reviewPage)
	if !valid {
		t.Fatalf("page = %T, want reviewPage", state.page)
	}

	return value
}

func homePageValue(t *testing.T, state *model) homePage {
	t.Helper()

	value, valid := state.page.(homePage)
	if !valid {
		t.Fatalf("page = %T, want homePage", state.page)
	}

	return value
}

func registrationPageValue(t *testing.T, state *model) registrationPage {
	t.Helper()

	value, valid := state.page.(registrationPage)
	if !valid {
		t.Fatalf("page = %T, want registrationPage", state.page)
	}

	return value
}

func commitPageValue(t *testing.T, state *model) commitPage {
	t.Helper()

	value, valid := state.page.(commitPage)
	if !valid {
		t.Fatalf("page = %T, want commitPage", state.page)
	}

	return value
}

func workspaceFixtureValue(t *testing.T, state *model) *workspaceFixture {
	t.Helper()

	value, valid := state.workspace.(*workspaceFixture)
	if !valid {
		t.Fatalf("workspace = %T, want *workspaceFixture", state.workspace)
	}

	return value
}

func key(name string) tea.KeyPressMsg {
	keys := map[string]tea.Key{
		keyEnter:    {Code: tea.KeyEnter},
		keyEscape:   {Code: tea.KeyEscape},
		"up":        {Code: tea.KeyUp},
		keyDown:     {Code: tea.KeyDown},
		keyLeft:     {Code: tea.KeyLeft},
		keyRight:    {Code: tea.KeyRight},
		keyTab:      {Code: tea.KeyTab},
		keyShiftTab: {Code: tea.KeyTab, Mod: tea.ModShift},
		"backspace": {Code: tea.KeyBackspace},
		"ctrl+c":    {Code: 'c', Mod: tea.ModCtrl},
		"ctrl+e":    {Code: 'e', Mod: tea.ModCtrl},
	}
	if message, found := keys[name]; found {
		return tea.KeyPressMsg(message)
	}

	characters := []rune(name)

	return tea.KeyPressMsg(tea.Key{Code: characters[0], Text: name})
}

//nolint:cyclop // The stream contract covers capacity, generation binding, and cancellation together.
func TestEventStreamIsBoundedAndCancellationAware(t *testing.T) {
	t.Parallel()

	stream := NewEventStream()
	stream.bind(7)
	for range eventQueueCapacity {
		if !stream.TryPublish(application.Event{Kind: application.EventPlanPrepared}) {
			t.Fatal("event queue dropped an event before reaching capacity")
		}
	}
	if stream.TryPublish(application.Event{}) || stream.DroppedEvents() != 1 {
		t.Fatalf("full queue accepted an event; dropped = %d", stream.DroppedEvents())
	}
	if observation, valid := stream.wait(t.Context())().(eventMsg); !valid || observation.sequence != 7 ||
		observation.event.Kind != application.EventPlanPrepared {
		t.Fatalf("wait() = %#v", observation)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, valid := stream.wait(cancelled)().(contextDoneMsg); !valid {
		t.Fatal("cancelled wait did not return contextDoneMsg")
	}
	if _, valid := waitForContext(cancelled)().(contextDoneMsg); !valid {
		t.Fatal("waitForContext() did not return contextDoneMsg")
	}
	if _, valid := messageForEvent(cancelled, eventMsg{}).(contextDoneMsg); !valid {
		t.Fatal("messageForEvent() did not prioritize cancellation")
	}
}

func TestRunValidatesDependenciesAndContext(t *testing.T) {
	t.Parallel()

	catalog := &catalogFixture{}
	workspace := newWorkspaceFixture()
	deployments := &deploymentWorkspaceFixture{}
	operations := newOperationsFixture()
	events := NewEventStream()
	tests := []struct {
		catalog     Catalog
		workspace   ServiceWorkspace
		deployments DeploymentWorkspace
		operations  Operations
		events      *EventStream
	}{
		{catalog: nil, workspace: workspace, deployments: deployments, operations: operations, events: events},
		{catalog: catalog, workspace: nil, deployments: deployments, operations: operations, events: events},
		{catalog: catalog, workspace: workspace, deployments: nil, operations: operations, events: events},
		{catalog: catalog, workspace: workspace, deployments: deployments, operations: nil, events: events},
		{catalog: catalog, workspace: workspace, deployments: deployments, operations: operations, events: nil},
	}
	for _, test := range tests {
		if _, err := RunWithAssistant(
			t.Context(), nil, io.Discard, test.catalog, test.workspace, test.deployments,
			nil, test.operations, test.events, Options{},
		); !errors.Is(err, errInvalidInput) {
			t.Fatalf("RunWithAssistant(invalid) error = %v", err)
		}
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := RunWithAssistant(
		cancelled, nil, io.Discard, catalog, workspace, deployments, nil, operations, events, Options{},
	); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("RunWithAssistant(cancelled) error = %v", err)
	}
	hasDeadline, timeRemaining, suspendContextErr := workspace.recordedSuspendContext()
	if !hasDeadline || suspendContextErr != nil || timeRemaining <= 0 || timeRemaining > workspaceSuspendTimeout {
		t.Fatalf(
			"Suspend context = deadline:%t error:%v remaining:%v",
			hasDeadline,
			suspendContextErr,
			timeRemaining,
		)
	}
}

func TestRunClosesAssistantOnInvalidDependencies(t *testing.T) {
	t.Parallel()

	workspace := newWorkspaceFixture()
	operations := newOperationsFixture()
	events := NewEventStream()
	assistant := &assistantFixture{}
	if _, err := RunWithAssistant(
		t.Context(), nil, io.Discard, nil, workspace,
		&deploymentAssistFixture{deploymentWorkflowFixture: newDeploymentWorkflowFixture()}, assistant,
		operations, events, Options{},
	); !errors.Is(err, errInvalidInput) || assistant.closed != 1 {
		t.Fatalf("RunWithAssistant(invalid dependency) error = %v, closes = %d", err, assistant.closed)
	}
	assistant = &assistantFixture{}
	if _, err := RunWithAssistant(
		t.Context(), nil, io.Discard, &catalogFixture{}, workspace, &deploymentWorkspaceFixture{}, assistant,
		operations, events, Options{},
	); !errors.Is(err, errInvalidInput) || assistant.closed != 1 {
		t.Fatalf("RunWithAssistant(invalid deployment workspace) error = %v, closes = %d", err, assistant.closed)
	}
}

func TestRunStartsHomeAndContainsReaderFailure(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	catalog := &catalogFixture{snapshot: CatalogSnapshot{State: CatalogMissing}, ready: ready}
	workspace := newWorkspaceFixture()
	assistant := &assistantFixture{}
	if _, err := RunWithAssistant(
		t.Context(),
		&signalReader{ready: ready, content: []byte("q")},
		io.Discard,
		catalog,
		workspace,
		&deploymentAssistFixture{deploymentWorkflowFixture: newDeploymentWorkflowFixture()},
		assistant,
		newOperationsFixture(),
		NewEventStream(),
		Options{},
	); err != nil {
		t.Fatalf("Run(success) error = %v", err)
	}
	if !slices.Equal(workspace.recordedCalls(), []string{"suspend"}) {
		t.Fatalf("Run(success) workspace calls = %q", workspace.recordedCalls())
	}
	if assistant.closed != 1 {
		t.Fatalf("RunWithAssistant() closes = %d", assistant.closed)
	}

	ready = make(chan struct{})
	catalog = &catalogFixture{snapshot: CatalogSnapshot{State: CatalogMissing}, ready: ready}
	if _, err := RunWithAssistant(
		t.Context(), failingReader{ready: ready}, io.Discard, catalog, newWorkspaceFixture(),
		&deploymentWorkspaceFixture{}, nil, newOperationsFixture(), NewEventStream(), Options{},
	); !errors.Is(err, errTestTUI) {
		t.Fatalf("RunWithAssistant(reader failure) error = %v", err)
	}
}

func TestRunReturnsContainedOperationFailure(t *testing.T) {
	t.Parallel()

	homeReady := make(chan struct{})
	evidenceReady := make(chan struct{})
	target := testTarget()
	catalog := &catalogFixture{
		snapshot: CatalogSnapshot{
			State: CatalogReady,
			Services: []Service{{
				ID: registeredAPIID, Location: registeredAPIID, Project: testProject,
				Name: testService, Runtime: testRuntime,
			}},
		},
		registeredResult: OpenResult{Targets: []Target{target}},
	}
	operations := newOperationsFixture()
	operations.evidence.Version = 0
	operations.evidenceReady = evidenceReady
	reader := &phasedReader{phases: []readerPhase{
		{ready: homeReady, content: []byte("\r")},
		{ready: evidenceReady, content: []byte("q")},
	}}
	output := &renderSignalWriter{needle: []byte(registeredAPIID), ready: homeReady}
	if _, err := RunWithAssistant(
		t.Context(), reader, output, catalog, newWorkspaceFixture(), &deploymentWorkspaceFixture{},
		nil, operations, NewEventStream(), Options{},
	); !errors.Is(err, errInvalidInput) {
		t.Fatalf("RunWithAssistant(operation failure) error = %v", err)
	}
}

func TestRunReturnsFrozenExportAfterTerminalSession(t *testing.T) {
	t.Parallel()

	homeReady := make(chan struct{})
	reviewReady := make(chan struct{})
	target := testTarget()
	catalog := &catalogFixture{
		snapshot: CatalogSnapshot{
			State: CatalogReady,
			Services: []Service{{
				ID: registeredAPIID, Location: registeredAPIID, Project: testProject,
				Name: testService, Runtime: testRuntime,
			}},
		},
		registeredResult: OpenResult{Targets: []Target{target}},
	}
	workspace := newWorkspaceFixture()
	reader := &phasedReader{phases: []readerPhase{
		{ready: homeReady, content: []byte("\r")},
		{ready: reviewReady, content: []byte("x")},
	}}
	output := &renderSequenceWriter{
		needles: [][]byte{[]byte(registeredAPIID), []byte("Review image change")},
		ready:   []chan struct{}{homeReady, reviewReady},
	}
	result, err := RunWithAssistant(
		t.Context(), reader, output, catalog, workspace, &deploymentWorkspaceFixture{},
		nil, newOperationsFixture(), NewEventStream(), Options{},
	)
	if err != nil || !strings.Contains(result.Export, "Maniud session details") ||
		!strings.Contains(result.Export, "registry.example/team/api@sha256:") {
		t.Fatalf("RunWithAssistant(export) = %q, %v", result.Export, err)
	}
	if !slices.Equal(workspace.recordedCalls(), []string{"suspend"}) {
		t.Fatalf("RunWithAssistant(export) workspace calls = %q", workspace.recordedCalls())
	}
}

type renderSignalWriter struct {
	mu       sync.Mutex
	rendered []byte
	needle   []byte
	ready    chan struct{}
	once     sync.Once
}

func (writer *renderSignalWriter) Write(content []byte) (int, error) {
	writer.mu.Lock()
	writer.rendered = append(writer.rendered, content...)
	ready := bytes.Contains(writer.rendered, writer.needle)
	writer.mu.Unlock()
	if ready {
		writer.once.Do(func() { close(writer.ready) })
	}

	return len(content), nil
}

type renderSequenceWriter struct {
	mu       sync.Mutex
	rendered []byte
	needles  [][]byte
	ready    []chan struct{}
	index    int
}

func (writer *renderSequenceWriter) Write(content []byte) (int, error) {
	writer.mu.Lock()
	defer writer.mu.Unlock()

	writer.rendered = append(writer.rendered, content...)
	for writer.index < len(writer.needles) && bytes.Contains(writer.rendered, writer.needles[writer.index]) {
		close(writer.ready[writer.index])
		writer.index++
	}

	return len(content), nil
}

type readerPhase struct {
	ready   <-chan struct{}
	content []byte
}

type phasedReader struct {
	phases []readerPhase
}

func (reader *phasedReader) Read(destination []byte) (int, error) {
	if len(reader.phases) == 0 {
		return 0, io.EOF
	}
	phase := &reader.phases[0]
	if err := waitForRenderSignal(phase.ready); err != nil {
		return 0, err
	}
	count := copy(destination, phase.content)
	phase.content = phase.content[count:]
	if len(phase.content) == 0 {
		reader.phases = reader.phases[1:]
	}

	return count, nil
}

type signalReader struct {
	ready   <-chan struct{}
	content []byte
}

func (reader *signalReader) Read(destination []byte) (int, error) {
	if err := waitForRenderSignal(reader.ready); err != nil {
		return 0, err
	}
	if len(reader.content) == 0 {
		return 0, io.EOF
	}

	count := copy(destination, reader.content)
	reader.content = reader.content[count:]

	return count, nil
}

type failingReader struct {
	ready <-chan struct{}
}

func (reader failingReader) Read([]byte) (int, error) {
	if err := waitForRenderSignal(reader.ready); err != nil {
		return 0, err
	}

	return 0, errTestTUI
}

func waitForRenderSignal(ready <-chan struct{}) error {
	select {
	case <-ready:
		return nil
	case <-time.After(renderSyncTimeout):
		return fmt.Errorf("wait for TUI render: %w", context.DeadlineExceeded)
	}
}

func TestSignalReaderDrainsContent(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	close(ready)
	reader := &signalReader{ready: ready, content: []byte("q")}
	buffer := make([]byte, 1)
	if count, err := reader.Read(buffer); count != 1 || err != nil || !bytes.Equal(buffer, []byte("q")) {
		t.Fatalf("first Read() = %d, %v, %q", count, err, buffer)
	}
	if count, err := reader.Read(buffer); count != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("second Read() = %d, %v", count, err)
	}
	if IsTerminal(^uintptr(0)) {
		t.Fatal("invalid descriptor reported as a terminal")
	}
}
