package tui

import (
	"context"
	"slices"
	"strconv"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/llm"
)

const (
	testLLMCancelled      = "Cancelled"
	testLLMGenericModel   = "model"
	testLLMIdentity       = "identity"
	testLLMInvalidChange  = "not-a-number"
	testLLMModel          = "gpt-5"
	testLLMOrigin         = "origin"
	testLLMToken          = "token"
	testLLMUnknownOutcome = "processed or billed"
	testDiscard           = "discard"
	testRecommendation    = "recommendation"
)

type assistantFixture struct {
	configuration    LLMConfiguration
	saved            LLMConfiguration
	result           LLMResult
	configurationErr error
	saveErr          error
	recommendErr     error
	acceptErr        error
	acceptHook       func()
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
	if fixture.acceptHook != nil {
		fixture.acceptHook()
	}

	return fixture.acceptErr
}

func (fixture *assistantFixture) Close() {
	fixture.closed++
}

type deploymentAssistFixture struct {
	*deploymentWorkflowFixture

	recommendation DeploymentEditPreview
	err            error
	previewHook    func()
	patches        []application.DeploymentPatch
}

func (fixture *deploymentAssistFixture) PreviewPatches(
	_ context.Context,
	_ application.Request,
	patches []application.DeploymentPatch,
) (DeploymentEditPreview, error) {
	fixture.calls = append(fixture.calls, testRecommendation)
	fixture.patches = slices.Clone(patches)
	if fixture.previewHook != nil {
		fixture.previewHook()
	}

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
		Choices: []llm.Choice{
			{
				Kind: llm.ChoiceRecommendation, Message: "Use a smaller CPU limit",
				Changes: []llm.Change{{FieldID: application.DeploymentCPUs.ID(), Value: "1.5"}},
			},
			{
				Kind: llm.ChoiceRecommendation, Message: "Tune memory and restart policy",
				Changes: []llm.Change{
					{FieldID: application.DeploymentMemory.ID(), Value: "1073741824"},
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
			Diff:        deploymentFixtureDiff,
			Changes: []DeploymentFieldChange{
				deploymentChange(application.DeploymentMemory, "1024", "1073741824"),
				deploymentChange(application.DeploymentRestart, "always", "unless-stopped"),
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

	deliver(t, state, chooseReviewOption(state, 1))
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
	save := state.handleKey(key(keyEnter))
	confirmation := mustLLMPage[llmSaveConfirmationPage](state.page)
	if confirmation.configuration.draft.APIKey != "" {
		t.Fatal("API key remained in the save confirmation while storage was pending")
	}
	deliver(t, state, save)
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
	if !valid || len(preview.preview.Changes) != 2 ||
		len(deployments.patches) != 2 ||
		deployments.patches[0].Field() != application.DeploymentMemory ||
		deployments.patches[1].Field() != application.DeploymentRestart {
		t.Fatalf("recommendation preview = %#v, patches = %#v", state.page, deployments.patches)
	}
	wantCalls := []string{
		"configuration", "save",
		"recommend:service:configuration-1:Recommend deployment parameters for this service.",
		"accept:recommendation-1:1",
	}
	if !slices.Equal(assistant.calls, wantCalls) ||
		!slices.Equal(deployments.calls, []string{testRecommendation}) {
		t.Fatalf("calls = %q / %q", assistant.calls, deployments.calls)
	}
	deliver(t, state, state.handleKey(key(keyEscape)))
	if _, valid := state.page.(llmQuestionPage); !valid {
		t.Fatalf("accepted recommendation returned to %T", state.page)
	}
}

//nolint:cyclop // The two response kinds share the same continuation contract.
func TestModelContinuesConversationAfterAnswerOrClarification(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name   string
		kind   llm.ChoiceKind
		text   string
		status string
	}{
		{name: "answer", kind: llm.ChoiceAnswer, text: "Two CPUs fit\n\nthe workload.",
			status: "Ask another deployment question"},
		{name: "clarification", kind: llm.ChoiceClarification, text: "How much memory is available?",
			status: "Answer the provider's question"},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			assistant := &assistantFixture{}
			state, deployments := newLLMTestModel(t, assistant)
			review := mustLLMPage[reviewPage](state.page)
			choice := llm.Choice{Kind: test.kind, Message: test.text}
			state.page = llmChoicesPage{
				question: llmQuestionPage{review: review, configuration: completeLLMConfiguration()},
				result: LLMResult{
					Token: testLLMToken, RequestedModel: testLLMModel, Choices: []llm.Choice{choice},
				},
			}

			deliver(t, state, state.handleKey(key(keyEnter)))
			question := mustLLMPage[llmQuestionPage](state.page)
			rendered := state.View().Content
			if question.assistant.Kind != choice.Kind || question.assistant.Message != choice.Message ||
				len(question.assistant.Changes) != 0 || question.value != "" || state.status != test.status ||
				!slices.Equal(assistant.calls, []string{"accept:" + testLLMToken + ":0"}) ||
				len(deployments.calls) != 0 {
				t.Fatalf("continued conversation = %#v; calls = %q / %q", state, assistant.calls, deployments.calls)
			}
			for line := range strings.SplitSeq(test.text, "\n") {
				if !strings.Contains(rendered, line) {
					t.Fatalf("assistant response line %q is absent from %q", line, rendered)
				}
			}
		})
	}
}

//nolint:cyclop,funlen // The table covers each recoverable TUI failure destination.
func TestModelContainsLLMConfigurationAndRecommendationFailures(t *testing.T) {
	t.Parallel()

	t.Run("configuration error", func(t *testing.T) {
		t.Parallel()

		assistant := &assistantFixture{configurationErr: errTestSecret}
		state, _ := newLLMTestModel(t, assistant)
		deliver(t, state, chooseReviewOption(state, 1))
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
		deliver(t, state, chooseReviewOption(state, 1))
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
		deliver(t, state, chooseReviewOption(state, 1))
		state.handleKey(key(keyEnter))
		state.handleKey(key(keyTab))
		deliver(t, state, state.handleKey(key(keyEnter)))
		if _, valid := state.page.(llmQuestionPage); !valid ||
			state.status != "LLM request failed; retry may create another billed request" ||
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
				Choices: []llm.Choice{{
					Kind: llm.ChoiceAnswer, Message: strings.Repeat("x", maximumLLMMessageBytes+1),
				}},
			},
		}
		state, _ := newLLMTestModel(t, assistant)
		deliver(t, state, chooseReviewOption(state, 1))
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
	deliver(t, state, chooseReviewOption(state, 1))
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
	confirmation := mustLLMPage[llmNetworkConfirmationPage](state.page)
	configuration := completeLLMConfiguration()
	configuration.KeySource = ""
	state.page = llmNetworkConfirmationPage{question: llmQuestionPage{
		review: confirmation.question.review, configuration: configuration,
	}}
	network = state.View().Content
	if !strings.Contains(network, "API key   Unavailable") || strings.Contains(network, "configured from Unavailable") {
		t.Fatalf("network view with unavailable key source = %q", network)
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

//nolint:cyclop,funlen // Each request outcome and recovery route is an independent UI contract.
func TestLLMRequestFailureShowsBoundedOutcomeAndClearsBeforeRetry(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	state.resize(100, 24)
	configuration := completeLLMConfiguration()
	for outcome, expected := range map[llm.RequestOutcome]string{
		llm.RequestNotStarted:       "request was not sent",
		llm.RequestOutcomeUnknown:   testLLMUnknownOutcome,
		llm.RequestResponseReceived: "HTTP response was received",
	} {
		failure := llmRequestFailureFromError(&llm.ActionError{
			Code: llm.ErrorTimeout, Stage: llm.ActionStageProviderRequest, RequestOutcome: outcome,
		})
		content := strings.Join(state.llmRequestFailureLines(failure, configuration, 80), "\n")
		for _, value := range []string{
			string(llm.ErrorTimeout), string(llm.ActionStageProviderRequest),
			"OpenAI", configuration.Model, configuration.Origin, expected,
		} {
			if !strings.Contains(content, value) {
				t.Fatalf("%s failure omitted %q: %q", outcome, value, content)
			}
		}
	}
	malformedOutcome := strings.Join(state.llmRequestFailureLines(llmRequestFailure{
		code: llm.ErrorProvider, stage: llm.ActionStageProviderRequest,
		outcome: llm.RequestOutcome("invalid"),
	}, configuration, 80), "\n")
	if !strings.Contains(malformedOutcome, testLLMUnknownOutcome) || strings.Contains(malformedOutcome, "invalid") {
		t.Fatalf("malformed outcome escaped bounded fallback: %q", malformedOutcome)
	}
	for _, stage := range []llm.ActionStage{
		llm.ActionStageRequestPreparation,
		llm.ActionStageProviderRequest,
		llm.ActionStageProviderResponse,
		llm.ActionStageContextValidation,
	} {
		failure := llmRequestFailureFromError(&llm.ActionError{
			Code: llm.ErrorProvider, Stage: stage, RequestOutcome: llm.RequestOutcomeUnknown,
		})
		if failure.stage != stage {
			t.Fatalf("failure stage %q became %q", stage, failure.stage)
		}
	}
	invalid := llmRequestFailureFromError(&llm.ActionError{
		Code: llm.ErrorCode("unsafe\ncode"), Stage: llm.ActionStage("unsafe\nstage"),
		RequestOutcome: llm.RequestOutcome("unsafe\noutcome"),
	})
	if invalid.code != llm.ErrorProvider || invalid.stage != llm.ActionStageProviderRequest ||
		invalid.outcome != llm.RequestOutcomeUnknown {
		t.Fatalf("invalid failure boundary = %#v", invalid)
	}
	private := llmRequestFailureFromError(errTestSecret)
	if private.code != llm.ErrorProvider || private.outcome != llm.RequestOutcomeUnknown {
		t.Fatalf("private failure boundary = %#v", private)
	}

	review := mustLLMPage[reviewPage](state.page)
	question := llmQuestionPage{
		review: review, configuration: configuration, value: "question",
	}
	state.sequence++
	state.busy = true
	state.handleLLMRecommendResult(llmRecommendResultMsg{
		sequence: state.sequence, page: llmNetworkConfirmationPage{question: question},
		err: &llm.ActionError{
			Code: llm.ErrorTimeout, Stage: llm.ActionStageProviderRequest,
			RequestOutcome: llm.RequestOutcomeUnknown, Category: errTestSecret.Error(),
		},
	})
	failed := mustLLMPage[llmQuestionPage](state.page)
	content := state.View().Content
	if failed.failure.code != llm.ErrorTimeout || !strings.Contains(content, "REQUEST FAILED") ||
		strings.Contains(content, errTestSecret.Error()) {
		t.Fatalf("failed request page = %#v, %q", failed.failure, content)
	}
	state.handleLLMQuestionKey(failed, key("x"))
	if edited := mustLLMPage[llmQuestionPage](state.page); edited.failure.code != "" {
		t.Fatalf("edited question retained failure = %#v", edited.failure)
	}
	state.handleLLMQuestionKey(failed, key(keyEnter))
	if retry := mustLLMPage[llmNetworkConfirmationPage](state.page); retry.question.failure.code != "" {
		t.Fatalf("retry confirmation retained failure = %#v", retry.question.failure)
	}
	state.handleLLMQuestionKey(failed, key(keyEditLLMConfiguration))
	configurationPage := mustLLMPage[llmConfigurationPage](state.page)
	if configurationPage.question == nil || configurationPage.question.failure.code != "" {
		t.Fatalf("configuration edit retained failure = %#v", configurationPage.question)
	}
}

//nolint:cyclop,funlen // This exercises the closed keyboard state machine at each recovery boundary.
func TestLLMConfigurationAndConfirmationKeyBoundaries(t *testing.T) {
	t.Parallel()
	assistant := &assistantFixture{configuration: LLMConfiguration{Provider: string(llm.ProviderOpenAI)}}
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
	state.handleKey(key(keyQuit))
	state.handleKey(key("c"))
	configuration = mustLLMPage[llmConfigurationPage](state.page)
	if configuration.draft.Model != "qc" {
		t.Fatalf("model printable input = %q", configuration.draft.Model)
	}
	state.handleKey(key("backspace"))
	state.handleKey(key("backspace"))
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
	state.handleKey(key(keyEscape))
	configuration = mustLLMPage[llmConfigurationPage](state.page)
	if configuration.step != llmModelStep {
		t.Fatalf("official provider back step = %d", configuration.step)
	}
	state.handleKey(key(keyEnter))
	state.handleKey(key("backspace"))
	state.handleKey(key("60"))
	state.handleKey(key(keyEnter))
	state.handleKey(key("secret"))
	state.handleKey(key(keyQuit))
	state.handleKey(key("c"))
	configuration = mustLLMPage[llmConfigurationPage](state.page)
	if configuration.draft.APIKey != "secretqc" {
		t.Fatalf("API key printable input = %q", configuration.draft.APIKey)
	}
	state.handleKey(key("ctrl+d"))
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
	if command := state.handleKey(key(keyQuit)); command != nil {
		t.Fatal("dirty configuration quit bypassed discard confirmation")
	}
	discard := mustLLMPage[llmDiscardConfirmationPage](state.page)
	if discard.configuration.draft.APIKey != "" || !discard.quit ||
		state.status != "Discard unsaved LLM configuration?" {
		t.Fatalf("LLM discard confirmation = %#v, %q", discard, state.status)
	}
	state.handleKey(key(keyEscape))
	if current := mustLLMPage[llmConfigurationPage](state.page); current.draft.APIKey != "" {
		t.Fatal("discard confirmation retained the API key")
	}
	state.page = discard
	state.handleKey(key(keyTab))
	discard = mustLLMPage[llmDiscardConfirmationPage](state.page)
	if command := state.handleKey(key(keyEnter)); command == nil {
		t.Fatal("confirmed discard and quit did not return a command")
	}
	state.resize(1, 1)
	state.page = discard
	state.handleKey(key(keyEnter))
	if _, valid := state.page.(llmConfigurationPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("small LLM discard confirmation = %T, %q", state.page, state.status)
	}
	state.resize(defaultWidth, defaultHeight)
	for _, navigation := range []string{keyLeft, keyRight, keyShiftTab, "?"} {
		discard.focus = confirmationBack
		state.page = discard
		state.handleKey(key(navigation))
	}
	discard.focus = confirmationApply
	discard.quit = false
	state.page = discard
	state.handleKey(key(keyEnter))
	if _, valid := state.page.(reviewPage); !valid {
		t.Fatalf("discard and leave page = %T", state.page)
	}
	state.handleLLMDiscardConfirmationKey(discard, "x")

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

//nolint:cyclop,funlen // The test keeps draft preservation, discard, and question return in one interaction.
func TestLLMConfigurationDraftSurvivesBackAndRequiresDiscardBeforeLeaving(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	state.page = newLLMConfigurationPage(review, LLMConfiguration{})

	state.handleKey(key(keyEnter))
	state.handleKey(key(testLLMGenericModel))
	state.handleKey(key(keyEscape))
	configuration := mustLLMPage[llmConfigurationPage](state.page)
	if configuration.step != llmProviderStep ||
		configuration.draft.Model != testLLMGenericModel || !configuration.dirty() ||
		!strings.Contains(state.View().Content, "Unsaved") {
		t.Fatalf("preserved LLM draft = %#v, %q", configuration, state.View().Content)
	}
	state.handleKey(key(keyEscape))
	discard := mustLLMPage[llmDiscardConfirmationPage](state.page)
	if discard.quit || discard.focus != confirmationBack {
		t.Fatalf("LLM leave confirmation = %#v", discard)
	}
	state.handleKey(key(keyEnter))
	if current := mustLLMPage[llmConfigurationPage](state.page); current.draft.Model != testLLMGenericModel {
		t.Fatalf("continued LLM draft = %#v", current.draft)
	}
	state.handleKey(key(keyEscape))
	state.handleKey(key(keyTab))
	if command := state.handleKey(key(keyEnter)); command != nil {
		t.Fatal("discard and leave returned an asynchronous command")
	}
	if _, valid := state.page.(reviewPage); !valid {
		t.Fatalf("discard and leave page = %T", state.page)
	}

	question := llmQuestionPage{
		review: review, configuration: completeLLMConfiguration(), value: "Keep this question",
	}
	state.page = question
	state.handleKey(key("ctrl+e"))
	state.handleKey(key(keyEnter))
	state.handleKey(key("x"))
	state.handleKey(key(keyEscape))
	state.handleKey(key(keyEscape))
	state.handleKey(key(keyTab))
	state.handleKey(key(keyEnter))
	returnedQuestion := mustLLMPage[llmQuestionPage](state.page)
	if returnedQuestion.value != question.value {
		t.Fatalf("discarded configuration did not return to question = %#v", returnedQuestion)
	}
	savedConfiguration := completeLLMConfiguration()
	savePage := newLLMConfigurationPage(review, question.configuration)
	savePage.question = &question
	state.sequence++
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{
		sequence: state.sequence, page: savePage, configuration: savedConfiguration,
	})
	returnedQuestion = mustLLMPage[llmQuestionPage](state.page)
	if returnedQuestion.value != question.value ||
		returnedQuestion.configuration.Identity != savedConfiguration.Identity {
		t.Fatalf("saved configuration did not return to question = %#v", returnedQuestion)
	}
}

//nolint:cyclop,funlen // Save outcomes, retry, and question networking form one contiguous recovery flow.
func TestLLMSaveUnknownQuestionAndNetworkBoundaries(t *testing.T) {
	t.Parallel()
	configuration := completeLLMConfiguration()
	assistant := &assistantFixture{
		configuration: configuration, saved: configuration, result: recommendationsFixture(),
	}
	state, _ := newLLMTestModel(t, assistant)
	review := mustLLMPage[reviewPage](state.page)
	resumeQuestion := llmQuestionPage{
		review: review, configuration: configuration, value: "Keep unknown question",
	}
	unknown := llmSaveOutcomeUnknownPage{
		review: review, configuration: configuration,
		question: &resumeQuestion, focus: confirmationBack,
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
	page := mustLLMPage[llmConfigurationPage](state.page)
	if page.question == nil || page.question.value != resumeQuestion.value {
		t.Fatalf("unknown outcome back lost question = %#v", page)
	}
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
	if page := mustLLMPage[llmQuestionPage](state.page); page.value != resumeQuestion.value {
		t.Fatalf("unknown outcome retry lost question = %#v", page)
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
	state.handleKey(key(keyQuit))
	questionInput := mustLLMPage[llmQuestionPage](state.page)
	if questionInput.value != question.value+"cq" {
		t.Fatalf("question printable input = %q", questionInput.value)
	}
	state.handleKey(key("ctrl+e"))
	configurationInput := mustLLMPage[llmConfigurationPage](state.page)
	if configurationInput.draft.Provider != configuration.Provider ||
		configurationInput.draft.Model != configuration.Model ||
		configurationInput.draft.Timeout != configuration.Timeout ||
		state.status != "Choose an LLM provider" {
		t.Fatalf("configuration edit entry = %#v, status = %q", configurationInput, state.status)
	}
	state.handleKey(key(keyEscape))
	returnedQuestion := mustLLMPage[llmQuestionPage](state.page)
	if returnedQuestion.value != questionInput.value || state.status != labelAskLLMDeployment {
		t.Fatalf("configuration edit return = %#v, status = %q", returnedQuestion, state.status)
	}
	state.handleKey(key(keyEscape))
}

//nolint:cyclop,funlen // Every stable action code and choice recovery path is a separate contract outcome.
func TestLLMErrorsChoicesAndCompletionBoundaries(t *testing.T) {
	t.Parallel()
	for code, expected := range map[llm.ErrorCode]string{
		llm.ErrorConfigInvalid: "LLM configuration", llm.ErrorQuestionInvalid: "deployment question",
		llm.ErrorConversationLimit: "new conversation",
		llm.ErrorForbiddenValue:    "protected deployment data", llm.ErrorAuthentication: "authentication failed",
		llm.ErrorRateLimited: "rate-limited", llm.ErrorContextLimit: "context limit", llm.ErrorRefused: "refused",
		llm.ErrorEmptyResponse: "no response", llm.ErrorTruncated: "truncated",
		llm.ErrorInvalidResponse: "local validation", llm.ErrorModelUnavailable: "model is unavailable",
		llm.ErrorTimeout: "timed out", llm.ErrorCancelled: "cancelled",
		llm.ErrorContextStale: "context changed", llm.ErrorProvider: "request failed",
	} {
		err := &llm.ActionError{Code: code}
		if status := llmRecommendationErrorStatus(llmActionErrorCode(err)); !strings.Contains(status, expected) {
			t.Fatalf("status for %s = %q", code, status)
		}
	}
	for code := range map[llm.ErrorCode]struct{}{
		LLMConfigReloadFailed: {}, LLMConfigSaveStale: {}, LLMConfigPathInvalid: {},
		llm.ErrorConfigInvalid: {}, llm.ErrorProvider: {},
	} {
		if llmSaveErrorStatus(&llm.ActionError{Code: code}) == "" {
			t.Fatalf("empty save status for %s", code)
		}
	}
	var nilFailure *llm.ActionError
	for _, failure := range []*llm.ActionError{nilFailure, {Code: llm.ErrorProvider}, {
		Code: llm.ErrorForbiddenValue, Category: "credential",
	}} {
		if failure.Error() == "" {
			t.Fatal("empty action error")
		}
	}
	if llmActionErrorCode(errTestSecret) != llm.ErrorProvider {
		t.Fatal("private error did not collapse to provider failure")
	}
	if llmActionErrorCode(&llm.ActionError{Code: llm.ErrorCode("unknown")}) != llm.ErrorProvider {
		t.Fatal("unknown public error did not collapse to provider failure")
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
	answerChoices := choices
	answerChoices.result.Choices = []llm.Choice{{Kind: llm.ChoiceAnswer, Message: "No change is needed."}}
	answerChoices.cursor = 0
	assistant.acceptErr = errTestSecret
	state.page = answerChoices
	deliver(t, state, state.handleKey(key(keyEnter)))
	if _, valid := state.page.(llmChoicesPage); !valid ||
		state.status != "Provider response could not be added to this conversation" {
		t.Fatalf("answer accept failure = %#v", state)
	}
	assistant.acceptErr = &llm.ActionError{Code: llm.ErrorConversationLimit}
	state.page = answerChoices
	deliver(t, state, state.handleKey(key(keyEnter)))
	if _, valid := state.page.(llmQuestionPage); !valid ||
		state.status != "Conversation limit reached; send again to start a new conversation" {
		t.Fatalf("answer history limit = %#v", state)
	}
	state.page = answerChoices
	state.handleLLMAcceptResult(llmAcceptResultMsg{sequence: state.sequence + 1, page: answerChoices})
	stalePage, valid := state.page.(llmChoicesPage)
	if !valid || len(stalePage.result.Choices) != 1 || stalePage.result.Choices[0].Kind != llm.ChoiceAnswer {
		t.Fatalf("stale answer acceptance changed page to %#v", state.page)
	}
	assistant.acceptErr = errTestSecret
	state.page = choices
	deploymentCalls := len(deployments.calls)
	deliver(t, state, state.handleKey(key(keyEnter)))
	if calls := deployments.calls[deploymentCalls:]; !slices.Equal(calls, []string{testRecommendation, testDiscard}) {
		t.Fatalf("accept failure deployment calls = %q", calls)
	}
	assistant.acceptErr = nil
	deployments.previewHook = state.requestCancellation
	state.page = choices
	acceptCalls := len(assistant.calls)
	deploymentCalls = len(deployments.calls)
	deliver(t, state, state.handleKey(key(keyEnter)))
	if calls := deployments.calls[deploymentCalls:]; !slices.Equal(calls, []string{testRecommendation, testDiscard}) {
		t.Fatalf("pre-accept cancellation deployment calls = %q", calls)
	}
	if len(assistant.calls) != acceptCalls {
		t.Fatal("pre-accept cancellation consumed the choice token")
	}
	deployments.previewHook = nil
	assistant.acceptHook = state.requestCancellation
	state.page = choices
	deploymentCalls = len(deployments.calls)
	deliver(t, state, state.handleKey(key(keyEnter)))
	if calls := deployments.calls[deploymentCalls:]; !slices.Equal(calls, []string{testRecommendation, testDiscard}) {
		t.Fatalf("cancelled acceptance deployment calls = %q", calls)
	}
	assistant.acceptHook = nil
	deployments.err = errTestSecret
	state.page = choices
	acceptCalls = len(assistant.calls)
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
		{Provider: string(llm.ProviderOpenAI), Model: testLLMGenericModel, Timeout: "60"},
		{Provider: string(llm.ProviderOpenAICompatible), Model: testLLMGenericModel,
			Endpoint: "https://example.com", Timeout: "60", APIKey: "key"},
		{Provider: string(llm.ProviderDeepSeek), Model: testLLMGenericModel, Timeout: "60", ClearAPIKey: true},
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
	discard := llmDiscardConfirmationPage{
		configuration: newLLMConfigurationPage(review, configuration),
	}
	state.page = discard
	_ = state.View()
	discard.focus = confirmationApply
	discard.quit = true
	state.page = discard
	_ = state.View()
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
		llmDiscardConfirmationPage{},
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
		{Provider: string(llm.ProviderOpenAI), Warnings: []string{"bad\nwarning"}},
		{Provider: string(llm.ProviderOpenAI), Complete: true},
	} {
		if _, valid := canonicalLLMConfiguration(candidate); valid {
			t.Fatalf("configuration %#v passed", candidate)
		}
	}
	validResult := recommendationsFixture()
	clarification := validResult
	clarification.Choices = []llm.Choice{{
		Kind: llm.ChoiceClarification, Message: "How much memory is available?",
	}}
	if _, valid := canonicalLLMResult(clarification); !valid {
		t.Fatal("valid clarification failed canonicalization")
	}
	invalidResults := []LLMResult{
		{Token: testLLMToken, RequestedModel: "bad\nmodel", Choices: validResult.Choices},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			ReportedModel: "bad\nmodel", Choices: validResult.Choices},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{
				Kind:    llm.ChoiceRecommendation,
				Changes: []llm.Change{{FieldID: application.DeploymentCPUs.ID()}},
			}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceRecommendation, Message: "ok"}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceRecommendation, Message: "ok", Changes: []llm.Change{{}}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceRecommendation, Message: "ok", Changes: []llm.Change{{
				FieldID: application.DeploymentCPUs.ID(), Value: "bad\nvalue",
			}}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceRecommendation, Message: "ok", Changes: []llm.Change{{
				FieldID: application.DeploymentCPUs.ID(), Value: testLLMInvalidChange,
			}}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceRecommendation, Message: "ok", Changes: []llm.Change{
				{FieldID: application.DeploymentCPUs.ID(), Value: "2"},
				{FieldID: application.DeploymentCPUs.ID(), Value: "3"},
			}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceAnswer, Message: "ok", Changes: []llm.Change{{
				FieldID: application.DeploymentCPUs.ID(), Value: "2",
			}}}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceAnswer,
				Message: strings.Repeat("x", maximumLLMMessageBytes+1)}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceAnswer,
				Message: strings.Repeat("line\n", maximumLLMMessageLines)}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceAnswer, Message: string([]byte{0xff})}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceRecommendation, Message: "too many changes",
				Changes: make([]llm.Change, len(application.DeploymentFields())+1)}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel,
			Choices: []llm.Choice{{Kind: llm.ChoiceKind("unknown"), Message: "unknown kind"}}},
		{Token: testLLMToken, RequestedModel: testLLMGenericModel, Choices: make([]llm.Choice, 4)},
	}
	for _, candidate := range invalidResults {
		if _, valid := canonicalLLMResult(candidate); valid {
			t.Fatalf("result %#v passed", candidate)
		}
	}
	if label := llmChoiceKindLabel(llm.ChoiceKind("unknown")); label != "Response" {
		t.Fatalf("unknown choice label = %q", label)
	}
}

