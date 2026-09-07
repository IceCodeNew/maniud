package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/llm"
)

const llmModelDifferenceWarning = "Models differ"

func TestLLMChoicesDiscloseModelsBeforeAcceptance(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		name, requested, reported, warning string
	}{
		{"mismatch", "requested-model", "reported-model", llmModelDifferenceWarning},
		{"equal", "same-model", "same-model", ""},
		{"missing", "requested-model", "", "Provider model is missing"},
		{"long", strings.Repeat("requested-", 20), strings.Repeat("reported-", 20), llmModelDifferenceWarning},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			for _, unicode := range []bool{false, true} {
				state, _, _ := newTestModel(t)
				state.options.Unicode = unicode
				result := recommendationsFixture()
				result.RequestedModel, result.ReportedModel = test.requested, test.reported
				state.page = llmChoicesPage{result: result}
				for _, size := range [][2]int{{80, 24}, {56, 16}, {100, 30}} {
					_, command := state.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
					deliver(t, state, command)
					view := state.View().Content
					assertViewContains(t, view, test.name, "Requested model", "Reported model", "Proposed change")
					if strings.Contains(view, llmModelDifferenceWarning) != (test.warning == llmModelDifferenceWarning) ||
						strings.Contains(view, "Provider model is missing") != (test.warning == "Provider model is missing") {
						t.Fatalf("wrong model warning: %s", view)
					}
					if test.name != "long" {
						assertViewContains(t, view, test.name, test.requested, test.reported)
					} else {
						assertViewContains(t, view, test.name, "requested-", "reported-")
					}
					assertBoundedView(t, view, size[0], size[1])
					t.Logf("render unicode=%t %dx%d\n%s", unicode, size[0], size[1], view)
				}
			}
		})
	}
}

func TestLLMChoicesKeepPaidResponseBelowCompact(t *testing.T) {
	t.Parallel()
	for _, kind := range []llm.ChoiceKind{llm.ChoiceAnswer, llm.ChoiceClarification, llm.ChoiceRecommendation} {
		for _, arrivesNarrow := range []bool{false, true} {
			t.Run(fmt.Sprintf("%s/arrives-narrow=%t", kind, arrivesNarrow), func(t *testing.T) {
				t.Parallel()
				assertLLMChoicesSurviveNarrowScreen(t, kind, arrivesNarrow)
			})
		}
	}
}

func assertLLMChoicesSurviveNarrowScreen(t *testing.T, kind llm.ChoiceKind, arrivesNarrow bool) {
	t.Helper()
	result := recommendationsFixture()
	result.Choices = result.Choices[:1]
	result.Choices[0].Kind = kind
	if kind != llm.ChoiceRecommendation {
		result.Choices[0].Changes = nil
	}
	assistant := &assistantFixture{result: result}
	state, deployments := newLLMTestModel(t, assistant)
	question := llmQuestionPage{review: mustLLMPage[reviewPage](state.page), configuration: completeLLMConfiguration()}
	state.resize(compactMinimum, compactMinHeight)
	state.page = llmChoicesPage{question: question, result: result}
	var response tea.Cmd
	if arrivesNarrow {
		state.page = llmNetworkConfirmationPage{question: question, focus: confirmationApply}
		_, response = state.Update(key(keyEnter))
	}
	_, resize := state.Update(tea.WindowSizeMsg{Width: hardMinimumWidth, Height: hardMinimumHeight})
	deliver(t, state, resize)
	deliver(t, state, response)
	beforeCalls := slices.Clone(assistant.calls)
	_, enter := state.Update(key(keyEnter))
	deliver(t, state, enter)
	if !slices.Equal(assistant.calls, beforeCalls) || len(deployments.calls) != 0 {
		t.Fatalf("hidden selection called assistant/deployment: %q / %q", assistant.calls, deployments.calls)
	}
	retained := mustLLMPage[llmChoicesPage](state.page)
	if retained.result.Token != result.Token || retained.result.Choices[0].Kind != kind {
		t.Fatal("resize discarded the paid response")
	}
	t.Logf("hard-floor\n%s", state.View().Content)
	_, resize = state.Update(tea.WindowSizeMsg{Width: compactMinimum, Height: compactMinHeight})
	deliver(t, state, resize)
	assertViewContains(t, state.View().Content, "enlarged response", result.Choices[0].Message)
	_, enter = state.Update(key(keyEnter))
	deliver(t, state, enter)
	if !slices.Equal(assistant.calls, append(beforeCalls, "accept:"+result.Token+":0")) {
		t.Fatalf("explicit acceptance after enlargement: %q", assistant.calls)
	}
	if (len(deployments.calls) != 0) != (kind == llm.ChoiceRecommendation) {
		t.Fatalf("preview calls = %q for %s", deployments.calls, kind)
	}
}

func TestLLMChoicesAllowNarrowBackAndQuit(t *testing.T) {
	t.Parallel()
	for _, input := range []string{keyEscape, keyQuit} {
		t.Run(input, func(t *testing.T) {
			t.Parallel()
			assistant := &assistantFixture{}
			state, deployments := newLLMTestModel(t, assistant)
			state.page = llmChoicesPage{result: recommendationsFixture()}
			_, command := state.Update(tea.WindowSizeMsg{Width: hardMinimumWidth, Height: hardMinimumHeight})
			deliver(t, state, command)
			_, command = state.Update(key(input))
			if input == keyEscape {
				if _, ok := state.page.(llmQuestionPage); !ok {
					t.Fatalf("Back remained on %T", state.page)
				}
			} else {
				if command == nil {
					t.Fatal("Quit did not return a command")
				}
				if _, ok := command().(tea.QuitMsg); !ok {
					t.Fatal("Quit did not exit")
				}
			}
			if len(assistant.calls) != 0 || len(deployments.calls) != 0 {
				t.Fatal("navigation accepted or previewed a response")
			}
		})
	}
}
