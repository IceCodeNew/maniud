package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type terminalFixture struct {
	io.Reader
	io.Writer

	descriptor uintptr
}

func (fixture terminalFixture) Fd() uintptr {
	return fixture.descriptor
}

func TestRequireInteractiveTerminal(t *testing.T) {
	t.Parallel()

	input := terminalFixture{Reader: bytes.NewReader(nil), descriptor: 1}
	output := terminalFixture{Writer: io.Discard, descriptor: 2}
	terminal := func(descriptor uintptr) bool { return descriptor == 1 || descriptor == 2 }

	tests := []struct {
		name      string
		input     io.Reader
		output    io.Writer
		term      string
		terminal  func(uintptr) bool
		wantError error
	}{
		{name: "available", input: input, output: output, term: "xterm-256color", terminal: terminal},
		{name: "input has no descriptor", input: bytes.NewReader(nil), output: output, terminal: terminal,
			wantError: errTUIInputUnavailable},
		{name: "input is not terminal", input: input, output: output, terminal: func(uintptr) bool { return false },
			wantError: errTUIInputUnavailable},
		{name: "probe missing", input: input, output: output, terminal: nil, wantError: errTUIInputUnavailable},
		{name: "output has no descriptor", input: input, output: io.Discard, terminal: terminal,
			wantError: errTUIOutputUnavailable},
		{name: "output is not terminal", input: input, output: output,
			terminal: func(descriptor uintptr) bool { return descriptor == 1 }, wantError: errTUIOutputUnavailable},
		{name: "dumb terminal", input: input, output: output, term: " DUMB ", terminal: terminal,
			wantError: errTUITermUnavailable},
	}
	for _, test := range tests {
		err := requireInteractiveTerminal(test.input, test.output, test.term, test.terminal)
		if !errors.Is(err, test.wantError) {
			t.Fatalf("requireInteractiveTerminal(%s) error = %v, want %v", test.name, err, test.wantError)
		}
	}
}

func TestTUIOptionsHonorTerminalEnvironment(t *testing.T) {
	t.Parallel()
	const languageEnvironment = "LANG"
	const utf8Locale = "en_US.UTF-8"

	tests := []struct {
		environment map[string]string
		want        tui.Options
	}{
		{environment: map[string]string{languageEnvironment: utf8Locale}, want: tui.Options{Color: true, Unicode: true}},
		{environment: map[string]string{"LC_CTYPE": "C.UTF8", "NO_COLOR": ""}, want: tui.Options{Unicode: true}},
		{environment: map[string]string{"LC_ALL": "C", languageEnvironment: utf8Locale}, want: tui.Options{Color: true}},
		{environment: map[string]string{languageEnvironment: utf8Locale, "MANIUD_TUI_ASCII": "1"},
			want: tui.Options{Color: true}},
		{environment: nil, want: tui.Options{Color: true}},
	}
	for _, test := range tests {
		if got := tuiOptions(test.environment); got != test.want {
			t.Fatalf("tuiOptions(%v) = %#v, want %#v", test.environment, got, test.want)
		}
	}
}

func TestClassifyTUIFailure(t *testing.T) {
	t.Parallel()

	tests := []struct {
		err  error
		code domain.ErrorCode
	}{
		{err: errTUIInputUnavailable, code: domain.ErrorTUIUnavailable},
		{err: errTUIOutputUnavailable, code: domain.ErrorTUIUnavailable},
		{err: errTUITermUnavailable, code: domain.ErrorTUIUnavailable},
		{err: errApplyTest, code: domain.ErrorApplyFailed},
	}
	for _, test := range tests {
		if got := classifyTUIFailure(test.err).Code(); got != test.code {
			t.Fatalf("classifyTUIFailure(%v) = %q, want %q", test.err, got, test.code)
		}
	}
}

type tuiCatalogFixture struct {
	snapshot tui.CatalogSnapshot
	ready    chan struct{}
}

func (fixture *tuiCatalogFixture) Snapshot(context.Context) tui.CatalogSnapshot {
	if fixture.ready != nil {
		close(fixture.ready)
		fixture.ready = nil
	}

	return fixture.snapshot
}

func (*tuiCatalogFixture) OpenRegistered(context.Context, string) tui.OpenResult {
	return tui.OpenResult{Blocker: tui.BlockerNotFound}
}

func (*tuiCatalogFixture) OpenPath(context.Context, string) tui.OpenResult {
	return tui.OpenResult{Blocker: tui.BlockerNotFound}
}

type tuiOperationsFixture struct{}

func (*tuiOperationsFixture) DryRun(
	context.Context,
	application.Request,
) (application.Plan, error) {
	return application.Plan{}, nil
}

func (*tuiOperationsFixture) Apply(
	context.Context,
	application.Request,
) (application.Plan, error) {
	return application.Plan{}, nil
}

func (*tuiOperationsFixture) Snapshot(
	context.Context,
	application.Request,
) (application.OperationSnapshot, error) {
	return application.OperationSnapshot{}, nil
}

func (*tuiOperationsFixture) Evidence(application.OperationSnapshot) (application.EvidenceBundle, error) {
	return application.EvidenceBundle{Version: application.EvidenceBundleVersion}, nil
}

func TestExecuteTUIRunsCatalogHome(t *testing.T) {
	t.Parallel()

	ready := make(chan struct{})
	catalog := &tuiCatalogFixture{
		snapshot: tui.CatalogSnapshot{State: tui.CatalogMissing},
		ready:    ready,
	}
	input := &tuiSignalReader{ready: ready, content: []byte("q")}
	dependencies := applyDependencies{operations: &tuiOperationsFixture{}}
	if err := executeTUI(
		t.Context(), input, io.Discard, catalog, dependencies, tui.NewEventStream(), tui.Options{},
	); err != nil {
		t.Fatalf("executeTUI() error = %v", err)
	}
}

func TestExecuteTUIRejectsInvalidDependencies(t *testing.T) {
	t.Parallel()

	validCatalog := &tuiCatalogFixture{}
	validDependencies := applyDependencies{operations: &tuiOperationsFixture{}}
	events := tui.NewEventStream()
	tests := []struct {
		catalog      tui.Catalog
		dependencies applyDependencies
		events       *tui.EventStream
	}{
		{catalog: nil, dependencies: validDependencies, events: events},
		{catalog: validCatalog, dependencies: applyDependencies{}, events: events},
		{catalog: validCatalog, dependencies: validDependencies, events: nil},
	}
	for _, test := range tests {
		if err := executeTUI(
			t.Context(), nil, io.Discard, test.catalog, test.dependencies, test.events, tui.Options{},
		); !errors.Is(err, errInvalidArguments) {
			t.Fatalf("executeTUI(invalid) error = %v", err)
		}
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
