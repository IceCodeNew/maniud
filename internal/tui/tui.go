// Package tui presents the application façade without opening runtime or
// journal resources itself.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

const eventQueueCapacity = 64

var errInvalidInput = errors.New("TUI input is invalid")

// Operations is the complete application boundary consumed by the TUI.
type Operations interface {
	DryRun(ctx context.Context, request application.Request) (application.Plan, error)
	Apply(ctx context.Context, request application.Request) (application.Plan, error)
	Snapshot(ctx context.Context, request application.Request) (application.OperationSnapshot, error)
	Evidence(snapshot application.OperationSnapshot) (application.EvidenceBundle, error)
}

var _ Operations = (*application.ApplyFacade)(nil)

// EventStream accepts bounded application events and exposes dropped-event
// counts to the application snapshot.
type EventStream struct {
	queue   chan application.Event
	dropped atomic.Uint64
}

// NewEventStream creates one process-local event stream for a TUI session.
func NewEventStream() *EventStream {
	return &EventStream{queue: make(chan application.Event, eventQueueCapacity)}
}

// TryPublish implements application.EventSink without blocking an operation.
func (stream *EventStream) TryPublish(event application.Event) bool {
	select {
	case stream.queue <- event:
		return true
	default:
		stream.dropped.Add(1)

		return false
	}
}

// DroppedEvents implements application.EventDropCounter.
func (stream *EventStream) DroppedEvents() uint64 {
	return stream.dropped.Load()
}

func (stream *EventStream) wait(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		select {
		case event := <-stream.queue:
			return eventMsg(event)
		case <-ctx.Done():
			return contextDoneMsg{}
		}
	}
}

// Run opens one Bubble Tea session over an injected application façade.
func Run(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	operations Operations,
	request application.Request,
	events *EventStream,
) error {
	if operations == nil || events == nil {
		return errInvalidInput
	}

	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := newModel(runCtx, operations, request, events)
	_, err := tea.NewProgram(
		state,
		tea.WithInput(input),
		tea.WithOutput(output),
	).Run()
	if err != nil {
		return fmt.Errorf("run TUI: %w", err)
	}
	if state.err != nil {
		return fmt.Errorf("run TUI operation: %w", state.err)
	}
	if err = ctx.Err(); err != nil {
		return fmt.Errorf("run TUI context: %w", err)
	}

	return nil
}
