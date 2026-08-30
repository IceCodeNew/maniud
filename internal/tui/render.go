package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	defaultWidth      = 80
	defaultHeight     = 24
	fullMinimumWidth  = 80
	fullMinimumHeight = 24
	compactMinimum    = 56
	compactMinHeight  = 16
	hardMinimumWidth  = 32
	hardMinimumHeight = 8
	fullRailWidth     = 18
	fullBodyOffset    = 20
	fullFrameRows     = 4
	compactFrameRows  = 5
	pathLabelWidth    = 6
	comparisonColumns = 2
	detailsPadding    = 2
	railExtraLines    = 6
	selectionBaseRows = 3
	detailsBaseRows   = 12
	colorAmber        = "\x1b[38;5;214m"
	colorMuted        = "\x1b[38;5;245m"
	colorSuccess      = "\x1b[38;5;42m"
	colorFailure      = "\x1b[38;5;196m"
	colorReset        = "\x1b[0m"
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
	switch layoutFor(width, height) {
	case layoutFull:
		lines = state.fullView(width, height)
	case layoutCompact:
		lines = state.compactView(width, height)
	case layoutHardFloor:
		lines = state.hardFloorView(width)
	case layoutResize:
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
	body := state.body(bodyWidth, false)
	contentHeight := min(max(len(rail), len(body)), max(height-fullFrameRows, 0))
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
	body := state.body(width, true)
	body = body[:min(len(body), max(height-compactFrameRows, 0))]
	lines := make([]string, 0, compactFrameRows+len(body))
	lines = append(lines, state.header(width), horizontal, state.locationLine())
	lines = append(lines, body...)
	lines = append(lines, horizontal, state.footer(width))

	return lines
}

func (state *model) hardFloorView(width int) []string {
	next := "q Quit"
	switch state.page.(type) {
	case homePage:
		next = "Enter Open   q Quit"
	case openPathPage:
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
	case registrationPage, registrationConfirmationPage:
		return "Home / Repository setup"
	default:
		return "Home"
	}
}

func (state *model) rail() []string {
	steps := []string{"Select", "Review", "Confirm", "Apply"}
	current := 0
	switch state.page.(type) {
	case reviewPage, detailsPage:
		current = 1
	case confirmationPage:
		current = 2
	}
	if state.busy && state.status == "Applying change" {
		if _, reviewing := state.page.(reviewPage); reviewing {
			current = 3
		}
	}

	lines := make([]string, 0, len(steps)+railExtraLines)
	lines = append(lines, state.muted("FLOW"), "")
	for index, step := range steps {
		marker := "  "
		if index == current {
			marker = state.accent(state.symbol("◆", ">")) + " "
		} else if index < current {
			marker = state.success(state.symbol("✓", "*")) + " "
		}
		lines = append(lines, marker+step)
	}
	lines = append(lines, "", state.muted("RETURN"), "Esc  Back", "q    Quit")

	return lines
}

func (state *model) body(width int, compact bool) []string {
	lines, setupPage := state.registrationPageBody(width)
	if !setupPage {
		switch current := state.page.(type) {
		case homePage:
			lines = state.homeBody(current, width)
		case openPathPage:
			lines = state.openPathBody(current, width)
		case selectServicePage:
			lines = state.selectServiceBody(current, width)
		case reviewPage:
			lines = state.reviewBody(current, width, compact)
		case detailsPage:
			lines = state.detailsBody(current, width)
		case confirmationPage:
			lines = state.confirmationBody(current, width)
		}
	}

	if state.mutationOutcome != "" {
		lines = append(lines, "", state.success(state.symbol("✓ ", "OK ")+state.mutationOutcome))
	}
	if state.err != nil {
		lines = append(lines, "", state.failure("Operation failed."),
			"Exit and rerun the equivalent command with --debug for diagnostic context.")
	}

	return lines
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
	lines := []string{
		state.title("Services"),
		"Choose a registered service or open a committed Compose file.",
		"",
		state.muted("REGISTERED SERVICES"),
	}
	if current.catalog.State != CatalogReady || len(current.catalog.Services) == 0 {
		lines = append(lines, "  "+state.status)
	}
	for index, service := range current.catalog.Services {
		label := service.Location
		if service.Blocker == BlockerNone {
			label += "  " + service.Name + "  " + service.Runtime
		} else {
			label += "  Blocked: " + string(service.Blocker)
		}
		lines = append(lines, state.choice(index == current.cursor, label, width))
	}
	lines = append(lines, "", state.choice(current.cursor == len(current.catalog.Services), "Open Compose file", width))
	if registrationSetupIndex(current.catalog) >= 0 {
		lines = append(lines, state.choice(
			current.cursor == len(current.catalog.Services)+1, "Set up desired-state repository", width,
		))
	}

	return lines
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
	line := "Path  " + terminaltext.Middle(current.value, max(width-pathLabelWidth, 1), "…")

	return []string{
		state.title("Set up repository"),
		"Create or register the desired-state repository used by maniud.",
		"",
		line,
		"",
		state.muted("Enter Review   Esc Skip"),
	}
}

