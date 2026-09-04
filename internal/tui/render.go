package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	defaultWidth       = 80
	defaultHeight      = 24
	fullMinimumWidth   = 80
	fullMinimumHeight  = 24
	compactMinimum     = 56
	compactMinHeight   = 16
	hardMinimumWidth   = 32
	hardMinimumHeight  = 8
	fullRailWidth      = 18
	fullBodyOffset     = 20
	fullFrameRows      = 4
	compactFrameRows   = 5
	pathLabelWidth     = 6
	serviceFieldWidth  = 10
	comparisonColumns  = 2
	diffSummaryRows    = 4
	detailsPadding     = 2
	selectionBaseRows  = 3
	reviewBodyBaseRows = 7
	reviewStatusRows   = 9
	statusCardBorders  = 2
	statusCardPadding  = 2
	detailsBaseRows    = 12
	diagnosticBaseRows = 16
	helpBodyBaseRows   = 8
	reviewOptionsRows  = 3
	colorAmber         = "\x1b[38;5;214m"
	colorMuted         = "\x1b[38;5;245m"
	colorSuccess       = "\x1b[38;5;42m"
	colorFailure       = "\x1b[38;5;196m"
	colorReset         = "\x1b[0m"
	confirmationKeys   = "Tab Focus   Enter Choose   Esc Back   q Quit"
	scrollBackKeys     = "Up/Down Scroll   d/Esc Back   q Quit"
)

type layoutTier uint8

const (
	layoutResize layoutTier = iota
	layoutHardFloor
	layoutCompact
	layoutFull
)

// Options selects terminal capabilities established by the CLI boundary.
type Options struct {
	Color   bool
	Unicode bool
}

func layoutFor(width, height int) layoutTier {
	switch {
	case width >= fullMinimumWidth && height >= fullMinimumHeight:
		return layoutFull
	case width >= compactMinimum && height >= compactMinHeight:
		return layoutCompact
	case width >= hardMinimumWidth && height >= hardMinimumHeight:
		return layoutHardFloor
	default:
		return layoutResize
	}
}

func (state *model) View() tea.View {
	width := state.width
	if width == 0 {
		width = defaultWidth
	}
	height := state.height
	if height == 0 {
		height = defaultHeight
	}

	var lines []string
	switch layoutFor(width, height) { //nolint:exhaustive // Default contains invalid internal layout values.
	case layoutFull:
		lines = state.fullView(width, height)
	case layoutCompact:
		lines = state.compactView(width, height)
	case layoutHardFloor:
		lines = state.hardFloorView(width)
	default:
		lines = []string{
			state.accent("maniud"),
			terminaltext.Clip("Resize to at least 32 x 8 to continue.", width),
		}
	}

	view := tea.NewView(fitView(lines, width, height))
	view.AltScreen = true

	return view
}

func (state *model) fullView(width, height int) []string {
	bodyWidth := width - fullBodyOffset
	header := state.header(width)
	rail := state.rail()
	contentLimit := max(height-fullFrameRows, 0)
	body := state.bodyWithin(bodyWidth, contentLimit)
	contentHeight := min(max(len(rail), len(body)), contentLimit)
	horizontal := strings.Repeat(state.symbol("─", "-"), width)
	lines := make([]string, 0, fullFrameRows+contentHeight)
	lines = append(lines, header, horizontal)
	for index := range contentHeight {
		left := ""
		if index < len(rail) {
			left = rail[index]
		}
		right := ""
		if index < len(body) {
			right = body[index]
		}
		lines = append(lines, padCells(left, fullRailWidth)+state.symbol("│ ", "| ")+right)
	}
	lines = append(lines, horizontal, state.footer(width))

	return lines
}

func (state *model) compactView(width, height int) []string {
	horizontal := strings.Repeat(state.symbol("─", "-"), width)
	bodyHeight := max(height-compactFrameRows, 0)
	body := state.compactBody(width, bodyHeight)
	lines := make([]string, 0, compactFrameRows+len(body))
	lines = append(lines, state.header(width), horizontal, state.locationLine())
	lines = append(lines, body...)
	lines = append(lines, horizontal, state.footer(width))

	return lines
}

