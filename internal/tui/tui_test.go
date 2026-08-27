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

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

var (
	errTestTUI       = errors.New("TUI test failure")
	errTestSecret    = errors.New("secret-token")
	errTestEnvSecret = errors.New("BARK_DEVICE_KEY=secret")
)

const (
	evidenceCall = "evidence"
	snapshotCall = "snapshot"
)

type operationsFixture struct {
	mu             sync.Mutex
	calls          []string
	plan           application.Plan
	snapshot       application.OperationSnapshot
	evidence       application.EvidenceBundle
	dryRunErr      error
	applyErr       error
	snapshotErr    error
	evidenceErr    error
	blockDryRun    bool
	cancelObserved chan struct{}
	evidenceRead   chan struct{}
	cancelOnce     sync.Once
	evidenceOnce   sync.Once
}

func (fixture *operationsFixture) DryRun(
	ctx context.Context,
	_ application.Request,
) (application.Plan, error) {
	fixture.record("dry-run")
	if fixture.blockDryRun {
		<-ctx.Done()
		fixture.cancelOnce.Do(func() { close(fixture.cancelObserved) })

		return application.Plan{}, fmt.Errorf("wait for cancellation: %w", ctx.Err())
	}

	return fixture.plan, fixture.dryRunErr
}

func (fixture *operationsFixture) Apply(
	context.Context,
	application.Request,
) (application.Plan, error) {
	fixture.record("apply")

	return fixture.plan, fixture.applyErr
}

func (fixture *operationsFixture) Snapshot(
	context.Context,
	application.Request,
) (application.OperationSnapshot, error) {
	fixture.record(snapshotCall)

	return fixture.snapshot, fixture.snapshotErr
}

func (fixture *operationsFixture) Evidence(
	application.OperationSnapshot,
) (application.EvidenceBundle, error) {
	fixture.record(evidenceCall)
	if fixture.evidenceRead != nil {
		fixture.evidenceOnce.Do(func() { close(fixture.evidenceRead) })
	}

	return fixture.evidence, fixture.evidenceErr
}

func (fixture *operationsFixture) record(call string) {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	fixture.calls = append(fixture.calls, call)
}

func (fixture *operationsFixture) recordedCalls() []string {
	fixture.mu.Lock()
	defer fixture.mu.Unlock()

	return slices.Clone(fixture.calls)
}

func TestModelConsumesEveryFacadeCapability(t *testing.T) {
	t.Parallel()

	fixture := newOperationsFixture()
	state := newModel(t.Context(), fixture, application.Request{}, NewEventStream())

	state.Update(state.startSnapshot()())
	if !state.hasSnapshot || !state.hasEvidence || !state.hasPlan || state.status != statusReady {
		t.Fatalf("snapshot state = %#v", state)
	}
	if diff := slices.Compare(fixture.recordedCalls(), []string{snapshotCall, evidenceCall}); diff != 0 {
		t.Fatalf("snapshot calls = %q", fixture.recordedCalls())
	}

	state.Update(state.startDryRun()())
	assertApplyRefreshesSnapshot(t, state, fixture)

	assertModelRendersEvents(t, state)
}

func assertApplyRefreshesSnapshot(t *testing.T, state *model, fixture *operationsFixture) {
	t.Helper()

	_, refresh := state.Update(state.startApply()())
	if refresh == nil || state.status != statusRefreshing || !state.busy || state.hasSnapshot || state.hasEvidence {
		t.Fatalf("post-apply refresh state = %#v, command nil = %t", state, refresh == nil)
	}
	state.Update(refresh())
	if state.status != statusReady || !slices.Equal(
		fixture.recordedCalls(),
		[]string{snapshotCall, evidenceCall, "dry-run", "apply", snapshotCall, evidenceCall},
	) {
		t.Fatalf("operation state = %#v, calls = %q", state, fixture.recordedCalls())
	}
}

