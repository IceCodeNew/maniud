package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

type deploymentFieldsResultMsg struct {
	sequence uint64
	review   reviewPage
	fields   []DeploymentFieldState
	err      error
}

type deploymentPreviewResultMsg struct {
	sequence uint64
	review   reviewPage
	previous page
	preview  DeploymentEditPreview
	err      error
}

type deploymentStageResultMsg struct {
	sequence uint64
	preview  deploymentPreviewPage
	staged   StagedDeploymentEdit
	err      error
}

type deploymentCommitResultMsg struct {
	sequence uint64
	commit   commitServicePage
	result   DeploymentCommitResult
	err      error
}

type deploymentDiscardResultMsg struct {
	sequence uint64
	preview  deploymentPreviewPage
	err      error
}

type deploymentHistoryResultMsg struct {
	sequence uint64
	review   reviewPage
	history  []DeploymentHistoryEntry
	err      error
}

type deploymentFieldsPage struct {
	review reviewPage
	fields []DeploymentFieldState
	cursor int
}

func (deploymentFieldsPage) isPage() {}

type deploymentValuePage struct {
	fields deploymentFieldsPage
	field  DeploymentFieldState
	value  string
}

func (deploymentValuePage) isPage() {}

type deploymentPreviewPage struct {
	review   reviewPage
	previous page
	preview  DeploymentEditPreview
}

func (deploymentPreviewPage) isPage() {}

type stageDeploymentConfirmationPage struct {
	preview deploymentPreviewPage
	focus   confirmationFocus
}

func (stageDeploymentConfirmationPage) isPage() {}

type deploymentHistoryPage struct {
	review  reviewPage
	history []DeploymentHistoryEntry
	cursor  int
}

func (deploymentHistoryPage) isPage() {}

type restoreDeploymentConfirmationPage struct {
	history deploymentHistoryPage
	entry   DeploymentHistoryEntry
	focus   confirmationFocus
}

func (restoreDeploymentConfirmationPage) isPage() {}

func (state *model) handleDeploymentWorkspaceMessage(message tea.Msg) (tea.Cmd, bool) {
	switch message := message.(type) {
	case deploymentFieldsResultMsg:
		return state.handleDeploymentFieldsResult(message), true
	case deploymentPreviewResultMsg:
		return state.handleDeploymentPreviewResult(message), true
	case deploymentStageResultMsg:
		return state.handleDeploymentStageResult(message), true
	case deploymentCommitResultMsg:
		return state.handleDeploymentCommitResult(message), true
	case deploymentDiscardResultMsg:
		return state.handleDeploymentDiscardResult(message), true
	case deploymentHistoryResultMsg:
		return state.handleDeploymentHistoryResult(message), true
	default:
		return nil, false
	}
}

func (state *model) handleDeploymentPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case deploymentFieldsPage:
		return state.handleDeploymentFieldsKey(current, message.String()), true
	case deploymentValuePage:
		return state.handleDeploymentValueKey(current, message), true
	case deploymentPreviewPage:
		return state.handleDeploymentPreviewKey(current, message.String()), true
	case stageDeploymentConfirmationPage:
		return state.handleDeploymentStageConfirmationKey(current, message.String()), true
	case deploymentHistoryPage:
		return state.handleDeploymentHistoryKey(current, message.String()), true
	case restoreDeploymentConfirmationPage:
		return state.handleRestoreDeploymentConfirmationKey(current, message.String()), true
	default:
		return nil, false
	}
}

//nolint:cyclop // One closed key dispatcher owns this selection page's complete behavior.
func (state *model) handleDeploymentFieldsKey(current deploymentFieldsPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + len(current.fields)) % len(current.fields)
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % len(current.fields)
	case keyEnter:
		field := current.fields[current.cursor]
		if !field.Available {
			state.status = "This field is unavailable for the selected service"

			return nil
		}
		value := ""
		if field.ID == application.DeploymentNoNewPrivileges.ID() {
			value = "true"
		}
		state.page = deploymentValuePage{fields: current, field: field, value: value}
		state.status = "Enter the deployment value"

		return nil
	case "u":
		field := current.fields[current.cursor]
		if !field.Available || !field.Present || !field.AllowsUnset {
			state.status = "This field cannot be removed"

			return nil
		}

		return state.startDeploymentPreview(current.review, current, field.ID, "", true)
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

