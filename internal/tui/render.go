package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
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
	reviewStatusRows   = 7
	detailsBaseRows    = 12
	diagnosticBaseRows = 16
	commitBaseRows     = 10
	stagedDiffBaseRows = 6
	colorAmber         = "\x1b[38;5;214m"
	colorMuted         = "\x1b[38;5;245m"
	colorSuccess       = "\x1b[38;5;42m"
	colorFailure       = "\x1b[38;5;196m"
	colorReset         = "\x1b[0m"
	confirmationKeys   = "Tab Focus   Enter Choose   Esc Back   q Quit"
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
		lines = append(lines, padCells(left, fullRailWidth)+state.symbol(" │ ", " | ")+right)
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

func (state *model) hardFloorView(width int) []string {
	next := "q Quit"
	switch state.page.(type) {
	case homePage:
		next = "Enter Open   q Quit"
	case openPathPage:
		next = "Esc Back   q Quit"
	case sourceDiagnosticPage:
		next = "Up/Down Scroll   Enter Back"
	case addServicePage, servicePreviewPage, stageServiceConfirmationPage,
		commitServicePage, stagedDiffPage, unsignedCommitConfirmationPage, preparationRequiredPage:
		next = "Esc Back   q Quit"
	case selectServicePage:
		next = "Enter Review   Esc Back"
	case reviewPage, detailsPage, confirmationPage:
		next = "r Review   Esc Back   q Quit"
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
	if location, valid := state.serviceWorkspaceLocation(); valid {
		return location
	}
	switch current := state.page.(type) {
	case reviewPage:
		return current.plan.project + " / " + current.plan.service + " / " + current.plan.runtime
	case detailsPage:
		return current.review.plan.project + " / " + current.review.plan.service + " / Details"
	case confirmationPage:
		return current.review.plan.project + " / " + current.review.plan.service + " / Confirm"
	case selectServicePage:
		return "Home / Select service"
	case openPathPage:
		return "Home / Open Compose file"
	case sourceDiagnosticPage:
		return "Home / Compose source issue"
	case registrationPage, registrationConfirmationPage:
		return "Home / Repository setup"
	default:
		return "Home"
	}
}

func (state *model) serviceWorkspaceLocation() (string, bool) {
	switch state.page.(type) {
	case addServicePage:
		return "Home / Add service / Input", true
	case servicePreviewPage, stageServiceConfirmationPage:
		return "Home / Add service / Preview", true
	case commitServicePage, stagedDiffPage, unsignedCommitConfirmationPage:
		return "Home / Add service / Commit", true
	case preparationRequiredPage:
		return "Home / Add service / Prepare", true
	default:
		return "", false
	}
}

func (state *model) rail() []string {
	if lines, valid := state.serviceWorkspaceRail(); valid {
		return lines
	}
	steps := []string{"Select", "Review", "Confirm", "Apply"}
	current := 0
	switch state.page.(type) {
	case reviewPage, detailsPage:
		current = 1
	case confirmationPage:
		current = 2
	}
	if state.busy && state.applying {
		if _, reviewing := state.page.(reviewPage); reviewing {
			current = 3
		}
	}

	return state.flowRail("FLOW", steps, current)
}

func (state *model) serviceWorkspaceRail() ([]string, bool) {
	current := -1
	switch state.page.(type) {
	case addServicePage:
		current = 0
	case servicePreviewPage, stageServiceConfirmationPage:
		current = 1
	case commitServicePage, stagedDiffPage, unsignedCommitConfirmationPage:
		current = 2
	case preparationRequiredPage:
		current = 3
	}
	if current < 0 {
		return nil, false
	}
	steps := []string{"Input", "Preview", "Commit", "Validate"}

	return state.flowRail("ADD SERVICE", steps, current), true
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
	lines = append(lines, "", state.muted("RETURN"), "Esc  Back", "q    Quit")

	return lines
}

func (state *model) body(width int) []string {
	return state.bodyWithin(width, int(^uint(0)>>1))
}

func (state *model) bodyWithin(width, height int) []string {
	lines, setupPage := state.auxiliaryPageBody(width)
	if !setupPage {
		switch current := state.page.(type) {
		case homePage:
			lines = state.homeBodyWithin(current, width, max(height-state.bodyStateRows(), 0))
		case openPathPage:
			lines = state.openPathBody(current, width)
		case sourceDiagnosticPage:
			lines = state.sourceDiagnosticBody(current, width)
		case selectServicePage:
			lines = state.selectServiceBody(current, width)
		case reviewPage:
			lines = state.reviewBody(current, width)
		case detailsPage:
			lines = state.detailsBody(current, width)
		case confirmationPage:
			lines = state.confirmationBody(current, width)
		}
	}

	return state.appendBodyState(lines)
}

func (state *model) bodyStateRows() int {
	return len(state.appendBodyState(nil))
}

func (state *model) appendBodyState(lines []string) []string {
	_, reviewing := state.page.(reviewPage)
	if state.mutationOutcome != "" && (!reviewing || state.mutationOutcome != statusApplyCompleted) {
		lines = append(lines, "", state.success(state.symbol("✓ ", "OK ")+state.mutationOutcome))
	}
	if state.err != nil {
		lines = append(lines, "", state.failure(state.symbol("× ", "[FAIL] ")+"Operation failed."),
			"Exit and rerun the equivalent command with --debug for diagnostic context.")
	}

	return lines
}

func (state *model) sourceDiagnosticBody(current sourceDiagnosticPage, width int) []string {
	lines := state.sourceDiagnosticLines(current.diagnostic, width)

	return lines[current.scroll:]
}

func (state *model) sourceDiagnosticLines(diagnostic SourceDiagnostic, width int) []string {
	position := "Unavailable"
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
	lines = append(lines, "", state.muted("Up/Down Scroll   Enter Back   q Quit"))

	return lines
}

func sourceDiagnosticText(reason SourceDiagnosticReason) (string, string) {
	switch reason {
	case DiagnosticYAMLSyntax:
		return "YAML syntax is invalid", "Fix the YAML syntax, then retry."
	case DiagnosticYAMLStructure:
		return "YAML mapping is invalid", "Remove duplicate keys or invalid YAML values, then retry."
	case DiagnosticYAMLUnsupported:
		return "YAML feature is not supported", "Replace the unsupported YAML feature with explicit values, then retry."
	case DiagnosticComposeValidation:
		return "Compose validation failed", "Fix the Compose fields or required variables, then retry."
	default:
		return "Compose validation failed", "Fix the Compose file, then retry."
	}
}

func (state *model) auxiliaryPageBody(width int) ([]string, bool) {
	if lines, valid := state.registrationPageBody(width); valid {
		return lines, true
	}

	return state.serviceWorkspacePageBody(width)
}

func (state *model) serviceWorkspacePageBody(width int) ([]string, bool) {
	switch current := state.page.(type) {
	case addServicePage:
		return state.addServiceBody(current, width), true
	case servicePreviewPage:
		return state.servicePreviewBody(current, width), true
	case stageServiceConfirmationPage:
		return state.stageServiceConfirmationBody(current, width), true
	case commitServicePage:
		return state.commitServiceBody(current, width), true
	case stagedDiffPage:
		return state.stagedDiffBody(current, width), true
	case unsignedCommitConfirmationPage:
		return state.unsignedCommitConfirmationBody(current, width), true
	case preparationRequiredPage:
		return state.preparationRequiredBody(current, width), true
	default:
		return nil, false
	}
}

func (state *model) registrationPageBody(width int) ([]string, bool) {
	switch current := state.page.(type) {
	case registrationPage:
		return state.registrationBody(current, width), true
	case registrationConfirmationPage:
		return state.registrationConfirmationBody(current, width), true
	default:
		return nil, false
	}
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

func (state *model) addServiceBody(current addServicePage, width int) []string {
	value := current.value
	if value == "" {
		value = "docker://registry.example/service@sha256:..."
	}

	return []string{
		state.title("Add service"),
		"Enter a fixed image URI or a complete docker, podman, or nerdctl create/run command.",
		"",
		state.accent(terminaltext.Middle(value, width, "…") + state.symbol("▌", "_")),
		"",
		"Examples:",
		"  docker://registry.example/service@sha256:<digest>",
		"  docker run --name service registry.example/service@sha256:<digest>",
		"",
		state.muted("Enter Preview   Esc Back"),
	}
}

func (state *model) servicePreviewBody(current servicePreviewPage, width int) []string {
	draft := current.draft
	title := "Review parsed service"
	description := "The pasted command was parsed, not run."
	continueLabel := "Review file mutation"
	if draft.Recovered {
		title = "Previous draft found"
		description = "The saved draft matches this service input. Continue to review and commit it."
		continueLabel = "Continue saved draft"
	}
	lines := []string{
		state.title(title),
		description,
		"",
		"Runtime   " + terminaltext.Middle(draft.Runtime, max(width-serviceFieldWidth, 1), "…"),
		"Service   " + terminaltext.Middle(draft.Service, max(width-serviceFieldWidth, 1), "…"),
		"Image     " + terminaltext.Middle(draft.Image, max(width-serviceFieldWidth, 1), "…"),
		"Compose   " + terminaltext.Middle(draft.ComposePath, max(width-serviceFieldWidth, 1), "…"),
	}
	if draft.Preparation != "" {
		lines = append(lines,
			"Prepare   "+terminaltext.Middle(draft.Preparation, max(width-serviceFieldWidth, 1), "…"),
		)
	}
	if draft.WarningCount > 0 {
		lines = append(lines, fmt.Sprintf("Warnings  %d item(s) require review", draft.WarningCount))
	}
	lines = append(lines, "", state.choice(true, continueLabel, width), state.muted("Enter Continue   Esc Edit"))

	return lines
}

func (state *model) stageServiceConfirmationBody(
	current stageServiceConfirmationPage,
	width int,
) []string {
	draft := current.preview.draft
	action := "Write and stage files"
	description := "Write the generated files to the desired-state repository and stage them in Git?"
	if draft.Recovered {
		action = "Stage saved draft"
		description = "Stage the saved draft files in Git?"
	}
	lines := []string{
		state.title("Confirm file mutation"),
		description,
		"No commit or runtime operation will run on this page.",
		"",
		"Compose   " + terminaltext.Middle(draft.ComposePath, max(width-serviceFieldWidth, 1), "…"),
	}
	if draft.Preparation != "" {
		lines = append(lines,
			"Prepare   "+terminaltext.Middle(draft.Preparation, max(width-serviceFieldWidth, 1), "…"),
		)
	}
	lines = append(lines,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, action, width),
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
	)

	return lines
}

func (state *model) commitServiceBody(current commitServicePage, width int) []string {
	message := terminaltext.Middle(current.message, max(width-serviceFieldWidth, 1), "…")
	if current.editing {
		message += state.symbol("▌", "_")
	}
	diff := stagedServiceDiffLines(current, width)
	diff = diff[current.scroll:min(current.scroll+diffSummaryRows, len(diff))]
	lines := make([]string, 0, commitBaseRows+len(diff))
	lines = append(lines,
		state.title("Review and commit"),
		"Target    "+terminaltext.Middle(current.staged.ComposePath, max(width-serviceFieldWidth, 1), "…"),
		"Message   "+message,
		"",
		state.muted("STAGED DIFF"),
	)
	lines = append(lines, diff...)
	lines = append(lines, "", state.choice(current.focus == confirmationBack, "Back and save draft", width),
		state.choice(current.focus == confirmationApply, "Create signed commit", width), "",
		state.muted("e Edit message   d Full diff   Up/Down Scroll   Tab Focus   Enter Select"))

	return lines
}

func (state *model) stagedDiffBody(current stagedDiffPage, width int) []string {
	lines := state.stagedDiffLines(current.commit, width)

	return lines[current.scroll:]
}

func (state *model) stagedDiffLines(commit commitServicePage, width int) []string {
	lines := make([]string, 0, stagedDiffBaseRows)
	lines = append(lines, state.title("Staged diff"), "This page is read-only.", "")
	lines = append(lines, stagedServiceDiffLines(commit, width)...)
	lines = append(lines, "", state.muted("Up/Down Scroll   d/Esc Back"))

	return lines
}

func stagedServiceDiffLines(current commitServicePage, width int) []string {
	available := max(width-detailsPadding, 1)
	if current.diffWidth == available && current.diffLines != nil {
		return current.diffLines
	}

	return terminaltext.Wrap(current.staged.Diff, available)
}

func (state *model) unsignedCommitConfirmationBody(
	current unsignedCommitConfirmationPage,
	width int,
) []string {
	message := terminaltext.Middle(current.commit.message, max(width-serviceFieldWidth, 1), "…")

	return []string{
		state.title("Confirm unsigned commit"),
		"Git could not create a signed commit with the configured identity and signing key.",
		"The staged files are unchanged. Continue only if an unsigned history entry is acceptable.",
		"",
		"Message   " + message,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Create unsigned commit", width),
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
	}
}

func (state *model) preparationRequiredBody(current preparationRequiredPage, width int) []string {
	return []string{
		state.title("Preparation required"),
		"The Compose commit was created, but this service needs a host preparation step.",
		"Apply is disabled for the rest of this TUI session.",
		"",
		"Service   " + terminaltext.Middle(current.draft.Service, max(width-serviceFieldWidth, 1), "…"),
		"Prepare   " + terminaltext.Middle(current.draft.Preparation, max(width-serviceFieldWidth, 1), "…"),
		"",
		state.choice(true, "Exit to next steps", width),
		state.muted("Enter/Esc Exit"),
	}
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
		"",
		state.muted("Enter  Open    Esc  Back"),
	}
}

