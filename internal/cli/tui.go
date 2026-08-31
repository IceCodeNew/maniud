package cli

import (
	"context"
	"errors"
	"fmt"
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

type tuiRunner func(
	context.Context,
	io.Reader,
	io.Writer,
	tui.Catalog,
	tui.ServiceWorkspace,
	tui.DeploymentWorkspace,
	tui.Assistant,
	applyDependencies,
	*tui.EventStream,
	tui.Options,
) error

func executeTUI(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog tui.Catalog,
	workspace tui.ServiceWorkspace,
	deployments tui.DeploymentWorkspace,
	dependencies applyDependencies,
	events *tui.EventStream,
	options tui.Options,
) error {
	return executeTUIWithAssistant(
		ctx, input, output, catalog, workspace, deployments, nil, dependencies, events, options,
	)
}

func executeTUIWithAssistant(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog tui.Catalog,
	workspace tui.ServiceWorkspace,
	deployments tui.DeploymentWorkspace,
	assistant tui.Assistant,
	dependencies applyDependencies,
	events *tui.EventStream,
	options tui.Options,
) error {
	if catalog == nil || workspace == nil || deployments == nil || dependencies.operations == nil || events == nil {
		return errInvalidArguments
	}

	return errors.Join(tui.RunWithAssistant(
		ctx, input, output, catalog, workspace, deployments, assistant, dependencies.operations, events, options,
	))
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
	return executeProductionTUIWith(
		ctx, input, output, stderr, environment, getWorkingDirectory, runtimes, notifications,
		tui.IsTerminal, executeTUIWithAssistant,
	)
}

func executeProductionTUIWith(
	ctx context.Context,
	input io.Reader,
	output, stderr io.Writer,
	environment map[string]string,
	getWorkingDirectory func() (string, error),
	runtimes runtimeplugin.Set,
	notifications *processNotifications,
	isTerminal func(uintptr) bool,
	run tuiRunner,
) error {
	if err := requireInteractiveTerminal(input, output, environment["TERM"], isTerminal); err != nil {
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
	workspace := defaultTUIServiceWorkspace(environment, runtimes)
	deployments := defaultTUIDeploymentWorkspace(environment)
	workingDirectory, err := getWorkingDirectory()
	if err != nil {
		return err
	}
	assistant := defaultTUIAssistant(environment, workingDirectory, deployments, dependencies.operations)

	runErr := runtimes.Classify(run(
		ctx, input, output, catalog, workspace, deployments, assistant,
		dependencies, tuiEvents, tuiOptions(environment),
	))
	instructionErr := writeTUIInstructions(output, workspace.Instructions())

	return errors.Join(runErr, instructionErr)
}

func writeTUIInstructions(output io.Writer, instructions []string) error {
	if len(instructions) == 0 {
		return nil
	}
	if _, err := io.WriteString(output, "Next steps:\n"); err != nil {
		return fmt.Errorf("write TUI next steps: %w", err)
	}
	for _, instruction := range instructions {
		if _, err := fmt.Fprintf(output, "$ %s\n", instruction); err != nil {
			return fmt.Errorf("write TUI next step: %w", err)
		}
	}

	return nil
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
		Unicode: unicodeTerminal(environment, terminaltext.Width),
	}
}

func unicodeTerminal(environment map[string]string, width func(string) int) bool {
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
		if width(symbol) != 1 {
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