func assertModelRendersEvents(t *testing.T, state *model) {
	t.Helper()

	for sequence := int64(1); sequence <= maximumDisplayedEvents+1; sequence++ {
		state.Update(eventMsg(application.Event{
			Kind: application.EventActionCompleted, Action: "create", Sequence: sequence,
		}))
	}
	if len(state.recent) != maximumDisplayedEvents || state.recent[0].Sequence != 2 {
		t.Fatalf("recent events = %#v", state.recent)
	}

	for _, size := range []tea.WindowSizeMsg{
		{Width: 120, Height: 40},
		{Width: 60, Height: 20},
		{Width: 160, Height: 50},
	} {
		state.Update(size)
		view := state.View()
		assertBoundedPlainView(t, view.Content, size.Width, size.Height)
		for _, text := range []string{"Service: project/service", "Snapshot:", "Evidence:", "Recent events:"} {
			if !strings.Contains(view.Content, text) {
				t.Fatalf("view at %dx%d is missing %q: %q", size.Width, size.Height, text, view.Content)
			}
		}
	}
}

func TestEscapeCancelsModelOperation(t *testing.T) {
	t.Parallel()

	fixture := newOperationsFixture()
	fixture.blockDryRun = true
	fixture.cancelObserved = make(chan struct{})
	state := newModel(t.Context(), fixture, application.Request{}, NewEventStream())
	command := state.startDryRun()
	result := make(chan tea.Msg, 1)
	go func() { result <- command() }()

	if command := state.handleKey("esc"); command != nil || !state.busy || state.status != "Cancelling" {
		t.Fatalf("escape state = %#v, command nil = %t", state, command == nil)
	}
	if state.startApply() != nil {
		t.Fatal("escape allowed a second operation before cancellation completed")
	}
	<-fixture.cancelObserved
	state.Update(<-result)
	if state.busy || state.status != "Cancelled" || state.err != nil {
		t.Fatalf("cancelled state = %#v", state)
	}
}

func TestQuitWaitsForOperationCancellation(t *testing.T) {
	t.Parallel()

	fixture := newOperationsFixture()
	fixture.blockDryRun = true
	fixture.cancelObserved = make(chan struct{})
	state := newModel(t.Context(), fixture, application.Request{}, NewEventStream())
	operation := state.startDryRun()
	result := make(chan tea.Msg, 1)
	go func() { result <- operation() }()

	if command := state.handleKey("q"); command != nil || !state.busy || state.status != "Cancelling" {
		t.Fatalf("quit state = %#v, command nil = %t", state, command == nil)
	}
	<-fixture.cancelObserved
	_, command := state.Update(<-result)
	if command == nil || state.busy || state.quitAfterOperation {
		t.Fatalf("completed quit state = %#v, command nil = %t", state, command == nil)
	}
}

func TestSuccessfulApplyQuitsWithoutStartingRefresh(t *testing.T) {
	t.Parallel()

	fixture := newOperationsFixture()
	state := newModel(t.Context(), fixture, application.Request{}, NewEventStream())
	apply := state.startApply()
	state.quitAfterOperation = true
	_, quit := state.Update(apply())
	if quit == nil || state.busy || state.status != "Apply completed" || state.hasSnapshot || state.hasEvidence ||
		!slices.Equal(fixture.recordedCalls(), []string{"apply"}) {
		t.Fatalf("completed apply quit state = %#v, calls = %q", state, fixture.recordedCalls())
	}
}

func TestModelRedactsOperationFailure(t *testing.T) {
	t.Parallel()

	fixture := newOperationsFixture()
	fixture.applyErr = errTestSecret
	state := newModel(t.Context(), fixture, application.Request{}, NewEventStream())
	state.Update(state.startApply()())
	view := state.View().Content
	if state.err == nil || state.status != "Operation failed" || strings.Contains(view, errTestSecret.Error()) {
		t.Fatalf("failed state = %#v, view = %q", state, view)
	}
}

func TestModelDoesNotReadEvidenceAfterSnapshotFailure(t *testing.T) {
	t.Parallel()

	fixture := newOperationsFixture()
	fixture.snapshotErr = errTestTUI
	state := newModel(t.Context(), fixture, application.Request{}, NewEventStream())
	state.Update(state.startSnapshot()())
	if !errors.Is(state.err, errTestTUI) || !slices.Equal(fixture.recordedCalls(), []string{snapshotCall}) {
		t.Fatalf("failed snapshot state = %#v, calls = %q", state, fixture.recordedCalls())
	}
}

func TestModelIgnoresStaleResult(t *testing.T) {
	t.Parallel()

	state := newModel(t.Context(), newOperationsFixture(), application.Request{}, NewEventStream())
	state.sequence = 2
	state.Update(snapshotResultMsg{sequence: 1, err: errTestTUI})
	state.Update(dryRunResultMsg{sequence: 1, err: errTestTUI})
	state.Update(applyResultMsg{sequence: 1, err: errTestTUI})
	if state.err != nil || state.status != "Loading" {
		t.Fatalf("stale result changed state = %#v", state)
	}
}

