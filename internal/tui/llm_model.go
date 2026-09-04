package tui

import (
	"context"
	"errors"
	"fmt"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/llm"
)

const (
	llmProviderStep = iota
	llmModelStep
	llmEndpointStep
	llmTimeoutStep
	llmAPIKeyStep
	llmStepCount
)

const (
	statusReviewLLMConfig      = "Review LLM configuration"
	statusLLMConfigSaved       = "LLM configuration saved"
	labelAskLLMDeployment      = "Ask LLM about deployment"
	displayUnavailable         = "Unavailable"
	keyEditLLMConfiguration    = "ctrl+e"
	llmModelWarningPrefixWidth = 16
	llmChoiceIndentWidth       = 2
	maximumLLMMessageBytes     = 1024
	maximumLLMMessageLines     = 8
)

func llmProviderValues() []string {
	providers := llm.SupportedProviders()
	values := make([]string, len(providers))
	for index, provider := range providers {
		values[index] = string(provider)
	}

	return values
}

type llmConfigurationResultMsg struct {
	sequence      uint64
	review        reviewPage
	configuration LLMConfiguration
	err           error
}

type llmSaveResultMsg struct {
	sequence      uint64
	page          llmConfigurationPage
	configuration LLMConfiguration
	err           error
}

type llmRecommendResultMsg struct {
	sequence uint64
	page     llmNetworkConfirmationPage
	result   LLMResult
	err      error
}

type llmPreviewResultMsg struct {
	sequence uint64
	page     llmChoicesPage
	preview  DeploymentEditPreview
	err      error
}

type llmAcceptResultMsg struct {
	sequence uint64
	page     llmChoicesPage
	err      error
}

type llmConfigurationPage struct {
	review         reviewPage
	question       *llmQuestionPage
	baseline       LLMSettings
	draft          LLMSettings
	step           int
	providerCursor int
}

func (llmConfigurationPage) isPage() {}

func (current llmConfigurationPage) acceptsTextInput() bool {
	return current.step != llmProviderStep
}

func (current llmConfigurationPage) dirty() bool {
	return current.draft != current.baseline
}

type llmSaveConfirmationPage struct {
	configuration llmConfigurationPage
	focus         confirmationFocus
}

func (llmSaveConfirmationPage) isPage() {}

type llmDiscardConfirmationPage struct {
	configuration llmConfigurationPage
	focus         confirmationFocus
	quit          bool
}

func (llmDiscardConfirmationPage) isPage() {}

type llmSaveOutcomeUnknownPage struct {
	review        reviewPage
	configuration LLMConfiguration
	question      *llmQuestionPage
	focus         confirmationFocus
}

func (llmSaveOutcomeUnknownPage) isPage() {}

type llmQuestionPage struct {
	review        reviewPage
	configuration LLMConfiguration
	assistant     llm.Choice
	value         string
	failure       llmRequestFailure
}

func (llmQuestionPage) isPage() {}

func (llmQuestionPage) acceptsTextInput() bool { return true }

type llmRequestFailure struct {
	code    llm.ErrorCode
	stage   llm.ActionStage
	outcome llm.RequestOutcome
}

type llmNetworkConfirmationPage struct {
	question llmQuestionPage
	focus    confirmationFocus
}

func (llmNetworkConfirmationPage) isPage() {}

type llmChoicesPage struct {
	question llmQuestionPage
	result   LLMResult
	cursor   int
}

func (llmChoicesPage) isPage() {}

func (state *model) handleLLMMessage(message tea.Msg) (tea.Cmd, bool) {
	switch message := message.(type) {
	case llmConfigurationResultMsg:
		return state.handleLLMConfigurationResult(message), true
	case llmSaveResultMsg:
		return state.handleLLMSaveResult(message), true
	case llmRecommendResultMsg:
		return state.handleLLMRecommendResult(message), true
	case llmPreviewResultMsg:
		return state.handleLLMPreviewResult(message), true
	case llmAcceptResultMsg:
		return state.handleLLMAcceptResult(message), true
	default:
		return nil, false
	}
}

