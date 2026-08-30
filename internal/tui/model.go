package tui

import (
	"context"
	"errors"
	"fmt"
	"path/filepath"
	"reflect"
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	statusReady               = "Ready for confirmation"
	statusRefreshing          = "Refreshing review"
	statusReviewLarger        = "Review again at a larger terminal"
	keyEscape                 = "esc"
	keyEnter                  = "enter"
	keyDown                   = "down"
	keyTab                    = "tab"
	keyLeft                   = "left"
	keyRight                  = "right"
	keyShiftTab               = "shift+tab"
	keyQuit                   = "q"
	statusReviewStaged        = "Review the staged change"
	statusSignedCommitMissing = "Signed commit was not created"
	maximumDisplayBytes       = 64 << 10
	maximumDisplayRunes       = 16 << 10
	maximumDisplayCells       = 16 << 10
	maximumCommitMessageBytes = 200
	maximumStagedDiffBytes    = 1 << 20
	maximumStagedDiffLines    = 16 << 10
	homeActionCount           = 2
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

type registrationResultMsg struct {
	sequence uint64
	result   RegistrationResult
}

type servicePreviewResultMsg struct {
	sequence uint64
	input    string
	draft    ServiceDraft
	err      error
}

type serviceStageResultMsg struct {
	sequence uint64
	preview  servicePreviewPage
	staged   StagedService
	err      error
}

type serviceCommitResultMsg struct {
	sequence uint64
	commit   commitServicePage
	result   ServiceCommitResult
	err      error
}

type serviceDiscardResultMsg struct {
	sequence uint64
	preview  servicePreviewPage
	err      error
}

type snapshotResultMsg struct {
	sequence uint64
	snapshot application.OperationSnapshot
	evidence application.EvidenceBundle
	dryRun   *application.Plan
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

type sourceDiagnosticPage struct {
	previous   page
	diagnostic SourceDiagnostic
	scroll     int
}

func (sourceDiagnosticPage) isPage() {}

type registrationPage struct {
	value string
}

func (registrationPage) isPage() {}

type registrationConfirmationPage struct {
	registration registrationPage
	focus        confirmationFocus
}

func (registrationConfirmationPage) isPage() {}

type addServicePage struct {
	value string
}

func (addServicePage) isPage() {}

type servicePreviewPage struct {
	input string
	draft ServiceDraft
}

func (servicePreviewPage) isPage() {}

type stageServiceConfirmationPage struct {
	preview servicePreviewPage
	focus   confirmationFocus
}

func (stageServiceConfirmationPage) isPage() {}

type commitServicePage struct {
	preview   servicePreviewPage
	staged    StagedService
	message   string
	focus     confirmationFocus
	editing   bool
	scroll    int
	diffWidth int
	diffLines []string
}

func (commitServicePage) isPage() {}

type stagedDiffPage struct {
	commit commitServicePage
	scroll int
}

func (stagedDiffPage) isPage() {}

type unsignedCommitConfirmationPage struct {
	commit commitServicePage
	focus  confirmationFocus
}

func (unsignedCommitConfirmationPage) isPage() {}

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
	workspace  ServiceWorkspace
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
	applying           bool
	sequence           uint64
	cancel             context.CancelFunc
	err                error
	quitAfterOperation bool
	registrationSeen   bool
}

func newModel(
	ctx context.Context,
	catalog Catalog,
	workspace ServiceWorkspace,
	operations Operations,
	events *EventStream,
	options Options,
) *model {
	return &model{
		ctx: ctx, catalog: catalog, workspace: workspace, operations: operations, events: events, options: options,
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
	if command, handled := state.handleServiceWorkspaceMessage(message); handled {
		return state, command
	}
	if command, handled := state.handleOperationMessage(message); handled {
		return state, command
	}
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		state.resize(message.Width, message.Height)
	case tea.KeyPressMsg:
		return state, state.handleKey(message)
	case catalogResultMsg:
		return state, state.handleCatalogResult(message)
	case openResultMsg:
		return state, state.handleOpenResult(message)
	case registrationResultMsg:
		return state, state.handleRegistrationResult(message)
	case eventMsg:
		return state, state.events.wait(state.ctx)
	case contextDoneMsg:
		return state, state.requestQuit()
	}

	return state, nil
}

func (state *model) handleOperationMessage(message tea.Msg) (tea.Cmd, bool) {
	switch message := message.(type) {
	case snapshotResultMsg:
		return state.handleSnapshotResult(message), true
	case applyResultMsg:
		return state.handleApplyResult(message), true
	default:
		return nil, false
	}
}

func (state *model) resize(width, height int) {
	previous := layoutFor(state.width, state.height)
	state.width = max(width, 1)
	state.height = max(height, 1)
	current := layoutFor(state.width, state.height)
	if previous >= layoutCompact && current < layoutCompact {
		state.invalidateConfirmation()
	}
	state.rewrapServiceDiff()
}

func (state *model) rewrapServiceDiff() {
	switch current := state.page.(type) {
	case commitServicePage:
		state.page = state.wrapServiceDiff(current)
	case stagedDiffPage:
		current.commit = state.wrapServiceDiff(current.commit)
		state.page = current
	case unsignedCommitConfirmationPage:
		current.commit = state.wrapServiceDiff(current.commit)
		state.page = current
	}
}

func (state *model) wrapServiceDiff(current commitServicePage) commitServicePage {
	width := state.serviceDiffWidth()
	if current.diffWidth == width && current.diffLines != nil {
		return current
	}
	current.diffWidth = width
	current.diffLines = terminaltext.Wrap(current.staged.Diff, width)

	return current
}

func (state *model) serviceDiffWidth() int {
	width := state.width
	if width == 0 {
		width = defaultWidth
	}
	height := state.height
	if height == 0 {
		height = defaultHeight
	}
	if layoutFor(width, height) == layoutFull {
		width -= fullBodyOffset
	}

	return max(width-detailsPadding, 1)
}

func (state *model) handleServiceWorkspaceMessage(message tea.Msg) (tea.Cmd, bool) {
	switch message := message.(type) {
	case servicePreviewResultMsg:
		return state.handleServicePreviewResult(message), true
	case serviceStageResultMsg:
		return state.handleServiceStageResult(message), true
	case serviceCommitResultMsg:
		return state.handleServiceCommitResult(message), true
	case serviceDiscardResultMsg:
		return state.handleServiceDiscardResult(message), true
	default:
		return nil, false
	}
}

func (state *model) invalidateConfirmation() {
	switch current := state.page.(type) {
	case confirmationPage:
		state.page = current.review
		state.status = statusReviewLarger
	case registrationConfirmationPage:
		state.page = current.registration
		state.status = statusReviewLarger
	case stageServiceConfirmationPage:
		state.page = current.preview
		state.status = statusReviewLarger
	case unsignedCommitConfirmationPage:
		state.page = current.commit
		state.status = statusReviewLarger
	case commitServicePage:
		current.focus = confirmationBack
		current.editing = false
		state.page = current
		state.status = statusReviewLarger
	}
}

//nolint:cyclop // The type switch is the single keyboard dispatch table for all page states.
func (state *model) handleKey(message tea.KeyPressMsg) tea.Cmd {
	key := message.String()
	if command, handled := state.handleSessionKey(key); handled {
		return command
	}
	if command, handled := state.handleAuxiliaryPageKey(message); handled {
		return command
	}

	switch current := state.page.(type) {
	case homePage:
		return state.handleHomeKey(current, key)
	case openPathPage:
		return state.handleOpenPathKey(current, message)
	case sourceDiagnosticPage:
		return state.handleSourceDiagnosticKey(current, key)
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

func (state *model) handleAuxiliaryPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	if command, handled := state.handleRegistrationPageKey(message); handled {
		return command, true
	}

	return state.handleServiceWorkspacePageKey(message)
}

func (state *model) handleServiceWorkspacePageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case addServicePage:
		return state.handleAddServiceKey(current, message), true
	case servicePreviewPage:
		return state.handleServicePreviewKey(current, message.String()), true
	case stageServiceConfirmationPage:
		return state.handleStageServiceConfirmationKey(current, message.String()), true
	case commitServicePage:
		return state.handleCommitServiceKey(current, message), true
	case stagedDiffPage:
		return state.handleStagedDiffKey(current, message.String()), true
	case unsignedCommitConfirmationPage:
		return state.handleUnsignedCommitConfirmationKey(current, message.String()), true
	default:
		return nil, false
	}
}

