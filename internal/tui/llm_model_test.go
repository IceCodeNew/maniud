package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	testLLMCancelled    = "Cancelled"
	testLLMGenericModel = "model"
	testLLMIdentity     = "identity"
	testLLMModel        = "gpt-5"
	testLLMOrigin       = "origin"
	testLLMToken        = "token"
)

type assistantFixture struct {
	configuration    LLMConfiguration
	saved            LLMConfiguration
	result           LLMResult
	configurationErr error
	saveErr          error
	recommendErr     error
	acceptErr        error
	settings         []LLMSettings
	calls            []string
	closed           int
}

func (fixture *assistantFixture) Configuration(context.Context) (LLMConfiguration, error) {
	fixture.calls = append(fixture.calls, "configuration")

	return fixture.configuration, fixture.configurationErr
}

func (fixture *assistantFixture) Save(
	_ context.Context,
	settings LLMSettings,
) (LLMConfiguration, error) {
	fixture.calls = append(fixture.calls, "save")
	fixture.settings = append(fixture.settings, settings)

	return fixture.saved, fixture.saveErr
}

func (fixture *assistantFixture) Recommend(
	_ context.Context,
	request application.Request,
	identity string,
	question string,
) (LLMResult, error) {
	fixture.calls = append(fixture.calls, strings.Join([]string{
		"recommend", request.Service, identity, question,
	}, ":"))

	return fixture.result, fixture.recommendErr
}

func (fixture *assistantFixture) Accept(_ context.Context, token string, choice int) error {
	fixture.calls = append(fixture.calls, "accept:"+token+":"+strconv.Itoa(choice))

	return fixture.acceptErr
}

func (fixture *assistantFixture) Close() {
	fixture.closed++
}

type deploymentAssistFixture struct {
	*deploymentWorkflowFixture

	recommendation DeploymentEditPreview
	err            error
	changes        []LLMChange
}

func (fixture *deploymentAssistFixture) PreviewRecommendation(
	_ context.Context,
	_ application.Request,
	changes []LLMChange,
) (DeploymentEditPreview, error) {
	fixture.calls = append(fixture.calls, "recommendation")
	fixture.changes = slices.Clone(changes)

	return fixture.recommendation, fixture.err
}

func completeLLMConfiguration() LLMConfiguration {
	return LLMConfiguration{
		Provider: "openai", Model: testLLMModel, Timeout: "60",
		Origin: "https://api.openai.com", KeySource: "process environment",
		KeyConfigured: true, Complete: true, Identity: "configuration-1",
	}
}

func recommendationsFixture() LLMResult {
	return LLMResult{
		Token: "recommendation-1", RequestedModel: testLLMModel, ReportedModel: "gpt-5.1",
		ModelWarning: true,
		Choices: []LLMRecommendation{
			{
				Summary: "Use a smaller CPU limit",
				Changes: []LLMChange{{FieldID: application.DeploymentCPUs.ID(), Value: "1.5"}},
			},
			{
				Summary: "Tune memory and restart policy",
				Changes: []LLMChange{
					{FieldID: application.DeploymentMemory.ID(), Value: "1g"},
					{FieldID: application.DeploymentRestart.ID(), Value: "unless-stopped"},
				},
			},
		},
	}
}

func newLLMTestModel(t *testing.T, assistant *assistantFixture) (*model, *deploymentAssistFixture) {
	t.Helper()

	state, _, _ := newTestModel(t)
	deployments := &deploymentAssistFixture{
		deploymentWorkflowFixture: newDeploymentWorkflowFixture(),
		recommendation: DeploymentEditPreview{
			ComposePath: testServicePath,
			FieldIDs: []string{
				application.DeploymentMemory.ID(), application.DeploymentRestart.ID(),
			},
		},
	}
	state.assistant = assistant
	state.deployments = deployments
	state.page = deploymentReviewPage()
	state.status = statusReady

	return state, deployments
}

//nolint:forcetypeassert,ireturn // A wrong fixture transition should fail at the assertion site.
func mustLLMPage[Page page](current page) Page {
	return current.(Page)
}