func (state *model) handleLLMPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case llmConfigurationPage:
		return state.handleLLMConfigurationKey(current, message), true
	case llmSaveConfirmationPage:
		return state.handleLLMSaveConfirmationKey(current, message.String()), true
	case llmDiscardConfirmationPage:
		return state.handleLLMDiscardConfirmationKey(current, message.String()), true
	case llmSaveOutcomeUnknownPage:
		return state.handleLLMSaveOutcomeUnknownKey(current, message.String()), true
	case llmQuestionPage:
		state.handleLLMQuestionKey(current, message)

		return nil, true
	case llmNetworkConfirmationPage:
		return state.handleLLMNetworkConfirmationKey(current, message.String()), true
	case llmChoicesPage:
		return state.handleLLMChoicesKey(current, message.String()), true
	default:
		return nil, false
	}
}

func (state *model) invalidateLLMConfirmation() bool {
	switch current := state.page.(type) {
	case llmSaveConfirmationPage:
		current.configuration.draft.APIKey = ""
		state.page = current.configuration
		state.status = statusReviewLarger

		return true
	case llmNetworkConfirmationPage:
		state.page = current.question
		state.status = statusReviewLarger

		return true
	case llmSaveOutcomeUnknownPage:
		state.page = current.review
		state.status = statusReviewLarger

		return true
	case llmConfigurationPage:
		current.draft.APIKey = ""
		state.page = current
		state.status = statusReviewLarger

		return true
	case llmDiscardConfirmationPage:
		current.configuration.draft.APIKey = ""
		state.page = current.configuration
		state.status = statusReviewLarger

		return true
	default:
		return false
	}
}

func (state *model) startLLMConfiguration(review reviewPage) tea.Cmd {
	if state.assistant == nil {
		state.status = "LLM assistance is unavailable"

		return nil
	}

	return state.begin("Loading LLM configuration", func(ctx context.Context, sequence uint64) tea.Cmd {
		assistant := state.assistant

		return func() tea.Msg {
			configuration, err := assistant.Configuration(ctx)

			return llmConfigurationResultMsg{
				sequence: sequence, review: review, configuration: configuration, err: err,
			}
		}
	})
}

func (state *model) handleLLMConfigurationResult(result llmConfigurationResultMsg) tea.Cmd {
	accepted, command := state.completeLLMOperation(result.sequence, result.err)
	if !accepted {
		return command
	}
	if result.err != nil {
		state.page = result.review
		state.status = "LLM configuration is unavailable"

		return command
	}
	configuration, valid := canonicalLLMConfiguration(result.configuration)
	if !valid {
		state.page = result.review
		state.status = "LLM configuration is invalid"

		return command
	}
	state.configReloadNeeded = false
	question := state.llmQuestionToResume
	state.llmQuestionToResume = nil
	if configuration.Complete {
		if question != nil {
			question.configuration = configuration
			state.page = *question
			state.status = labelAskLLMDeployment

			return command
		}
		state.page = llmQuestionPage{
			review: result.review, configuration: configuration,
			value: "Recommend deployment parameters for this service.",
		}
		state.status = labelAskLLMDeployment

		return command
	}
	page := newLLMConfigurationPage(result.review, configuration)
	page.question = question
	state.page = page
	state.status = "Choose an LLM provider"

	return command
}

func newLLMConfigurationPage(review reviewPage, configuration LLMConfiguration) llmConfigurationPage {
	providers := llmProviderValues()
	provider := configuration.Provider
	if !slices.Contains(providers, provider) {
		provider = providers[0]
	}
	timeout := configuration.Timeout
	if timeout == "" {
		timeout = "60"
	}
	cursor := slices.Index(providers, provider)

	draft := LLMSettings{
		Provider: provider, Model: configuration.Model,
		Endpoint: configuration.Endpoint, Timeout: timeout,
	}

	return llmConfigurationPage{
		review: review, baseline: draft, draft: draft, providerCursor: cursor,
	}
}

func (state *model) handleLLMConfigurationKey(
	current llmConfigurationPage,
	message tea.KeyPressMsg,
) tea.Cmd {
	key := message.String()
	if key == keyEscape {
		if current.step != llmProviderStep {
			current.draft.APIKey = ""
			current.step--
			if current.step == llmEndpointStep &&
				current.draft.Provider != string(llm.ProviderOpenAICompatible) {
				current.step--
			}
			state.page = current
			state.status = llmConfigurationStepStatus(current.step)

			return nil
		}

		return state.leaveLLMConfiguration(current, false)
	}
	if current.step == llmProviderStep {
		if key == keyQuit {
			return state.leaveLLMConfiguration(current, true)
		}

		return state.handleLLMProviderKey(current, key)
	}

	return state.handleLLMConfigurationValueKey(current, message)
}

