package tui

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	statusReady         = "Ready for confirmation"
	statusRefreshing    = "Refreshing review"
	keyEscape           = "esc"
	keyEnter            = "enter"
	keyDown             = "down"
	keyTab              = "tab"
	keyQuit             = "q"
	maximumDisplayBytes = 64 << 10
	maximumDisplayRunes = 16 << 10
	maximumDisplayCells = 16 << 10
)

func displayLimits() terminaltext.Limits {
	return terminaltext.Limits{
		Bytes:     maximumDisplayBytes,
		Runes:     maximumDisplayRunes,
		Lines:     1,
		LineCells: maximumDisplayCells,
	}
}

type catalogResultMsg struct {
	sequence uint64
	snapshot CatalogSnapshot
}

type openResultMsg struct {
	sequence uint64
	result   OpenResult
}

type snapshotResultMsg struct {
	sequence uint64
	snapshot application.OperationSnapshot
	evidence application.EvidenceBundle
	err      error
}

type applyResultMsg struct {
	sequence uint64
	err      error
}

type eventMsg application.Event

type contextDoneMsg struct{}

type page interface {
	isPage()
}

type homePage struct {
	catalog CatalogSnapshot
	cursor  int
}

func (homePage) isPage() {}

type openPathPage struct {
	value string
}

func (openPathPage) isPage() {}

type serviceChoice struct {
	project string
	service string
	runtime string
	request application.Request
}

type selectServicePage struct {
	choices []serviceChoice
	cursor  int
}

func (selectServicePage) isPage() {}

type planView struct {
	kind        string
	project     string
	service     string
	runtime     string
	platform    string
	current     string
	proposed    string
	status      string
	warningText string
}

type reviewPage struct {
	request application.Request
	plan    planView
}

func (reviewPage) isPage() {}

type detailsPage struct {
	review reviewPage
	scroll int
}

func (detailsPage) isPage() {}

type confirmationFocus uint8

const (
	confirmationBack confirmationFocus = iota
	confirmationApply
)

type confirmationPage struct {
	review reviewPage
	focus  confirmationFocus
}

func (confirmationPage) isPage() {}

type model struct {
	ctx        context.Context //nolint:containedctx // The model and context share one Bubble Tea session lifetime.
	catalog    Catalog
	operations Operations
	events     *EventStream
	options    Options

	width              int
	height             int
	page               page
	activeRequest      application.Request
	status             string
	mutationOutcome    string
	busy               bool
	sequence           uint64
	cancel             context.CancelFunc
	err                error
	quitAfterOperation bool
}

func newModel(
	ctx context.Context,
	catalog Catalog,
	operations Operations,
	events *EventStream,
	options Options,
) *model {
	return &model{
		ctx: ctx, catalog: catalog, operations: operations, events: events, options: options,
		page: homePage{catalog: CatalogSnapshot{State: CatalogMissing}}, status: "Loading services",
	}
}

func (state *model) Init() tea.Cmd {
	return tea.Batch(
		state.startCatalog(),
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
		state.resize(message.Width, message.Height)
	case tea.KeyPressMsg:
		return state, state.handleKey(message)
	case catalogResultMsg:
		return state, state.handleCatalogResult(message)
	case openResultMsg:
		return state, state.handleOpenResult(message)
	case snapshotResultMsg:
		return state, state.handleSnapshotResult(message)
	case applyResultMsg:
		return state, state.handleApplyResult(message)
	case eventMsg:
		return state, state.events.wait(state.ctx)
	case contextDoneMsg:
		return state, state.requestQuit()
	}

	return state, nil
}

func (state *model) resize(width, height int) {
	previous := layoutFor(state.width, state.height)
	state.width = max(width, 1)
	state.height = max(height, 1)
	current := layoutFor(state.width, state.height)
	if previous >= layoutCompact && current < layoutCompact {
		if confirmation, valid := state.page.(confirmationPage); valid {
			state.page = confirmation.review
			state.status = "Review again at a larger terminal"
		}
	}
}

func (state *model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.String()
	if command, handled := state.handleSessionKey(key); handled {
		return command
	}

	switch current := state.page.(type) {
	case homePage:
		return state.handleHomeKey(current, key)
	case openPathPage:
		return state.handleOpenPathKey(current, message)
	case selectServicePage:
		return state.handleServiceChoiceKey(current, key)
	case reviewPage:
		return state.handleReviewKey(current, key)
	case detailsPage:
		return state.handleDetailsKey(current, key)
	case confirmationPage:
		return state.handleConfirmationKey(current, key)
	default:
		return nil
	}
}

func (state *model) handleSessionKey(key string) (tea.Cmd, bool) {
	if key == "ctrl+c" {
		return state.requestQuit(), true
	}
	if !state.busy {
		return nil, false
	}

	switch key {
	case keyEscape:
		state.requestCancellation()
		state.status = "Cancelling"
	case keyQuit:
		return state.requestQuit(), true
	}

	return nil, true
}