func TestModelReportsCancelledResult(t *testing.T) {
	t.Parallel()

	state := newModel(t.Context(), newOperationsFixture(), application.Request{}, NewEventStream())
	state.Update(snapshotResultMsg{sequence: 0, err: context.Canceled})
	if state.status != "Cancelled" || state.err != nil {
		t.Fatalf("cancelled result state = %#v", state)
	}
}

func TestModelInitializesOneOperation(t *testing.T) {
	t.Parallel()

	state := newModel(t.Context(), newOperationsFixture(), application.Request{}, NewEventStream())
	if state.Init() == nil || !state.busy || state.status != statusRefreshing {
		t.Fatalf("Init() state = %#v", state)
	}
	if state.startApply() != nil {
		t.Fatal("begin() started a second operation")
	}
	state.finishOperation()
	state.Update(tea.WindowSizeMsg{Width: 0, Height: 0})
	if state.width != 1 || state.height != 1 {
		t.Fatalf("window size = %dx%d", state.width, state.height)
	}
}

func TestModelRoutesOperationKeys(t *testing.T) {
	t.Parallel()

	state := newModel(t.Context(), newOperationsFixture(), application.Request{}, NewEventStream())

	for _, test := range []struct {
		key    string
		status string
	}{
		{key: "d", status: "Checking"},
		{key: "a", status: "Applying"},
		{key: "r", status: statusRefreshing},
	} {
		command := state.handleKey(test.key)
		if command == nil || state.status != test.status {
			t.Fatalf("handleKey(%q) state = %#v", test.key, state)
		}
		state.finishOperation()
	}
}

func TestModelRoutesQuitAndIdleKeys(t *testing.T) {
	t.Parallel()

	state := newModel(t.Context(), newOperationsFixture(), application.Request{}, NewEventStream())
	for _, key := range []string{"q", "ctrl+c"} {
		command := state.handleKey(key)
		if command == nil {
			t.Fatalf("handleKey(%q) did not quit", key)
		}
	}
	state.handleKey("esc")
	state.handleKey("unknown")
	state.requestCancellation()
	state.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
}

func TestModelRoutesMessages(t *testing.T) {
	t.Parallel()

	state := newModel(t.Context(), newOperationsFixture(), application.Request{}, NewEventStream())
	state.Update(tea.KeyPressMsg(tea.Key{Text: "x", Code: 'x'}))
	state.Update(struct{}{})
	_, quit := state.Update(contextDoneMsg{})
	if quit == nil {
		t.Fatal("contextDoneMsg did not quit")
	}
}

func TestEventStreamIsBoundedAndCancellable(t *testing.T) {
	t.Parallel()

	stream := NewEventStream()
	for range eventQueueCapacity {
		if !stream.TryPublish(application.Event{Kind: application.EventPlanPrepared}) {
			t.Fatal("event queue dropped an event before reaching capacity")
		}
	}
	if stream.TryPublish(application.Event{}) || stream.DroppedEvents() != 1 {
		t.Fatalf("full event queue accepted an event; dropped = %d", stream.DroppedEvents())
	}
	if event, valid := stream.wait(t.Context())().(eventMsg); !valid ||
		application.Event(event).Kind != application.EventPlanPrepared {
		t.Fatalf("wait() = %#v", event)
	}

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, valid := NewEventStream().wait(ctx)().(contextDoneMsg); !valid {
		t.Fatal("cancelled wait did not return contextDoneMsg")
	}
	queued := NewEventStream()
	if !queued.TryPublish(application.Event{Kind: application.EventPlanPrepared}) {
		t.Fatal("cancelled wait fixture dropped its event")
	}
	if _, valid := queued.wait(ctx)().(contextDoneMsg); !valid {
		t.Fatal("cancelled wait consumed a queued event")
	}
	if _, valid := waitForContext(ctx)().(contextDoneMsg); !valid {
		t.Fatal("waitForContext() did not return contextDoneMsg")
	}
}