func (state *model) compactBody(width, height int) []string {
	if current, ok := state.page.(reviewPage); ok {
		summary := []string{
			state.muted("CURRENT"), terminaltext.Middle(current.plan.current, width, "…"),
			state.muted("PROPOSED"), terminaltext.Middle(current.plan.proposed, width, "…"),
		}
		fixed := state.appendBodyState(state.reviewStatusBody(current, width, true))
		if height-len(fixed) > len(summary) {
			summary = append([]string{state.title("Review image change")}, summary...)
		}
		summary = summary[:min(len(summary), max(height-len(fixed), 0))]

		return append(summary, fixed...)
	}
	body := state.bodyWithin(width, height)

	return body[:min(len(body), height)]
}

//nolint:cyclop // This switch is the closed page-to-footer contract for the hard-floor layout.
func (state *model) hardFloorView(width int) []string {
	next := "q Quit"
	switch state.page.(type) {
	case homePage:
		next = "Enter Open   q Quit"
	case openPathPage:
		next = "Esc Back   q Quit"
	case sourceDiagnosticPage:
		next = "Up/Down Scroll   Enter Back"
	case contextualHelpPage:
		next = "?/Esc Back   q Quit"
	case deploymentDraftConfirmationPage, llmDiscardConfirmationPage:
		next = "Esc Continue editing"
	case addServicePage, servicePreviewPage, stageServiceConfirmationPage,
		commitPage, stagedDiffPage, unsignedCommitConfirmationPage,
		deploymentFieldsPage, deploymentValuePage, deploymentPreviewPage, deploymentDetailsPage,
		stageDeploymentConfirmationPage, deploymentDiffPage,
		deploymentHistoryPage, restoreDeploymentConfirmationPage, preparationRequiredPage, llmConfigurationPage,
		llmSaveConfirmationPage, llmSaveOutcomeUnknownPage,
		llmQuestionPage, llmNetworkConfirmationPage, llmChoicesPage:
		next = "Esc Back   q Quit"
	case registrationPage, registrationConfirmationPage:
		next = state.pageFooterKeys()
	case selectServicePage:
		next = "Enter Review   Esc Back"
	case reviewOptionsPage:
		next = "Enter Select   Esc Back   q Quit"
	case reviewPage, detailsPage, confirmationPage, healthConfirmationPage:
		next = "r Review   Esc Back   q Quit"
	case explainPage:
		next = "Enter/Esc Fresh review   q Quit"
	}
	textInput := pageAcceptsText(state.page)
	if textInput {
		next = state.pageFooterKeys()
	}
	if _, help := state.page.(contextualHelpPage); !help && !textInput {
		next = "? Help   " + next
	}

	return []string{
		state.header(width),
		terminaltext.Middle(state.locationLine(), width, "…"),
		terminaltext.Middle("Status: "+state.status, width, "…"),
		terminaltext.Clip(next, width),
	}
}

func (state *model) header(width int) string {
	context := state.locationLine()
	available := max(width-terminaltext.Width("maniud  "), 0)

	return state.accent("maniud") + "  " + state.muted(terminaltext.Middle(context, available, "…"))
}

func (state *model) locationLine() string {
	if current, valid := state.page.(contextualHelpPage); valid {
		return current.location + " / Help"
	}

	return state.pageLocationLine()
}

//nolint:cyclop // This switch is the closed page-to-location contract for navigation context.
func (state *model) pageLocationLine() string {
	if location, valid := state.workspaceLocation(); valid {
		return location
	}
	if location, valid := state.registrationLocation(); valid {
		return location
	}
	switch current := state.page.(type) {
	case reviewPage:
		return current.plan.project + " / " + current.plan.service + " / " + current.plan.runtime
	case reviewOptionsPage:
		return current.review.plan.project + " / " + current.review.plan.service + " / Options"
	case explainPage:
		return current.review.plan.project + " / " + current.review.plan.service + " / Explain"
	case detailsPage:
		return current.review.plan.project + " / " + current.review.plan.service + " / Details"
	case confirmationPage:
		return current.review.plan.project + " / " + current.review.plan.service + " / Confirm"
	case healthConfirmationPage:
		return current.review.plan.project + " / " + current.review.plan.service + " / Health decision"
	case selectServicePage:
		return "Home / Select service"
	case openPathPage:
		return "Home / Open Compose file"
	case sourceDiagnosticPage:
		return "Home / Compose source issue"
	default:
		return "Home"
	}
}

func (state *model) workspaceLocation() (string, bool) {
	if location, valid := state.llmWorkspaceLocation(); valid {
		return location, true
	}
	if location, valid := state.deploymentWorkspaceLocation(); valid {
		return location, true
	}

	return state.serviceWorkspaceLocation()
}