//nolint:cyclop,funlen // This test keeps the configuration slides and explicit selection in order.
func TestModelConfiguresLLMAndPreviewsExplicitChoice(t *testing.T) {
	t.Parallel()

	configured := completeLLMConfiguration()
	configured.Provider = "openai-compatible"
	configured.Endpoint = "https://provider.example/v1"
	configured.Origin = "https://provider.example"
	assistant := &assistantFixture{
		configuration: LLMConfiguration{Provider: "openai-compatible"},
		saved:         configured,
		result:        recommendationsFixture(),
	}
	state, deployments := newLLMTestModel(t, assistant)

	deliver(t, state, state.handleKey(key("a")))
	configuration, valid := state.page.(llmConfigurationPage)
	if !valid || configuration.step != llmProviderStep || configuration.providerCursor != 1 {
		t.Fatalf("provider slide = %#v", state.page)
	}
	state.handleKey(key(keyEnter))
	state.handleKey(key(testLLMModel))
	state.handleKey(key(keyEnter))
	state.handleKey(key("https://provider.example/v1"))
	state.handleKey(key(keyEnter))
	state.handleKey(key(keyEnter))
	state.handleKey(key("protected-test-key"))
	if strings.Contains(state.View().Content, "protected-test-key") {
		t.Fatal("API key appeared in the rendered configuration slide")
	}
	state.handleKey(key(keyEnter))
	if _, valid = state.page.(llmSaveConfirmationPage); !valid {
		t.Fatalf("save confirmation = %T", state.page)
	}

	state.resize(compactMinimum-1, compactMinHeight)
	configuration, valid = state.page.(llmConfigurationPage)
	if !valid || configuration.draft.APIKey != "" || state.status != statusReviewLarger {
		t.Fatalf("resized protected draft = %#v, status = %q", state.page, state.status)
	}
	state.resize(defaultWidth, defaultHeight)
	state.handleKey(key("replacement-test-key"))
	state.handleKey(key(keyEnter))
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	question, valid := state.page.(llmQuestionPage)
	if !valid || question.configuration.Identity != configured.Identity || len(assistant.settings) != 1 ||
		assistant.settings[0].APIKey != "replacement-test-key" {
		t.Fatalf("saved question = %#v, settings = %#v", state.page, assistant.settings)
	}

	state.handleKey(key(keyEnter))
	if _, valid = state.page.(llmNetworkConfirmationPage); !valid {
		t.Fatalf("network confirmation = %T", state.page)
	}
	state.resize(compactMinimum-1, compactMinHeight)
	if _, valid = state.page.(llmQuestionPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("resized network confirmation = %#v, status = %q", state.page, state.status)
	}
	state.resize(defaultWidth, defaultHeight)
	state.handleKey(key(keyEnter))
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	choices, valid := state.page.(llmChoicesPage)
	if !valid || choices.cursor != 0 || len(choices.result.Choices) != 2 ||
		!strings.Contains(state.View().Content, "Provider model: gpt-5.1") {
		t.Fatalf("recommendation choices = %#v", state.page)
	}
	state.handleKey(key(keyDown))
	deliver(t, state, state.handleKey(key(keyEnter)))
	preview, valid := state.page.(deploymentPreviewPage)
	if !valid || len(preview.preview.FieldIDs) != 2 ||
		!slices.Equal(deployments.changes, recommendationsFixture().Choices[1].Changes) {
		t.Fatalf("recommendation preview = %#v, changes = %#v", state.page, deployments.changes)
	}
	wantCalls := []string{
		"configuration", "save",
		"recommend:service:configuration-1:Recommend deployment parameters for this service.",
		"accept:recommendation-1:1",
	}
	if !slices.Equal(assistant.calls, wantCalls) ||
		!slices.Equal(deployments.calls, []string{"recommendation"}) {
		t.Fatalf("calls = %q / %q", assistant.calls, deployments.calls)
	}
}