func (state *model) leaveLLMConfiguration(current llmConfigurationPage, quit bool) tea.Cmd {
	current.draft.APIKey = ""
	if current.dirty() {
		state.page = llmDiscardConfirmationPage{
			configuration: current, focus: confirmationBack, quit: quit,
		}
		state.status = "Discard unsaved LLM configuration?"

		return nil
	}
	if quit {
		return tea.Quit
	}
	if current.question != nil {
		state.page = *current.question
		state.status = labelAskLLMDeployment

		return nil
	}
	state.page = current.review
	state.status = current.review.plan.status

	return nil
}

func (state *model) handleLLMProviderKey(current llmConfigurationPage, key string) tea.Cmd {
	providers := llmProviderValues()
	switch key {
	case "up", "left", "k", keyShiftTab:
		current.providerCursor = (current.providerCursor - 1 + len(providers)) % len(providers)
	case keyDown, "right", "j", keyTab:
		current.providerCursor = (current.providerCursor + 1) % len(providers)
	case keyEnter:
		current.draft.Provider = providers[current.providerCursor]
		current.step = llmModelStep
		state.status = "Enter the model name"
	}
	state.page = current

	return nil
}

//nolint:cyclop // The four closed input steps share navigation and secret-clearing semantics.
func (state *model) handleLLMConfigurationValueKey(
	current llmConfigurationPage,
	message tea.KeyPressMsg,
) tea.Cmd {
	key := message.String()
	if key == keyEnter {
		if !validLLMConfigurationStep(current) {
			state.status = "Complete this configuration value"

			return nil
		}
		if current.step == llmAPIKeyStep {
			state.page = llmSaveConfirmationPage{configuration: current, focus: confirmationBack}
			state.status = "Review LLM configuration before saving"

			return nil
		}
		current.step++
		if current.step == llmEndpointStep && current.draft.Provider != string(llm.ProviderOpenAICompatible) {
			current.step++
		}
		state.status = llmConfigurationStepStatus(current.step)
		state.page = current

		return nil
	}
	if key == "ctrl+d" && current.step == llmAPIKeyStep {
		current.draft.APIKey = ""
		current.draft.ClearAPIKey = true
		state.status = "The protected key will be cleared after confirmation"
		state.page = current

		return nil
	}
	switch current.step {
	case llmModelStep:
		current.draft.Model = editSingleLine(current.draft.Model, message)
	case llmEndpointStep:
		current.draft.Endpoint = editSingleLine(current.draft.Endpoint, message)
	case llmTimeoutStep:
		current.draft.Timeout = editSingleLine(current.draft.Timeout, message)
	case llmAPIKeyStep:
		current.draft.APIKey = editSingleLine(current.draft.APIKey, message)
		current.draft.ClearAPIKey = false
	}
	state.page = current

	return nil
}

func validLLMConfigurationStep(current llmConfigurationPage) bool {
	switch current.step {
	case llmModelStep:
		return current.draft.Model != ""
	case llmEndpointStep:
		return current.draft.Endpoint != ""
	case llmTimeoutStep:
		return current.draft.Timeout != ""
	case llmAPIKeyStep:
		return true
	default:
		return false
	}
}

func llmConfigurationStepStatus(step int) string {
	switch step {
	case llmModelStep:
		return "Enter the model name"
	case llmEndpointStep:
		return "Enter the compatible HTTPS endpoint"
	case llmTimeoutStep:
		return "Enter the per-attempt timeout in seconds"
	case llmAPIKeyStep:
		return "Enter a new API key or leave it blank to preserve the current key"
	default:
		return statusReviewLLMConfig
	}
}

func (state *model) handleLLMSaveConfirmationKey(
	current llmSaveConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.configuration
			state.status = statusReviewLLMConfig

			return nil
		}

		configuration := current.configuration
		current.configuration.draft.APIKey = ""
		state.page = current

		return state.startLLMSave(configuration)
	case keyEscape:
		state.page = current.configuration
		state.status = statusReviewLLMConfig

		return nil
	case keyQuit:
		return state.leaveLLMConfiguration(current.configuration, true)
	}
	state.page = current

	return nil
}

