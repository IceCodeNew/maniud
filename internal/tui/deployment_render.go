package tui

import (
	"fmt"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	deploymentMinimumVisibleRows = 4
	deploymentMaximumVisibleRows = 12
	deploymentFrameRows          = 10
	deploymentComparisonGap      = 2
	deploymentComparisonLabel    = 18
	deploymentComparisonParts    = 3
	deploymentComposeDefault     = "Compose default"
	deploymentDiffLeadRows       = 4
	deploymentReviewAction       = "Review file mutation"
	deploymentReviewEmpty        = "No deployment parameters differ in this revision."
	deploymentReviewReady        = "Ready for file review"
	deploymentReviewTitle        = "Review deployment edit"
)

//nolint:cyclop // The page type switch is the deployment workflow's closed rendering table.
func (state *model) deploymentPageBody(width int) ([]string, bool) {
	switch current := state.page.(type) {
	case deploymentFieldsPage:
		return state.deploymentFieldsBody(current, width), true
	case deploymentValuePage:
		return state.deploymentValueBody(current, width), true
	case deploymentPreviewPage:
		return state.deploymentPreviewBody(current, width), true
	case deploymentDetailsPage:
		return state.deploymentDetailsBody(current, width), true
	case stageDeploymentConfirmationPage:
		return state.stageDeploymentConfirmationBody(current, width), true
	case deploymentDiffPage:
		return state.deploymentDiffBody(current, width, state.deploymentDiffViewportHeight()), true
	case deploymentDraftConfirmationPage:
		return state.deploymentDraftConfirmationBody(current, width), true
	case deploymentHistoryPage:
		return state.deploymentHistoryBody(current, width), true
	case restoreDeploymentConfirmationPage:
		return state.restoreDeploymentConfirmationBody(current, width), true
	default:
		return nil, false
	}
}

func (state *model) deploymentFieldsBody(current deploymentFieldsPage, width int) []string {
	lines := []string{
		state.title("Edit deployment"),
		"Choose one Compose field to change.",
	}
	if current.draft.dirty() {
		lines = append(lines, state.muted("Unsaved"))
	}
	lines = append(lines, "")
	limit := max(
		min(state.height-deploymentFrameRows, deploymentMaximumVisibleRows),
		deploymentMinimumVisibleRows,
	)
	start, end := visibleSelection(len(current.fields), current.cursor, limit)
	if start > 0 {
		lines = append(lines, state.muted(fmt.Sprintf("%d earlier field(s)", start)))
	}
	for index := start; index < end; index++ {
		field := current.fields[index]
		value := deploymentComposeDefault
		if field.Present {
			value = field.Value
		}
		if !field.Available {
			value = displayUnavailable
		}
		label := deploymentFieldLabel(field.ID) + "  " + value
		lines = append(lines, state.choice(index == current.cursor, label, width))
	}
	if end < len(current.fields) {
		lines = append(lines, state.muted(fmt.Sprintf("%d later field(s)", len(current.fields)-end)))
	}

	return lines
}

func visibleSelection(total, cursor, limit int) (int, int) {
	if total <= limit {
		return 0, total
	}
	start := max(cursor-limit/2, 0)
	start = min(start, total-limit)

	return start, start + limit
}

func (state *model) deploymentValueBody(current deploymentValuePage, width int) []string {
	value := current.value
	if value == "" {
		value = deploymentFieldPlaceholder(current.field.ID)
	}
	currentValue := deploymentComposeDefault
	if current.field.Present {
		currentValue = current.field.Value
	}

	lines := []string{
		state.title("Set deployment value"),
		deploymentFieldLabel(current.field.ID),
	}
	if current.fields.draft.dirty() {
		lines = append(lines, state.muted("Unsaved"))
	}
	lines = append(lines,
		"",
		"Current   "+terminaltext.Middle(currentValue, max(width-serviceFieldWidth, 1), "…"),
		"New       "+state.accent(terminaltext.Middle(value, max(width-serviceFieldWidth, 1), "…")+
			state.symbol("▌", "_")),
		"",
		deploymentFieldHint(current.field.ID),
	)

	return lines
}

func (state *model) deploymentPreviewBody(current deploymentPreviewPage, width int) []string {
	header, comparison, tail, available := state.deploymentPreviewSections(current, width)
	start := min(current.scroll, max(len(comparison)-available, 0))
	end := min(start+available, len(comparison))
	lines := make([]string, 0, len(header)+available+len(tail))
	lines = append(lines, header...)
	lines = append(lines, comparison[start:end]...)
	lines = append(lines, tail...)

	return lines
}