func (state *model) deploymentWorkspaceLocation() (string, bool) {
	switch state.page.(type) {
	case deploymentFieldsPage:
		return "Home / Edit deployment / Field", true
	case deploymentValuePage:
		return "Home / Edit deployment / Value", true
	case deploymentPreviewPage, deploymentDetailsPage, stageDeploymentConfirmationPage, deploymentDiffPage,
		deploymentDraftConfirmationPage, restoreDeploymentConfirmationPage:
		return "Home / Edit deployment / Review", true
	case deploymentHistoryPage:
		return "Home / Edit deployment / History", true
	case commitPage, stagedDiffPage, unsignedCommitConfirmationPage:
		return "Home / Edit deployment / Commit", state.deploymentCommitPage()
	default:
		return "", false
	}
}

func (state *model) rail() []string {
	if lines, valid := state.llmWorkspaceRail(); valid {
		return lines
	}
	if lines, valid := state.deploymentWorkspaceRail(); valid {
		return lines
	}
	if lines, valid := state.serviceWorkspaceRail(); valid {
		return lines
	}
	steps := []string{"Select", "Review", "Confirm", "Apply"}
	current := 0
	switch state.page.(type) {
	case contextualHelpPage:
		return []string{
			state.muted("HELP"), "", state.accent(state.symbol("● ", "[*] ") + "Keyboard"),
			"", state.muted("RETURN"), "Previous page",
		}
	case reviewPage, reviewOptionsPage, explainPage, detailsPage:
		current = 1
	case confirmationPage, healthConfirmationPage:
		current = 2
	}
	if state.busy && state.applying {
		if _, reviewing := state.page.(reviewPage); reviewing {
			current = 3
		}
	}

	return state.flowRail("FLOW", steps, current)
}

func (state *model) deploymentWorkspaceRail() ([]string, bool) {
	current := -1
	switch state.page.(type) {
	case deploymentFieldsPage, deploymentHistoryPage:
		current = 0
	case deploymentValuePage:
		current = 1
	case deploymentPreviewPage, deploymentDetailsPage, stageDeploymentConfirmationPage, deploymentDiffPage,
		deploymentDraftConfirmationPage, restoreDeploymentConfirmationPage:
		current = 2
	case commitPage, stagedDiffPage, unsignedCommitConfirmationPage:
		if state.deploymentCommitPage() {
			current = 3
		}
	}
	if current < 0 {
		return nil, false
	}
	steps := []string{"Field", "Value", "Review", "Commit"}

	return state.flowRail("EDIT DEPLOYMENT", steps, current), true
}

func (state *model) flowRail(label string, steps []string, current int) []string {
	lines := []string{state.muted(label), ""}
	for index, step := range steps {
		marker := state.muted(state.symbol("○ ", "[ ] "))
		switch {
		case index < current:
			marker = state.success(state.symbol("✓ ", "[x] "))
		case index == current:
			marker = state.accent(state.symbol("● ", "[*] "))
		}
		lines = append(lines, marker+step)
		if index < len(steps)-1 {
			lines = append(lines, state.muted(state.symbol("│", " |")))
		}
	}

	return lines
}

func (state *model) body(width int) []string {
	return state.bodyWithin(width, int(^uint(0)>>1))
}

//nolint:cyclop // This switch is the closed renderer for core navigation pages.
func (state *model) bodyWithin(width, height int) []string {
	lines, setupPage := state.auxiliaryPageBody(width, height)
	if !setupPage {
		switch current := state.page.(type) {
		case homePage:
			lines = state.homeBodyWithin(current, width, max(height-state.bodyStateRows(), 0))
		case openPathPage:
			lines = state.openPathBody(current, width)
		case sourceDiagnosticPage:
			lines = state.sourceDiagnosticBody(current, width)
		case contextualHelpPage:
			lines = state.contextualHelpBody(current, width)
		case selectServicePage:
			lines = state.selectServiceBody(current, width)
		case reviewPage:
			lines = state.reviewBody(current, width)
		case reviewOptionsPage:
			lines = state.reviewOptionsBody(current, width)
		case explainPage:
			lines = state.explainBody(current, width)
		case detailsPage:
			lines = state.detailsBody(current, width)
		case confirmationPage:
			lines = state.confirmationBody(current, width)
		case healthConfirmationPage:
			lines = state.healthConfirmationBody(current, width)
		}
	}

	return state.appendBodyState(lines)
}