//nolint:cyclop,funlen // The table covers each recoverable TUI failure destination.
func TestModelContainsLLMConfigurationAndRecommendationFailures(t *testing.T) {
	t.Parallel()

	t.Run("configuration error", func(t *testing.T) {
		t.Parallel()

		assistant := &assistantFixture{configurationErr: errTestSecret}
		state, _ := newLLMTestModel(t, assistant)
		deliver(t, state, state.handleKey(key("a")))
		if _, valid := state.page.(reviewPage); !valid ||
			state.status != "LLM configuration is unavailable" ||
			strings.Contains(state.View().Content, errTestSecret.Error()) {
			t.Fatalf("configuration failure = %#v", state)
		}
	})

	t.Run("unknown provider", func(t *testing.T) {
		t.Parallel()

		configuration := completeLLMConfiguration()
		configuration.Provider = "unsupported"
		assistant := &assistantFixture{configuration: configuration}
		state, _ := newLLMTestModel(t, assistant)
		deliver(t, state, state.handleKey(key("a")))
		if _, valid := state.page.(reviewPage); !valid || state.status != "LLM configuration is invalid" {
			t.Fatalf("unknown provider result = %#v", state)
		}
	})

	t.Run("provider error remains recoverable", func(t *testing.T) {
		t.Parallel()

		assistant := &assistantFixture{
			configuration: completeLLMConfiguration(), recommendErr: errTestSecret,
		}
		state, _ := newLLMTestModel(t, assistant)
		deliver(t, state, state.handleKey(key("a")))
		state.handleKey(key(keyEnter))
		state.handleKey(key(keyTab))
		deliver(t, state, state.handleKey(key(keyEnter)))
		if _, valid := state.page.(llmQuestionPage); !valid ||
			state.status != "LLM recommendation failed; retry may create another billed request" ||
			strings.Contains(state.View().Content, errTestSecret.Error()) {
			t.Fatalf("provider failure = %#v", state)
		}
	})

	t.Run("invalid local result", func(t *testing.T) {
		t.Parallel()

		assistant := &assistantFixture{
			configuration: completeLLMConfiguration(),
			result: LLMResult{
				Token: "recommendation-1", RequestedModel: testLLMModel,
				Choices: []LLMRecommendation{{Summary: "unsafe\nsummary"}},
			},
		}
		state, _ := newLLMTestModel(t, assistant)
		deliver(t, state, state.handleKey(key("a")))
		state.handleKey(key(keyEnter))
		state.handleKey(key(keyTab))
		deliver(t, state, state.handleKey(key(keyEnter)))
		if _, valid := state.page.(llmQuestionPage); !valid ||
			state.status != "LLM response did not pass local validation" {
			t.Fatalf("invalid local result = %#v", state)
		}
	})
}

func TestLLMRendererKeepsConfigurationAndNetworkReviewConcise(t *testing.T) {
	t.Parallel()

	assistant := &assistantFixture{configuration: completeLLMConfiguration()}
	state, _ := newLLMTestModel(t, assistant)
	deliver(t, state, state.handleKey(key("a")))
	question := state.View().Content
	if !strings.Contains(question, "bounded deployment projection") ||
		strings.Contains(question, "process environment") {
		t.Fatalf("question view = %q", question)
	}
	state.handleKey(key(keyEnter))
	network := state.View().Content
	for _, value := range []string{
		"Confirm provider request", "OpenAI", "gpt-5", "https://api.openai.com",
		"configured from process environment", "retried and billed up to three times",
	} {
		if !strings.Contains(network, value) {
			t.Fatalf("network view omitted %q: %q", value, network)
		}
	}

	if _, valid := canonicalLLMConfiguration(LLMConfiguration{Provider: "unknown"}); valid {
		t.Fatal("unknown provider configuration passed local validation")
	}
	if _, valid := canonicalLLMResult(LLMResult{
		Token: testLLMToken, RequestedModel: testLLMGenericModel,
	}); valid {
		t.Fatal("empty recommendation result passed local validation")
	}
}