func (state *model) deploymentPreviewSections(
	current deploymentPreviewPage,
	width int,
) (header, comparison, tail []string, available int) {
	preview := current.preview
	header = []string{
		state.title(deploymentReviewTitle),
		state.muted("Unsaved"),
		"Compose   " + terminaltext.Middle(preview.ComposePath, max(width-serviceFieldWidth, 1), "…"),
	}
	if preview.Restore != "" {
		header = append(header, "Revision  "+preview.Restore[:12])
	}
	comparison = state.deploymentComparisonLines(preview.Changes, width)
	if len(comparison) == 0 {
		comparison = []string{deploymentReviewEmpty}
	}
	height := state.height
	if height == 0 {
		height = defaultHeight
	}
	frameRows := compactFrameRows
	full := layoutFor(state.width, state.height) == layoutFull
	if full {
		frameRows = fullFrameRows
	}
	status := []string{"", state.statusTitle(deploymentReviewReady)}
	if full {
		status = append([]string{""}, state.deploymentPreviewStatusCard(width)...)
	}
	status = append(status, state.choice(true, deploymentReviewAction, width))
	tail = status
	available = max(height-frameRows-len(header)-len(tail)-state.bodyStateRows(), 0)

	return header, comparison, tail, available
}

func (state *model) deploymentDetailsBody(current deploymentDetailsPage, width int) []string {
	lines := state.deploymentDetailsLines(current.preview.preview.Changes, width)

	return lines[min(current.scroll, len(lines)):]
}

func (state *model) deploymentDetailsLines(changes []DeploymentFieldChange, width int) []string {
	available := max(width-detailsPadding, 1)
	lines := []string{
		state.title("Deployment value details"),
		"Complete values for every changed field. This page is read-only.",
		"",
	}
	for _, change := range changes {
		lines = append(lines, state.muted(strings.ToUpper(deploymentFieldLabel(change.FieldID))))
		lines = append(lines, terminaltext.Wrap(
			"Current   "+deploymentPreviewValue(change.CurrentValue, change.CurrentPresent), available,
		)...)
		lines = append(lines, terminaltext.Wrap(
			"Proposed  "+deploymentPreviewValue(change.ProposedValue, change.ProposedPresent), available,
		)...)
		lines = append(lines, "")
	}
	if len(changes) == 0 {
		lines = append(lines, deploymentReviewEmpty)
	}

	return lines
}

func (state *model) deploymentComparisonLines(changes []DeploymentFieldChange, width int) []string {
	groups := []struct {
		label  string
		fields []application.DeploymentField
	}{
		{label: "RESOURCES", fields: []application.DeploymentField{
			application.DeploymentCPUs, application.DeploymentMemory,
			application.DeploymentPIDs, application.DeploymentSharedMemory,
		}},
		{label: "LIFECYCLE", fields: []application.DeploymentField{
			application.DeploymentRestart, application.DeploymentStopGrace, application.DeploymentInit,
		}},
		{label: "HEALTH & SAFETY", fields: []application.DeploymentField{
			application.DeploymentReadOnly, application.DeploymentNoNewPrivileges,
			application.DeploymentHealthInterval, application.DeploymentHealthTimeout,
			application.DeploymentHealthRetries, application.DeploymentHealthStartPeriod,
			application.DeploymentHealthStartInterval,
		}},
	}
	byField := make(map[string]DeploymentFieldChange, len(changes))
	for _, change := range changes {
		byField[change.FieldID] = change
	}
	lines := make([]string, 0, len(changes)+len(groups)*2)
	for _, group := range groups {
		groupChanges := make([]DeploymentFieldChange, 0, len(group.fields))
		for _, field := range group.fields {
			if change, valid := byField[field.ID()]; valid {
				groupChanges = append(groupChanges, change)
			}
		}
		if len(groupChanges) == 0 {
			continue
		}
		lines = append(lines, state.muted(group.label), deploymentComparisonHeader(width))
		for _, change := range groupChanges {
			lines = append(lines, deploymentComparisonRow(change, width))
		}
	}

	return lines
}

func deploymentComparisonHeader(width int) string {
	return deploymentComparisonColumns("FIELD", "CURRENT", "PROPOSED", width)
}