func TestRecommendationPatchesConvertsAtTUIBoundary(t *testing.T) {
	t.Parallel()

	changes := []llm.Change{
		{FieldID: application.DeploymentCPUs.ID(), Value: "2"},
		{FieldID: application.DeploymentReadOnly.ID(), Value: "true"},
	}
	patches, err := recommendationPatches(changes)
	if err != nil || len(patches) != 2 || patches[0].Field() != application.DeploymentCPUs ||
		patches[1].Field() != application.DeploymentReadOnly {
		t.Fatalf("recommendationPatches() = %#v, %v", patches, err)
	}
	for _, invalid := range [][]llm.Change{
		{{FieldID: "unknown", Value: "2"}},
		{{FieldID: application.DeploymentCPUs.ID(), Value: testLLMInvalidChange}},
		{
			{FieldID: application.DeploymentCPUs.ID(), Value: "2"},
			{FieldID: application.DeploymentCPUs.ID(), Value: "3"},
		},
	} {
		if patches, err = recommendationPatches(invalid); err == nil || patches != nil {
			t.Fatalf("recommendationPatches(%#v) = %#v, %v", invalid, patches, err)
		}
	}
}

func TestLLMInvalidRecommendationDoesNotStartPreview(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	choices := llmChoicesPage{
		question: llmQuestionPage{review: review, configuration: completeLLMConfiguration()},
		result:   recommendationsFixture(),
	}
	choices.result.Choices[0].Changes[0].Value = testLLMInvalidChange
	deployments, valid := state.deployments.(*deploymentAssistFixture)
	if !valid {
		t.Fatalf("deployment fixture = %T", state.deployments)
	}
	if command := state.startLLMChoicePreview(choices); command != nil || len(deployments.calls) != 0 ||
		state.status != "LLM response did not pass local validation" {
		t.Fatalf("invalid recommendation preview started = %v, %#v, %q", command, deployments.calls, state.status)
	}
}