func (state *model) handleDeploymentValueKey(
	current deploymentValuePage,
	message tea.KeyPressMsg,
) tea.Cmd {
	switch message.String() {
	case keyEnter:
		if current.value == "" {
			state.status = "Enter a deployment value"

			return nil
		}

		return state.startDeploymentPreview(
			current.fields.review, current.fields, current.field.ID, current.value, false,
		)
	case keyEscape:
		state.page = current.fields
		state.status = "Choose a deployment field"

		return nil
	case keyQuit:
		return tea.Quit
	}
	current.value = editSingleLine(current.value, message)
	state.page = current

	return nil
}

func (state *model) handleDeploymentPreviewKey(
	current deploymentPreviewPage,
	key string,
) tea.Cmd {
	switch key {
	case keyEnter:
		if layoutFor(state.width, state.height) < layoutCompact {
			state.status = "Resize to review the file mutation"

			return nil
		}
		state.page = stageDeploymentConfirmationPage{preview: current, focus: confirmationBack}
		state.status = "Confirm file mutation or go back"
	case keyEscape:
		state.page = current.previous
		state.status = statusDeploymentUnchanged
	case keyQuit:
		return tea.Quit
	}

	return nil
}

func (state *model) handleDeploymentStageConfirmationKey(
	current stageDeploymentConfirmationPage,
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
			state.status = statusDeploymentUnchanged

			return nil
		}

		return state.startDeploymentStage(current.preview)
	case keyEscape:
		state.page = current.preview
		state.status = statusDeploymentUnchanged

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleDeploymentHistoryKey(
	current deploymentHistoryPage,
	key string,
) tea.Cmd {
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + len(current.history)) % len(current.history)
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % len(current.history)
	case keyEnter:
		if current.cursor == 0 {
			state.status = "This revision is already current"

			return nil
		}
		state.page = restoreDeploymentConfirmationPage{
			history: current, entry: current.history[current.cursor], focus: confirmationBack,
		}
		state.status = "Confirm the history revision to validate"

		return nil
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

func (state *model) handleRestoreDeploymentConfirmationKey(
	current restoreDeploymentConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.history
		state.status = statusReviewLarger

		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.history
			state.status = "History is unchanged"

			return nil
		}

		return state.startDeploymentRestore(current.history, current.entry.Revision)
	case keyEscape:
		state.page = current.history
		state.status = "History is unchanged"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) startDeploymentFields(review reviewPage) tea.Cmd {
	if state.deployments == nil {
		state.status = "Deployment editing is unavailable"

		return nil
	}

	return state.begin("Loading deployment fields", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			fields, err := deployments.Fields(ctx, review.request)

			return deploymentFieldsResultMsg{sequence: sequence, review: review, fields: fields, err: err}
		}
	})
}

func (state *model) startDeploymentPreview(
	review reviewPage,
	previous page,
	fieldID string,
	value string,
	unset bool,
) tea.Cmd {
	return state.begin("Validating deployment edit", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			preview, err := deployments.Preview(ctx, review.request, fieldID, value, unset)

			return deploymentPreviewResultMsg{
				sequence: sequence, review: review, previous: previous, preview: preview, err: err,
			}
		}
	})
}

func (state *model) startDeploymentStage(preview deploymentPreviewPage) tea.Cmd {
	return state.begin("Writing and staging deployment edit", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			staged, err := deployments.Stage(ctx)

			return deploymentStageResultMsg{sequence: sequence, preview: preview, staged: staged, err: err}
		}
	})
}

func (state *model) startDeploymentCommit(commit commitServicePage, unsigned bool) tea.Cmd {
	state.page = commit

	return state.begin("Creating deployment commit", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			result, err := deployments.Commit(ctx, commit.message, unsigned)

			return deploymentCommitResultMsg{
				sequence: sequence, commit: commit, result: result, err: err,
			}
		}
	})
}