func deploymentComparisonRow(change DeploymentFieldChange, width int) string {
	return deploymentComparisonColumns(
		deploymentFieldLabel(change.FieldID),
		deploymentPreviewValue(change.CurrentValue, change.CurrentPresent),
		deploymentPreviewValue(change.ProposedValue, change.ProposedPresent),
		width,
	)
}

func deploymentComparisonColumns(label, current, proposed string, width int) string {
	labelWidth := min(deploymentComparisonLabel, max(width/deploymentComparisonParts, 1))
	valueWidth := max((width-labelWidth-deploymentComparisonGap*2)/comparisonColumns, 1)
	gap := strings.Repeat(" ", deploymentComparisonGap)

	return padCells(terminaltext.Middle(label, labelWidth, "…"), labelWidth) + gap +
		padCells(terminaltext.Middle(current, valueWidth, "…"), valueWidth) + gap +
		terminaltext.Middle(proposed, valueWidth, "…")
}

func deploymentPreviewValue(value string, present bool) string {
	if !present {
		return deploymentComposeDefault
	}

	return value
}

func (state *model) deploymentPreviewStatusCard(width int) []string {
	return state.statusCardWith(
		deploymentReviewReady,
		"The changed deployment values passed fresh Compose validation.",
		"No file has changed.",
		width,
	)
}