func TestLLMStepProgressCountsOnlyVisibleSlides(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		provider string
		step     int
		position int
		total    int
	}{
		{provider: string(llm.ProviderOpenAI), step: llmTimeoutStep, position: 3, total: 4},
		{provider: string(llm.ProviderDeepSeek), step: llmAPIKeyStep, position: 4, total: 4},
		{provider: string(llm.ProviderOpenAICompatible), step: llmEndpointStep, position: 3, total: 5},
		{provider: string(llm.ProviderOpenAICompatible), step: llmAPIKeyStep, position: 5, total: 5},
	} {
		position, total := llmStepProgress(test.step, test.provider)
		if position != test.position || total != test.total {
			t.Fatalf("llmStepProgress(%q, %d) = %d/%d", test.provider, test.step, position, total)
		}
	}
}

//nolint:funlen // Each asynchronous save result has a distinct safe recovery destination.
func TestLLMResultAndResizeInvalidationBoundaries(t *testing.T) {
	t.Parallel()
	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	configuration := completeLLMConfiguration()
	configurationPage := newLLMConfigurationPage(review, configuration)
	for _, page := range []page{
		llmSaveConfirmationPage{configuration: newLLMConfigurationPage(review, configuration)},
		llmDiscardConfirmationPage{configuration: newLLMConfigurationPage(review, configuration)},
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
		{sequence: 1, page: configurationPage, configuration: LLMConfiguration{Provider: "bad\nprovider"},
			err: &llm.ActionError{Code: LLMConfigSaveUnknown}},
		{sequence: 2, page: configurationPage, configuration: configuration,
			err: &llm.ActionError{Code: LLMConfigSaveUnknown}},
		{sequence: 3, page: configurationPage, configuration: configuration,
			err: &llm.ActionError{Code: LLMConfigSaveStale}},
		{sequence: 4, page: configurationPage, configuration: LLMConfiguration{}},
		{sequence: 5, page: configurationPage,
			configuration: LLMConfiguration{Provider: string(llm.ProviderOpenAI)}},
		{sequence: 6, page: configurationPage, configuration: configuration},
	}
	for _, result := range results {
		state.sequence = result.sequence
		state.busy = true
		state.handleLLMSaveResult(result)
	}
	state.sequence = 8
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{sequence: 7, page: configurationPage})

	question := llmQuestionPage{review: review, configuration: configuration}
	network := llmNetworkConfirmationPage{question: question}
	state.sequence = 9
	state.busy = true
	state.handleLLMRecommendResult(llmRecommendResultMsg{sequence: 8, page: network})

	for _, candidate := range []LLMConfiguration{
		{Provider: string(llm.ProviderOpenAI), Model: testLLMGenericModel,
			Origin: testLLMOrigin, KeyConfigured: true, Complete: true},
		{Provider: string(llm.ProviderOpenAI), Identity: testLLMIdentity,
			Origin: testLLMOrigin, KeyConfigured: true, Complete: true},
		{Provider: string(llm.ProviderOpenAI), Identity: testLLMIdentity,
			Model: testLLMGenericModel, KeyConfigured: true, Complete: true},
		{Provider: string(llm.ProviderOpenAI), Identity: testLLMIdentity,
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

//nolint:cyclop // Safe-draft preservation checks each typed save failure destination.
func TestLLMSaveFailuresPreserveOnlySafeDraftState(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	draft := newLLMConfigurationPage(review, completeLLMConfiguration())
	draft.step = llmAPIKeyStep
	draft.draft.Model = "unsaved-model"
	draft.draft.APIKey = "unsaved-secret"
	question := llmQuestionPage{
		review: review, configuration: completeLLMConfiguration(), value: "Keep failed-save question",
	}
	draft.question = &question

	state.sequence++
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{
		sequence: state.sequence, page: draft, err: &llm.ActionError{Code: llm.ErrorConfigInvalid},
	})
	recovered := mustLLMPage[llmConfigurationPage](state.page)
	if recovered.draft.Model != "unsaved-model" || recovered.draft.APIKey != "" ||
		recovered.question == nil || recovered.question.value != question.value {
		t.Fatalf("ordinary save failure draft = %#v", recovered.draft)
	}

	reloaded := completeLLMConfiguration()
	reloaded.Model = "externally-updated-model"
	state.sequence++
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{
		sequence: state.sequence, page: draft, configuration: reloaded,
		err: &llm.ActionError{Code: LLMConfigSaveStale},
	})
	recovered = mustLLMPage[llmConfigurationPage](state.page)
	if recovered.draft.Model != reloaded.Model || recovered.dirty() ||
		recovered.question == nil || recovered.question.value != question.value {
		t.Fatalf("stale save did not use the reloaded baseline: %#v", recovered)
	}
	state.sequence++
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{
		sequence: state.sequence, page: draft,
		configuration: LLMConfiguration{Provider: "invalid\nprovider"},
		err:           &llm.ActionError{Code: LLMConfigSaveStale},
	})
	if _, valid := state.page.(reviewPage); !valid ||
		state.llmQuestionToResume == nil || state.llmQuestionToResume.value != question.value {
		t.Fatalf("invalid stale reload page = %T", state.page)
	}

	state.sequence++
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{
		sequence: state.sequence, page: draft, err: &llm.ActionError{Code: LLMConfigReloadFailed},
	})
	if _, valid := state.page.(reviewPage); !valid ||
		state.status != "Configuration changed; effective reload failed" ||
		state.llmQuestionToResume == nil || state.llmQuestionToResume.value != question.value {
		t.Fatalf("stale configuration reload failure = %#v", state)
	}
}