func (state *model) registrationBody(current registrationPage, width int) []string {
	switch current.step {
	case registrationModeStep:
		return []string{
			state.title("Set up repository"),
			"Choose where the desired-state repository comes from.",
			"",
			state.choice(current.cursor == 0, "Create private GitHub repository", width),
			state.choice(current.cursor == 1, "Use existing Git repository", width),
			"",
			state.muted("Up/Down Choose   Enter Continue   Esc Skip"),
		}
	case registrationRemoteStep:
		title := "GitHub repository"
		description := "Enter OWNER/REPOSITORY. Maniud will invoke gh and create a private repository."
		label := "Name  "
		if current.mode == RepositorySetupExisting {
			title = "Existing Git repository"
			description = "Enter an HTTPS or file remote. This path uses git and does not invoke gh."
			label = "Remote  "
		}
		line := label + terminaltext.Middle(current.remote, max(width-terminaltext.Width(label), 1), "…")

		return []string{
			state.title(title), description, "", state.accent(line + state.symbol("▌", "_")), "",
			state.muted("Enter Continue   Esc Back"),
		}
	case registrationCheckoutStep:
		label := "Path  "
		line := label + terminaltext.Middle(current.checkout, max(width-terminaltext.Width(label), 1), "…")

		return []string{
			state.title("Local checkout"),
			"Choose an absolute path for the desired-state checkout.",
			"",
			state.accent(line + state.symbol("▌", "_")),
			"",
			state.muted("Enter Review   Esc Back"),
		}
	default:
		return []string{state.title("Set up repository"), "", "Repository setup is unavailable"}
	}
}

