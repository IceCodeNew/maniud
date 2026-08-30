package cli

import (
	"context"
	"errors"
	"io"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
	"github.com/IceCodeNew/maniud/internal/tui"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

var (
	errTUIInputUnavailable  = errors.New("terminal input is unavailable")
	errTUIOutputUnavailable = errors.New("terminal output is unavailable")
	errTUITermUnavailable   = errors.New("TERM is not interactive")
)

func executeTUI(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog tui.Catalog,
	dependencies applyDependencies,
	events *tui.EventStream,
	options tui.Options,
) error {
	if catalog == nil || dependencies.operations == nil || events == nil {
		return errInvalidArguments
	}

	return errors.Join(tui.Run(ctx, input, output, catalog, dependencies.operations, events, options))
}

func executeProductionTUI(
	ctx context.Context,
	input io.Reader,
	output, stderr io.Writer,
	environment map[string]string,
	getWorkingDirectory func() (string, error),
	runtimes runtimeplugin.Set,
	notifications *processNotifications,
) error {
	if err := requireInteractiveTerminal(input, output, environment["TERM"], tui.IsTerminal); err != nil {
		return err
	}

	// The process-scoped dispatcher has an explicit bounded shutdown and is shared with apply and daemon.
	//nolint:contextcheck // The command context must not terminate queued notification delivery before shutdown.
	notificationEvents, err := notifications.Open(environment, stderr)
	if err != nil {
		return err
	}
	tuiEvents := tui.NewEventStream()
	events := combinedEventSink{first: notificationEvents, second: tuiEvents}
	dependencies, err := defaultApplyDependencies(environment, stderr, getWorkingDirectory, events, runtimes)
	if err != nil {
		return err
	}
	catalog := defaultTUICatalog(environment, dependencies.loadSource)

	return errors.Join(runtimes.Classify(executeTUI(
		ctx, input, output, catalog, dependencies, tuiEvents, tuiOptions(environment),
	)))
}

type fileDescriptor interface {
	Fd() uintptr
}

func requireInteractiveTerminal(
	input io.Reader,
	output io.Writer,
	termName string,
	isTerminal func(uintptr) bool,
) error {
	inputDescriptor, inputValid := input.(fileDescriptor)
	if !inputValid || isTerminal == nil || !isTerminal(inputDescriptor.Fd()) {
		return errTUIInputUnavailable
	}
	outputDescriptor, outputValid := output.(fileDescriptor)
	if !outputValid || !isTerminal(outputDescriptor.Fd()) {
		return errTUIOutputUnavailable
	}
	if strings.EqualFold(strings.TrimSpace(termName), "dumb") {
		return errTUITermUnavailable
	}

	return nil
}

func tuiOptions(environment map[string]string) tui.Options {
	_, noColor := environment["NO_COLOR"]

	return tui.Options{
		Color:   !noColor,
		Unicode: unicodeTerminal(environment),
	}
}

func unicodeTerminal(environment map[string]string) bool {
	if environment["MANIUD_TUI_ASCII"] == "1" {
		return false
	}
	locale := environment["LC_ALL"]
	if locale == "" {
		locale = environment["LC_CTYPE"]
	}
	if locale == "" {
		locale = environment["LANG"]
	}
	normalized := strings.ToUpper(locale)
	if !strings.Contains(normalized, "UTF-8") && !strings.Contains(normalized, "UTF8") {
		return false
	}
	for _, symbol := range []string{"◆", "✓", "›", "│", "─", "…", "▌"} {
		if terminaltext.Width(symbol) != 1 {
			return false
		}
	}

	return true
}

func classifyTUIFailure(err error) *domain.FailureError {
	switch {
	case errors.Is(err, errTUIInputUnavailable):
		return domain.TUIInputUnavailable()
	case errors.Is(err, errTUIOutputUnavailable):
		return domain.TUIOutputUnavailable()
	case errors.Is(err, errTUITermUnavailable):
		return domain.TUITermUnavailable()
	default:
		return classifyApplyFailure(err)
	}
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