func (state *model) stageDeploymentConfirmationBody(
	current stageDeploymentConfirmationPage,
	width int,
) []string {
	diff := deploymentPreviewDiffLines(current.preview, width)
	diff = diff[:min(len(diff), diffSummaryRows)]
	lines := make([]string, 0, len(diff))
	lines = append(lines,
		state.title("Confirm file mutation"),
		state.muted("Unsaved"),
		"Replace the selected Compose file and stage the reviewed Git blob?",
		"No commit or runtime operation will run on this page.",
		"",
		"Compose   "+terminaltext.Middle(
			current.preview.preview.ComposePath, max(width-serviceFieldWidth, 1), "…",
		),
		"",
		state.muted("EXACT COMMIT DIFF"),
	)
	lines = append(lines, diff...)
	lines = append(lines, "", state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Write and stage edit", width))

	return lines
}

func (state *model) deploymentDiffBody(current deploymentDiffPage, width, height int) []string {
	lead := []string{
		state.title("Exact commit diff"),
		state.muted("Unsaved"),
		"This read-only diff is bound to the file confirmation.",
		"",
	}
	diff := deploymentPreviewDiffLines(current.confirmation.preview, width)
	total := len(lead) + len(diff)
	start := min(max(current.scroll, 0), max(total-height, 0))
	end := min(start+max(height, 0), total)
	lines := make([]string, 0, end-start)
	if leadEnd := min(end, len(lead)); start < leadEnd {
		lines = append(lines, lead[start:leadEnd]...)
	}
	diffStart := max(start-len(lead), 0)
	diffEnd := max(end-len(lead), 0)
	if diffStart < diffEnd {
		lines = append(lines, diff[diffStart:diffEnd]...)
	}

	return lines
}

func (state *model) deploymentDraftConfirmationBody(
	current deploymentDraftConfirmationPage,
	width int,
) []string {
	destination := "leave deployment editing"
	if current.quit {
		destination = "quit Maniud"
	}
	draftState := "The current value or validated Compose candidate has not been staged."
	if current.staged {
		draftState = "The staged Compose candidate has not been committed."
	}

	return []string{
		state.title("Discard unsaved deployment edit"),
		draftState,
		"Discard it and " + destination + "?",
		"",
		state.choice(current.focus == confirmationBack, "Continue editing", width),
		state.choice(current.focus == confirmationApply, "Discard and leave", width),
	}
}

func deploymentPreviewDiffLines(current deploymentPreviewPage, width int) []string {
	if current.diffLines != nil {
		return current.diffLines
	}
	available := max(width-detailsPadding, hardMinimumWidth-detailsPadding)

	return terminaltext.Wrap(current.preview.Diff, available)
}

func (state *model) deploymentHistoryBody(current deploymentHistoryPage, width int) []string {
	lines := []string{
		state.title("Deployment history"),
		"Choose a first-parent revision that changed this Compose file.",
		"",
	}
	limit := max(
		min(state.height-deploymentFrameRows, deploymentMaximumVisibleRows),
		deploymentMinimumVisibleRows,
	)
	start, end := visibleSelection(len(current.history), current.cursor, limit)
	if start > 0 {
		lines = append(lines, state.muted(fmt.Sprintf("%d newer revision(s)", start)))
	}
	for index := start; index < end; index++ {
		entry := current.history[index]
		signature := "no signature"
		if entry.SignaturePresent {
			signature = "signature present"
		}
		label := entry.Revision[:12] + "  " + entry.Subject + "  " + signature
		if index == 0 {
			label += "  current"
		}
		lines = append(lines, state.choice(index == current.cursor, label, width))
	}
	if end < len(current.history) {
		lines = append(lines, state.muted(fmt.Sprintf("%d older revision(s)", len(current.history)-end)))
	}

	return lines
}

func (state *model) restoreDeploymentConfirmationBody(
	current restoreDeploymentConfirmationPage,
	width int,
) []string {
	entry := current.entry

	return []string{
		state.title("Review history revision"),
		"Load this file revision into a fresh candidate for the selected service?",
		"The current file and Git history stay unchanged until later confirmation.",
		"",
		"Revision  " + entry.Revision[:12],
		"Subject   " + terminaltext.Middle(entry.Subject, max(width-serviceFieldWidth, 1), "…"),
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Validate revision", width),
	}
}

//nolint:cyclop // The closed field catalog owns one concise user-facing label per field.
func deploymentFieldLabel(identifier string) string {
	field, _ := application.ParseDeploymentField(identifier)
	label := identifier
	switch field {
	case application.DeploymentCPUs:
		label = "CPU limit"
	case application.DeploymentMemory:
		label = "Memory limit"
	case application.DeploymentPIDs:
		label = "Process limit"
	case application.DeploymentRestart:
		label = "Restart policy"
	case application.DeploymentSharedMemory:
		label = "Shared memory"
	case application.DeploymentStopGrace:
		label = "Stop grace period"
	case application.DeploymentInit:
		label = "Init process"
	case application.DeploymentReadOnly:
		label = "Read-only root"
	case application.DeploymentNoNewPrivileges:
		label = "No new privileges"
	case application.DeploymentHealthInterval:
		label = "Health interval"
	case application.DeploymentHealthTimeout:
		label = "Health timeout"
	case application.DeploymentHealthRetries:
		label = "Health retries"
	case application.DeploymentHealthStartPeriod:
		label = "Health start period"
	case application.DeploymentHealthStartInterval:
		label = "Health start interval"
	}

	return label
}

func deploymentFieldPlaceholder(identifier string) string {
	const duration = "30s"

	field, _ := application.ParseDeploymentField(identifier)
	value := duration
	switch field {
	case application.DeploymentCPUs:
		value = "2.5"
	case application.DeploymentMemory, application.DeploymentSharedMemory:
		value = "536870912"
	case application.DeploymentPIDs, application.DeploymentHealthRetries:
		value = "100"
	case application.DeploymentRestart:
		value = "unless-stopped"
	case application.DeploymentInit, application.DeploymentReadOnly, application.DeploymentNoNewPrivileges:
		value = "true"
	case application.DeploymentStopGrace, application.DeploymentHealthInterval,
		application.DeploymentHealthTimeout, application.DeploymentHealthStartPeriod,
		application.DeploymentHealthStartInterval:
		value = duration
	}

	return value
}

func deploymentFieldHint(identifier string) string {
	const duration = "Enter a positive duration, for example 30s or 2m."

	field, _ := application.ParseDeploymentField(identifier)
	hint := duration
	switch field {
	case application.DeploymentCPUs:
		hint = "Use a positive CPU count, for example 2.5."
	case application.DeploymentMemory, application.DeploymentSharedMemory:
		hint = "Enter a positive byte count."
	case application.DeploymentPIDs:
		hint = "Enter a positive process count or -1 for unlimited."
	case application.DeploymentRestart:
		hint = "Use no, always, unless-stopped, on-failure, or on-failure:N."
	case application.DeploymentInit, application.DeploymentReadOnly:
		hint = "Enter true or false."
	case application.DeploymentNoNewPrivileges:
		hint = "Enter true. This security setting cannot be removed here."
	case application.DeploymentHealthRetries:
		hint = "Enter a positive retry count."
	case application.DeploymentStopGrace, application.DeploymentHealthInterval,
		application.DeploymentHealthTimeout, application.DeploymentHealthStartPeriod,
		application.DeploymentHealthStartInterval:
		hint = duration
	}

	return hint
}