func (state *model) registrationConfirmationBody(
	current registrationConfirmationPage,
	width int,
) []string {
	request := registrationRequest(current.registration)
	remote := terminaltext.Middle(request.Remote, max(width-pathLabelWidth, 1), "…")
	path := terminaltext.Middle(request.Checkout, max(width-pathLabelWidth, 1), "…")
	description := "Create the private GitHub repository, clone it, and register the checkout?"
	action := "Create and register"
	if request.Mode == RepositorySetupExisting {
		description = "Clone or reuse this Git repository and register the clean checkout?"
		action = "Clone and register"
	}

	return []string{
		state.title("Confirm repository setup"),
		description,
		"",
		"Source  " + remote,
		"Path    " + path,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, action, width),
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
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
	lines = append(lines,
		state.title("Review image change"),
		"Compare the current and proposed image identities before continuing.",
		"",
		fmt.Sprintf("Service   %s / %s", plan.project, plan.service),
		fmt.Sprintf("Runtime   %s on %s", plan.runtime, plan.platform),
		"Action    "+plan.kind,
		"",
	)
	lines = append(lines, imageComparison(plan.current, plan.proposed, width)...)
	lines = append(lines, state.reviewStatusBody(current, width, false)...)

	return lines
}

func (state *model) reviewStatusBody(review reviewPage, width int, compact bool) []string {
	plan := review.plan
	status := plan.status
	if state.mutationOutcome == statusApplyCompleted {
		status = statusApplyCompleted
	}
	lines := make([]string, 0, reviewStatusRows)
	if !compact {
		lines = append(lines, "")
	}
	lines = append(lines, state.statusCard(status))
	if plan.warningText != "" {
		lines = append(lines, state.failure(plan.warningText))
	}
	if !compact {
		lines = append(lines, "Compose validation and read-only runtime checks passed.")
	}
	if latest := state.timeline.latestCorrelated(review.correlation); latest != "" {
		lines = append(lines, state.muted("Latest observation: "+latest))
	}
	if state.mutationOutcome != statusApplyCompleted {
		lines = append(lines, "No runtime change has started.")
	}
	if !compact {
		lines = append(lines, "")
	}
	lines = append(lines, state.choice(true, "Continue to confirmation", width))
	if !compact {
		lines = append(lines, state.muted("d Details   x Export   r Refresh   Esc Back"))
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
	lines = append(lines,
		state.title("Image details"),
		"Full image identities. This page is read-only.",
		"",
		state.muted("CURRENT"),
	)
	lines = append(lines, terminaltext.Wrap(projection.current, available)...)
	lines = append(lines, "", state.muted("PROPOSED"))
	lines = append(lines, terminaltext.Wrap(projection.proposed, available)...)
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
	lines = append(lines, "", state.muted("Up/Down Scroll   x Export   d/Esc Back"))

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
		"",
		state.muted("Tab Change focus   Enter Choose   Esc Back"),
	}
}