func (state *model) handleLLMDiscardConfirmationKey(
	current llmDiscardConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.configuration
		state.status = statusReviewLarger

		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.configuration
			state.status = llmConfigurationStepStatus(current.configuration.step)

			return nil
		}
		if current.quit {
			return tea.Quit
		}
		if current.configuration.question != nil {
			state.page = *current.configuration.question
			state.status = labelAskLLMDeployment

			return nil
		}
		state.page = current.configuration.review
		state.status = current.configuration.review.plan.status

		return nil
	case keyEscape:
		state.page = current.configuration
		state.status = llmConfigurationStepStatus(current.configuration.step)

		return nil
	}
	state.page = current

	return nil
}

func (state *model) startLLMSave(current llmConfigurationPage) tea.Cmd {
	return state.begin("Saving protected LLM configuration", func(ctx context.Context, sequence uint64) tea.Cmd {
		assistant := state.assistant
		settings := current.draft
		settings.APIKey = strings.Clone(settings.APIKey)
		current.draft.APIKey = ""

		return func() tea.Msg {
			configuration, err := assistant.Save(ctx, settings)
			settings.APIKey = ""

			return llmSaveResultMsg{
				sequence: sequence, page: current, configuration: configuration, err: err,
			}
		}
	})
}

func (state *model) handleLLMSaveResult(result llmSaveResultMsg) tea.Cmd {
	accepted, command := state.completeLLMOperation(result.sequence, result.err)
	if !accepted {
		return command
	}
	if result.err != nil {
		state.handleLLMSaveFailure(result)

		return command
	}
	review := result.page.review
	configuration, valid := canonicalLLMConfiguration(result.configuration)
	if !valid || !configuration.Complete {
		page := newLLMConfigurationPage(review, configuration)
		page.question = result.page.question
		state.page = page
		state.status = "Saved configuration is incomplete"

		return command
	}
	if result.page.question != nil {
		question := *result.page.question
		question.configuration = configuration
		state.page = question
	} else {
		state.page = llmQuestionPage{
			review: review, configuration: configuration,
			value: "Recommend deployment parameters for this service.",
		}
	}
	state.status = statusLLMConfigSaved
	state.mutationOutcome = statusLLMConfigSaved

	return command
}

func (state *model) handleLLMSaveFailure(result llmSaveResultMsg) {
	review := result.page.review
	//nolint:exhaustive // Remaining provider and validation failures return the safe local draft.
	switch errorCode := llmActionErrorCode(result.err); errorCode {
	case LLMConfigSaveUnknown:
		configuration, valid := canonicalLLMConfiguration(result.configuration)
		if !valid {
			configuration = LLMConfiguration{}
		}
		state.page = llmSaveOutcomeUnknownPage{
			review: review, configuration: configuration,
			question: result.page.question, focus: confirmationBack,
		}
		state.status = "Configuration is visible, but storage durability is unknown"
	case LLMConfigSavedReloadFailed:
		state.page = review
		state.status = "Configuration saved; effective reload failed"
		state.mutationOutcome = statusLLMConfigSaved
		state.configReloadNeeded = true
		state.llmQuestionToResume = result.page.question
	case LLMConfigSaveStale:
		configuration, valid := canonicalLLMConfiguration(result.configuration)
		if valid {
			page := newLLMConfigurationPage(review, configuration)
			page.question = result.page.question
			state.page = page
		} else {
			state.page = review
			state.llmQuestionToResume = result.page.question
		}
		state.status = llmSaveErrorStatus(result.err)
	case LLMConfigReloadFailed:
		state.page = review
		state.status = llmSaveErrorStatus(result.err)
		state.llmQuestionToResume = result.page.question
	default:
		result.page.draft.APIKey = ""
		state.page = result.page
		state.status = llmSaveErrorStatus(result.err)
	}
}

func (state *model) handleLLMSaveOutcomeUnknownKey(
	current llmSaveOutcomeUnknownPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = llmRetryConfigurationPage(current)
			state.status = statusReviewLLMConfig

			return nil
		}

		return state.startLLMSave(llmRetryConfigurationPage(current))
	case keyEscape:
		state.page = llmRetryConfigurationPage(current)
		state.status = statusReviewLLMConfig

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func llmRetryConfigurationPage(current llmSaveOutcomeUnknownPage) llmConfigurationPage {
	configuration := newLLMConfigurationPage(current.review, current.configuration)
	configuration.question = current.question
	configuration.draft.APIKey = ""
	configuration.draft.ClearAPIKey = false

	return configuration
}