func (state *model) handleRegistrationPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case registrationPage:
		state.handleRegistrationKey(current, message)

		return nil, true
	case registrationConfirmationPage:
		return state.handleRegistrationConfirmationKey(current, message.String()), true
	default:
		return nil, false
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
	setupIndex := registrationSetupIndex(current.catalog)
	items := homeItemCount(current.catalog)
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + items) % items
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % items
	case keyEnter:
		return state.activateHomeItem(current, setupIndex)
	case "r":
		return state.startCatalog()
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) activateHomeItem(current homePage, setupIndex int) tea.Cmd {
	switch current.cursor {
	case addServiceIndex(current.catalog):
		return state.activateAddService(current.catalog)
	case openComposeIndex(current.catalog):
		state.page = openPathPage{}
		state.status = "Enter a committed Compose path"

		return nil
	case setupIndex:
		state.page = registrationPage{value: current.catalog.SuggestedRepository}
		state.status = "Review the desired-state repository path"

		return nil
	}
	service := current.catalog.Services[current.cursor]
	if service.Blocker != BlockerNone {
		if diagnostic, valid := canonicalSourceDiagnostic(service.Diagnostic); valid {
			state.page = sourceDiagnosticPage{previous: current, diagnostic: diagnostic}
			state.status = "Review the Compose source issue"

			return nil
		}
		state.status = blockerMessage(service.Blocker)

		return nil
	}

	return state.startOpenRegistered(service.ID)
}

