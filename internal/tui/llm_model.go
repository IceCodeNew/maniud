package tui

import (
	"context"
	"errors"
	"slices"
	"strings"

	tea "charm.land/bubbletea/v2"
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
	llmProviderOpenAI           = "openai"
	llmProviderOpenAICompatible = "openai-compatible"
	llmProviderDeepSeek         = "deepseek"
	statusReviewLLMConfig       = "Review LLM configuration"
	displayUnavailable          = "Unavailable"
	llmKeySourcePrefixWidth     = 24
	llmModelWarningPrefixWidth  = 16
	llmChoiceIndentWidth        = 2
)

func llmProviderValues() []string {
	return []string{llmProviderOpenAI, llmProviderOpenAICompatible, llmProviderDeepSeek}
}

type llmConfigurationResultMsg struct {
	sequence      uint64
	review        reviewPage
	configuration LLMConfiguration
	err           error
}

type llmSaveResultMsg struct {
	sequence      uint64
	review        reviewPage
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

type llmConfigurationPage struct {
	review         reviewPage
	draft          LLMSettings
	step           int
	providerCursor int
}

func (llmConfigurationPage) isPage() {}

type llmSaveConfirmationPage struct {
	configuration llmConfigurationPage
	focus         confirmationFocus
}

func (llmSaveConfirmationPage) isPage() {}

type llmSaveOutcomeUnknownPage struct {
	review        reviewPage
	configuration LLMConfiguration
	focus         confirmationFocus
}

func (llmSaveOutcomeUnknownPage) isPage() {}

type llmQuestionPage struct {
	review        reviewPage
	configuration LLMConfiguration
	value         string
}

func (llmQuestionPage) isPage() {}

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
	case llmSaveOutcomeUnknownPage:
		return state.handleLLMSaveOutcomeUnknownKey(current, message.String()), true
	case llmQuestionPage:
		return state.handleLLMQuestionKey(current, message), true
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
		state.page = current.review
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
	if !accepted || result.err != nil {
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
	if configuration.Complete {
		state.page = llmQuestionPage{
			review: result.review, configuration: configuration,
			value: "Recommend deployment parameters for this service.",
		}
		state.status = "Ask for deployment recommendations"

		return command
	}
	state.page = newLLMConfigurationPage(result.review, configuration)
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

	return llmConfigurationPage{
		review: review,
		draft: LLMSettings{
			Provider: provider, Model: configuration.Model,
			Endpoint: configuration.Endpoint, Timeout: timeout,
		},
		providerCursor: cursor,
	}
}

func (state *model) handleLLMConfigurationKey(
	current llmConfigurationPage,
	message tea.KeyPressMsg,
) tea.Cmd {
	key := message.String()
	if key == keyEscape {
		current.draft.APIKey = ""
		state.page = current.review
		state.status = current.review.plan.status

		return nil
	}
	if key == keyQuit {
		current.draft.APIKey = ""

		return tea.Quit
	}
	if current.step == llmProviderStep {
		return state.handleLLMProviderKey(current, key)
	}

	return state.handleLLMConfigurationValueKey(current, message)
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
		if current.step == llmEndpointStep && current.draft.Provider != llmProviderOpenAICompatible {
			current.step++
		}
		state.status = llmConfigurationStepStatus(current.step)
		state.page = current

		return nil
	}
	if key == "c" && current.step == llmAPIKeyStep {
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
		current.configuration.draft.APIKey = ""

		return tea.Quit
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
				sequence: sequence, review: current.review, configuration: configuration, err: err,
			}
		}
	})
}

func (state *model) handleLLMSaveResult(result llmSaveResultMsg) tea.Cmd {
	accepted, command := state.completeLLMOperation(result.sequence, result.err)
	if !accepted {
		return command
	}
	if llmActionErrorCode(result.err) == LLMConfigSaveUnknown {
		configuration, valid := canonicalLLMConfiguration(result.configuration)
		if !valid {
			configuration = LLMConfiguration{}
		}
		state.page = llmSaveOutcomeUnknownPage{
			review: result.review, configuration: configuration, focus: confirmationBack,
		}
		state.status = "Configuration is visible, but storage durability is unknown"

		return command
	}
	if result.err != nil {
		state.page = result.review
		state.status = llmSaveErrorStatus(result.err)

		return command
	}
	configuration, valid := canonicalLLMConfiguration(result.configuration)
	if !valid || !configuration.Complete {
		state.page = newLLMConfigurationPage(result.review, configuration)
		state.status = "Saved configuration is incomplete"

		return command
	}
	state.page = llmQuestionPage{
		review: result.review, configuration: configuration,
		value: "Recommend deployment parameters for this service.",
	}
	state.status = "LLM configuration saved"
	state.mutationOutcome = "LLM configuration saved"

	return command
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
			state.page = newLLMConfigurationPage(current.review, current.configuration)
			state.status = statusReviewLLMConfig

			return nil
		}

		return state.startLLMSave(llmRetryConfigurationPage(current))
	case keyEscape:
		state.page = newLLMConfigurationPage(current.review, current.configuration)
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
	configuration.draft.APIKey = ""
	configuration.draft.ClearAPIKey = false

	return configuration
}