func (state *model) footer(width int) string {
	if keys, valid := state.serviceWorkspaceFooter(); valid {
		return state.muted(terminaltext.Clip(keys, width))
	}
	keys := "↑/↓ Navigate   Enter Select   q Quit"
	switch state.page.(type) {
	case reviewPage:
		keys = "Enter Continue   d Details   x Export   r Refresh   Esc Back   q Quit"
	case detailsPage:
		keys = "↑/↓ Scroll   x Export   d/Esc Back   q Quit"
	case confirmationPage:
		keys = confirmationKeys
	case openPathPage:
		keys = "Type path   Enter Open   Esc Back"
	case registrationPage:
		keys = "Edit path   Enter Review   Esc Skip"
	case registrationConfirmationPage:
		keys = confirmationKeys
	}

	return state.muted(terminaltext.Clip(keys, width))
}

func (state *model) serviceWorkspaceFooter() (string, bool) {
	switch state.page.(type) {
	case addServicePage:
		return "Type input   Enter Preview   Esc Back", true
	case servicePreviewPage:
		return "Enter Continue   Esc Edit   q Quit", true
	case stageServiceConfirmationPage, unsignedCommitConfirmationPage:
		return confirmationKeys, true
	case commitServicePage:
		return "e Edit   d Diff   Tab Focus   Enter Choose   q Quit", true
	case stagedDiffPage:
		return "Up/Down Scroll   d/Esc Back   q Quit", true
	case preparationRequiredPage:
		return "Enter/Esc Exit", true
	default:
		return "", false
	}
}