func (state *model) handleSourceDiagnosticKey(current sourceDiagnosticPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case keyEnter, keyEscape:
		state.page = current.previous
		state.status = "Fix the Compose source and refresh"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) activateAddService(catalog CatalogSnapshot) tea.Cmd {
	if catalog.State == CatalogReady {
		state.page = addServicePage{}
		state.status = "Enter a fixed image URI or a complete runtime command"

		return nil
	}
	state.status = "Set up a desired-state repository before adding a service"
	if catalog.SuggestedRepository != "" {
		state.page = registrationPage{value: catalog.SuggestedRepository}
	}

	return nil
}

func homeItemCount(catalog CatalogSnapshot) int {
	count := len(catalog.Services) + homeActionCount
	if registrationSetupIndex(catalog) >= 0 {
		count++
	}

	return count
}

func registrationSetupIndex(catalog CatalogSnapshot) int {
	if catalog.State == CatalogReady || catalog.SuggestedRepository == "" {
		return -1
	}

	return len(catalog.Services) + homeActionCount
}

func addServiceIndex(catalog CatalogSnapshot) int {
	return len(catalog.Services)
}

func openComposeIndex(catalog CatalogSnapshot) int {
	return len(catalog.Services) + 1
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

func (state *model) handleRegistrationKey(current registrationPage, message tea.KeyPressMsg) {
	switch message.String() {
	case keyEnter:
		if current.value == "" {
			state.status = "Enter a repository path"

			return
		}
		state.page = registrationConfirmationPage{registration: current, focus: confirmationBack}
		state.status = "Confirm repository setup or go back"

		return
	case keyEscape:
		state.registrationSeen = true
		state.page = homePage{catalog: CatalogSnapshot{
			State: CatalogMissing, SuggestedRepository: current.value,
		}}
		state.status = "No registered repository"

		return
	}

	current.value = editSingleLine(current.value, message)
	state.page = current
}

func editSingleLine(value string, message tea.KeyPressMsg) string {
	if message.String() == "backspace" {
		characters := []rune(value)
		if len(characters) > 0 {
			return string(characters[:len(characters)-1])
		}

		return value
	}
	text := message.Key().Text
	if !printableSingleLine(text) {
		return value
	}
	candidate := value + text
	if _, err := terminaltext.Canonicalize(candidate, displayLimits()); err != nil {
		return value
	}

	return candidate
}

func (state *model) handleAddServiceKey(current addServicePage, message tea.KeyPressMsg) tea.Cmd {
	switch message.String() {
	case keyEnter:
		if current.value == "" {
			state.status = "Enter a fixed image URI or a complete runtime command"

			return nil
		}

		return state.startServicePreview(current.value)
	case keyEscape:
		return state.startCatalog()
	case keyQuit:
		return tea.Quit
	}
	current.value = editSingleLine(current.value, message)
	state.page = current

	return nil
}

func (state *model) handleServicePreviewKey(current servicePreviewPage, key string) tea.Cmd {
	switch key {
	case keyEnter:
		if layoutFor(state.width, state.height) < layoutCompact {
			state.status = "Resize to review the file mutation"

			return nil
		}
		state.page = stageServiceConfirmationPage{preview: current, focus: confirmationBack}
		state.status = "Confirm file mutation or go back"
	case keyEscape:
		state.page = addServicePage{value: current.input}
		state.status = "Edit service input"
	case keyQuit:
		return tea.Quit
	}

	return nil
}

func (state *model) handleStageServiceConfirmationKey(
	current stageServiceConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.preview
		state.status = statusReviewLarger

		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.preview
			state.status = "Generated service is unchanged"

			return nil
		}

		return state.startServiceStage(current.preview)
	case keyEscape:
		state.page = current.preview
		state.status = "Generated service is unchanged"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func toggledConfirmationFocus(focus confirmationFocus) confirmationFocus {
	if focus == confirmationBack {
		return confirmationApply
	}

	return confirmationBack
}

func (state *model) handleCommitServiceKey(current commitServicePage, message tea.KeyPressMsg) tea.Cmd {
	if current.editing {
		return state.handleCommitMessageKey(current, message)
	}

	return state.handleCommitReviewKey(current, message.String())
}

func (state *model) handleCommitReviewKey(current commitServicePage, key string) tea.Cmd {
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d":
		state.page = stagedDiffPage{commit: current}
		state.status = "Full staged diff"

		return nil
	case "e":
		current.editing = true
		state.status = "Edit the commit message"
	case keyEnter:
		if current.focus == confirmationBack {
			return state.startServiceDiscard(current.preview)
		}

		return state.startServiceCommit(current, false)
	case keyEscape:
		return state.startServiceDiscard(current.preview)
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleCommitMessageKey(current commitServicePage, message tea.KeyPressMsg) tea.Cmd {
	switch message.String() {
	case keyEnter, keyEscape:
		current.editing = false
		state.status = statusReviewStaged
	default:
		candidate := editSingleLine(current.message, message)
		if len(candidate) <= maximumCommitMessageBytes {
			current.message = candidate
		}
	}
	state.page = current

	return nil
}

func (state *model) handleStagedDiffKey(current stagedDiffPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d", keyEscape:
		state.page = current.commit
		state.status = statusReviewStaged

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleUnsignedCommitConfirmationKey(
	current unsignedCommitConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.commit
		state.status = statusReviewLarger

		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.commit
			state.status = statusSignedCommitMissing

			return nil
		}

		return state.startServiceCommit(current.commit, true)
	case keyEscape:
		state.page = current.commit
		state.status = statusSignedCommitMissing

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleRegistrationConfirmationKey(
	current registrationConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.registration
		state.status = statusReviewLarger

		return nil
	}

	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.registration
			state.status = "Review repository path"

			return nil
		}

		return state.startRegistration(current.registration.value)
	case keyEscape:
		state.page = current.registration
		state.status = "Review repository path"

		return nil
	case keyQuit:
		return tea.Quit
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
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
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

func (state *model) startRegistration(path string) tea.Cmd {
	return state.begin("Setting up repository", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			return registrationResultMsg{sequence: sequence, result: catalog.Register(ctx, path)}
		}
	})
}