//nolint:cyclop // Sticky save ownership and question restoration form one reload sequence.
func TestLLMSavedReloadFailureRequiresSuccessfulReload(t *testing.T) {
	t.Parallel()

	assistant := &assistantFixture{configuration: completeLLMConfiguration()}
	state, _ := newLLMTestModel(t, assistant)
	review := mustLLMPage[reviewPage](state.page)
	draft := newLLMConfigurationPage(review, completeLLMConfiguration())
	question := llmQuestionPage{
		review: review, configuration: completeLLMConfiguration(), value: "Keep reload question",
	}
	draft.question = &question
	state.sequence++
	state.busy = true
	state.handleLLMSaveResult(llmSaveResultMsg{
		sequence: state.sequence, page: draft, err: &llm.ActionError{Code: LLMConfigSavedReloadFailed},
	})
	if _, valid := state.page.(reviewPage); !valid || !state.configReloadNeeded ||
		state.mutationOutcome != statusLLMConfigSaved ||
		state.status != "Configuration saved; effective reload failed" ||
		state.llmQuestionToResume == nil || state.llmQuestionToResume.value != question.value {
		t.Fatalf("post-save reload failure = %#v", state)
	}
	if content := state.View().Content; !strings.Contains(content, "Reload LLM configuration") {
		t.Fatalf("post-save reload view = %q", content)
	}
	if command := state.startApply(review); command != nil ||
		state.status != "Reload LLM configuration before applying this change" {
		t.Fatalf("apply was not blocked before configuration reload: %#v", state)
	}
	state.page = review
	deliver(t, state, state.activateReview(review))
	reloadedQuestion, valid := state.page.(llmQuestionPage)
	if !valid || state.configReloadNeeded || reloadedQuestion.value != question.value ||
		state.llmQuestionToResume != nil {
		t.Fatalf("successful configuration reload = %#v", state)
	}
}

