package cli

import (
	"context"
	"errors"
	"io"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/tui"
)

func executeTUI(
	ctx context.Context,
	arguments applyInvocation,
	input io.Reader,
	output io.Writer,
	dependencies applyDependencies,
	events *tui.EventStream,
) error {
	if dependencies.operations == nil || events == nil {
		return errInvalidArguments
	}

	request, err := loadApplyRequest(ctx, arguments, dependencies)
	if err != nil {
		return err
	}

	return errors.Join(tui.Run(ctx, input, output, dependencies.operations, request, events))
}

type combinedEventSink struct {
	first  application.EventSink
	second application.EventSink
}

func (sink combinedEventSink) TryPublish(event application.Event) bool {
	firstAccepted := sink.first != nil && sink.first.TryPublish(event)
	secondAccepted := sink.second != nil && sink.second.TryPublish(event)

	return firstAccepted || secondAccepted
}

func (sink combinedEventSink) DroppedEvents() uint64 {
	return droppedEventCount(sink.first) + droppedEventCount(sink.second)
}

func droppedEventCount(sink application.EventSink) uint64 {
	counter, valid := sink.(application.EventDropCounter)
	if !valid {
		return 0
	}

	return counter.DroppedEvents()
}