func TestViewDefaultsBoundsAndRedactsTypedContent(t *testing.T) {
	t.Parallel()

	state := newModel(t.Context(), newOperationsFixture(), application.Request{}, NewEventStream())
	view := state.View()
	if !view.AltScreen || !strings.Contains(view.Content, "Status: Loading") {
		t.Fatalf("default view = %#v", view)
	}

	state.handleSnapshotResult(snapshotResultMsg{
		sequence: 0,
		snapshot: application.OperationSnapshot{Plan: testPlan()},
		evidence: application.EvidenceBundle{},
	})
	state.err = errTestEnvSecret
	state.recordEvent(application.Event{
		Kind: application.EventActionCompleted, Action: strings.Repeat("x", 100), Sequence: 1,
	})
	view = state.View()
	assertBoundedPlainView(t, view.Content, defaultWidth, defaultHeight)
	if strings.Contains(view.Content, errTestEnvSecret.Error()) {
		t.Fatalf("view exposed an error: %q", view.Content)
	}

	lines := []string{"abcdef", "second", "third"}
	if got := fitView(lines, 3, 2); got != "abc\nsec" {
		t.Fatalf("fitView() = %q", got)
	}
}

func TestRunValidatesInputContextAndOperationFailures(t *testing.T) {
	t.Parallel()

	if !errors.Is(Run(t.Context(), nil, io.Discard, nil, application.Request{}, nil), errInvalidInput) {
		t.Fatal("Run(nil) accepted invalid input")
	}
	if !errors.Is(
		Run(t.Context(), nil, io.Discard, newOperationsFixture(), application.Request{}, nil),
		errInvalidInput,
	) {
		t.Fatal("Run(nil events) accepted invalid input")
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := Run(
		cancelled, nil, io.Discard, newOperationsFixture(), application.Request{}, NewEventStream(),
	); !errors.Is(err, context.Canceled) {
		t.Fatalf("Run(cancelled) error = %v", err)
	}

	fixture := newOperationsFixture()
	fixture.snapshotErr = errTestTUI
	fixture.evidenceRead = make(chan struct{})
	input := &signalReader{ready: fixture.evidenceRead, content: []byte("q")}
	fixture.snapshotErr = nil
	fixture.evidenceErr = errTestTUI
	if err := Run(
		t.Context(), input, io.Discard, fixture, application.Request{}, NewEventStream(),
	); !errors.Is(err, errTestTUI) {
		t.Fatalf("Run(operation failure) error = %v", err)
	}

	if err := Run(
		t.Context(), failingReader{}, io.Discard, newOperationsFixture(), application.Request{}, NewEventStream(),
	); !errors.Is(err, errTestTUI) {
		t.Fatalf("Run(input failure) error = %v", err)
	}

	success := newOperationsFixture()
	success.evidenceRead = make(chan struct{})
	if err := Run(
		t.Context(),
		&signalReader{ready: success.evidenceRead, content: []byte("q")},
		io.Discard,
		success,
		application.Request{},
		NewEventStream(),
	); err != nil {
		t.Fatalf("Run(success) error = %v", err)
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

type failingReader struct{}

func (failingReader) Read([]byte) (int, error) {
	return 0, errTestTUI
}

func newOperationsFixture() *operationsFixture {
	plan := testPlan()

	return &operationsFixture{
		plan: plan,
		snapshot: application.OperationSnapshot{
			Plan: plan, HasTransaction: true, DroppedEvents: 2,
			Actions: []application.SnapshotAction{{Sequence: 1, Kind: "create", State: "completed"}},
		},
		evidence: application.EvidenceBundle{
			Version: application.EvidenceBundleVersion,
			Project: "project", Service: "service",
			Items: []application.EvidenceItem{{ID: "plan.desired", Kind: application.EvidencePlanDesired}},
		},
	}
}

func testPlan() application.Plan {
	return application.Plan{
		Kind: application.PlanBootstrap, Project: "project", Service: "service",
		Runtime:  domain.RuntimeDocker,
		Platform: domain.Platform{OS: "linux", Architecture: "amd64"},
	}
}

func assertBoundedPlainView(t *testing.T, content string, width, height int) {
	t.Helper()

	if strings.Contains(content, "\x1b") {
		t.Fatalf("view contains ANSI: %q", content)
	}
	lines := strings.Split(content, "\n")
	if len(lines) > height {
		t.Fatalf("view has %d lines, want at most %d", len(lines), height)
	}
	for _, line := range lines {
		if len([]rune(line)) > width {
			t.Fatalf("view line has %d characters, want at most %d: %q", len([]rune(line)), width, line)
		}
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
}
