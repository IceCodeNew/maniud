package tui

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"slices"
	"sync"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

var (
	errTestTUI    = errors.New("TUI test failure")
	errTestSecret = errors.New("BARK_DEVICE_KEY=secret")
)

const (
	testProject     = "project"
	testService     = "service"
	testRuntime     = "docker"
	testPlatform    = "linux/amd64"
	testUpgrade     = "upgrade"
	testAPI         = "api"
	testWorker      = "worker"
	snapshotCall    = "snapshot"
	evidenceCall    = "evidence"
	registeredAPIID = "services/api.yaml"
)

type catalogFixture struct {
	mu               sync.Mutex
	snapshot         CatalogSnapshot
	registeredResult OpenResult
	pathResult       OpenResult
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
	snapshot       application.OperationSnapshot
	evidence       application.EvidenceBundle
	applyErr       error
	snapshotErr    error
	evidenceErr    error
	blockApply     bool
	cancelObserved chan struct{}
	cancelOnce     sync.Once
}

func (fixture *operationsFixture) Apply(
	ctx context.Context,
	request application.Request,
) (application.Plan, error) {
	fixture.record("apply", request)
	if fixture.blockApply {
		<-ctx.Done()
		fixture.cancelOnce.Do(func() { close(fixture.cancelObserved) })

		return application.Plan{}, fmt.Errorf("wait for cancellation: %w", ctx.Err())
	}

	return fixture.snapshot.Plan, fixture.applyErr
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

func newOperationsFixture() *operationsFixture {
	plan := testPlan()

	return &operationsFixture{
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
	state := newModel(t.Context(), catalog, operations, NewEventStream(), Options{})
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

func key(name string) tea.KeyPressMsg {
	keys := map[string]tea.Key{
		keyEnter:    {Code: tea.KeyEnter},
		keyEscape:   {Code: tea.KeyEscape},
		"up":        {Code: tea.KeyUp},
		keyDown:     {Code: tea.KeyDown},
		"left":      {Code: tea.KeyLeft},
		"right":     {Code: tea.KeyRight},
		keyTab:      {Code: tea.KeyTab},
		"shift+tab": {Code: tea.KeyTab, Mod: tea.ModShift},
		"backspace": {Code: tea.KeyBackspace},
		"ctrl+c":    {Code: 'c', Mod: tea.ModCtrl},
	}
	if message, found := keys[name]; found {
		return tea.KeyPressMsg(message)
	}

	characters := []rune(name)

	return tea.KeyPressMsg(tea.Key{Code: characters[0], Text: name})
}

func TestEventStreamIsBoundedAndCancellationAware(t *testing.T) {
	t.Parallel()

	stream := NewEventStream()
	for range eventQueueCapacity {
		if !stream.TryPublish(application.Event{Kind: application.EventPlanPrepared}) {
			t.Fatal("event queue dropped an event before reaching capacity")
		}
	}
	if stream.TryPublish(application.Event{}) || stream.DroppedEvents() != 1 {
		t.Fatalf("full queue accepted an event; dropped = %d", stream.DroppedEvents())
	}
	if event, valid := stream.wait(t.Context())().(eventMsg); !valid ||
		application.Event(event).Kind != application.EventPlanPrepared {
		t.Fatalf("wait() = %#v", event)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if _, valid := stream.wait(cancelled)().(contextDoneMsg); !valid {
		t.Fatal("cancelled wait did not return contextDoneMsg")
	}
	if _, valid := waitForContext(cancelled)().(contextDoneMsg); !valid {
		t.Fatal("waitForContext() did not return contextDoneMsg")
	}
	if _, valid := messageForEvent(cancelled, application.Event{}).(contextDoneMsg); !valid {
		t.Fatal("messageForEvent() did not prioritize cancellation")
	}
}

func TestRunValidatesDependenciesAndContext(t *testing.T) {
	t.Parallel()

	catalog := &catalogFixture{}
	operations := newOperationsFixture()
	events := NewEventStream()
	tests := []struct {
		catalog    Catalog
		operations Operations
		events     *EventStream
	}{
		{catalog: nil, operations: operations, events: events},
		{catalog: catalog, operations: nil, events: events},
		{catalog: catalog, operations: operations, events: nil},
	}
	for _, test := range tests {
		if err := Run(
			t.Context(), nil, io.Discard, test.catalog, test.operations, test.events, Options{},
		); !errors.Is(err, errInvalidInput) {
			t.Fatalf("Run(invalid) error = %v", err)
		}
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Run(cancelled, nil, io.Discard, catalog, operations, events, Options{}); !errors.Is(
		err,
		context.Canceled,
	) {
		t.Fatalf("Run(cancelled) error = %v", err)
	}
}

func TestRunStartsHomeAndContainsReaderFailure(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	catalog := &catalogFixture{snapshot: CatalogSnapshot{State: CatalogMissing}, ready: ready}
	if err := Run(
		t.Context(),
		&signalReader{ready: ready, content: []byte("q")},
		io.Discard,
		catalog,
		newOperationsFixture(),
		NewEventStream(),
		Options{},
	); err != nil {
		t.Fatalf("Run(success) error = %v", err)
	}

	ready = make(chan struct{})
	catalog = &catalogFixture{snapshot: CatalogSnapshot{State: CatalogMissing}, ready: ready}
	if err := Run(
		t.Context(), failingReader{ready: ready}, io.Discard, catalog, newOperationsFixture(), NewEventStream(), Options{},
	); !errors.Is(err, errTestTUI) {
		t.Fatalf("Run(reader failure) error = %v", err)
	}
}

type signalReader struct {
	ready   <-chan struct{}
	content []byte
}

func (reader *signalReader) Read(destination []byte) (int, error) {
	<-reader.ready
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
	<-reader.ready

	return 0, errTestTUI
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
}
