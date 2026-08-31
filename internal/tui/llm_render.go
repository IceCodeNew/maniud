package tui

import (
	"fmt"
	"strings"

	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

func (state *model) llmPageBody(width int) ([]string, bool) {
	switch current := state.page.(type) {
	case llmConfigurationPage:
		return state.llmConfigurationBody(current, width), true
	case llmSaveConfirmationPage:
		return state.llmSaveConfirmationBody(current, width), true
	case llmSaveOutcomeUnknownPage:
		return state.llmSaveOutcomeUnknownBody(current, width), true
	case llmQuestionPage:
		return state.llmQuestionBody(current, width), true
	case llmNetworkConfirmationPage:
		return state.llmNetworkConfirmationBody(current, width), true
	case llmChoicesPage:
		return state.llmChoicesBody(current, width), true
	default:
		return nil, false
	}
}

func (state *model) llmConfigurationBody(current llmConfigurationPage, width int) []string {
	lines := []string{
		state.title("Configure LLM assistance"),
		fmt.Sprintf("Step %d of 5", current.step+1),
		"",
	}
	switch current.step {
	case llmProviderStep:
		lines = append(lines, state.muted("PROVIDER"))
		for index, provider := range llmProviderValues() {
			lines = append(lines, state.choice(index == current.providerCursor, llmProviderLabel(provider), width))
		}
		lines = append(lines, "", state.muted("Up/Down Choose   Enter Continue"))
	case llmModelStep:
		lines = append(lines,
			"Model",
			state.accent(llmInputValue(current.draft.Model, "model name", width)+state.symbol("▌", "_")),
			"",
			state.muted("Enter Continue   Esc Discard"),
		)
	case llmEndpointStep:
		lines = append(lines,
			"HTTPS endpoint",
			state.accent(llmInputValue(current.draft.Endpoint, "https://provider.example/v1", width)+
				state.symbol("▌", "_")),
			"",
			state.muted("Enter Continue   Esc Discard"),
		)
	case llmTimeoutStep:
		lines = append(lines,
			"Per-attempt timeout (5–120 seconds)",
			state.accent(llmInputValue(current.draft.Timeout, "60", width)+state.symbol("▌", "_")),
			"The provider may make up to three attempts within three times this value.",
			"",
			state.muted("Enter Continue   Esc Discard"),
		)
	case llmAPIKeyStep:
		keyState := "Keep the effective key"
		if current.draft.APIKey != "" {
			keyState = "New key entered"
		}
		if current.draft.ClearAPIKey {
			keyState = "Clear the protected XDG key"
		}
		lines = append(lines,
			"API key",
			state.accent(keyState),
			"The key value is hidden and only a new value is written to the protected XDG .env.",
			"",
			state.muted("Type Replace   c Clear XDG key   Enter Review   Esc Discard"),
		)
	}

	return lines
}

func llmProviderLabel(provider string) string {
	switch provider {
	case "openai":
		return "OpenAI"
	case "openai-compatible":
		return "OpenAI-compatible HTTPS endpoint"
	case "deepseek":
		return "DeepSeek"
	default:
		return provider
	}
}

func llmInputValue(value, placeholder string, width int) string {
	if value == "" {
		value = placeholder
	}

	return terminaltext.Middle(value, max(width-detailsPadding, 1), "…")
}

func (state *model) llmSaveConfirmationBody(current llmSaveConfirmationPage, width int) []string {
	draft := current.configuration.draft
	endpoint := "Official endpoint"
	if draft.Provider == llmProviderOpenAICompatible {
		endpoint = draft.Endpoint
	}
	keyAction := "Keep effective key"
	if draft.APIKey != "" {
		keyAction = "Replace protected key"
	}
	if draft.ClearAPIKey {
		keyAction = "Clear protected XDG key"
	}

	return []string{
		state.title("Save LLM configuration"),
		"Saving changes the protected XDG .env. It does not contact the provider.",
		"",
		"Provider  " + llmProviderLabel(draft.Provider),
		"Model     " + terminaltext.Middle(draft.Model, max(width-serviceFieldWidth, 1), "…"),
		"Endpoint  " + terminaltext.Middle(endpoint, max(width-serviceFieldWidth, 1), "…"),
		"Timeout   " + draft.Timeout + " seconds per attempt",
		"API key   " + keyAction,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Save configuration", width),
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
	}
}

func (state *model) llmSaveOutcomeUnknownBody(current llmSaveOutcomeUnknownPage, width int) []string {
	configuration := current.configuration
	keyState := "not configured"
	if configuration.KeyConfigured {
		keyState = "configured"
		if configuration.KeySource != "" {
			keyState += " from " + configuration.KeySource
		}
	}

	return []string{
		state.title("Configuration durability is unknown"),
		"The configuration below is currently visible, but directory sync did not confirm durable storage.",
		"Retry Save writes these visible values again. It does not contact the provider.",
		"",
		"Provider  " + llmProviderLabel(configuration.Provider),
		"Model     " + terminaltext.Middle(configuration.Model, max(width-serviceFieldWidth, 1), "…"),
		"Origin    " + terminaltext.Middle(configuration.Origin, max(width-serviceFieldWidth, 1), "…"),
		"API key   " + terminaltext.Middle(keyState, max(width-serviceFieldWidth, 1), "…"),
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Retry Save", width),
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
	}
}

func (state *model) llmQuestionBody(current llmQuestionPage, width int) []string {
	value := llmInputValue(current.value, "Ask for deployment parameter recommendations", width)
	lines := []string{
		state.title("Ask for deployment recommendations"),
		"The provider receives a bounded deployment projection after confirmation.",
		"",
		state.accent(value + state.symbol("▌", "_")),
	}
	if len(current.configuration.Warnings) > 0 {
		lines = append(lines, "", state.failure(fmt.Sprintf(
			"%d configuration source warning(s); effective values are shown before sending.",
			len(current.configuration.Warnings),
		)))
	}
	lines = append(lines, "", state.muted("Enter Review network request   c Configure   Esc Back"))

	return lines
}

func (state *model) llmNetworkConfirmationBody(
	current llmNetworkConfirmationPage,
	width int,
) []string {
	configuration := current.question.configuration
	keySource := configuration.KeySource
	if keySource == "" {
		keySource = displayUnavailable
	}

	return []string{
		state.title("Confirm provider request"),
		"The request may be retried and billed up to three times.",
		"",
		"Provider  " + llmProviderLabel(configuration.Provider),
		"Model     " + terminaltext.Middle(configuration.Model, max(width-serviceFieldWidth, 1), "…"),
		"Origin    " + terminaltext.Middle(configuration.Origin, max(width-serviceFieldWidth, 1), "…"),
		"API key   configured from " + terminaltext.Middle(
			keySource, max(width-llmKeySourcePrefixWidth, 1), "…",
		),
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Send request", width),
		"",
		state.muted("Tab Change focus   Enter Select   Esc Back"),
	}
}

func (state *model) llmChoicesBody(current llmChoicesPage, width int) []string {
	lines := []string{
		state.title("Choose a recommendation"),
		"Only locally validated choices are shown. Maniud will not choose one for you.",
		"",
	}
	if current.result.ModelWarning {
		reported := current.result.ReportedModel
		if reported == "" {
			reported = "not reported"
		}
		lines = append(lines, state.failure("Provider model: "+terminaltext.Middle(
			reported, max(width-llmModelWarningPrefixWidth, 1), "…",
		)), "")
	}
	for index, choice := range current.result.Choices {
		label := choice.Summary
		if len(choice.Changes) > 1 {
			label += fmt.Sprintf("  (%d fields)", len(choice.Changes))
		}
		lines = append(lines, state.choice(index == current.cursor, label, width))
		if index == current.cursor {
			fields := make([]string, len(choice.Changes))
			for changeIndex, change := range choice.Changes {
				fields[changeIndex] = deploymentFieldLabel(change.FieldID)
			}
			lines = append(lines, "  "+terminaltext.Middle(
				strings.Join(fields, ", "), max(width-llmChoiceIndentWidth, 1), "…",
			))
		}
	}
	lines = append(lines, "", state.muted("Up/Down Choose   Enter Preview edit   Esc Ask again"))

	return lines
}

func (state *model) llmWorkspaceLocation() (string, bool) {
	switch state.page.(type) {
	case llmConfigurationPage, llmSaveConfirmationPage, llmSaveOutcomeUnknownPage:
		return "Home / LLM assistance / Configure", true
	case llmQuestionPage, llmNetworkConfirmationPage:
		return "Home / LLM assistance / Ask", true
	case llmChoicesPage:
		return "Home / LLM assistance / Choose", true
	default:
		return "", false
	}
}

func (state *model) llmWorkspaceRail() ([]string, bool) {
	current := -1
	switch state.page.(type) {
	case llmConfigurationPage, llmSaveConfirmationPage, llmSaveOutcomeUnknownPage:
		current = 0
	case llmQuestionPage:
		current = 1
	case llmNetworkConfirmationPage:
		current = 2
	case llmChoicesPage:
		current = 3
	}
	if current < 0 {
		return nil, false
	}

	return state.flowRail("LLM ASSIST", []string{"Configure", "Ask", "Confirm", "Choose"}, current), true
}

func (state *model) llmWorkspaceFooter() (string, bool) {
	switch current := state.page.(type) {
	case llmConfigurationPage:
		if current.step == llmProviderStep {
			return "Up/Down Choose   Enter Continue   Esc Discard", true
		}
		if current.step == llmAPIKeyStep {
			return "Type Replace   c Clear   Enter Review   Esc Discard", true
		}

		return "Type value   Enter Continue   Esc Discard", true
	case llmSaveConfirmationPage, llmSaveOutcomeUnknownPage, llmNetworkConfirmationPage:
		return confirmationKeys, true
	case llmQuestionPage:
		return "Type question   Enter Review   c Configure   Esc Back", true
	case llmChoicesPage:
		return "Up/Down Choose   Enter Preview   Esc Ask again", true
	default:
		return "", false
	}
}
