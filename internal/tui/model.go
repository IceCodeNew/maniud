package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	maximumDisplayedEvents = 5
	statusReady            = "Ready"
	statusRefreshing       = "Refreshing"
)

type snapshotResultMsg struct {
	sequence uint64
	snapshot application.OperationSnapshot
	evidence application.EvidenceBundle
	err      error
}

type dryRunResultMsg struct {
	sequence uint64
	plan     application.Plan
	err      error
}

type applyResultMsg struct {
	sequence uint64
	plan     application.Plan
	err      error
}

type eventMsg application.Event

type contextDoneMsg struct{}

type model struct {
	ctx        context.Context //nolint:containedctx // The model and context share one Bubble Tea session lifetime.
	operations Operations
	request    application.Request
	events     *EventStream

	width              int
	height             int
	status             string
	busy               bool
	sequence           uint64
	cancel             context.CancelFunc
	err                error
	quitAfterOperation bool

	plan        application.Plan
	hasPlan     bool
	snapshot    application.OperationSnapshot
	hasSnapshot bool
	evidence    application.EvidenceBundle
	hasEvidence bool
	recent      []application.Event
}

func newModel(
	ctx context.Context,
	operations Operations,
	request application.Request,
	events *EventStream,
) *model {
	return &model{
		ctx: ctx, operations: operations, request: request, events: events,
		status: "Loading",
	}
}

func (state *model) Init() tea.Cmd {
	return tea.Batch(
		state.startSnapshot(),
		state.events.wait(state.ctx),
		waitForContext(state.ctx),
	)
}

func waitForContext(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		<-ctx.Done()

		return contextDoneMsg{}
	}
}

func (state *model) Update(message tea.Msg) (tea.Model, tea.Cmd) { //nolint:ireturn // Bubble Tea requires tea.Model.
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		state.width = max(message.Width, 1)
		state.height = max(message.Height, 1)
	case tea.KeyPressMsg:
		return state, state.handleKey(message.String())
	case snapshotResultMsg:
		return state, state.handleSnapshotResult(message)
	case dryRunResultMsg:
		return state, state.handleDryRunResult(message)
	case applyResultMsg:
		return state, state.handleApplyResult(message)
	case eventMsg:
		state.recordEvent(application.Event(message))

		return state, state.events.wait(state.ctx)
	case contextDoneMsg:
		return state, state.requestQuit()
	}

	return state, nil
}

func (state *model) handleKey(key string) tea.Cmd {
	switch key {
	case "ctrl+c", "q":
		return state.requestQuit()
	case "esc":
		if state.busy {
			state.requestCancellation()
			state.status = "Cancelling"
		}
	case "d":
		return state.startDryRun()
	case "a":
		return state.startApply()
	case "r":
		return state.startSnapshot()
	}

	return nil
}

func (state *model) begin(
	status string,
	command func(context.Context, uint64) tea.Cmd,
) tea.Cmd {
	if state.busy {
		return nil
	}

	state.sequence++
	sequence := state.sequence
	operationCtx, cancel := context.WithCancel(state.ctx)
	state.cancel = cancel
	state.busy = true
	state.err = nil
	state.status = status

	return command(operationCtx, sequence)
}

func (state *model) startSnapshot() tea.Cmd {
	return state.begin(statusRefreshing, state.snapshotCommand)
}

func (state *model) startDryRun() tea.Cmd {
	return state.begin("Checking", state.dryRunCommand)
}

func (state *model) startApply() tea.Cmd {
	return state.begin("Applying", state.applyCommand)
}

func (state *model) snapshotCommand(ctx context.Context, sequence uint64) tea.Cmd {
	operations := state.operations
	request := state.request

	return func() tea.Msg {
		result := snapshotResultMsg{sequence: sequence}
		result.snapshot, result.err = operations.Snapshot(ctx, request)
		if result.err == nil {
			result.evidence, result.err = operations.Evidence(result.snapshot)
		}

		return result
	}
}

func (state *model) dryRunCommand(ctx context.Context, sequence uint64) tea.Cmd {
	operations := state.operations
	request := state.request

	return func() tea.Msg {
		plan, err := operations.DryRun(ctx, request)

		return dryRunResultMsg{sequence: sequence, plan: plan, err: err}
	}
}

func (state *model) applyCommand(ctx context.Context, sequence uint64) tea.Cmd {
	operations := state.operations
	request := state.request

	return func() tea.Msg {
		plan, err := operations.Apply(ctx, request)

		return applyResultMsg{sequence: sequence, plan: plan, err: err}
	}
}

func (state *model) handleSnapshotResult(result snapshotResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}

	state.snapshot = result.snapshot
	state.hasSnapshot = true
	state.evidence = result.evidence
	state.hasEvidence = true
	state.plan = result.snapshot.Plan
	state.hasPlan = true
	state.status = statusReady

	return command
}

func (state *model) handleDryRunResult(result dryRunResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}

	state.plan = result.plan
	state.hasPlan = true
	state.status = "Check passed"

	return command
}

func (state *model) handleApplyResult(result applyResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}

	state.plan = result.plan
	state.hasPlan = true
	state.snapshot = application.OperationSnapshot{}
	state.hasSnapshot = false
	state.evidence = application.EvidenceBundle{}
	state.hasEvidence = false
	if command != nil {
		state.status = "Apply completed"

		return command
	}

	return state.startSnapshot()
}

func (state *model) completeOperation(sequence uint64, err error) (bool, tea.Cmd) {
	if sequence != state.sequence {
		return false, nil
	}

	state.finishOperation()
	if errors.Is(err, context.Canceled) {
		state.status = "Cancelled"
	} else if err != nil {
		state.err = err
		state.status = "Operation failed"
	}

	if state.quitAfterOperation {
		state.quitAfterOperation = false

		return true, tea.Quit
	}

	return true, nil
}

func (state *model) requestQuit() tea.Cmd {
	if !state.busy {
		return tea.Quit
	}

	state.quitAfterOperation = true
	state.requestCancellation()
	state.status = "Cancelling"

	return nil
}

func (state *model) requestCancellation() {
	if state.cancel != nil {
		state.cancel()
	}
}

func (state *model) finishOperation() {
	if state.cancel != nil {
		state.cancel()
		state.cancel = nil
	}
	state.busy = false
}

func (state *model) recordEvent(event application.Event) {
	state.recent = append(state.recent, event)
	if len(state.recent) > maximumDisplayedEvents {
		state.recent = state.recent[len(state.recent)-maximumDisplayedEvents:]
	}
}