func TestLLMStaleResultsDoNotReplaceCurrentPage(t *testing.T) {
	t.Parallel()
	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	configuration := completeLLMConfiguration()
	question := llmQuestionPage{review: review, configuration: configuration}
	choices := llmChoicesPage{question: question, result: recommendationsFixture()}
	const currentStatus = "Current page remains active"

	tests := []struct {
		name   string
		handle func()
	}{
		{"configuration", func() {
			state.handleLLMConfigurationResult(llmConfigurationResultMsg{
				sequence: 1, review: review, err: context.DeadlineExceeded,
			})
		}},
		{"save", func() {
			state.handleLLMSaveResult(llmSaveResultMsg{
				sequence: 1, page: newLLMConfigurationPage(review, configuration), err: context.DeadlineExceeded,
			})
		}},
		{"recommendation", func() {
			state.handleLLMRecommendResult(llmRecommendResultMsg{
				sequence: 1, page: llmNetworkConfirmationPage{question: question},
				err: context.DeadlineExceeded,
			})
		}},
		{"preview", func() {
			state.handleLLMPreviewResult(llmPreviewResultMsg{
				sequence: 1, page: choices, err: context.DeadlineExceeded,
			})
		}},
	}
	for _, test := range tests {
		state.sequence = 2
		state.busy = true
		state.page = review
		state.status = currentStatus
		test.handle()
		if _, valid := state.page.(reviewPage); !valid || state.status != currentStatus || !state.busy {
			t.Fatalf("%s stale result changed current state: %#v", test.name, state)
		}
	}
}