func (state *model) statusCard(status string) string {
	if status == statusApplyCompleted {
		return state.success(state.symbol("✓ ", "[x] ") + status)
	}

	return state.accent(state.symbol("⬟ ", "[OK] ") + status)
}

func (state *model) choice(selected bool, label string, width int) string {
	marker := "  "
	if selected {
		marker = state.accent(state.symbol("› ", "> "))
	}

	return marker + terminaltext.Middle(label, max(width-terminaltext.Width(marker), 1), "…")
}

func (state *model) symbol(unicodeValue, asciiValue string) string {
	if state.options.Unicode {
		return unicodeValue
	}

	return asciiValue
}

func (state *model) title(value string) string {
	return state.accent(value)
}

func (state *model) accent(value string) string {
	return state.color(colorAmber, value)
}

func (state *model) muted(value string) string {
	return state.color(colorMuted, value)
}

func (state *model) success(value string) string {
	return state.color(colorSuccess, value)
}

func (state *model) failure(value string) string {
	return state.color(colorFailure, value)
}

func (state *model) color(code, value string) string {
	if !state.options.Color {
		return value
	}

	return code + value + colorReset
}

func padCells(value string, width int) string {
	clipped := terminaltext.Clip(value, width)

	return clipped + strings.Repeat(" ", max(width-terminaltext.Width(clipped), 0))
}

func fitView(lines []string, width, height int) string {
	visible := lines[:min(len(lines), height)]
	for index, line := range visible {
		visible[index] = terminaltext.Clip(line, width)
	}

	return strings.Join(visible, "\n")
}