func (state *model) handleHomeKey(current homePage, key string) tea.Cmd {
	items := len(current.catalog.Services) + 1
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + items) % items
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % items
	case keyEnter:
		if current.cursor == len(current.catalog.Services) {
			state.page = openPathPage{}
			state.status = "Enter a committed Compose path"

			return nil
		}
		service := current.catalog.Services[current.cursor]
		if service.Blocker != BlockerNone {
			state.status = blockerMessage(service.Blocker)

			return nil
		}

		return state.startOpenRegistered(service.ID)
	case "r":
		return state.startCatalog()
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleOpenPathKey(current openPathPage, message tea.KeyPressMsg) tea.Cmd {
	key := message.String()
	switch key {
	case keyEnter:
		if current.value == "" {
			state.status = "Enter a Compose path"

			return nil
		}

		return state.startOpenPath(current.value)
	case "backspace":
		characters := []rune(current.value)
		if len(characters) > 0 {
			current.value = string(characters[:len(characters)-1])
		}
	case keyEscape:
		return state.startCatalog()
	default:
		text := message.Key().Text
		if printableSingleLine(text) {
			candidate := current.value + text
			if _, err := terminaltext.Canonicalize(candidate, displayLimits()); err == nil {
				current.value = candidate
			}
		}
	}
	state.page = current

	return nil
}

func printableSingleLine(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, character := range value {
		if !unicode.IsPrint(character) {
			return false
		}
	}

	return true
}

func (state *model) handleServiceChoiceKey(current selectServicePage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + len(current.choices)) % len(current.choices)
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % len(current.choices)
	case keyEnter:
		return state.startSnapshot(current.choices[current.cursor].request)
	case keyEscape:
		state.page = openPathPage{}
		state.status = "Enter a committed Compose path"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleReviewKey(current reviewPage, key string) tea.Cmd {
	switch key {
	case keyEnter:
		if layoutFor(state.width, state.height) < layoutCompact {
			state.status = "Resize to continue to confirmation"

			return nil
		}
		state.page = confirmationPage{review: current, focus: confirmationBack}
		state.status = "Confirm or go back"
	case "d":
		state.page = detailsPage{review: current}
		state.status = "Full image references"
	case "r":
		return state.startSnapshot(current.request)
	case keyEscape:
		return state.startCatalog()
	case keyQuit:
		return tea.Quit
	}

	return nil
}

func (state *model) handleDetailsKey(current detailsPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d", keyEscape:
		state.page = current.review
		state.status = current.review.plan.status

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleConfirmationKey(current confirmationPage, key string) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.review
		state.status = "Review again at a larger terminal"

		return nil
	}

	switch key {
	case "tab", "left", "right", "shift+tab":
		if current.focus == confirmationBack {
			current.focus = confirmationApply
		} else {
			current.focus = confirmationBack
		}
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.review
			state.status = current.review.plan.status

			return nil
		}

		return state.startApply(current.review)
	case keyEscape:
		state.page = current.review
		state.status = current.review.plan.status

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) begin(status string, command func(context.Context, uint64) tea.Cmd) tea.Cmd {
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

func (state *model) startCatalog() tea.Cmd {
	return state.begin("Loading services", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			return catalogResultMsg{sequence: sequence, snapshot: catalog.Snapshot(ctx)}
		}
	})
}

func (state *model) startOpenRegistered(id string) tea.Cmd {
	return state.begin("Opening service", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			return openResultMsg{sequence: sequence, result: catalog.OpenRegistered(ctx, id)}
		}
	})
}

func (state *model) startOpenPath(path string) tea.Cmd {
	return state.begin("Opening Compose file", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			return openResultMsg{sequence: sequence, result: catalog.OpenPath(ctx, path)}
		}
	})
}

func (state *model) startSnapshot(request application.Request) tea.Cmd {
	state.activeRequest = request

	return state.begin(statusRefreshing, func(ctx context.Context, sequence uint64) tea.Cmd {
		operations := state.operations

		return func() tea.Msg {
			result := snapshotResultMsg{sequence: sequence}
			result.snapshot, result.err = operations.Snapshot(ctx, request)
			if result.err == nil {
				result.evidence, result.err = operations.Evidence(result.snapshot)
			}

			return result
		}
	})
}

func (state *model) startApply(review reviewPage) tea.Cmd {
	state.page = review

	return state.begin("Applying change", func(ctx context.Context, sequence uint64) tea.Cmd {
		operations := state.operations

		return func() tea.Msg {
			_, err := operations.Apply(ctx, review.request)

			return applyResultMsg{sequence: sequence, err: err}
		}
	})
}

func (state *model) handleCatalogResult(result catalogResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, nil)
	if !accepted {
		return command
	}

	snapshot := canonicalCatalog(result.snapshot)
	state.page = homePage{catalog: snapshot}
	state.status = catalogMessage(snapshot)

	return command
}