func TestLLMPreviewNoChangeAndUnknownWorktreeReturnToSafePages(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	choices := llmChoicesPage{
		question: llmQuestionPage{review: review, configuration: completeLLMConfiguration()},
		result:   recommendationsFixture(),
	}
	deployments, valid := state.deployments.(*deploymentAssistFixture)
	if !valid {
		t.Fatalf("deployment fixture = %T", state.deployments)
	}
	state.deploymentFailure = DeploymentWorktreeUnknown
	if command := state.startLLMChoicePreview(choices); command != nil || len(deployments.calls) != 0 ||
		state.status != deploymentWorktreeUnknownStatus {
		t.Fatalf("unsafe recommendation preview started = %v, %#v, %q", command, deployments.calls, state.status)
	}
	state.deploymentFailure = ""
	state.sequence++
	state.busy = true
	state.handleLLMPreviewResult(llmPreviewResultMsg{
		sequence: state.sequence, page: choices, preview: DeploymentEditPreview{NoChanges: true},
	})
	question, valid := state.page.(llmQuestionPage)
	if !valid || question.assistant.Kind != llm.ChoiceRecommendation ||
		question.assistant.Message != choices.result.Choices[0].Message ||
		state.status != "Recommendation already matches current source" {
		t.Fatalf("no-change preview = %T, %q", state.page, state.status)
	}
}