func (state *model) startServicePreview(input string) tea.Cmd {
	return state.begin("Preparing service preview", func(ctx context.Context, sequence uint64) tea.Cmd {
		workspace := state.workspace

		return func() tea.Msg {
			draft, err := workspace.Preview(ctx, input)

			return servicePreviewResultMsg{sequence: sequence, input: input, draft: draft, err: err}
		}
	})
}

func (state *model) startServiceStage(preview servicePreviewPage) tea.Cmd {
	return state.begin("Writing and staging files", func(ctx context.Context, sequence uint64) tea.Cmd {
		workspace := state.workspace

		return func() tea.Msg {
			staged, err := workspace.Stage(ctx)

			return serviceStageResultMsg{sequence: sequence, preview: preview, staged: staged, err: err}
		}
	})
}

func (state *model) startServiceCommit(commit commitServicePage, unsigned bool) tea.Cmd {
	state.page = commit

	return state.begin("Creating commit", func(ctx context.Context, sequence uint64) tea.Cmd {
		workspace := state.workspace

		return func() tea.Msg {
			result, err := workspace.Commit(ctx, commit.message, unsigned)

			return serviceCommitResultMsg{sequence: sequence, commit: commit, result: result, err: err}
		}
	})
}

func (state *model) startServiceDiscard(preview servicePreviewPage) tea.Cmd {
	return state.begin("Discarding staged files", func(ctx context.Context, sequence uint64) tea.Cmd {
		workspace := state.workspace

		return func() tea.Msg {
			return serviceDiscardResultMsg{sequence: sequence, preview: preview, err: workspace.Discard(ctx)}
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

func (state *model) startCommittedSnapshot(request application.Request) tea.Cmd {
	state.activeRequest = request

	return state.begin("Validating committed service", func(ctx context.Context, sequence uint64) tea.Cmd {
		operations := state.operations

		return func() tea.Msg {
			result := snapshotResultMsg{sequence: sequence}
			plan, err := operations.DryRun(ctx, request)
			result.err = err
			if result.err == nil {
				result.dryRun = &plan
				result.snapshot, result.err = operations.Snapshot(ctx, request)
			}
			if result.err == nil {
				result.evidence, result.err = operations.Evidence(result.snapshot)
			}

			return result
		}
	})
}

func (state *model) startApply(review reviewPage) tea.Cmd {
	state.page = review

	command := state.begin("Applying change", func(ctx context.Context, sequence uint64) tea.Cmd {
		operations := state.operations

		return func() tea.Msg {
			_, err := operations.Apply(ctx, review.request)

			return applyResultMsg{sequence: sequence, err: err}
		}
	})
	state.applying = command != nil

	return command
}

func (state *model) handleCatalogResult(result catalogResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, nil)
	if !accepted {
		return command
	}

	snapshot := canonicalCatalog(result.snapshot)
	if snapshot.State == CatalogMissing && snapshot.SuggestedRepository != "" && !state.registrationSeen {
		state.registrationSeen = true
		state.page = registrationPage{value: snapshot.SuggestedRepository}
		state.status = "Set up a desired-state repository or press Esc to skip"

		return command
	}
	state.page = homePage{catalog: snapshot}
	state.status = catalogMessage(snapshot)

	return command
}

func (state *model) handleRegistrationResult(result registrationResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, nil)
	if !accepted {
		return command
	}
	if result.result.Blocker != BlockerNone {
		state.status = blockerMessage(result.result.Blocker)

		return command
	}

	snapshot := canonicalCatalog(result.result.Snapshot)
	state.page = homePage{catalog: snapshot}
	state.status = "Repository ready"
	state.mutationOutcome = "Repository created"

	return command
}

func (state *model) handleServicePreviewResult(result servicePreviewResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	draft, err := canonicalServiceDraft(result.draft)
	if err != nil {
		state.err = errors.Join(errInvalidInput, err)
		state.status = "Generated service could not be displayed safely"

		return command
	}
	state.page = servicePreviewPage{input: result.input, draft: draft}
	state.status = "Review parsed service"

	return command
}

func (state *model) handleServiceStageResult(result serviceStageResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	staged, err := canonicalStagedService(result.staged)
	if err != nil {
		state.err = errors.Join(errInvalidInput, err)
		state.status = "Staged change could not be displayed safely"

		return command
	}
	state.page = state.wrapServiceDiff(commitServicePage{
		preview: result.preview,
		staged:  staged,
		message: staged.CommitMessage,
		focus:   confirmationBack,
	})
	state.status = statusReviewStaged
	state.mutationOutcome = "Files staged"

	return command
}

func (state *model) handleServiceCommitResult(result serviceCommitResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	if result.result.Committed && result.result.NeedsUnsignedApproval ||
		result.result.ValidationUnavailable && !result.result.Committed {
		state.err = errInvalidInput
		state.status = "Commit result could not be verified"

		return command
	}
	if result.result.NeedsUnsignedApproval {
		state.page = unsignedCommitConfirmationPage{commit: result.commit, focus: confirmationBack}
		state.status = statusSignedCommitMissing

		return command
	}
	if !result.result.Committed {
		state.err = errInvalidInput
		state.status = "Commit result could not be verified"

		return command
	}
	state.mutationOutcome = "Compose commit created"
	if result.result.ValidationUnavailable {
		state.page = homePage{catalog: CatalogSnapshot{State: CatalogUnavailable}}
		state.status = "Commit created; validation is unavailable"

		return command
	}

	return tea.Batch(command, state.startCommittedSnapshot(result.result.Request))
}

func (state *model) handleServiceDiscardResult(result serviceDiscardResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	state.page = result.preview
	state.status = "Staged files discarded"
	state.mutationOutcome = ""

	return command
}

func (state *model) handleOpenResult(result openResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, nil)
	if !accepted {
		return command
	}
	if result.result.Blocker != BlockerNone {
		if diagnostic, valid := canonicalSourceDiagnostic(result.result.Diagnostic); valid {
			state.page = sourceDiagnosticPage{previous: state.page, diagnostic: diagnostic}
			state.status = "Review the Compose source issue"

			return command
		}
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
	if result.dryRun != nil && !reflect.DeepEqual(*result.dryRun, result.snapshot.Plan) {
		state.err = errInvalidInput
		state.status = "Committed service changed during validation"

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
	state.applying = false
}

func canonicalCatalog(snapshot CatalogSnapshot) CatalogSnapshot {
	suggested, err := canonicalDisplay(snapshot.SuggestedRepository)
	if err != nil {
		snapshot.State = CatalogUnavailable
		snapshot.SuggestedRepository = ""
	} else {
		snapshot.SuggestedRepository = suggested
	}
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
		if diagnostic, valid := canonicalSourceDiagnostic(service.Diagnostic); valid && service.Blocker == BlockerInvalid {
			services[len(services)-1].Diagnostic = diagnostic
		}
	}
	snapshot.Services = services

	return snapshot
}

func canonicalSourceDiagnostic(diagnostic SourceDiagnostic) (SourceDiagnostic, bool) {
	file, valid := canonicalSourceDiagnosticFile(diagnostic.File)
	if !valid || !validSourceDiagnosticPosition(diagnostic.Line, diagnostic.Column) ||
		!validSourceDiagnosticReason(diagnostic.Reason) {
		return SourceDiagnostic{}, false
	}
	diagnostic.File = file

	return diagnostic, true
}

func canonicalSourceDiagnosticFile(file string) (string, bool) {
	canonical, err := canonicalDisplay(file)
	native := filepath.FromSlash(canonical)
	if err != nil || canonical == "" || filepath.IsAbs(native) || !filepath.IsLocal(native) ||
		filepath.ToSlash(filepath.Clean(native)) != canonical {
		return "", false
	}

	return canonical, true
}

func validSourceDiagnosticPosition(line, column int) bool {
	return line >= 0 && column >= 0 && (line != 0 || column == 0)
}

func validSourceDiagnosticReason(reason SourceDiagnosticReason) bool {
	switch reason {
	case DiagnosticYAMLSyntax, DiagnosticYAMLStructure, DiagnosticYAMLUnsupported, DiagnosticComposeValidation:
		return true
	default:
		return false
	}
}

func canonicalServiceDraft(draft ServiceDraft) (ServiceDraft, error) {
	values := []*string{
		&draft.Runtime, &draft.Image, &draft.Service, &draft.ComposePath, &draft.Preparation,
	}
	for _, value := range values {
		canonical, err := canonicalDisplay(*value)
		if err != nil {
			return ServiceDraft{}, err
		}
		*value = canonical
	}
	if draft.Runtime == "" || draft.Image == "" || draft.Service == "" || draft.ComposePath == "" ||
		draft.WarningCount < 0 {
		return ServiceDraft{}, errInvalidInput
	}

	return draft, nil
}

func canonicalStagedService(staged StagedService) (StagedService, error) {
	composePath, err := canonicalDisplay(staged.ComposePath)
	if err != nil {
		return StagedService{}, err
	}
	preparation, err := canonicalDisplay(staged.Preparation)
	if err != nil {
		return StagedService{}, err
	}
	message, err := canonicalDisplay(staged.CommitMessage)
	if err != nil || len(message) > maximumCommitMessageBytes {
		return StagedService{}, errors.Join(errInvalidInput, err)
	}
	diff, err := terminaltext.Canonicalize(staged.Diff, terminaltext.Limits{
		Bytes:     maximumStagedDiffBytes,
		Runes:     maximumStagedDiffBytes,
		Lines:     maximumStagedDiffLines,
		LineCells: maximumDisplayCells,
	})
	if err != nil || diff == "" || composePath == "" || message == "" {
		return StagedService{}, errors.Join(errInvalidInput, err)
	}

	return StagedService{
		Diff: diff, ComposePath: composePath, Preparation: preparation, CommitMessage: message,
	}, nil
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