func (state *model) contextualHelpBody(current contextualHelpPage, width int) []string {
	lines := make([]string, 0, helpBodyBaseRows)
	lines = append(lines,
		state.title("Keyboard help"),
		"",
		"Keys for "+current.location+".",
		"",
		state.muted("AVAILABLE KEYS"),
	)
	lines = append(lines, terminaltext.Wrap(current.keys, max(width-detailsPadding, 1))...)
	lines = append(lines, "", "Press ? or Esc to return.")

	return lines
}

func (state *model) bodyStateRows() int {
	return len(state.appendBodyState(nil))
}

func (state *model) appendBodyState(lines []string) []string {
	_, reviewing := state.page.(reviewPage)
	if state.mutationOutcome != "" && (!reviewing || state.mutationOutcome != statusApplyCompleted) {
		lines = append(lines, "", state.success(state.symbol("✓ ", "OK ")+state.mutationOutcome))
	}
	if state.deploymentFailure != "" {
		lines = append(lines, "", state.failure(state.symbol("× ", "[FAILED] ")+"Action did not finish."),
			deploymentFailureStatus(state.deploymentFailure))
	}
	if state.err != nil {
		lines = append(lines, "", state.failure(state.symbol("× ", "[FAIL] ")+"Operation failed."),
			"Exit and run `maniud --debug tui` for diagnostic context.")
	}

	return lines
}

func (state *model) sourceDiagnosticBody(current sourceDiagnosticPage, width int) []string {
	lines := state.sourceDiagnosticLines(current.diagnostic, width)

	return lines[current.scroll:]
}

func (state *model) sourceDiagnosticLines(diagnostic SourceDiagnostic, width int) []string {
	position := displayUnavailable
	if diagnostic.Line > 0 {
		position = fmt.Sprintf("line %d", diagnostic.Line)
		if diagnostic.Column > 0 {
			position += fmt.Sprintf(", column %d", diagnostic.Column)
		}
	}
	reason, action := sourceDiagnosticText(diagnostic.Reason)
	lines := make([]string, 0, diagnosticBaseRows)
	lines = append(lines,
		state.title("Compose source needs attention"),
		"",
		state.muted("FILE"),
	)
	lines = append(lines, terminaltext.Wrap(diagnostic.File, max(width-detailsPadding, 1))...)
	lines = append(lines,
		"",
		state.muted("POSITION"),
		position,
		"",
		state.muted("REASON"),
	)
	lines = append(lines, terminaltext.Wrap(reason, max(width-detailsPadding, 1))...)
	lines = append(lines, "", state.muted("ACTION"))
	lines = append(lines, terminaltext.Wrap(action, max(width-detailsPadding, 1))...)

	return lines
}

func sourceDiagnosticText(reason SourceDiagnosticReason) (string, string) {
	switch reason {
	case DiagnosticYAMLSyntax:
		return "YAML syntax is invalid", "Fix the YAML syntax outside Maniud, exit, then rerun `maniud tui`."
	case DiagnosticYAMLStructure:
		return "YAML mapping is invalid",
			"Remove duplicate keys or invalid YAML values outside Maniud, exit, then rerun `maniud tui`."
	case DiagnosticYAMLUnsupported:
		return "YAML feature is not supported",
			"Replace the unsupported YAML feature outside Maniud, exit, then rerun `maniud tui`."
	case DiagnosticComposeValidation:
		return "Compose validation failed",
			"Fix the Compose fields or required variables outside Maniud, exit, then rerun `maniud tui`."
	default:
		return "Compose validation failed", "Fix the Compose file outside Maniud, exit, then rerun `maniud tui`."
	}
}

func (state *model) auxiliaryPageBody(width, height int) ([]string, bool) {
	if lines, valid := state.llmPageBody(width); valid {
		return lines, true
	}
	if lines, valid := state.registrationPageBody(width); valid {
		return lines, true
	}
	if lines, valid := state.commitPageBody(width, height); valid {
		return lines, true
	}
	if lines, valid := state.deploymentPageBody(width); valid {
		return lines, true
	}

	return state.serviceWorkspacePageBody(width)
}

func (state *model) homeBody(current homePage, width int) []string {
	return state.homeBodyWithin(current, width, int(^uint(0)>>1))
}