func TestLLMInvalidProtocolPreviewResultsReturnToSafePages(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	choices := llmChoicesPage{
		question: llmQuestionPage{review: review, configuration: completeLLMConfiguration()},
		result:   recommendationsFixture(),
	}
	state.sequence++
	state.busy = true
	state.handleLLMPreviewResult(llmPreviewResultMsg{
		sequence: state.sequence, page: choices, preview: DeploymentEditPreview{},
	})
	if _, valid := state.page.(llmChoicesPage); !valid ||
		state.status != "Recommended edit could not be displayed safely" {
		t.Fatalf("invalid preview = %T, %q", state.page, state.status)
	}
	state.sequence++
	state.busy = true
	state.handleLLMPreviewResult(llmPreviewResultMsg{
		sequence: state.sequence, page: choices, err: &llm.ActionError{Code: llm.ErrorContextStale},
	})
	if _, valid := state.page.(llmChoicesPage); !valid ||
		state.status != "Deployment context changed; review current values before sending again" {
		t.Fatalf("stale recommendation acceptance = %T, %q", state.page, state.status)
	}
	state.sequence++
	state.busy = true
	state.handleLLMPreviewResult(llmPreviewResultMsg{
		sequence: state.sequence, page: choices, err: &llm.ActionError{Code: llm.ErrorConversationLimit},
	})
	if _, valid := state.page.(llmQuestionPage); !valid ||
		state.status != "Conversation limit reached; send again to start a new conversation" {
		t.Fatalf("recommendation history limit = %T, %q", state.page, state.status)
	}
}