//nolint:cyclop,funlen // This exercises the closed keyboard state machine at each recovery boundary.
func TestLLMConfigurationAndConfirmationKeyBoundaries(t *testing.T) {
	t.Parallel()
	assistant := &assistantFixture{configuration: LLMConfiguration{Provider: llmProviderOpenAI}}
	state, _ := newLLMTestModel(t, assistant)
	review := mustLLMPage[reviewPage](state.page)
	configuration := newLLMConfigurationPage(review, LLMConfiguration{})
	state.page = configuration

	state.handleKey(key("x"))
	state.handleKey(key("up"))
	state.handleKey(key("left"))
	state.handleKey(key("k"))
	state.handleKey(key(keyShiftTab))
	state.handleKey(key("right"))
	state.handleKey(key("j"))
	state.handleKey(key(keyTab))
	state.handleKey(key(keyDown))
	state.handleKey(key(keyEnter))
	state.handleKey(key(keyEnter))
	if state.status != "Complete this configuration value" {
		t.Fatalf("empty model status = %q", state.status)
	}
	state.handleKey(key(testLLMGenericModel))
	state.handleKey(key(keyEnter))
	configuration = mustLLMPage[llmConfigurationPage](state.page)
	if configuration.step != llmTimeoutStep {
		t.Fatalf("official provider step = %d", configuration.step)
	}
	state.handleKey(key("backspace"))
	state.handleKey(key("60"))
	state.handleKey(key(keyEnter))
	state.handleKey(key("secret"))
	state.handleKey(key("c"))
	configuration = mustLLMPage[llmConfigurationPage](state.page)
	if !configuration.draft.ClearAPIKey {
		t.Fatal("clear key did not update the draft")
	}
	state.handleKey(key("replacement"))
	state.handleKey(key(keyEnter))
	confirmation := mustLLMPage[llmSaveConfirmationPage](state.page)
	state.width = compactMinimum - 1
	state.handleKey(key(keyEnter))
	state.width = defaultWidth
	for _, navigation := range []string{keyTab, keyLeft, keyRight, keyShiftTab} {
		state.page = confirmation
		state.handleKey(key(navigation))
		confirmation = mustLLMPage[llmSaveConfirmationPage](state.page)
	}
	confirmation.focus = confirmationBack
	state.page = confirmation
	state.handleKey(key(keyEnter))
	state.page = confirmation
	state.handleKey(key(keyEscape))
	state.page = confirmation
	state.handleKey(key("x"))
	state.page = confirmation
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("quit did not return a command")
	}

	state.page = newLLMConfigurationPage(review, LLMConfiguration{})
	state.handleKey(key(keyEscape))
	state.page = newLLMConfigurationPage(review, LLMConfiguration{})
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("configuration quit did not return a command")
	}
	if validLLMConfigurationStep(llmConfigurationPage{}) ||
		validLLMConfigurationStep(llmConfigurationPage{step: llmEndpointStep}) ||
		validLLMConfigurationStep(llmConfigurationPage{step: llmTimeoutStep}) ||
		validLLMConfigurationStep(llmConfigurationPage{step: llmStepCount}) {
		t.Fatal("invalid configuration step accepted")
	}
	invalidStep := newLLMConfigurationPage(review, LLMConfiguration{})
	invalidStep.step = llmStepCount
	state.page = invalidStep
	state.handleKey(key("x"))
	for step := llmModelStep; step <= llmStepCount; step++ {
		if llmConfigurationStepStatus(step) == "" {
			t.Fatalf("empty status for step %d", step)
		}
	}
}

//nolint:funlen // Save outcomes, retry, and question networking form one contiguous recovery flow.
func TestLLMSaveUnknownQuestionAndNetworkBoundaries(t *testing.T) {
	t.Parallel()
	configuration := completeLLMConfiguration()
	assistant := &assistantFixture{
		configuration: configuration, saved: configuration, result: recommendationsFixture(),
	}
	state, _ := newLLMTestModel(t, assistant)
	review := mustLLMPage[reviewPage](state.page)
	unknown := llmSaveOutcomeUnknownPage{
		review: review, configuration: configuration, focus: confirmationBack,
	}
	state.page = unknown
	state.width = compactMinimum - 1
	state.handleKey(key(keyEnter))
	state.width = defaultWidth
	for _, navigation := range []string{keyTab, keyLeft, keyRight, keyShiftTab} {
		state.page = unknown
		state.handleKey(key(navigation))
		unknown = mustLLMPage[llmSaveOutcomeUnknownPage](state.page)
	}
	unknown.focus = confirmationBack
	state.page = unknown
	state.handleKey(key(keyEnter))
	state.page = unknown
	state.handleKey(key(keyEscape))
	state.page = unknown
	state.handleKey(key("x"))
	state.page = unknown
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("unknown outcome quit did not return a command")
	}
	unknown.focus = confirmationApply
	state.page = unknown
	deliver(t, state, state.handleKey(key(keyEnter)))
	if len(assistant.settings) != 1 || assistant.settings[0].APIKey != "" || assistant.settings[0].ClearAPIKey {
		t.Fatalf("retry settings = %#v", assistant.settings)
	}

	question := llmQuestionPage{review: review, configuration: configuration}
	state.page = question
	state.handleKey(key(keyEnter))
	state.handleKey(key("question"))
	question = mustLLMPage[llmQuestionPage](state.page)
	state.handleKey(key(keyEnter))
	network := mustLLMPage[llmNetworkConfirmationPage](state.page)
	state.width = compactMinimum - 1
	state.handleKey(key(keyEnter))
	state.width = defaultWidth
	for _, navigation := range []string{keyTab, keyLeft, keyRight, keyShiftTab} {
		state.page = network
		state.handleKey(key(navigation))
		network = mustLLMPage[llmNetworkConfirmationPage](state.page)
	}
	network.focus = confirmationBack
	state.page = network
	state.handleKey(key(keyEnter))
	state.page = network
	state.handleKey(key(keyEscape))
	state.page = network
	state.handleKey(key("x"))
	state.page = network
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("network quit did not return a command")
	}
	state.page = question
	state.handleKey(key("c"))
	state.page = question
	state.handleKey(key(keyEscape))
	state.page = question
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("question quit did not return a command")
	}
}