func (state *model) homeBodyWithin(current homePage, width, height int) []string {
	const homeActionRows = 3

	lines := []string{
		state.title("Services"),
		"Choose a registered service or open a committed Compose file.",
		"",
		state.muted("REGISTERED SERVICES"),
	}
	if current.catalog.State != CatalogReady || len(current.catalog.Services) == 0 {
		lines = append(lines, "  "+state.status)
	}
	fixedRows := len(lines) + homeActionRows
	if registrationSetupIndex(current.catalog) >= 0 {
		fixedRows++
	}
	start, end := visibleHomeServices(
		len(current.catalog.Services), current.cursor, max(height-fixedRows, 0),
	)
	for index := start; index < end; index++ {
		service := current.catalog.Services[index]
		label := service.Location
		if service.Blocker == BlockerNone {
			label += "  " + service.Name + "  " + service.Runtime
		} else {
			label += "  Blocked: " + string(service.Blocker)
		}
		lines = append(lines, state.choice(index == current.cursor, label, width))
	}
	lines = append(lines, "",
		state.choice(current.cursor == addServiceIndex(current.catalog), "Add service", width),
		state.choice(current.cursor == openComposeIndex(current.catalog), "Open Compose file", width),
	)
	if registrationSetupIndex(current.catalog) >= 0 {
		lines = append(lines, state.choice(
			current.cursor == registrationSetupIndex(current.catalog), "Set up desired-state repository", width,
		))
	}

	return lines
}

func visibleHomeServices(total, cursor, limit int) (int, int) {
	limit = min(max(limit, 0), total)
	if limit == total {
		return 0, total
	}
	focus := min(max(cursor, 0), max(total-1, 0))
	start := min(max(focus-limit+1, 0), total-limit)

	return start, start + limit
}

func (state *model) openPathBody(current openPathPage, width int) []string {
	value := current.value
	if value == "" {
		value = "compose.yaml"
	}
	line := "Path  " + terminaltext.Middle(value, max(width-pathLabelWidth, 1), "…")

	return []string{
		state.title("Open Compose file"),
		"Enter a path from a clean Git checkout.",
		"",
		state.accent(line + state.symbol("▌", "_")),
	}
}

func (state *model) selectServiceBody(current selectServicePage, width int) []string {
	lines := make([]string, 0, selectionBaseRows+len(current.choices))
	lines = append(lines, state.title("Select service"), "Choose one service from this Compose project.", "")
	for index, choice := range current.choices {
		label := choice.project + " / " + choice.service + "  " + choice.runtime
		lines = append(lines, state.choice(index == current.cursor, label, width))
	}

	return lines
}

func (state *model) reviewBody(current reviewPage, width int) []string {
	plan := current.plan
	lines := make([]string, 0, reviewBodyBaseRows)
	title := "Review image change"
	description := "Compare the current and proposed image identities before continuing."
	if plan.health != application.HealthConvergenceNone {
		title = "Review workload health"
		description = "Review the bounded health state before choosing the next action."
	}
	lines = append(lines,
		state.title(title),
		description,
		"",
		"Platform  "+plan.platform,
		"Action    "+plan.kind,
	)
	if plan.health != application.HealthConvergenceNone {
		lines = append(lines, "Health    "+healthSummary(plan))
	}
	lines = append(lines, "")
	lines = append(lines, imageComparison(plan.current, plan.proposed, width)...)
	lines = append(lines, state.reviewStatusBody(current, width, false)...)

	return lines
}

//nolint:cyclop // Compact and full layouts render the same closed health and action states.
func (state *model) reviewStatusBody(review reviewPage, width int, compact bool) []string {
	plan := review.plan
	status := plan.status
	if state.mutationOutcome == statusApplyCompleted {
		status = statusApplyCompleted
	}
	lines := make([]string, 0, reviewStatusRows)
	if compact {
		lines = append(lines, state.statusTitle(status))
	} else {
		lines = append(lines, "")
		if plan.health == application.HealthConvergenceNone {
			lines = append(lines, state.statusCard(status, width)...)
		} else {
			lines = append(lines, state.healthStatusCard(plan, width)...)
		}
	}
	if plan.warningText != "" {
		lines = append(lines, state.failure(plan.warningText))
	}
	if latest := state.timeline.latestCorrelated(review.correlation); latest != "" {
		lines = append(lines, state.muted("Latest observation: "+latest))
	}
	if compact && plan.health == application.HealthConvergenceNone &&
		state.mutationOutcome != statusApplyCompleted && state.err == nil {
		lines = append(lines, "No runtime change has started.")
	}
	if !compact {
		lines = append(lines, "")
	}
	primary, secondary := "Continue to confirmation", "Explore options"
	if state.configReloadNeeded {
		primary = "Reload LLM configuration"
	} else if plan.health != application.HealthConvergenceNone {
		primary = healthActionLabel(plan)
		secondary = "View details"
	}
	lines = append(lines,
		state.choice(review.focus == reviewContinue, primary, width),
		state.choice(review.focus == reviewExplore, secondary, width),
	)

	return lines
}

