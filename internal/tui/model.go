package tui

import (
	"context"
	"errors"
	"reflect"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	statusReady               = "Ready for confirmation"
	statusApplyCompleted      = "Apply completed"
	statusRefreshing          = "Refreshing review"
	statusReviewLarger        = "Review again at a larger terminal"
	statusOperationFailed     = "Operation failed"
	statusHealthDetails       = "Workload health details"
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
	statusCommitUnverified    = "Commit result could not be verified"
	statusDeploymentUnchanged = "Deployment candidate is unchanged"
	statusConfirmFileMutation = "Confirm file mutation or go back"
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
	err      error
}

type openResultMsg struct {
	sequence uint64
	result   OpenResult
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

type healthResolutionResultMsg struct {
	sequence uint64
	err      error
}

type healthPollMsg struct {
	sequence uint64
}

type eventMsg struct {
	sequence uint64
	event    application.Event
}

type contextDoneMsg struct{}

type page interface {
	isPage()
}

type textInputPage interface {
	acceptsTextInput() bool
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

func (openPathPage) acceptsTextInput() bool { return true }

type sourceDiagnosticPage struct {
	previous   page
	diagnostic SourceDiagnostic
	scroll     int
}

func (sourceDiagnosticPage) isPage() {}

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
	kind             string
	project          string
	service          string
	runtime          string
	platform         string
	current          string
	proposed         string
	status           string
	warningText      string
	health           application.HealthConvergence
	healthState      string
	healthPoll       time.Duration
	healthFails      uint32
	transaction      string
	resolution       application.HealthResolutionAction
	restoresPrevious bool
	healthProof      application.HealthResolutionObservation
}

type reviewFocus uint8

const (
	reviewContinue reviewFocus = iota
	reviewExplore
)

type reviewPage struct {
	request     application.Request
	plan        planView
	correlation eventCorrelation
	focus       reviewFocus
}

func (reviewPage) isPage() {}

type reviewOptionsPage struct {
	review reviewPage
	cursor int
}

func (reviewOptionsPage) isPage() {}

type explainPage struct {
	review reviewPage
}

func (explainPage) isPage() {}

type detailsPage struct {
	review reviewPage
	scroll int
}

func (detailsPage) isPage() {}

type contextualHelpPage struct {
	previous page
	location string
	keys     string
}

func (contextualHelpPage) isPage() {}

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

type healthConfirmationPage struct {
	review reviewPage
	focus  confirmationFocus
}

func (healthConfirmationPage) isPage() {}

type model struct {
	ctx         context.Context //nolint:containedctx // The model and context share one Bubble Tea session lifetime.
	catalog     Catalog
	workspace   ServiceWorkspace
	deployments DeploymentWorkspace
	assistant   Assistant
	operations  Operations
	events      *EventStream
	options     Options

	width               int
	height              int
	page                page
	activeRequest       application.Request
	status              string
	mutationOutcome     string
	busy                bool
	applying            bool
	sequence            uint64
	cancel              context.CancelFunc
	err                 error
	quitAfterOperation  bool
	registrationSeen    bool
	applyBlocked        bool
	deploymentFailure   DeploymentFailure
	configReloadNeeded  bool
	llmQuestionToResume *llmQuestionPage
	timeline            sessionTimeline
	result              Result
}

func newModelWithCapabilities(
	ctx context.Context,
	catalog Catalog,
	workspace ServiceWorkspace,
	deployments DeploymentWorkspace,
	assistant Assistant,
	operations Operations,
	events *EventStream,
	options Options,
) *model {
	return &model{
		ctx: ctx, catalog: catalog, workspace: workspace, deployments: deployments,
		assistant: assistant, operations: operations, events: events, options: options,
		page: homePage{catalog: CatalogSnapshot{State: CatalogMissing}}, status: "Loading services",
		timeline: newSessionTimeline(),
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

//nolint:cyclop,ireturn // Bubble Tea requires one closed root-message dispatcher and tea.Model.
func (state *model) Update(message tea.Msg) (tea.Model, tea.Cmd) {
	if command, handled := state.handleWorkspaceMessage(message); handled {
		return state, command
	}
	if command, handled := state.handleOperationMessage(message); handled {
		return state, command
	}
	switch message := message.(type) {
	case tea.WindowSizeMsg:
		return state, state.resize(message.Width, message.Height)
	case commitDiffResizeMsg:
		if message.width == state.commitDiffWidth() {
			state.rewrapCommitDiff()
			state.clampPageScroll()
		}
	case tea.KeyPressMsg:
		return state, state.handleKey(message)
	case catalogResultMsg:
		return state, state.handleCatalogResult(message)
	case openResultMsg:
		return state, state.handleOpenResult(message)
	case registrationResultMsg:
		return state, state.handleRegistrationResult(message)
	case eventMsg:
		state.observeApplicationEvent(message)

		return state, state.events.wait(state.ctx)
	case contextDoneMsg:
		return state, state.requestQuit()
	case healthPollMsg:
		return state, state.handleHealthPoll(message)
	}

	return state, nil
}

func (state *model) handleWorkspaceMessage(message tea.Msg) (tea.Cmd, bool) {
	if command, handled := state.handleLLMMessage(message); handled {
		return command, true
	}
	if command, handled := state.handleServiceWorkspaceMessage(message); handled {
		return command, true
	}

	return state.handleDeploymentWorkspaceMessage(message)
}

func (state *model) handleOperationMessage(message tea.Msg) (tea.Cmd, bool) {
	switch message := message.(type) {
	case snapshotResultMsg:
		return state.handleSnapshotResult(message), true
	case applyResultMsg:
		return state.handleApplyResult(message), true
	case healthResolutionResultMsg:
		return state.handleHealthResolutionResult(message), true
	default:
		return nil, false
	}
}

func (state *model) resize(width, height int) tea.Cmd {
	previous := layoutFor(state.width, state.height)
	state.width = max(width, 1)
	state.height = max(height, 1)
	current := layoutFor(state.width, state.height)
	if previous >= layoutCompact && current < layoutCompact {
		if help, valid := state.page.(contextualHelpPage); valid {
			state.page = help.previous
		}
		state.invalidateConfirmation()
	}
	state.clampPageScroll()

	return state.scheduleCommitDiffResize()
}

func (state *model) bodyWidth() int {
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

	return max(width, 1)
}

func (state *model) clampPageScroll() {
	width := state.bodyWidth()
	switch current := state.page.(type) {
	case sourceDiagnosticPage:
		current.scroll = boundedScroll(current.scroll, len(state.sourceDiagnosticLines(current.diagnostic, width)))
		state.page = current
	case commitPage:
		if current.diffLines != nil {
			current.scroll = boundedScroll(current.scroll, len(current.diffLines))
		}
		state.page = current
	case stagedDiffPage:
		if current.commit.diffLines != nil {
			current.scroll = boundedScroll(current.scroll, stagedDiffLineCount(current.commit, width))
		}
		state.page = current
	case deploymentPreviewPage:
		_, comparison, _, available := state.deploymentPreviewSections(current, width)
		current.scroll = min(max(current.scroll, 0), max(len(comparison)-available, 0))
		state.page = current
	case deploymentDiffPage:
		lineCount := deploymentDiffLeadRows +
			len(deploymentPreviewDiffLines(current.confirmation.preview, width))
		current.scroll = min(
			max(current.scroll, 0),
			max(lineCount-state.deploymentDiffViewportHeight(), 0),
		)
		state.page = current
	case deploymentDetailsPage:
		current.scroll = boundedScroll(
			current.scroll, len(state.deploymentDetailsLines(current.preview.preview.Changes, width)),
		)
		state.page = current
	case detailsPage:
		current.scroll = boundedScroll(current.scroll, len(state.detailsLines(current.review, width)))
		state.page = current
	}
}

func boundedScroll(scroll, lineCount int) int {
	return min(max(scroll, 0), max(lineCount-1, 0))
}

func (state *model) deploymentDiffViewportHeight() int {
	width := state.width
	if width == 0 {
		width = defaultWidth
	}
	height := state.height
	if height == 0 {
		height = defaultHeight
	}
	switch layoutFor(width, height) { //nolint:exhaustive // Non-content layouts share a zero-height viewport.
	case layoutFull:
		return max(height-fullFrameRows-state.bodyStateRows(), 0)
	case layoutCompact:
		return max(height-compactFrameRows-state.bodyStateRows(), 0)
	default:
		return 0
	}
}

//nolint:cyclop // The closed confirmation-page catalog restores each page to its safe predecessor.
func (state *model) invalidateConfirmation() {
	if state.invalidateLLMConfirmation() {
		return
	}
	switch current := state.page.(type) {
	case confirmationPage:
		state.page = current.review
		state.status = statusReviewLarger
	case healthConfirmationPage:
		state.page = current.review
		state.status = statusReviewLarger
	case registrationConfirmationPage:
		state.page = current.registration
		state.status = statusReviewLarger
	case stageServiceConfirmationPage:
		state.page = current.preview
		state.status = statusReviewLarger
	case stageDeploymentConfirmationPage:
		state.page = current.preview
		state.status = statusReviewLarger
	case deploymentDiffPage:
		state.page = current.confirmation.preview
		state.status = statusReviewLarger
	case deploymentDraftConfirmationPage:
		state.page = current.previous
		state.status = statusReviewLarger
	case restoreDeploymentConfirmationPage:
		state.page = current.history
		state.status = statusReviewLarger
	case unsignedCommitConfirmationPage:
		state.page = current.commit
		state.status = statusReviewLarger
	case commitPage:
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
	if current, valid := state.page.(contextualHelpPage); valid {
		return state.handleContextualHelpKey(current, key)
	}
	if key == "?" && !pageAcceptsText(state.page) {
		state.page = contextualHelpPage{
			previous: state.page,
			location: state.locationLine(),
			keys:     state.footerKeys(),
		}

		return nil
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
	case reviewOptionsPage:
		return state.handleReviewOptionsKey(current, key)
	case explainPage:
		return state.handleExplainKey(current, key)
	case detailsPage:
		return state.handleDetailsKey(current, key)
	case confirmationPage:
		return state.handleConfirmationKey(current, key)
	case healthConfirmationPage:
		return state.handleHealthConfirmationKey(current, key)
	default:
		return nil
	}
}

func (state *model) handleContextualHelpKey(current contextualHelpPage, key string) tea.Cmd {
	switch key {
	case "?", keyEscape:
		state.page = current.previous
		if review, valid := current.previous.(reviewPage); valid &&
			review.plan.health == application.HealthConvergencePending {
			return state.startSnapshot(review.request)
		}
	case keyQuit:
		state.page = current.previous

		return state.handleKey(tea.KeyPressMsg(tea.Key{Code: 'q', Text: keyQuit}))
	}

	return nil
}

func pageAcceptsText(current page) bool {
	input, valid := current.(textInputPage)

	return valid && input.acceptsTextInput()
}

func (state *model) handleAuxiliaryPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	if command, handled := state.handleLLMPageKey(message); handled {
		return command, true
	}
	if command, handled := state.handleRegistrationPageKey(message); handled {
		return command, true
	}
	if command, handled := state.handleCommitPageKey(message); handled {
		return command, true
	}
	if command, handled := state.handleDeploymentPageKey(message); handled {
		return command, true
	}

	return state.handleServiceWorkspacePageKey(message)
}

func (state *model) handleSessionKey(key string) (tea.Cmd, bool) {
	if key == "ctrl+c" {
		return state.requestQuit(), true
	}
	if key == "x" {
		if review, valid := exportableReview(state.page); valid {
			switch {
			case state.busy:
				state.status = "Wait for the current operation before exporting"
			case layoutFor(state.width, state.height) < layoutCompact:
				state.status = "Resize to export session details"
			default:
				state.result.Export = state.detailProjection(review).plain()

				return tea.Quit, true
			}

			return nil, true
		}
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

func (state *model) begin(status string, command func(context.Context, uint64) tea.Cmd) tea.Cmd {
	if state.busy {
		return nil
	}

	state.sequence++
	sequence := state.sequence
	state.events.bind(sequence)
	operationCtx, cancel := context.WithCancel(state.ctx)
	state.cancel = cancel
	state.busy = true
	state.err = nil
	state.status = status

	return command(operationCtx, sequence)
}

func (state *model) startCatalog() tea.Cmd {
	state.llmQuestionToResume = nil

	return state.begin("Loading services", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			snapshot := catalog.Snapshot(ctx)

			return catalogResultMsg{sequence: sequence, snapshot: snapshot, err: ctx.Err()}
		}
	})
}

func (state *model) startOpenRegistered(id string) tea.Cmd {
	return state.begin("Opening service", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			result := catalog.OpenRegistered(ctx, id)

			return openResultMsg{sequence: sequence, result: result, err: ctx.Err()}
		}
	})
}

func (state *model) startOpenPath(path string) tea.Cmd {
	return state.begin("Opening Compose file", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			result := catalog.OpenPath(ctx, path)

			return openResultMsg{sequence: sequence, result: result, err: ctx.Err()}
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
	if state.configReloadNeeded {
		state.status = "Reload LLM configuration before applying this change"

		return nil
	}
	if !state.deploymentOperationReady() {
		return nil
	}
	if state.applyBlocked {
		state.status = "Exit, run the preparation step, then start maniud tui again"

		return nil
	}
	if state.busy {
		return nil
	}
	state.llmQuestionToResume = nil
	state.page = review

	command := state.begin("Applying change", func(ctx context.Context, sequence uint64) tea.Cmd {
		operations := state.operations

		return func() tea.Msg {
			_, err := operations.Apply(ctx, review.request)

			return applyResultMsg{sequence: sequence, err: err}
		}
	})
	review.correlation.sequence = state.sequence
	state.page = review
	state.applying = true

	return command
}

func (state *model) startHealthResolution(current healthConfirmationPage) tea.Cmd {
	if state.busy || current.review.plan.resolution == "" || current.review.plan.transaction == "" {
		return nil
	}
	state.page = current.review

	return state.begin("Applying health decision", func(ctx context.Context, sequence uint64) tea.Cmd {
		operations := state.operations
		request := current.review.request
		resolution := application.HealthResolution{
			Transaction: current.review.plan.transaction,
			Action:      current.review.plan.resolution,
			Observation: current.review.plan.healthProof,
		}

		return func() tea.Msg {
			_, err := operations.ResolveHealth(ctx, request, resolution)

			return healthResolutionResultMsg{sequence: sequence, err: err}
		}
	})
}

func (state *model) handleCatalogResult(result catalogResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}

	snapshot := canonicalCatalog(result.snapshot)
	if snapshot.State == CatalogMissing && snapshot.SuggestedRepository != "" && !state.registrationSeen {
		state.registrationSeen = true
		state.page = newRegistrationPage(snapshot.SuggestedRepository)
		state.status = "Choose a repository setup method or press Esc to skip"

		return command
	}
	state.page = homePage{catalog: snapshot}
	state.status = catalogMessage(snapshot)

	return command
}

func (state *model) handleOpenResult(result openResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
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
	review := reviewPage{
		request: state.activeRequest, plan: view,
		correlation: eventCorrelationForSnapshot(result.sequence, result.snapshot),
	}
	state.page = review
	state.status = view.status
	if view.health == application.HealthConvergenceHealthy {
		return tea.Batch(command, state.startApply(review))
	}
	if view.health == application.HealthConvergencePending {
		return tea.Batch(command, state.waitForHealth(review))
	}

	return command
}
func (state *model) handleApplyResult(result applyResultMsg) tea.Cmd {
	if errors.Is(result.err, application.ErrHealthPending) ||
		errors.Is(result.err, application.ErrHealthDegraded) {
		accepted, command := state.completeOperation(result.sequence, nil)
		if !accepted {
			return command
		}
		review, valid := state.page.(reviewPage)
		if !valid {
			state.err = errInvalidInput
			state.status = "Health result could not be refreshed"

			return command
		}

		return tea.Batch(command, state.startSnapshot(review.request))
	}
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
	state.mutationOutcome = statusApplyCompleted
	if command != nil {
		state.status = state.mutationOutcome

		return command
	}

	return state.startSnapshot(review.request)
}

func (state *model) handleHealthResolutionResult(result healthResolutionResultMsg) tea.Cmd {
	refresh := result.err == nil || errors.Is(result.err, application.ErrHealthPending) ||
		errors.Is(result.err, application.ErrHealthDegraded) ||
		errors.Is(result.err, application.ErrSnapshotStale)
	err := result.err
	if refresh {
		err = nil
	}
	accepted, command := state.completeOperation(result.sequence, err)
	if !accepted || !refresh {
		return command
	}
	review, valid := state.page.(reviewPage)
	if !valid {
		state.err = errInvalidInput
		state.status = "Health decision could not be refreshed"

		return command
	}

	return tea.Batch(command, state.startSnapshot(review.request))
}

func (state *model) waitForHealth(review reviewPage) tea.Cmd {
	if review.plan.health != application.HealthConvergencePending || review.plan.healthPoll <= 0 {
		return nil
	}
	sequence := state.sequence

	return tea.Tick(review.plan.healthPoll, func(time.Time) tea.Msg {
		return healthPollMsg{sequence: sequence}
	})
}

func (state *model) handleHealthPoll(message healthPollMsg) tea.Cmd {
	if message.sequence != state.sequence || state.busy {
		return nil
	}
	review, valid := state.page.(reviewPage)
	if !valid || review.plan.health != application.HealthConvergencePending {
		return nil
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
		state.status = statusOperationFailed
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
