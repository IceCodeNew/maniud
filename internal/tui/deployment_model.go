package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

const deploymentWorktreeUnknownStatus = "Worktree state is uncertain; run git status and recover it outside maniud"

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
	commit   commitPage
	result   CommitResult
	err      error
}

type deploymentDiscardResultMsg struct {
	sequence    uint64
	destination page
	quit        bool
	staged      bool
	err         error
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
	draft  deploymentValueDraft
	cursor int
}

func (deploymentFieldsPage) isPage() {}

type deploymentValueDraft struct {
	fieldID string
	initial string
	value   string
}

func (draft deploymentValueDraft) dirty() bool {
	return draft.fieldID != "" && draft.value != draft.initial
}

type deploymentValuePage struct {
	fields deploymentFieldsPage
	field  DeploymentFieldState
	value  string
}

func (deploymentValuePage) isPage() {}

func (deploymentValuePage) acceptsTextInput() bool { return true }

type deploymentPreviewPage struct {
	review    reviewPage
	previous  page
	preview   DeploymentEditPreview
	scroll    int
	diffWidth int
	diffLines []string
}

func (deploymentPreviewPage) isPage() {}

type deploymentDetailsPage struct {
	preview deploymentPreviewPage
	scroll  int
}

func (deploymentDetailsPage) isPage() {}

type stageDeploymentConfirmationPage struct {
	preview deploymentPreviewPage
	focus   confirmationFocus
}

func (stageDeploymentConfirmationPage) isPage() {}

type deploymentDiffPage struct {
	confirmation stageDeploymentConfirmationPage
	scroll       int
}

func (deploymentDiffPage) isPage() {}

type deploymentDraftConfirmationPage struct {
	previous    page
	destination page
	focus       confirmationFocus
	quit        bool
	staged      bool
}

func (deploymentDraftConfirmationPage) isPage() {}

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

//nolint:cyclop // The page type switch is the deployment workflow's closed dispatch table.
func (state *model) handleDeploymentPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case deploymentFieldsPage:
		return state.handleDeploymentFieldsKey(current, message.String()), true
	case deploymentValuePage:
		return state.handleDeploymentValueKey(current, message), true
	case deploymentPreviewPage:
		return state.handleDeploymentPreviewKey(current, message.String()), true
	case deploymentDetailsPage:
		return state.handleDeploymentDetailsKey(current, message.String()), true
	case stageDeploymentConfirmationPage:
		return state.handleDeploymentStageConfirmationKey(current, message.String()), true
	case deploymentDiffPage:
		return state.handleDeploymentDiffKey(current, message.String()), true
	case deploymentDraftConfirmationPage:
		return state.handleDeploymentDraftConfirmationKey(current, message.String()), true
	case deploymentHistoryPage:
		return state.handleDeploymentHistoryKey(current, message.String()), true
	case restoreDeploymentConfirmationPage:
		return state.handleRestoreDeploymentConfirmationKey(current, message.String()), true
	default:
		return nil, false
	}
}

//nolint:cyclop,funlen // One closed key dispatcher owns this selection page's complete behavior.
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
		if current.draft.dirty() && current.draft.fieldID != field.ID {
			state.status = "Return to the unsaved field or discard it before editing another field"

			return nil
		}
		if current.draft.fieldID == field.ID {
			value = current.draft.value
		} else {
			current.draft = deploymentValueDraft{fieldID: field.ID, initial: value, value: value}
		}
		state.page = deploymentValuePage{fields: current, field: field, value: value}
		state.status = deploymentDraftStatus(current.draft, "Enter the deployment value")

		return nil
	case "u":
		field := current.fields[current.cursor]
		if current.draft.dirty() {
			state.status = "Discard the unsaved value before removing a field"

			return nil
		}
		if !field.Available || !field.Present || !field.AllowsUnset {
			state.status = "This field cannot be removed"

			return nil
		}

		return state.startDeploymentPreview(current.review, current, field.ID, "", true)
	case keyEscape:
		if current.draft.dirty() {
			return state.leaveDeploymentDraft(current, current.review, false)
		}
		state.clearRecoverableDeploymentFailure()
		state.page = current.review
		state.status = current.review.plan.status

		return nil
	case keyQuit:
		if current.draft.dirty() {
			return state.leaveDeploymentDraft(current, nil, true)
		}

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
			current.fields.review, current, current.field.ID, current.value, false,
		)
	case keyEscape:
		state.page = current.fields
		state.status = deploymentDraftStatus(current.fields.draft, "Choose a deployment field")

		return nil
	}
	value := editSingleLine(current.value, message)
	if value != current.value {
		state.clearRecoverableDeploymentFailure()
	}
	current.value = value
	current.fields.draft.value = current.value
	state.page = current
	state.status = deploymentDraftStatus(current.fields.draft, "Enter the deployment value")

	return nil
}