func (state *model) reviewOptionsBody(current reviewOptionsPage, width int) []string {
	options := []string{
		"Explain this change",
		labelAskLLMDeployment,
		"Edit deployment parameters",
		"View deployment history",
	}
	lines := make([]string, 0, reviewOptionsRows+len(options))
	lines = append(lines,
		state.title("Explore options"),
		"Choose another way to inspect or update this service.",
		"",
	)
	for index, option := range options {
		lines = append(lines, state.choice(current.cursor == index, option, width))
	}

	return lines
}

func (state *model) explainBody(current explainPage, width int) []string {
	plan := current.review.plan
	lines := []string{
		state.title("Why this change"),
		"Maniud derived this explanation from the validated local plan.",
		"",
		"Action   " + plan.kind,
		"Service  " + terminaltext.Middle(plan.project+" / "+plan.service, max(width-serviceFieldWidth, 1), "…"),
		"Reason   The proposed immutable image identity differs from the current runtime identity.",
		"",
		"No provider request or mutation has started.",
	}

	return lines
}

func imageComparison(current, proposed string, width int) []string {
	gap := 3
	column := max((width-gap)/comparisonColumns, 1)

	return []string{
		padCells("CURRENT", column) + strings.Repeat(" ", gap) + "PROPOSED",
		padCells(terminaltext.Middle(current, column, "…"), column) + strings.Repeat(" ", gap) +
			terminaltext.Middle(proposed, column, "…"),
	}
}

func (state *model) detailsBody(current detailsPage, width int) []string {
	lines := state.detailsLines(current.review, width)

	return lines[current.scroll:]
}

func (state *model) detailsLines(review reviewPage, width int) []string {
	available := max(width-detailsPadding, 1)
	projection := state.detailProjection(review)
	lines := make([]string, 0, detailsBaseRows)
	title := "Image details"
	description := "Full image identities. This page is read-only."
	if review.plan.health != application.HealthConvergenceNone {
		title = "Workload health details"
		description = "Bounded runtime health and image identities. This page is read-only."
	}
	lines = append(lines,
		state.title(title),
		description,
		"",
		state.muted("CURRENT"),
	)
	lines = append(lines, terminaltext.Wrap(projection.current, available)...)
	lines = append(lines, "", state.muted("PROPOSED"))
	lines = append(lines, terminaltext.Wrap(projection.proposed, available)...)
	if review.plan.health != application.HealthConvergenceNone {
		lines = append(lines, "", state.muted("HEALTH"), healthSummary(review.plan))
	}
	lines = append(lines, "", state.muted("SESSION TIMELINE"))
	if len(projection.timeline) == 0 {
		lines = append(lines, "No application observations.")
	} else {
		for _, entry := range projection.timeline {
			lines = append(lines, terminaltext.Wrap(entry, available)...)
		}
	}
	lines = append(lines, fmt.Sprintf("Dropped events: %d", projection.dropped))
	if projection.truncated {
		lines = append(lines, "Timeline truncated: yes")
	}

	return lines
}

func (state *model) confirmationBody(current confirmationPage, width int) []string {
	plan := current.review.plan

	return []string{
		state.title("Confirm apply"),
		fmt.Sprintf("Apply %s to %s / %s?", plan.kind, plan.project, plan.service),
		"The runtime may pull an image and replace the managed workload.",
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Apply", width),
	}
}