func (state *model) startDeploymentDiscard(preview deploymentPreviewPage) tea.Cmd {
	return state.begin("Discarding staged deployment edit", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			return deploymentDiscardResultMsg{
				sequence: sequence, preview: preview, err: deployments.Discard(ctx),
			}
		}
	})
}

func (state *model) startDeploymentHistory(review reviewPage) tea.Cmd {
	if state.deployments == nil {
		state.status = "Deployment history is unavailable"

		return nil
	}

	return state.begin("Loading deployment history", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			history, err := deployments.History(ctx, review.request)

			return deploymentHistoryResultMsg{sequence: sequence, review: review, history: history, err: err}
		}
	})
}

func (state *model) startDeploymentRestore(
	history deploymentHistoryPage,
	revision string,
) tea.Cmd {
	return state.begin("Validating history revision", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			preview, err := deployments.PreviewRestore(ctx, history.review.request, revision)

			return deploymentPreviewResultMsg{
				sequence: sequence, review: history.review, previous: history, preview: preview, err: err,
			}
		}
	})
}

func (state *model) handleDeploymentFieldsResult(result deploymentFieldsResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	fields, err := canonicalDeploymentFields(result.fields)
	if err != nil {
		state.err = err
		state.status = "Deployment fields could not be displayed safely"

		return command
	}
	state.page = deploymentFieldsPage{review: result.review, fields: fields}
	state.status = "Choose a deployment field"

	return command
}

func (state *model) handleDeploymentPreviewResult(result deploymentPreviewResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	preview, err := canonicalDeploymentPreview(result.preview)
	if err != nil {
		state.err = err
		state.status = "Deployment preview could not be displayed safely"

		return command
	}
	state.page = deploymentPreviewPage{
		review: result.review, previous: result.previous, preview: preview,
	}
	state.status = "Deployment edit passed Compose validation"

	return command
}

func (state *model) handleDeploymentStageResult(result deploymentStageResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	staged, err := canonicalStagedDeployment(result.staged)
	if err != nil {
		state.err = err
		state.status = "Staged deployment edit could not be displayed safely"

		return command
	}
	deployment := result.preview
	state.page = state.wrapServiceDiff(commitServicePage{
		staged: StagedService{
			Diff: staged.Diff, ComposePath: staged.ComposePath, CommitMessage: staged.CommitMessage,
		},
		message: staged.CommitMessage, focus: confirmationBack, deployment: &deployment,
	})
	state.status = statusReviewStaged
	state.mutationOutcome = "Deployment edit staged"

	return command
}

//nolint:cyclop // Commit proof outcomes stay in one fail-closed state transition.
func (state *model) handleDeploymentCommitResult(result deploymentCommitResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	if result.commit.deployment == nil {
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return command
	}
	if result.result.Committed && result.result.NeedsUnsignedApproval ||
		result.result.ValidationUnavailable && !result.result.Committed {
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return command
	}
	if result.result.NeedsUnsignedApproval {
		state.page = unsignedCommitConfirmationPage{commit: result.commit, focus: confirmationBack}
		state.status = statusSignedCommitMissing

		return command
	}
	if !result.result.Committed {
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return command
	}
	state.mutationOutcome = "Deployment commit created"
	if result.result.ValidationUnavailable {
		state.page = homePage{catalog: CatalogSnapshot{State: CatalogUnavailable}}
		state.status = "Commit created; validation is unavailable"

		return command
	}

	return tea.Batch(command, state.startCommittedSnapshot(result.result.Request))
}

func (state *model) handleDeploymentDiscardResult(result deploymentDiscardResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	state.page = result.preview
	state.status = "Staged deployment edit discarded"
	state.mutationOutcome = ""

	return command
}

func (state *model) handleDeploymentHistoryResult(result deploymentHistoryResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	history, err := canonicalDeploymentHistory(result.history)
	if err != nil || len(history) == 0 {
		state.err = errors.Join(errInvalidInput, err)
		state.status = "Deployment history could not be displayed safely"

		return command
	}
	state.page = deploymentHistoryPage{review: result.review, history: history}
	state.status = "Choose a revision to restore"

	return command
}