func deploymentDraftStatus(draft deploymentValueDraft, clean string) string {
	if draft.dirty() {
		return "Unsaved deployment value"
	}

	return clean
}

func (state *model) handleDeploymentPreviewKey(
	current deploymentPreviewPage,
	key string,
) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
		state.page = current
	case keyDown, "j":
		current.scroll++
		state.page = current
		state.clampPageScroll()
	case "d":
		state.page = deploymentDetailsPage{preview: current}
		state.status = "Complete deployment values"
	case keyEnter:
		if layoutFor(state.width, state.height) < layoutCompact {
			state.status = "Resize to review the file mutation"

			return nil
		}
		current = state.wrapDeploymentPreviewDiff(current)
		state.page = stageDeploymentConfirmationPage{preview: current, focus: confirmationBack}
		state.status = statusConfirmFileMutation
	case keyEscape:
		return state.startDeploymentDiscard(current.previous, false, false)
	case keyQuit:
		return state.leaveDeploymentDraft(current, nil, true)
	}

	return nil
}

func (state *model) handleDeploymentDetailsKey(current deploymentDetailsPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d", keyEscape:
		state.page = current.preview
		state.status = statusDeploymentUnchanged

		return nil
	case keyQuit:
		return state.leaveDeploymentDraft(current, nil, true)
	}
	state.page = current
	state.clampPageScroll()

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
	case "d":
		state.page = deploymentDiffPage{confirmation: current}
		state.status = "Exact commit diff"

		return nil
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
		return state.leaveDeploymentDraft(current, nil, true)
	}
	state.page = current

	return nil
}

func (state *model) handleDeploymentDiffKey(current deploymentDiffPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d", keyEscape:
		state.page = current.confirmation
		state.status = statusConfirmFileMutation

		return nil
	case keyQuit:
		return state.leaveDeploymentDraft(current, nil, true)
	}
	state.page = current
	state.clampPageScroll()

	return nil
}

func (state *model) leaveDeploymentDraft(previous, destination page, quit bool) tea.Cmd {
	state.page = deploymentDraftConfirmationPage{
		previous: previous, destination: destination, focus: confirmationBack, quit: quit,
	}
	state.status = "Discard unsaved deployment edit?"

	return nil
}

func (state *model) handleDeploymentDraftConfirmationKey(
	current deploymentDraftConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.previous
		state.status = statusReviewLarger

		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.previous
			state.status = "Unsaved deployment edit"

			return nil
		}

		return state.startDeploymentDiscard(current.destination, current.quit, current.staged)
	case keyEscape:
		state.page = current.previous
		state.status = "Unsaved deployment edit"

		return nil
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
		state.clearRecoverableDeploymentFailure()
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
	if !state.deploymentOperationReady() {
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
	if !state.deploymentOperationReady() {
		return nil
	}

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
	if !state.deploymentOperationReady() {
		return nil
	}

	return state.begin("Writing and staging deployment edit", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			staged, err := deployments.Stage(ctx)

			return deploymentStageResultMsg{sequence: sequence, preview: preview, staged: staged, err: err}
		}
	})
}