func (state *model) handleLLMQuestionKey(current llmQuestionPage, message tea.KeyPressMsg) {
	switch message.String() {
	case keyEnter:
		if strings.TrimSpace(current.value) == "" {
			state.status = "Enter a deployment question"

			return
		}
		current.failure = llmRequestFailure{}
		state.page = llmNetworkConfirmationPage{question: current, focus: confirmationBack}
		state.status = "Confirm provider networking or go back"

		return
	case keyEditLLMConfiguration:
		configuration := newLLMConfigurationPage(current.review, current.configuration)
		current.failure = llmRequestFailure{}
		configuration.question = &current
		state.page = configuration
		state.status = "Choose an LLM provider"

		return
	case keyEscape:
		state.page = current.review
		state.status = current.review.plan.status

		return
	}
	current.value = editSingleLine(current.value, message)
	current.failure = llmRequestFailure{}
	state.page = current
}

func (state *model) handleLLMNetworkConfirmationKey(
	current llmNetworkConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.question
			state.status = "Review the deployment question"

			return nil
		}

		return state.startLLMRecommendation(current)
	case keyEscape:
		state.page = current.question
		state.status = "Review the deployment question"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) startLLMRecommendation(current llmNetworkConfirmationPage) tea.Cmd {
	return state.begin("Waiting for the LLM response", func(ctx context.Context, sequence uint64) tea.Cmd {
		assistant := state.assistant
		question := current.question

		return func() tea.Msg {
			result, err := assistant.Recommend(
				ctx, question.review.request, question.configuration.Identity, question.value,
			)

			return llmRecommendResultMsg{sequence: sequence, page: current, result: result, err: err}
		}
	})
}

func (state *model) handleLLMRecommendResult(result llmRecommendResultMsg) tea.Cmd {
	accepted, command := state.completeLLMOperation(result.sequence, result.err)
	if !accepted {
		return command
	}
	if result.err != nil {
		question := result.page.question
		question.failure = llmRequestFailureFromError(result.err)
		state.page = question
		state.status = llmRecommendationErrorStatus(question.failure.code)

		return command
	}
	canonical, valid := canonicalLLMResult(result.result)
	if !valid {
		state.page = result.page.question
		state.status = "LLM response did not pass local validation"

		return command
	}
	state.page = llmChoicesPage{question: result.page.question, result: canonical}
	state.status = "Choose an assistant response"

	return command
}

func (state *model) handleLLMChoicesKey(current llmChoicesPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + len(current.result.Choices)) % len(current.result.Choices)
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % len(current.result.Choices)
	case keyEnter:
		if current.result.Choices[current.cursor].Kind == llm.ChoiceRecommendation {
			return state.startLLMChoicePreview(current)
		}
		state.clearRecoverableDeploymentFailure()

		return state.startLLMChoiceAccept(current)
	case keyEscape:
		state.clearRecoverableDeploymentFailure()
		state.page = current.question
		state.status = "Ask another deployment question"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) startLLMChoiceAccept(current llmChoicesPage) tea.Cmd {
	return state.begin("Recording provider response", func(ctx context.Context, sequence uint64) tea.Cmd {
		assistant := state.assistant

		return func() tea.Msg {
			return llmAcceptResultMsg{
				sequence: sequence, page: current,
				err: assistant.Accept(ctx, current.result.Token, current.cursor),
			}
		}
	})
}