//nolint:cyclop,funlen // Every stable action code and choice recovery path is a separate contract outcome.
func TestLLMErrorsChoicesAndCompletionBoundaries(t *testing.T) {
	t.Parallel()
	for code, expected := range map[LLMActionCode]string{
		LLMConfigInvalid: "LLM configuration", LLMQuestionInvalid: "deployment question",
		LLMForbiddenValue: "protected deployment data", LLMAuthenticationFailed: "authentication failed",
		LLMRateLimited: "rate-limited", LLMContextLimit: "context limit", LLMRefused: "refused",
		LLMEmptyResponse: "no recommendation", LLMTruncated: "truncated", LLMInvalidResponse: "local validation",
		LLMModelUnavailable: "model is unavailable", LLMTimeout: "timed out", LLMCancelled: "cancelled",
		LLMContextStale: "context changed", LLMProviderFailed: "recommendation failed",
	} {
		err := &LLMActionError{Code: code}
		if status := llmRecommendationErrorStatus(err); !strings.Contains(status, expected) {
			t.Fatalf("status for %s = %q", code, status)
		}
	}
	for code := range map[LLMActionCode]struct{}{
		LLMConfigSaveStale: {}, LLMConfigPathInvalid: {}, LLMConfigInvalid: {}, LLMProviderFailed: {},
	} {
		if llmSaveErrorStatus(&LLMActionError{Code: code}) == "" {
			t.Fatalf("empty save status for %s", code)
		}
	}
	var nilFailure *LLMActionError
	for _, failure := range []*LLMActionError{nilFailure, {Code: LLMProviderFailed}, {
		Code: LLMForbiddenValue, Category: "credential",
	}} {
		if failure.Error() == "" {
			t.Fatal("empty action error")
		}
	}
	if llmActionErrorCode(errTestSecret) != LLMProviderFailed {
		t.Fatal("private error did not collapse to provider failure")
	}

	assistant := &assistantFixture{configuration: completeLLMConfiguration(), result: recommendationsFixture()}
	state, deployments := newLLMTestModel(t, assistant)
	review := mustLLMPage[reviewPage](state.page)
	question := llmQuestionPage{review: review, configuration: completeLLMConfiguration(), value: "question"}
	choices := llmChoicesPage{question: question, result: recommendationsFixture()}
	for _, navigation := range []string{"up", "k", keyDown, "j", keyTab} {
		state.page = choices
		state.handleKey(key(navigation))
		choices = mustLLMPage[llmChoicesPage](state.page)
	}
	state.page = choices
	state.handleKey(key(keyEscape))
	state.page = choices
	state.handleKey(key("x"))
	state.page = choices
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("choices quit did not return a command")
	}
	state.deployments = deployments.deploymentWorkflowFixture
	state.page = choices
	state.handleKey(key(keyEnter))
	if state.status != "Recommended deployment editing is unavailable" {
		t.Fatalf("unavailable workspace status = %q", state.status)
	}
	state.deployments = deployments
	assistant.acceptErr = errTestSecret
	state.page = choices
	deliver(t, state, state.handleKey(key(keyEnter)))
	assistant.acceptErr = nil
	deployments.err = errTestSecret
	state.page = choices
	acceptCalls := len(assistant.calls)
	deliver(t, state, state.handleKey(key(keyEnter)))
	if len(assistant.calls) != acceptCalls {
		t.Fatal("failed recommendation preview consumed the choice token")
	}
	deployments.err = nil
	deployments.recommendation = DeploymentEditPreview{}
	state.page = choices
	deliver(t, state, state.handleKey(key(keyEnter)))

	state.sequence = 8
	state.busy = true
	if accepted, _ := state.completeLLMOperation(7, nil); accepted {
		t.Fatal("stale operation accepted")
	}
	state.quitAfterOperation = true
	if accepted, command := state.completeLLMOperation(8, nil); !accepted || command == nil {
		t.Fatal("deferred quit was not completed")
	}
	state.sequence = 9
	state.busy = true
	if accepted, _ := state.completeLLMOperation(9, context.Canceled); !accepted || state.status != testLLMCancelled {
		t.Fatalf("cancel completion = %t, %q", accepted, state.status)
	}
}