func (state *model) handleLLMQuestionKey(current llmQuestionPage, message tea.KeyPressMsg) tea.Cmd {
	switch message.String() {
	case keyEnter:
		if strings.TrimSpace(current.value) == "" {
			state.status = "Enter a deployment question"

			return nil
		}
		state.page = llmNetworkConfirmationPage{question: current, focus: confirmationBack}
		state.status = "Confirm provider networking or go back"

		return nil
	case "c":
		state.page = newLLMConfigurationPage(current.review, current.configuration)
		state.status = "Choose an LLM provider"

		return nil
	case keyEscape:
		state.page = current.review
		state.status = current.review.plan.status

		return nil
	case keyQuit:
		return tea.Quit
	}
	current.value = editSingleLine(current.value, message)
	state.page = current

	return nil
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
	return state.begin("Waiting for LLM recommendations", func(ctx context.Context, sequence uint64) tea.Cmd {
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
		state.page = result.page.question
		state.status = llmRecommendationErrorStatus(result.err)

		return command
	}
	canonical, valid := canonicalLLMResult(result.result)
	if !valid {
		state.page = result.page.question
		state.status = "LLM response did not pass local validation"

		return command
	}
	state.page = llmChoicesPage{question: result.page.question, result: canonical}
	state.status = "Choose a validated recommendation"

	return command
}

//nolint:exhaustive // Save owns configuration codes; provider codes collapse to the private fallback.
func llmSaveErrorStatus(err error) string {
	switch llmActionErrorCode(err) {
	case LLMConfigSaveStale:
		return "Configuration changed; reload and review before saving again"
	case LLMConfigPathInvalid:
		return "Protected configuration path does not meet the security requirements"
	case llm.ErrorConfigInvalid:
		return "LLM configuration is invalid"
	default:
		return "LLM configuration was not saved"
	}
}

//nolint:cyclop,exhaustive // Each relevant role code owns one privacy-safe recovery instruction.
func llmRecommendationErrorStatus(err error) string {
	switch llmActionErrorCode(err) {
	case llm.ErrorConfigInvalid:
		return "LLM configuration changed or is incomplete; review it before retrying"
	case llm.ErrorQuestionInvalid:
		return "The deployment question is invalid; edit it before retrying"
	case llm.ErrorForbiddenValue:
		return "The question includes protected deployment data; remove it before retrying"
	case llm.ErrorAuthentication:
		return "Provider authentication failed; review the configured API key"
	case llm.ErrorRateLimited:
		return "The provider rate-limited this request; wait before retrying"
	case llm.ErrorContextLimit:
		return "The provider context limit was exceeded; shorten the question"
	case llm.ErrorRefused:
		return "The provider refused this request; revise the question"
	case llm.ErrorEmptyResponse:
		return "The provider returned no recommendation; retrying may create another billed request"
	case llm.ErrorTruncated:
		return "The provider truncated the recommendation; retrying may create another billed request"
	case llm.ErrorInvalidResponse:
		return "The provider response did not pass local validation"
	case llm.ErrorModelUnavailable:
		return "The configured model is unavailable; review the model name"
	case llm.ErrorTimeout:
		return "The provider request timed out; retrying may create another billed request"
	case llm.ErrorCancelled:
		return "Provider request cancelled"
	case llm.ErrorContextStale:
		return "Deployment context changed; review current values before sending again"
	default:
		return "LLM recommendation failed; retry may create another billed request"
	}
}

func llmActionErrorCode(err error) llm.ErrorCode {
	action, found := errors.AsType[*LLMActionError](err)
	if found {
		return action.Code
	}

	return llm.ErrorProvider
}

func (state *model) handleLLMChoicesKey(current llmChoicesPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + len(current.result.Choices)) % len(current.result.Choices)
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % len(current.result.Choices)
	case keyEnter:
		return state.startLLMChoicePreview(current)
	case keyEscape:
		state.page = current.question
		state.status = "Ask another deployment question"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) startLLMChoicePreview(current llmChoicesPage) tea.Cmd {
	workspace, valid := state.deployments.(DeploymentRecommendationWorkspace)
	if !valid {
		state.status = "Recommended deployment editing is unavailable"

		return nil
	}

	return state.begin("Validating recommended deployment edit", func(ctx context.Context, sequence uint64) tea.Cmd {
		assistant := state.assistant
		choice := current.result.Choices[current.cursor]

		return func() tea.Msg {
			preview, err := workspace.PreviewRecommendation(ctx, current.question.review.request, choice.Changes)
			if err != nil {
				return llmPreviewResultMsg{sequence: sequence, page: current, err: err}
			}
			if err = ctx.Err(); err != nil {
				err = errors.Join(err, workspace.Discard(context.WithoutCancel(ctx)))
			} else if err = assistant.Accept(ctx, current.result.Token, current.cursor); err != nil {
				err = errors.Join(err, workspace.Discard(context.WithoutCancel(ctx)))
			} else if err = ctx.Err(); err != nil {
				err = errors.Join(err, workspace.Discard(context.WithoutCancel(ctx)))
			}

			return llmPreviewResultMsg{sequence: sequence, page: current, preview: preview, err: err}
		}
	})
}