func (state *model) handleOpenResult(result openResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, nil)
	if !accepted {
		return command
	}
	if result.result.Blocker != BlockerNone {
		state.status = blockerMessage(result.result.Blocker)

		return command
	}

	choices, err := canonicalChoices(result.result.Targets)
	if err != nil || len(choices) == 0 {
		state.err = errors.Join(errInvalidInput, err)
		state.status = "Compose source could not be displayed safely"

		return command
	}
	if len(choices) > 1 {
		state.page = selectServicePage{choices: choices}
		state.status = "Choose a service"

		return command
	}

	return tea.Batch(command, state.startSnapshot(choices[0].request))
}

func (state *model) handleSnapshotResult(result snapshotResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	if result.evidence.Version != application.EvidenceBundleVersion {
		state.err = errInvalidInput
		state.status = "Review evidence is unavailable"

		return command
	}

	view, err := projectPlan(result.snapshot)
	if err != nil {
		state.err = errors.Join(errInvalidInput, err)
		state.status = "Review content could not be displayed safely"

		return command
	}
	state.page = reviewPage{request: state.activeRequest, plan: view}
	state.status = view.status

	return command
}

func (state *model) handleApplyResult(result applyResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}

	review, valid := state.page.(reviewPage)
	if !valid {
		state.err = errInvalidInput
		state.status = "Apply result could not be refreshed"

		return command
	}
	state.mutationOutcome = "Apply completed"
	if command != nil {
		state.status = state.mutationOutcome

		return command
	}

	return state.startSnapshot(review.request)
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

func canonicalCatalog(snapshot CatalogSnapshot) CatalogSnapshot {
	services := make([]Service, 0, len(snapshot.Services))
	for _, service := range snapshot.Services {
		location, locationErr := canonicalDisplay(service.Location)
		project, projectErr := canonicalDisplay(service.Project)
		name, nameErr := canonicalDisplay(service.Name)
		runtimeName, runtimeErr := canonicalDisplay(service.Runtime)
		if locationErr != nil || projectErr != nil || nameErr != nil || runtimeErr != nil {
			services = append(services, Service{ID: service.ID, Location: "Invalid source", Blocker: BlockerInvalid})

			continue
		}
		services = append(services, Service{
			ID: service.ID, Location: location, Project: project, Name: name,
			Runtime: runtimeName, Blocker: service.Blocker,
		})
	}
	snapshot.Services = services

	return snapshot
}

func canonicalChoices(targets []Target) ([]serviceChoice, error) {
	choices := make([]serviceChoice, 0, len(targets))
	for _, target := range targets {
		project, err := canonicalDisplay(target.Project)
		if err != nil {
			return nil, err
		}
		service, err := canonicalDisplay(target.Service)
		if err != nil {
			return nil, err
		}
		runtimeName, err := canonicalDisplay(target.Runtime)
		if err != nil {
			return nil, err
		}
		choices = append(choices, serviceChoice{
			project: project, service: service, runtime: runtimeName, request: target.Request,
		})
	}

	return choices, nil
}

func projectPlan(snapshot application.OperationSnapshot) (planView, error) {
	plan := snapshot.Plan
	current := "Not deployed"
	if snapshot.HasApplied {
		current = snapshot.Applied.Reference
	}
	raw := []string{
		plan.Project,
		plan.Service,
		plan.Runtime.String(),
		platformName(plan),
		current,
		plan.Image.Reference,
		string(plan.Kind),
	}
	values := make([]string, len(raw))
	for index, value := range raw {
		canonical, err := canonicalDisplay(value)
		if err != nil {
			return planView{}, err
		}
		values[index] = canonical
	}

	view := planView{
		kind: values[6], project: values[0], service: values[1], runtime: values[2],
		platform: values[3], current: values[4], proposed: values[5],
		status: statusReady,
	}
	if plan.Kind == application.PlanUnchanged {
		view.status = "No runtime change needed"
	}
	if len(plan.Warnings) > 0 {
		view.warningText = fmt.Sprintf("%d warning(s) require review", len(plan.Warnings))
	}

	return view, nil
}

func platformName(plan application.Plan) string {
	value := plan.Platform.OS + "/" + plan.Platform.Architecture
	if plan.Platform.Variant != "" {
		value += "/" + plan.Platform.Variant
	}

	return value
}

func canonicalDisplay(value string) (string, error) {
	canonical, err := terminaltext.Canonicalize(value, displayLimits())
	if err != nil {
		return "", fmt.Errorf("canonicalize display text: %w", err)
	}

	return canonical, nil
}

func catalogMessage(snapshot CatalogSnapshot) string {
	switch snapshot.State {
	case CatalogReady:
		if len(snapshot.Services) == 0 {
			return "No registered services"
		}

		return "Choose a service"
	case CatalogMissing:
		return "No registered repository"
	case CatalogUnavailable:
		return "Registered services unavailable"
	default:
		return "Registered services unavailable"
	}
}

func blockerMessage(blocker SourceBlocker) string {
	switch blocker {
	case BlockerNone, BlockerInvalid:
		return "Compose source did not pass validation"
	case BlockerNotFound:
		return "Compose source is no longer registered"
	case BlockerUnavailable:
		return "Compose source is unavailable"
	default:
		return "Compose source did not pass validation"
	}
}