//nolint:cyclop,funlen // Rendering each page directly keeps output branches independent from keyboard navigation.
func TestLLMRenderingAndCanonicalizationBoundaries(t *testing.T) {
	t.Parallel()
	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	configuration := completeLLMConfiguration()
	configuration.Warnings = []string{"current source skipped"}
	for step := range llmStepCount {
		page := newLLMConfigurationPage(review, configuration)
		page.step = step
		page.draft.APIKey = "entered"
		if step == llmAPIKeyStep {
			page.draft.ClearAPIKey = true
		}
		state.page = page
		if state.View().Content == "" {
			t.Fatalf("empty configuration view for step %d", step)
		}
	}
	emptyKey := newLLMConfigurationPage(review, configuration)
	emptyKey.step = llmAPIKeyStep
	emptyKey.draft.APIKey = ""
	emptyKey.draft.ClearAPIKey = false
	state.page = emptyKey
	_ = state.View()
	invalidStep := emptyKey
	invalidStep.step = llmStepCount
	state.page = invalidStep
	_ = state.View()
	for _, provider := range append(llmProviderValues(), "custom") {
		if llmProviderLabel(provider) == "" {
			t.Fatalf("empty provider label for %q", provider)
		}
	}
	_ = llmInputValue("", "placeholder", 1)

	save := llmSaveConfirmationPage{configuration: newLLMConfigurationPage(review, configuration)}
	for _, draft := range []LLMSettings{
		{Provider: llmProviderOpenAI, Model: testLLMGenericModel, Timeout: "60"},
		{Provider: llmProviderOpenAICompatible, Model: testLLMGenericModel,
			Endpoint: "https://example.com", Timeout: "60", APIKey: "key"},
		{Provider: llmProviderDeepSeek, Model: testLLMGenericModel, Timeout: "60", ClearAPIKey: true},
	} {
		save.configuration.draft = draft
		state.page = save
		_ = state.View()
	}
	configuredWithoutSource := configuration
	configuredWithoutSource.KeySource = ""
	for _, configured := range []LLMConfiguration{
		{}, configuration, configuredWithoutSource,
	} {
		state.page = llmSaveOutcomeUnknownPage{review: review, configuration: configured}
		_ = state.View()
	}
	state.page = llmQuestionPage{review: review, configuration: configuration}
	_ = state.View()
	configuration.KeySource = ""
	state.page = llmNetworkConfirmationPage{question: llmQuestionPage{review: review, configuration: configuration}}
	_ = state.View()
	result := recommendationsFixture()
	result.ReportedModel = ""
	state.page = llmChoicesPage{result: result}
	_ = state.View()
	result.ModelWarning = false
	state.page = llmChoicesPage{result: result}
	_ = state.View()
	for _, current := range []page{
		emptyKey,
		llmSaveConfirmationPage{},
		llmSaveOutcomeUnknownPage{},
		llmQuestionPage{},
		llmNetworkConfirmationPage{},
		llmChoicesPage{},
	} {
		state.page = current
		if len(state.hardFloorView(hardMinimumWidth)) == 0 {
			t.Fatalf("empty hard-floor view for %T", current)
		}
	}
	state.page = review
	if _, handled := state.llmPageBody(20); handled {
		t.Fatal("non-LLM page rendered as LLM")
	}

	for _, candidate := range []LLMConfiguration{
		{Provider: "bad\nprovider"},
		{Provider: llmProviderOpenAI, Warnings: []string{"bad\nwarning"}},
		{Provider: llmProviderOpenAI, Complete: true},
	} {
		if _, valid := canonicalLLMConfiguration(candidate); valid {
			t.Fatalf("configuration %#v passed", candidate)
		}
	}
	validResult := recommendationsFixture()
	invalidResults := []LLMResult{
		{Token: testLLMToken, RequestedModel: "bad\nmodel", Choices: validResult.Choices},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			ReportedModel: "bad\nmodel", Choices: validResult.Choices},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []LLMRecommendation{{Summary: "", Changes: []LLMChange{{FieldID: "cpus"}}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []LLMRecommendation{{Summary: "ok"}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []LLMRecommendation{{Summary: "ok", Changes: []LLMChange{{}}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []LLMRecommendation{{Summary: "ok", Changes: []LLMChange{{
				FieldID: "cpus", Value: "bad\nvalue",
			}}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel, Choices: make([]LLMRecommendation, 4)},
	}
	for _, candidate := range invalidResults {
		if _, valid := canonicalLLMResult(candidate); valid {
			t.Fatalf("result %#v passed", candidate)
		}
	}
}

//nolint:funlen // Each asynchronous save result has a distinct safe recovery destination.
func TestLLMResultAndResizeInvalidationBoundaries(t *testing.T) {
	t.Parallel()
	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	configuration := completeLLMConfiguration()
	for _, page := range []page{
		llmSaveConfirmationPage{configuration: newLLMConfigurationPage(review, configuration)},
		llmNetworkConfirmationPage{question: llmQuestionPage{review: review, configuration: configuration}},
		llmSaveOutcomeUnknownPage{review: review, configuration: configuration},
		newLLMConfigurationPage(review, configuration),
	} {
		state.page = page
		if !state.invalidateLLMConfirmation() || state.status != statusReviewLarger {
			t.Fatalf("page %T was not invalidated", page)
		}
	}
	state.page = review
	if state.invalidateLLMConfirmation() {
		t.Fatal("review page was invalidated as an LLM confirmation")
	}

	results := []llmSaveResultMsg{
		{sequence: 1, review: review, configuration: LLMConfiguration{Provider: "bad\nprovider"},
			err: &LLMActionError{Code: LLMConfigSaveUnknown}},
		{sequence: 2, review: review, configuration: configuration, err: &LLMActionError{Code: LLMConfigSaveUnknown}},
		{sequence: 3, review: review, err: &LLMActionError{Code: LLMConfigSaveStale}},
		{sequence: 4, review: review, configuration: LLMConfiguration{}},
		{sequence: 5, review: review, configuration: LLMConfiguration{Provider: llmProviderOpenAI}},
		{sequence: 6, review: review, configuration: configuration},
	}
	for _, result := range results {
		state.sequence = result.sequence
		state.busy = true
		state.handleLLMSaveResult(result)
	}
	state.sequence = 8
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{sequence: 7, review: review})

	question := llmQuestionPage{review: review, configuration: configuration}
	network := llmNetworkConfirmationPage{question: question}
	state.sequence = 9
	state.busy = true
	state.handleLLMRecommendResult(llmRecommendResultMsg{sequence: 8, page: network})

	for _, candidate := range []LLMConfiguration{
		{Provider: llmProviderOpenAI, Model: testLLMGenericModel,
			Origin: testLLMOrigin, KeyConfigured: true, Complete: true},
		{Provider: llmProviderOpenAI, Identity: testLLMIdentity,
			Origin: testLLMOrigin, KeyConfigured: true, Complete: true},
		{Provider: llmProviderOpenAI, Identity: testLLMIdentity,
			Model: testLLMGenericModel, KeyConfigured: true, Complete: true},
		{Provider: llmProviderOpenAI, Identity: testLLMIdentity,
			Model: testLLMGenericModel, Origin: testLLMOrigin, Complete: true},
	} {
		if _, valid := canonicalLLMConfiguration(candidate); valid {
			t.Fatalf("incomplete configuration %#v passed", candidate)
		}
	}
	configuration.Warnings = []string{"current .env skipped: content is malformed"}
	if canonical, valid := canonicalLLMConfiguration(configuration); !valid || len(canonical.Warnings) != 1 {
		t.Fatalf("configuration with warning = %#v, %t", canonical, valid)
	}
}