func (state *model) registrationConfirmationBody(
	current registrationConfirmationPage,
	width int,
) []string {
	path := terminaltext.Middle(current.registration.value, max(width-pathLabelWidth, 1), "…")

	return []string{
		state.title("Confirm repository setup"),
		"Create a Git repository when the path is absent, or register a clean existing checkout.",
		"",
		"Path  " + path,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Set up repository", width),
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

func (state *model) reviewBody(current reviewPage, width int, compact bool) []string {
	plan := current.plan
	lines := []string{
		state.title("Review image change"),
		"Compare the current and proposed image identities before continuing.",
		"",
		fmt.Sprintf("Service   %s / %s", plan.project, plan.service),
		fmt.Sprintf("Runtime   %s on %s", plan.runtime, plan.platform),
		"Action    " + plan.kind,
		"",
	}
	if compact {
		lines = append(lines,
			state.muted("CURRENT"), terminaltext.Middle(plan.current, width, "…"),
			state.muted("PROPOSED"), terminaltext.Middle(plan.proposed, width, "…"),
		)
	} else {
		lines = append(lines, imageComparison(plan.current, plan.proposed, width)...)
	}
	lines = append(lines, "", state.statusCard(plan.status))
	if plan.warningText != "" {
		lines = append(lines, state.failure(plan.warningText))
	}
	lines = append(lines,
		"Compose validation and read-only runtime checks passed.",
		"No runtime change has started.",
		"",
		state.choice(true, "Continue to confirmation", width),
		state.muted("d Details   r Refresh   Esc Back"),
	)

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
	available := max(width-detailsPadding, 1)
	lines := make([]string, 0, detailsBaseRows)
	lines = append(lines,
		state.title("Image details"),
		"Full image identities. This page is read-only.",
		"",
		state.muted("CURRENT"),
	)
	lines = append(lines, terminaltext.Wrap(current.review.plan.current, available)...)
	lines = append(lines, "", state.muted("PROPOSED"))
	lines = append(lines, terminaltext.Wrap(current.review.plan.proposed, available)...)
	lines = append(lines, "", state.muted("Up/Down Scroll   d/Esc Back"))
	if current.scroll >= len(lines) {
		current.scroll = max(len(lines)-1, 0)
	}

	return lines[current.scroll:]
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
	keys := "↑/↓ Navigate   Enter Select   q Quit"
	switch state.page.(type) {
	case reviewPage:
		keys = "Enter Continue   d Details   r Refresh   Esc Back   q Quit"
	case detailsPage:
		keys = "↑/↓ Scroll   d/Esc Back   q Quit"
	case confirmationPage:
		keys = "Tab Focus   Enter Choose   Esc Back   q Quit"
	case openPathPage:
		keys = "Type path   Enter Open   Esc Back"
	case registrationPage:
		keys = "Edit path   Enter Review   Esc Skip"
	case registrationConfirmationPage:
		keys = "Tab Focus   Enter Choose   Esc Back   q Quit"
	}

	return state.muted(terminaltext.Clip(keys, width))
}

func (state *model) statusCard(status string) string {
	return state.success(state.symbol("◆ ", "OK ") + status)
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
