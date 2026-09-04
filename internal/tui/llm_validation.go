package tui

import (
	"errors"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/llm"
)

//nolint:exhaustive // Save owns configuration codes; provider codes collapse to the private fallback.
func llmSaveErrorStatus(err error) string {
	switch llmActionErrorCode(err) {
	case LLMConfigReloadFailed:
		return "Configuration changed; effective reload failed"
	case LLMConfigSaveStale:
		return "Configuration changed; review the reloaded values before saving again"
	case LLMConfigPathInvalid:
		return "Protected configuration path does not meet the security requirements"
	case llm.ErrorConfigInvalid:
		return "LLM configuration is invalid"
	default:
		return "LLM configuration was not saved"
	}
}

//nolint:cyclop,exhaustive // Each relevant role code owns one privacy-safe recovery instruction.
func llmRecommendationErrorStatus(code llm.ErrorCode) string {
	switch code {
	case llm.ErrorConfigInvalid:
		return "LLM configuration changed or is incomplete; review it before retrying"
	case llm.ErrorQuestionInvalid:
		return "The deployment question is invalid; edit it before retrying"
	case llm.ErrorConversationLimit:
		return "Conversation limit reached; send again to start a new conversation"
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
		return "The provider returned no response; retrying may create another billed request"
	case llm.ErrorTruncated:
		return "The provider truncated the response; retrying may create another billed request"
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
		return "LLM request failed; retry may create another billed request"
	}
}

func llmRequestFailureFromError(err error) llmRequestFailure {
	action, valid := errors.AsType[*llm.ActionError](err)
	if !valid {
		return llmRequestFailure{
			code: llm.ErrorProvider, stage: llm.ActionStageProviderRequest,
			outcome: llm.RequestOutcomeUnknown,
		}
	}
	stage := action.Stage
	switch stage {
	case llm.ActionStageRequestPreparation, llm.ActionStageProviderRequest,
		llm.ActionStageProviderResponse, llm.ActionStageContextValidation:
	default:
		stage = llm.ActionStageProviderRequest
	}
	outcome := action.RequestOutcome
	switch outcome {
	case llm.RequestNotStarted, llm.RequestOutcomeUnknown, llm.RequestResponseReceived:
	default:
		outcome = llm.RequestOutcomeUnknown
	}

	return llmRequestFailure{
		code: llmActionErrorCode(action), stage: stage, outcome: outcome,
	}
}

func llmActionErrorCode(err error) llm.ErrorCode {
	action, found := errors.AsType[*llm.ActionError](err)
	if !found {
		return llm.ErrorProvider
	}
	switch action.Code {
	case LLMConfigPathInvalid, LLMConfigReloadFailed, LLMConfigSaveStale,
		LLMConfigSaveUnknown, LLMConfigSavedReloadFailed,
		llm.ErrorConfigInvalid, llm.ErrorQuestionInvalid, llm.ErrorConversationLimit,
		llm.ErrorForbiddenValue, llm.ErrorAuthentication, llm.ErrorRateLimited,
		llm.ErrorContextLimit, llm.ErrorRefused, llm.ErrorEmptyResponse,
		llm.ErrorTruncated, llm.ErrorInvalidResponse, llm.ErrorModelUnavailable,
		llm.ErrorTimeout, llm.ErrorCancelled, llm.ErrorProvider, llm.ErrorContextStale:
		return action.Code
	default:
		return llm.ErrorProvider
	}
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
		choice := &result.Choices[choiceIndex]
		message, messageErr := canonicalLLMMessage(choice.Message)
		if messageErr != nil || message == "" || !validLLMChoiceShape(*choice) {
			return LLMResult{}, false
		}
		choice.Message = message
		for changeIndex := range choice.Changes {
			change := &choice.Changes[changeIndex]
			field, fieldErr := canonicalDisplay(change.FieldID)
			value, valueErr := canonicalDisplay(change.Value)
			if fieldErr != nil || valueErr != nil || field == "" {
				return LLMResult{}, false
			}
			change.FieldID = field
			change.Value = value
		}
		if choice.Kind == llm.ChoiceRecommendation {
			if _, patchErr := recommendationPatches(choice.Changes); patchErr != nil {
				return LLMResult{}, false
			}
		}
	}

	return result, true
}

func canonicalLLMMessage(value string) (string, error) {
	lines := strings.Split(value, "\n")
	if len(value) > maximumLLMMessageBytes || len(lines) > maximumLLMMessageLines {
		return "", errInvalidInput
	}
	for index, line := range lines {
		canonical, err := canonicalDisplay(line)
		if err != nil {
			return "", err
		}
		lines[index] = canonical
	}

	return strings.Join(lines, "\n"), nil
}

func validLLMChoiceShape(choice llm.Choice) bool {
	switch choice.Kind {
	case llm.ChoiceAnswer, llm.ChoiceClarification:
		return len(choice.Changes) == 0
	case llm.ChoiceRecommendation:
		return len(choice.Changes) > 0 && len(choice.Changes) <= len(application.DeploymentFields())
	default:
		return false
	}
}
