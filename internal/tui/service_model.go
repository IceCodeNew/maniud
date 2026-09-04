package tui

import (
	"context"
	"errors"

	tea "charm.land/bubbletea/v2"
)

const serviceCommitCreated = "Compose commit created"

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
	commit   commitPage
	result   CommitResult
	err      error
}

type serviceSuspendResultMsg struct {
	sequence uint64
	preview  servicePreviewPage
	err      error
}

type addServicePage struct {
	value string
}

func (addServicePage) isPage() {}

func (addServicePage) acceptsTextInput() bool { return true }

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

type preparationRequiredPage struct {
	draft ServiceDraft
}

func (preparationRequiredPage) isPage() {}

func (state *model) handleServiceWorkspaceMessage(message tea.Msg) (tea.Cmd, bool) {
	switch message := message.(type) {
	case servicePreviewResultMsg:
		return state.handleServicePreviewResult(message), true
	case serviceStageResultMsg:
		return state.handleServiceStageResult(message), true
	case serviceCommitResultMsg:
		return state.handleServiceCommitResult(message), true
	case serviceSuspendResultMsg:
		return state.handleServiceSuspendResult(message), true
	default:
		return nil, false
	}
}

func (state *model) handleServiceWorkspacePageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case addServicePage:
		return state.handleAddServiceKey(current, message), true
	case servicePreviewPage:
		return state.handleServicePreviewKey(current, message.String()), true
	case stageServiceConfirmationPage:
		return state.handleStageServiceConfirmationKey(current, message.String()), true
	case preparationRequiredPage:
		return state.handlePreparationRequiredKey(message.String()), true
	default:
		return nil, false
	}
}

func (state *model) activateAddService(catalog CatalogSnapshot) tea.Cmd {
	if catalog.State == CatalogReady {
		state.page = addServicePage{}
		state.status = "Enter a fixed image URI or a complete runtime command"

		return nil
	}
	state.status = "Set up a desired-state repository before adding a service"
	if catalog.SuggestedRepository != "" {
		state.page = newRegistrationPage(catalog.SuggestedRepository)
	}

	return nil
}

func addServiceIndex(catalog CatalogSnapshot) int {
	return len(catalog.Services)
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
		state.status = statusConfirmFileMutation
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

func (state *model) handlePreparationRequiredKey(key string) tea.Cmd {
	switch key {
	case keyEnter, keyEscape, keyQuit:
		return tea.Quit
	default:
		return nil
	}
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
	if !state.deploymentOperationReady() {
		return nil
	}

	return state.begin("Writing and staging files", func(ctx context.Context, sequence uint64) tea.Cmd {
		workspace := state.workspace

		return func() tea.Msg {
			staged, err := workspace.Stage(ctx)

			return serviceStageResultMsg{sequence: sequence, preview: preview, staged: staged, err: err}
		}
	})
}

func (state *model) startServiceSuspend(preview servicePreviewPage) tea.Cmd {
	if !state.deploymentOperationReady() {
		return nil
	}

	return state.begin("Saving draft", func(ctx context.Context, sequence uint64) tea.Cmd {
		workspace := state.workspace

		return func() tea.Msg {
			return serviceSuspendResultMsg{sequence: sequence, preview: preview, err: workspace.Suspend(ctx)}
		}
	})
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
	state.page = state.wrapCommitDiff(commitPage{
		kind:    commitKindService,
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
	switch result.result.Outcome {
	case CommitNeedsUnsignedApproval:
		state.page = unsignedCommitConfirmationPage{commit: result.commit, focus: confirmationBack}
		state.status = statusSignedCommitMissing

		return command
	case CommitPreparationRequired:
		state.mutationOutcome = serviceCommitCreated
		state.applyBlocked = true
		state.page = preparationRequiredPage{draft: result.commit.preview.draft}
		state.status = "Preparation is required before validation"

		return command
	case CommitValidationUnavailable:
		state.mutationOutcome = serviceCommitCreated
		state.page = homePage{catalog: CatalogSnapshot{State: CatalogUnavailable}}
		state.status = "Commit created; validation is unavailable"

		return command
	case CommitSucceeded:
		state.mutationOutcome = serviceCommitCreated

		return tea.Batch(command, state.startCommittedSnapshot(result.result.Request))
	default:
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return command
	}
}

func (state *model) handleServiceSuspendResult(result serviceSuspendResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		return command
	}
	state.page = result.preview
	state.status = "Draft saved for this service"
	state.mutationOutcome = ""

	return command
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
	diff, err := canonicalStagedDiff(staged.Diff)
	if err != nil || composePath == "" || message == "" {
		return StagedService{}, errors.Join(errInvalidInput, err)
	}

	return StagedService{
		Diff: diff, ComposePath: composePath, Preparation: preparation, CommitMessage: message,
	}, nil
}