func (state *model) startLLMChoicePreview(current llmChoicesPage) tea.Cmd {
	workspace := state.deployments.(DeploymentPatchWorkspace) //nolint:forcetypeassert // Setup proved the capability.
	if !state.deploymentOperationReady() {
		return nil
	}
	choice := current.result.Choices[current.cursor]
	patches, err := recommendationPatches(choice.Changes)
	if err != nil {
		state.status = "LLM response did not pass local validation"

		return nil
	}

	return state.begin("Validating recommended deployment edit", func(ctx context.Context, sequence uint64) tea.Cmd {
		assistant := state.assistant
		deployments := state.deployments

		return func() tea.Msg {
			preview, err := workspace.PreviewPatches(ctx, current.question.review.request, patches)
			if err != nil {
				return llmPreviewResultMsg{sequence: sequence, page: current, err: err}
			}
			if err = ctx.Err(); err != nil {
				err = errors.Join(err, deployments.Discard(context.WithoutCancel(ctx)))
			} else if err = assistant.Accept(ctx, current.result.Token, current.cursor); err != nil {
				err = errors.Join(err, deployments.Discard(context.WithoutCancel(ctx)))
			} else if err = ctx.Err(); err != nil {
				err = errors.Join(err, deployments.Discard(context.WithoutCancel(ctx)))
			}

			return llmPreviewResultMsg{sequence: sequence, page: current, preview: preview, err: err}
		}
	})
}

func recommendationPatches(changes []llm.Change) ([]application.DeploymentPatch, error) {
	patches := make([]application.DeploymentPatch, 0, len(changes))
	seen := make(map[application.DeploymentField]struct{}, len(changes))
	for _, change := range changes {
		patch, err := application.ParseDeploymentPatch(change.FieldID, change.Value, change.Unset)
		if err != nil {
			return nil, fmt.Errorf("parse recommendation patch: %w", err)
		}
		field := patch.Field()
		if _, duplicate := seen[field]; duplicate {
			return nil, application.ErrInvalidDeploymentPatch
		}
		seen[field] = struct{}{}
		patches = append(patches, patch)
	}

	return patches, nil
}

func (state *model) handleLLMPreviewResult(result llmPreviewResultMsg) tea.Cmd {
	if _, deploymentError := errors.AsType[*DeploymentActionError](result.err); deploymentError {
		accepted, command := state.completeDeploymentOperation(result.sequence, result.err)
		if accepted {
			state.page = result.page
		}

		return command
	}
	accepted, command := state.completeLLMOperation(result.sequence, result.err)
	if !accepted {
		return command
	}
	if result.err != nil {
		action, actionError := errors.AsType[*llm.ActionError](result.err)
		if actionError {
			state.page = result.page
			state.status = llmRecommendationErrorStatus(llmActionErrorCode(action))
			if action.Code == llm.ErrorConversationLimit {
				state.page = result.page.question
			}
		} else {
			state.page = result.page
			state.status = "Recommended edit did not pass fresh Compose validation"
		}

		return command
	}
	preview, err := canonicalDeploymentPreview(result.preview)
	if err != nil {
		state.page = result.page
		state.status = "Recommended edit could not be displayed safely"

		return command
	}
	if preview.NoChanges {
		state.page = llmContinuationPage(result.page)
		state.status = "Recommendation already matches current source"

		return command
	}
	state.page = deploymentPreviewPage{
		review: result.page.question.review, previous: llmContinuationPage(result.page), preview: preview,
	}
	state.status = "Review the recommended deployment edit"

	return command
}

func (state *model) handleLLMAcceptResult(result llmAcceptResultMsg) tea.Cmd {
	accepted, command := state.completeLLMOperation(result.sequence, result.err)
	if !accepted {
		return command
	}
	if result.err != nil {
		state.page = result.page
		state.status = "Provider response could not be added to this conversation"
		if llmActionErrorCode(result.err) == llm.ErrorConversationLimit {
			state.page = result.page.question
			state.status = llmRecommendationErrorStatus(llmActionErrorCode(result.err))
		}

		return command
	}
	state.page = llmContinuationPage(result.page)
	if result.page.result.Choices[result.page.cursor].Kind == llm.ChoiceClarification {
		state.status = "Answer the provider's question"
	} else {
		state.status = "Ask another deployment question"
	}

	return command
}

func llmContinuationPage(current llmChoicesPage) llmQuestionPage {
	question := current.question
	question.assistant = current.result.Choices[current.cursor]
	question.value = ""

	return question
}

func (state *model) completeLLMOperation(sequence uint64, err error) (bool, tea.Cmd) {
	if sequence != state.sequence {
		return false, nil
	}
	state.finishOperation()
	if state.quitAfterOperation {
		state.quitAfterOperation = false

		return true, tea.Quit
	}
	if errors.Is(err, context.Canceled) {
		state.status = "Cancelled"
	}

	return true, nil
}
