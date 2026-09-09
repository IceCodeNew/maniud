package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

func TestCompactSaveAndStageExposeKeyboardEffects(t *testing.T) {
	t.Parallel()

	for _, unicode := range []bool{false, true} {
		for _, staging := range []bool{false, true} {
			t.Run(fmt.Sprintf("unicode=%t/staging=%t", unicode, staging), func(t *testing.T) {
				t.Parallel()
				testCompactSaveAndStage(t, unicode, staging)
			})
		}
	}
}

func testCompactSaveAndStage(t *testing.T, unicode, staging bool) {
	t.Helper()

	assistant := &assistantFixture{saved: completeLLMConfiguration()}
	state, deployments := newLLMTestModel(t, assistant)
	state.options.Unicode = unicode
	review := deploymentReviewPage()
	configuration := newLLMConfigurationPage(review, completeLLMConfiguration())
	configuration.draft.Model = strings.Repeat("long-model-", 30)
	configuration.step = llmAPIKeyStep
	preview := deploymentPreviewPage{review: review, preview: deployments.preview}
	preview.preview.Diff = "+first staged change\n" + strings.Repeat("+long changed line\n", 40) + "+last staged change\n"
	state.page = llmSaveConfirmationPage{configuration: configuration}
	action := "Save configuration"
	if staging {
		state.page = stageDeploymentConfirmationPage{preview: preview}
		action = "Write and stage edit"
	}
	assertConfirmationResizeFocus(t, state, action)
	if staging {
		assertStageConfirmationDiffNavigation(t, state)
	}
	state.Update(key(keyTab))
	state.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
	_, command := state.Update(key(keyEnter))
	deliver(t, state, command)
	if len(assistant.calls)+len(deployments.calls) != 0 {
		t.Fatalf("hidden effect: %q / %q", assistant.calls, deployments.calls)
	}
	state.Update(tea.WindowSizeMsg{Width: 56, Height: 16})
	_, command = state.Update(key(keyEnter))
	deliver(t, state, command)
	if !staging {
		state.Update(key(keyEnter))
	}
	if len(assistant.calls)+len(deployments.calls) != 0 {
		t.Fatalf("returning to confirmation ran an effect: %q / %q", assistant.calls, deployments.calls)
	}
	state.Update(key(keyTab))
	assertViewContains(t, state.View().Content, "visible effect", action)
	_, command = state.Update(key(keyEnter))
	deliver(t, state, command)
	if staging {
		if !slices.Equal(deployments.calls, []string{stageCall}) {
			t.Fatalf("Stage calls: %q", deployments.calls)
		}
	} else if len(assistant.settings) != 1 {
		t.Fatalf("Save calls: %q", assistant.calls)
	}
}

func assertStageConfirmationDiffNavigation(t *testing.T, state *model) {
	t.Helper()

	state.Update(key("d"))
	assertViewContains(t, state.View().Content, "first exact diff line", "+first staged change")
	for range 50 {
		state.Update(key(keyDown))
	}
	assertViewContains(t, state.View().Content, "last exact diff line", "+last staged change")
	state.Update(key(keyEscape))
}

func assertConfirmationResizeFocus(t *testing.T, state *model, action string) {
	t.Helper()

	for _, size := range [][2]int{{80, 24}, {56, 16}, {80, 24}, {56, 16}} {
		state.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		for range 2 {
			content := state.View().Content
			t.Logf("CAPTURE %v unicode=%t %s\n%s\nEND CAPTURE", size, state.options.Unicode, action, content)
			assertViewContains(t, content, action, "Back", action, "Esc Back")
			assertBoundedView(t, content, size[0], size[1])
			state.Update(key(keyTab))
		}
	}
}

func TestReviewHealthMarkersAndOperationPrecedence(t *testing.T) {
	t.Parallel()

	markers := map[application.HealthConvergence][2]string{
		application.HealthConvergencePending:  {"[.] ", "· "},
		application.HealthConvergenceDegraded: {"[!] ", "! "},
	}
	for mode, unicode := range []bool{false, true} {
		for _, size := range [][2]int{{80, 24}, {56, 16}} {
			state, _, _ := newTestModel(t)
			state.options.Unicode = unicode
			state.resize(size[0], size[1])
			for _, health := range []application.HealthConvergence{
				application.HealthConvergencePending, application.HealthConvergenceDegraded,
			} {
				review := deploymentReviewPage()
				projectHealthPlan(application.OperationSnapshot{Plan: application.Plan{Health: health}}, &review.plan)
				state.page = review
				content := state.View().Content
				if strings.Contains(content, "[OK]") || strings.Contains(content, "No runtime change has started") {
					t.Fatalf("health %s: %s", health, content)
				}
				assertViewContains(t, content, "health marker", markers[health][mode]+review.plan.status)
				state.startApply(review)
				state.Update(key(keyEscape))
				content = state.View().Content
				if !strings.Contains(content, testReviewCancelling) || strings.Contains(content, review.plan.status) {
					t.Fatalf("health cancellation: %s", content)
				}
				state.finishOperation()
			}
		}
	}
}

func TestUnchangedRecoveryRetainsExplicitHealthDecision(t *testing.T) {
	t.Parallel()

	for _, action := range []application.HealthResolutionAction{
		application.HealthResolutionRollback, application.HealthResolutionCancelAdoption,
		application.HealthResolutionRetryRestoreStart,
	} {
		t.Run(string(action), func(t *testing.T) {
			t.Parallel()

			state, _, operations := newTestModel(t)
			operations.snapshot.Plan.Kind = application.PlanUnchanged
			operations.snapshot.HasTransaction = true
			operations.snapshot.Transaction.ID = strings.Repeat("a", 32)
			operations.snapshot.Plan.Health = application.HealthConvergenceDegraded
			operations.snapshot.AvailableHealthResolution = action
			operations.snapshot.HealthResolutionRestoresPrevious = action == application.HealthResolutionRollback
			operations.snapshot.Plan.Warnings = []application.Warning{{Code: application.WarningDaemonMountProbeUnavailable}}
			deliver(t, state, state.startSnapshot(application.Request{}))
			if reviewPageValue(t, state).plan.settled {
				t.Fatal("an unresolved health transaction became a settled no-op")
			}
			state.Update(key(keyEnter))
			assertConfirmationResizeFocus(t, state,
				healthActionLabel(mustLLMPage[healthConfirmationPage](state.page).review.plan))
			assertViewContains(t, state.View().Content, "health confirmation", "1 warning", "d Details")
			state.Update(key("d"))
			if _, valid := state.page.(detailsPage); !valid {
				t.Fatal("health confirmation did not expose warning Details")
			}
			state.Update(key(keyEscape))
			state.Update(key(keyEnter))
			state.Update(key(keyTab))
			state.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
			state.Update(key(keyEnter))
			if len(operations.recordedResolutions()) != 0 {
				t.Fatal("hidden health resolution")
			}
			state.Update(tea.WindowSizeMsg{Width: 56, Height: 16})
			state.Update(key(keyEnter))
			state.Update(key(keyTab))
			_, command := state.Update(key(keyEnter))
			deliver(t, state, command)
			if resolutions := operations.recordedResolutions(); len(resolutions) != 1 || resolutions[0].Action != action {
				t.Fatalf("explicit recovery calls: %#v", resolutions)
			}
		})
	}
}
