package tui

import (
	"fmt"

	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const serviceCommitLocation = "Home / Add service / Commit"

func (state *model) serviceWorkspaceLocation() (string, bool) {
	switch current := state.page.(type) {
	case addServicePage:
		return "Home / Add service / Input", true
	case servicePreviewPage, stageServiceConfirmationPage:
		return "Home / Add service / Preview", true
	case commitPage:
		return serviceCommitLocation, current.kind == commitKindService
	case stagedDiffPage:
		return serviceCommitLocation, current.commit.kind == commitKindService
	case unsignedCommitConfirmationPage:
		return serviceCommitLocation, current.commit.kind == commitKindService
	case preparationRequiredPage:
		return "Home / Add service / Prepare", true
	default:
		return "", false
	}
}

func (state *model) serviceWorkspaceRail() ([]string, bool) {
	step := -1
	if serviceCommitPage(state.page) {
		step = 2
	}
	switch state.page.(type) {
	case addServicePage:
		step = 0
	case servicePreviewPage, stageServiceConfirmationPage:
		step = 1
	case preparationRequiredPage:
		step = 3
	}
	if step < 0 {
		return nil, false
	}
	steps := []string{"Input", "Preview", "Commit", "Validate"}

	return state.flowRail("ADD SERVICE", steps, step), true
}

func serviceCommitPage(current page) bool {
	switch current := current.(type) {
	case commitPage:
		return current.kind == commitKindService
	case stagedDiffPage:
		return current.commit.kind == commitKindService
	case unsignedCommitConfirmationPage:
		return current.commit.kind == commitKindService
	default:
		return false
	}
}

func (state *model) serviceWorkspacePageBody(width int) ([]string, bool) {
	switch current := state.page.(type) {
	case addServicePage:
		return state.addServiceBody(current, width), true
	case servicePreviewPage:
		return state.servicePreviewBody(current, width), true
	case stageServiceConfirmationPage:
		return state.stageServiceConfirmationBody(current, width), true
	case preparationRequiredPage:
		return state.preparationRequiredBody(current, width), true
	default:
		return nil, false
	}
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
	}
}

func (state *model) servicePreviewBody(current servicePreviewPage, width int) []string {
	draft := current.draft
	title := "Review parsed service"
	description := "The pasted command was parsed, not run."
	continueLabel := deploymentReviewAction
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
	lines = append(lines, "", state.choice(true, continueLabel, width))

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
	)

	return lines
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
	}
}

func (state *model) serviceWorkspaceFooter() (string, bool) {
	switch state.page.(type) {
	case addServicePage:
		return "Type input   Enter Preview   Esc Back", true
	case servicePreviewPage:
		return "Enter Continue   Esc Edit   q Quit", true
	case stageServiceConfirmationPage:
		return confirmationKeys, true
	case preparationRequiredPage:
		return "Enter/Esc Exit", true
	default:
		return "", false
	}
}