func (state *model) handleLLMPreviewResult(result llmPreviewResultMsg) tea.Cmd {
	accepted, command := state.completeLLMOperation(result.sequence, result.err)
	if !accepted || result.err != nil {
		state.page = result.page
		state.status = "Recommended edit did not pass fresh Compose validation"

		return command
	}
	preview, err := canonicalDeploymentPreview(result.preview)
	if err != nil {
		state.page = result.page
		state.status = "Recommended edit could not be displayed safely"

		return command
	}
	state.page = deploymentPreviewPage{
		review: result.page.question.review, previous: result.page, preview: preview,
	}
	state.status = "Review the recommended deployment edit"

	return command
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

//nolint:cyclop // Every presentation field and warning crosses the same terminal-text trust boundary.
func canonicalLLMConfiguration(configuration LLMConfiguration) (LLMConfiguration, bool) {
	values := []*string{
		&configuration.Provider, &configuration.Model, &configuration.Endpoint, &configuration.Timeout,
		&configuration.Origin, &configuration.KeySource,
	}
	for _, value := range values {
		canonical, err := canonicalDisplay(*value)
		if err != nil {
			return LLMConfiguration{}, false
		}
		*value = canonical
	}
	warnings := make([]string, 0, len(configuration.Warnings))
	for _, warning := range configuration.Warnings {
		canonical, err := canonicalDisplay(warning)
		if err != nil {
			return LLMConfiguration{}, false
		}
		warnings = append(warnings, canonical)
	}
	configuration.Warnings = warnings
	if configuration.Provider != "" && !slices.Contains(llmProviderValues(), configuration.Provider) {
		return LLMConfiguration{}, false
	}
	if configuration.Complete {
		return configuration, configuration.Identity != "" && configuration.Provider != "" &&
			configuration.Model != "" && configuration.Origin != "" && configuration.KeyConfigured
	}

	return configuration, true
}

//nolint:cyclop // Nested choices and mutations are one untrusted role result and fail closed together.
func canonicalLLMResult(result LLMResult) (LLMResult, bool) {
	if result.Token == "" || len(result.Choices) == 0 || len(result.Choices) > 3 {
		return LLMResult{}, false
	}
	requested, err := canonicalDisplay(result.RequestedModel)
	if err != nil || requested == "" {
		return LLMResult{}, false
	}
	reported, err := canonicalDisplay(result.ReportedModel)
	if err != nil {
		return LLMResult{}, false
	}
	result.RequestedModel = requested
	result.ReportedModel = reported
	for choiceIndex := range result.Choices {
		summary, summaryErr := canonicalDisplay(result.Choices[choiceIndex].Summary)
		if summaryErr != nil || summary == "" || len(result.Choices[choiceIndex].Changes) == 0 {
			return LLMResult{}, false
		}
		result.Choices[choiceIndex].Summary = summary
		for changeIndex := range result.Choices[choiceIndex].Changes {
			change := &result.Choices[choiceIndex].Changes[changeIndex]
			field, fieldErr := canonicalDisplay(change.FieldID)
			value, valueErr := canonicalDisplay(change.Value)
			if fieldErr != nil || valueErr != nil || field == "" {
				return LLMResult{}, false
			}
			change.FieldID = field
			change.Value = value
		}
	}

	return result, true
}
