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
)

func (state *model) deploymentPageBody(width int) ([]string, bool) {
	switch current := state.page.(type) {
	case deploymentFieldsPage:
		return state.deploymentFieldsBody(current, width), true
	case deploymentValuePage:
		return state.deploymentValueBody(current, width), true
	case deploymentPreviewPage:
		return state.deploymentPreviewBody(current, width), true
	case stageDeploymentConfirmationPage:
		return state.stageDeploymentConfirmationBody(current, width), true
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
		"",
	}
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
		value := "Compose default"
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
	lines = append(lines, "", state.muted("Enter Edit   u Remove field   Esc Back"))

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
	currentValue := "Compose default"
	if current.field.Present {
		currentValue = current.field.Value
	}

	return []string{
		state.title("Set deployment value"),
		deploymentFieldLabel(current.field.ID),
		"",
		"Current   " + terminaltext.Middle(currentValue, max(width-serviceFieldWidth, 1), "…"),
		"New       " + state.accent(terminaltext.Middle(value, max(width-serviceFieldWidth, 1), "…")+
			state.symbol("▌", "_")),
		"",
		deploymentFieldHint(current.field.ID),
		"",
		state.muted("Enter Validate   Esc Back"),
	}
}

func (state *model) deploymentPreviewBody(current deploymentPreviewPage, width int) []string {
	preview := current.preview
	lines := []string{
		state.title("Review deployment edit"),
		"The candidate passed fresh Compose validation. No file has changed.",
		"",
		"Compose   " + terminaltext.Middle(preview.ComposePath, max(width-serviceFieldWidth, 1), "…"),
	}
	if preview.Restore != "" {
		lines = append(lines, "Revision  "+preview.Restore[:12])
	} else {
		labels := make([]string, len(preview.FieldIDs))
		for index, fieldID := range preview.FieldIDs {
			labels[index] = deploymentFieldLabel(fieldID)
		}
		lines = append(lines, "Fields    "+terminaltext.Middle(
			strings.Join(labels, ", "), max(width-serviceFieldWidth, 1), "…",
		))
	}
	lines = append(lines,
		"",
		state.choice(true, "Review file mutation", width),
		state.muted("Enter Continue   Esc Back"),
	)

	return lines
}

func (state *model) stageDeploymentConfirmationBody(
	current stageDeploymentConfirmationPage,
	width int,
) []string {
	return []string{
		state.title("Confirm file mutation"),
		"Replace the selected Compose file and stage its exact Git diff?",
		"No commit or runtime operation will run on this page.",
		"",
		"Compose   " + terminaltext.Middle(
			current.preview.preview.ComposePath, max(width-serviceFieldWidth, 1), "…",
		),
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Write and stage edit", width),
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
	}
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
	lines = append(lines, "", state.muted("Enter Review restore   Esc Back"))

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
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
	}
}

//nolint:cyclop // The closed field catalog owns one concise user-facing label per field.
func deploymentFieldLabel(identifier string) string {
	switch identifier {
	case "cpus":
		return "CPU limit"
	case "mem_limit":
		return "Memory limit"
	case "pids_limit":
		return "Process limit"
	case "restart":
		return "Restart policy"
	case "shm_size":
		return "Shared memory"
	case "stop_grace_period":
		return "Stop grace period"
	case "init":
		return "Init process"
	case "read_only":
		return "Read-only root"
	case "no_new_privileges":
		return "No new privileges"
	case "healthcheck.interval":
		return "Health interval"
	case "healthcheck.timeout":
		return "Health timeout"
	case "healthcheck.retries":
		return "Health retries"
	case "healthcheck.start_period":
		return "Health start period"
	case "healthcheck.start_interval":
		return "Health start interval"
	default:
		return identifier
	}
}

func deploymentFieldPlaceholder(identifier string) string {
	switch identifier {
	case "cpus":
		return "2.5"
	case "mem_limit", "shm_size":
		return "536870912"
	case "pids_limit", "healthcheck.retries":
		return "100"
	case "restart":
		return "unless-stopped"
	case "init", "read_only", "no_new_privileges":
		return "true"
	default:
		return "30s"
	}
}

func deploymentFieldHint(identifier string) string {
	switch identifier {
	case application.DeploymentCPUs.ID():
		return "Use a positive CPU count, for example 2.5."
	case application.DeploymentMemory.ID(), application.DeploymentSharedMemory.ID():
		return "Enter a positive byte count."
	case application.DeploymentPIDs.ID():
		return "Enter a positive process count or -1 for unlimited."
	case application.DeploymentRestart.ID():
		return "Use no, always, unless-stopped, on-failure, or on-failure:N."
	case application.DeploymentInit.ID(), application.DeploymentReadOnly.ID():
		return "Enter true or false."
	case application.DeploymentNoNewPrivileges.ID():
		return "Enter true. This security setting cannot be removed here."
	case application.DeploymentHealthRetries.ID():
		return "Enter a positive retry count."
	default:
		return "Enter a positive duration, for example 30s or 2m."
	}
}
