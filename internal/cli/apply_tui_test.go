package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"slices"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type interactiveOperationsFixture struct {
	calls        []string
	snapshot     application.OperationSnapshot
	evidence     application.EvidenceBundle
	request      application.Request
	evidenceRead chan struct{}
}

func (fixture *interactiveOperationsFixture) DryRun(
	context.Context,
	application.Request,
) (application.Plan, error) {
	fixture.calls = append(fixture.calls, "dry-run")

	return fixture.snapshot.Plan, nil
}

func (fixture *interactiveOperationsFixture) Apply(
	context.Context,
	application.Request,
) (application.Plan, error) {
	fixture.calls = append(fixture.calls, "apply")

	return fixture.snapshot.Plan, nil
}

func (fixture *interactiveOperationsFixture) Snapshot(
	_ context.Context,
	request application.Request,
) (application.OperationSnapshot, error) {
	fixture.calls = append(fixture.calls, "snapshot")
	fixture.request = request

	return fixture.snapshot, nil
}

func (fixture *interactiveOperationsFixture) Evidence(
	application.OperationSnapshot,
) (application.EvidenceBundle, error) {
	fixture.calls = append(fixture.calls, "evidence")
	close(fixture.evidenceRead)

	return fixture.evidence, nil
}

func TestExecuteTUIUsesLoadedRequestAndInteractiveFacade(t *testing.T) {
	t.Parallel()

	fixture := &interactiveOperationsFixture{
		snapshot: application.OperationSnapshot{Plan: application.Plan{
			Kind: application.PlanUnchanged, Project: testProjectName, Service: testServiceName,
			Runtime: domain.RuntimeDocker,
		}},
		evidence:     application.EvidenceBundle{Version: application.EvidenceBundleVersion},
		evidenceRead: make(chan struct{}),
	}
	events := tui.NewEventStream()
	source := compose.Source{Content: []byte("services: {}")}
	dependencies := applyDependencies{
		loadSource: func(context.Context, string) (compose.Source, error) { return source, nil },
		operations: fixture,
	}
	input := &tuiSignalReader{ready: fixture.evidenceRead, content: []byte("q")}

	err := executeTUI(
		t.Context(),
		applyInvocation{compose: composeFileValue, service: applyServiceValue, tui: true},
		input,
		io.Discard,
		dependencies,
		events,
	)
	if err != nil {
		t.Fatalf("executeTUI() error = %v", err)
	}
	if !slices.Equal(fixture.calls, []string{"snapshot", "evidence"}) {
		t.Fatalf("facade calls = %q", fixture.calls)
	}
	if !bytes.Equal(fixture.request.Source.Content, source.Content) || fixture.request.Service != applyServiceValue {
		t.Fatalf("facade request = %#v", fixture.request)
	}
}

func TestExecuteTUIRejectsInvalidDependenciesAndSourceFailure(t *testing.T) {
	t.Parallel()

	arguments := applyInvocation{compose: composeFileValue, tui: true}
	if err := executeTUI(t.Context(), arguments, nil, io.Discard, applyDependencies{}, nil); !errors.Is(
		err,
		errInvalidArguments,
	) {
		t.Fatalf("executeTUI(invalid) error = %v", err)
	}

	fixture := &interactiveOperationsFixture{evidenceRead: make(chan struct{})}
	dependencies := applyDependencies{
		loadSource: func(context.Context, string) (compose.Source, error) {
			return compose.Source{}, errApplyTest
		},
		operations: fixture,
	}
	if err := executeTUI(
		t.Context(), arguments, nil, io.Discard, dependencies, tui.NewEventStream(),
	); !errors.Is(err, errApplyTest) {
		t.Fatalf("executeTUI(source failure) error = %v", err)
	}
}

func TestCombinedEventSinkPublishesAndCountsDrops(t *testing.T) {
	t.Parallel()

	first := &eventSinkFixture{accepted: false, dropped: 2}
	second := &eventSinkFixture{accepted: true, dropped: 3}
	sink := combinedEventSink{first: first, second: second}
	event := application.Event{Kind: application.EventPlanPrepared}
	accepted := sink.TryPublish(event)
	if !accepted || first.events != 1 || second.events != 1 {
		t.Fatalf("TryPublish() = %t, calls = %d/%d", accepted, first.events, second.events)
	}
	if got := sink.DroppedEvents(); got != 5 {
		t.Fatalf("DroppedEvents() = %d", got)
	}
	if (combinedEventSink{}).TryPublish(event) || (combinedEventSink{}).DroppedEvents() != 0 {
		t.Fatal("empty combined sink accepted or counted an event")
	}
	if got := droppedEventCount(eventSinkWithoutCounter{}); got != 0 {
		t.Fatalf("droppedEventCount(non-counter) = %d", got)
	}
}

type eventSinkFixture struct {
	accepted bool
	dropped  uint64
	events   int
}

func (sink *eventSinkFixture) TryPublish(application.Event) bool {
	sink.events++

	return sink.accepted
}

func (sink *eventSinkFixture) DroppedEvents() uint64 {
	return sink.dropped
}

type eventSinkWithoutCounter struct{}

func (eventSinkWithoutCounter) TryPublish(application.Event) bool {
	return false
}

type tuiSignalReader struct {
	ready   <-chan struct{}
	content []byte
}

func (reader *tuiSignalReader) Read(destination []byte) (int, error) {
	<-reader.ready
	if len(reader.content) == 0 {
		return 0, io.EOF
	}

	count := copy(destination, reader.content)
	reader.content = reader.content[count:]

	return count, nil
}

func TestTUISignalReaderDrainsContent(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	close(ready)
	reader := &tuiSignalReader{ready: ready, content: []byte("q")}
	buffer := make([]byte, 1)
	if count, err := reader.Read(buffer); count != 1 || err != nil || !bytes.Equal(buffer, []byte("q")) {
		t.Fatalf("first Read() = %d, %v, %q", count, err, buffer)
	}
	if count, err := reader.Read(buffer); count != 0 || !errors.Is(err, io.EOF) {
		t.Fatalf("second Read() = %d, %v", count, err)
	}
}