func (state *model) startDeploymentCommit(commit commitPage, unsigned bool) tea.Cmd {
	if !state.deploymentOperationReady() {
		return nil
	}
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

func (state *model) startDeploymentDiscard(
	destination page,
	quit bool,
	staged bool,
) tea.Cmd {
	if !state.deploymentOperationReady() {
		return nil
	}

	return state.begin("Discarding deployment edit", func(ctx context.Context, sequence uint64) tea.Cmd {
		deployments := state.deployments

		return func() tea.Msg {
			return deploymentDiscardResultMsg{
				sequence: sequence, destination: destination,
				quit: quit, staged: staged, err: deployments.Discard(ctx),
			}
		}
	})
}

func (state *model) startDeploymentHistory(review reviewPage) tea.Cmd {
	if state.deployments == nil {
		state.status = "Deployment history is unavailable"

		return nil
	}
	if !state.deploymentOperationReady() {
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
	if !state.deploymentOperationReady() {
		return nil
	}

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
	accepted, command := state.completeDeploymentOperation(result.sequence, result.err)
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
	accepted, command := state.completeDeploymentOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	preview, err := canonicalDeploymentPreview(result.preview)
	if err != nil {
		state.err = err
		state.status = "Deployment preview could not be displayed safely"

		return command
	}
	if preview.NoChanges {
		state.page = result.previous
		state.status = "Already matches current source"

		return command
	}
	state.page = deploymentPreviewPage{
		review: result.review, previous: result.previous, preview: preview,
	}
	state.status = "Deployment edit passed Compose validation"

	return command
}

func (state *model) handleDeploymentStageResult(result deploymentStageResultMsg) tea.Cmd {
	accepted, command := state.completeDeploymentOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	staged, err := canonicalStagedDeployment(result.staged)
	if err != nil || staged.Diff != result.preview.preview.Diff {
		state.err = errors.Join(errInvalidInput, err)
		state.status = "Staged deployment edit could not be displayed safely"

		return command
	}
	state.page = state.wrapCommitDiff(commitPage{
		kind: commitKindDeployment,
		staged: StagedService{
			Diff: staged.Diff, ComposePath: staged.ComposePath, CommitMessage: staged.CommitMessage,
		},
		message: staged.CommitMessage, focus: confirmationBack, deployment: result.preview,
	})
	state.status = statusReviewStaged
	state.mutationOutcome = "Deployment edit staged"

	return command
}

func (state *model) handleDeploymentCommitResult(result deploymentCommitResultMsg) tea.Cmd {
	accepted, command := state.completeDeploymentOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	if result.commit.kind != commitKindDeployment {
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return command
	}
	switch result.result.Outcome {
	case CommitNeedsUnsignedApproval:
		state.page = unsignedCommitConfirmationPage{commit: result.commit, focus: confirmationBack}
		state.status = statusSignedCommitMissing

		return command
	case CommitValidationUnavailable:
		state.mutationOutcome = "Deployment commit created"
		state.page = homePage{catalog: CatalogSnapshot{State: CatalogUnavailable}}
		state.status = "Commit created; validation is unavailable"

		return command
	case CommitSucceeded:
		state.mutationOutcome = "Deployment commit created"

		return tea.Batch(command, state.startCommittedSnapshot(result.result.Request))
	case CommitPreparationRequired:
		fallthrough
	default:
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return command
	}
}

func (state *model) handleDeploymentDiscardResult(result deploymentDiscardResultMsg) tea.Cmd {
	accepted, command := state.completeDeploymentOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	if result.quit {
		return tea.Batch(command, tea.Quit)
	}
	state.page = result.destination
	state.status = statusDeploymentUnchanged
	if result.staged {
		state.status = "Staged deployment edit discarded"
	}
	state.mutationOutcome = ""

	return command
}

func (state *model) handleDeploymentHistoryResult(result deploymentHistoryResultMsg) tea.Cmd {
	accepted, command := state.completeDeploymentOperation(result.sequence, result.err)
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

func (state *model) deploymentOperationReady() bool {
	if state.deploymentFailure == DeploymentWorktreeUnknown {
		state.status = deploymentFailureStatus(DeploymentWorktreeUnknown)

		return false
	}
	state.clearRecoverableDeploymentFailure()

	return true
}

func (state *model) clearRecoverableDeploymentFailure() {
	if state.deploymentFailure != DeploymentWorktreeUnknown {
		state.deploymentFailure = ""
	}
}

func (state *model) completeDeploymentOperation(sequence uint64, err error) (bool, tea.Cmd) {
	action, valid := errors.AsType[*DeploymentActionError](err)
	if errors.Is(err, context.Canceled) && (!valid || action.Code != DeploymentWorktreeUnknown) {
		return state.completeOperation(sequence, err)
	}
	if !valid {
		accepted, command := state.completeOperation(sequence, err)
		if accepted && err == nil {
			state.deploymentFailure = ""
		}

		return accepted, command
	}
	accepted, command := state.completeOperation(sequence, nil)
	if !accepted {
		return false, command
	}
	state.deploymentFailure = canonicalDeploymentFailure(action.Code)
	state.status = deploymentFailureStatus(state.deploymentFailure)

	return true, command
}

func canonicalDeploymentFailure(failure DeploymentFailure) DeploymentFailure {
	switch failure {
	case DeploymentPreconditionFailed, DeploymentUnsupportedSource, DeploymentValidationFailed,
		DeploymentPublishFailed, DeploymentWorktreeUnknown, DeploymentCommitFailed,
		DeploymentHistoryUnavailable, DeploymentHistoryEntryInvalid:
		return failure
	default:
		return DeploymentPreconditionFailed
	}
}

//nolint:exhaustive // The private fallback contains malformed adapter output.
func deploymentFailureStatus(failure DeploymentFailure) string {
	switch failure {
	case DeploymentUnsupportedSource:
		return "This Compose source cannot be edited safely"
	case DeploymentValidationFailed:
		return "Deployment value failed local validation; edit it before retrying"
	case DeploymentPublishFailed:
		return "Staging failed; the original Compose file was restored. Reload before retrying"
	case DeploymentWorktreeUnknown:
		return deploymentWorktreeUnknownStatus
	case DeploymentCommitFailed:
		return "Commit failed; the staged deployment edit is unchanged and can be retried"
	case DeploymentHistoryUnavailable:
		return "Deployment history could not be read; the worktree is unchanged"
	case DeploymentHistoryEntryInvalid:
		return "That history entry can no longer be restored; reload history"
	default:
		return "Deployment source changed; reload it before editing"
	}
}