func (state *model) healthConfirmationBody(current healthConfirmationPage, width int) []string {
	plan := current.review.plan
	title := "Confirm health decision"
	detail := "This action is no longer available if the transaction or workload health has changed."
	action := "Apply health decision"
	switch plan.resolution {
	case application.HealthResolutionRollback:
		action = "Rollback candidate"
		detail = "Maniud will stop and discard the transaction-owned candidate."
		if plan.restoresPrevious {
			detail += " It will then restore the previous workload."
		}
	case application.HealthResolutionCancelAdoption:
		action = "Cancel adoption"
		detail = "Maniud will remove only the local adoption intent. The unmanaged workload will remain unchanged."
	case application.HealthResolutionRetryRestoreStart:
		action = "Retry restore start"
		detail = "Maniud will restart the exact stopped predecessor shown here. " +
			"The rolled-back candidate will remain discarded."
	}

	return []string{
		state.title(title),
		fmt.Sprintf("Resolve health for %s / %s?", plan.project, plan.service),
		detail,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, action, width),
	}
}

func healthActionLabel(plan planView) string {
	switch plan.resolution {
	case application.HealthResolutionRollback:
		return "Rollback candidate"
	case application.HealthResolutionCancelAdoption:
		return "Cancel adoption"
	case application.HealthResolutionRetryRestoreStart:
		return "Retry restore start"
	default:
		return "Refresh health"
	}
}

func healthSummary(plan planView) string {
	state := plan.healthState
	if state == "" {
		state = displayUnavailable
	}
	if state == "unhealthy" && plan.healthFails > 0 {
		return fmt.Sprintf("%s (%d failing checks)", state, plan.healthFails)
	}

	return state
}

func (state *model) footer(width int) string {
	keys := state.footerKeys()
	if _, help := state.page.(contextualHelpPage); !help && !pageAcceptsText(state.page) &&
		!strings.Contains(keys, "? Help") {
		keys = "? Help   " + keys
	}

	return state.muted(terminaltext.Clip(keys, width))
}

func (state *model) footerKeys() string {
	if _, valid := state.page.(contextualHelpPage); valid {
		return "?/Esc Back   q Quit"
	}

	return state.pageFooterKeys()
}

func (state *model) pageFooterKeys() string {
	if keys, valid := state.workspaceFooter(); valid {
		return keys
	}
	if keys, valid := state.registrationFooter(); valid {
		return keys
	}
	keys := "↑/↓ Navigate   Enter Select   q Quit"
	switch state.page.(type) {
	case reviewPage:
		keys = "Tab Focus  Enter Choose  o Explore  d Details  x Export  r Refresh  Esc Back  q Quit"
	case reviewOptionsPage:
		keys = "Up/Down Navigate   Enter Select   Esc Back   q Quit"
	case explainPage:
		keys = "Enter/Esc Fresh review   q Quit"
	case detailsPage:
		keys = "↑/↓ Scroll   x Export   d/Esc Back   q Quit"
	case confirmationPage, healthConfirmationPage:
		keys = confirmationKeys
	case openPathPage:
		keys = "Type path   Enter Open   Esc Back"
	case sourceDiagnosticPage:
		keys = "Up/Down Scroll   Enter Back   q Quit"
	}

	return keys
}

func (state *model) workspaceFooter() (string, bool) {
	if keys, valid := state.llmWorkspaceFooter(); valid {
		return keys, true
	}
	if keys, valid := state.commitFooter(); valid {
		return keys, true
	}
	if keys, valid := state.deploymentWorkspaceFooter(); valid {
		return keys, true
	}
	if keys, valid := state.serviceWorkspaceFooter(); valid {
		return keys, true
	}

	return "", false
}

func (state *model) deploymentWorkspaceFooter() (string, bool) {
	switch state.page.(type) {
	case deploymentFieldsPage:
		return "Up/Down Navigate   Enter Edit   u Remove field   Esc Back", true
	case deploymentValuePage:
		return "Type value   Enter Validate   Esc Back", true
	case deploymentPreviewPage:
		return "Up/Down Scroll   d Details   Enter Continue   Esc Back   q Quit", true
	case deploymentDetailsPage:
		return scrollBackKeys, true
	case deploymentDiffPage:
		return scrollBackKeys, true
	case deploymentDraftConfirmationPage:
		return "Tab Focus   Enter Choose   Esc Continue editing", true
	case deploymentHistoryPage:
		return "Up/Down Navigate   Enter Review restore   Esc Back", true
	case stageDeploymentConfirmationPage:
		return "Tab Focus   Enter Choose   d Full diff   Esc Back   q Quit", true
	case restoreDeploymentConfirmationPage:
		return confirmationKeys, true
	}

	return "", false
}