func TestLLMInvalidDeploymentPreviewReturnsToSafePage(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	choices := llmChoicesPage{
		question: llmQuestionPage{review: review, configuration: completeLLMConfiguration()},
		result:   recommendationsFixture(),
	}
	state.sequence++
	state.busy = true
	state.handleLLMPreviewResult(llmPreviewResultMsg{
		sequence: state.sequence, page: choices,
		err: &DeploymentActionError{Code: DeploymentValidationFailed},
	})
	if _, valid := state.page.(llmChoicesPage); !valid || state.err != nil ||
		state.status != "Deployment value failed local validation; edit it before retrying" {
		t.Fatalf("invalid recommended deployment = %T, %v, %q", state.page, state.err, state.status)
	}
	state.handleLLMChoicesKey(choices, keyEscape)
	if _, valid := state.page.(llmQuestionPage); !valid || state.deploymentFailure != "" {
		t.Fatalf("leaving invalid recommendation = %T, %q", state.page, state.deploymentFailure)
	}
}

func TestLLMStaleDeploymentPreviewFailureDoesNotChangeCurrentState(t *testing.T) {
	t.Parallel()

	state, _ := newLLMTestModel(t, &assistantFixture{})
	review := mustLLMPage[reviewPage](state.page)
	choices := llmChoicesPage{
		question: llmQuestionPage{review: review, configuration: completeLLMConfiguration()},
		result:   recommendationsFixture(),
	}
	state.sequence++
	state.busy = true
	state.status = "Current operation"
	state.handleLLMPreviewResult(llmPreviewResultMsg{
		sequence: state.sequence - 1, page: choices,
		err: &DeploymentActionError{Code: DeploymentValidationFailed},
	})
	if _, valid := state.page.(reviewPage); !valid || state.status != "Current operation" || !state.busy {
		t.Fatalf("stale recommended deployment changed state = %T, %q, %t", state.page, state.status, state.busy)
	}
}
