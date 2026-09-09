package tui

import (
	"context"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	testReviewCancelling = "Cancelling"
	testSuspendCall      = "suspend"
)

func TestReviewDisclosesActiveEffect(t *testing.T) {
	t.Parallel()

	for _, size := range [][2]int{{80, 24}, {56, 16}} {
		state, _, _ := newTestModel(t)
		deliver(t, state, state.startCatalog())
		deliver(t, state, state.handleKey(key("enter")))
		state.Update(tea.WindowSizeMsg{Width: size[0], Height: size[1]})
		state.Update(key("enter"))
		state.Update(key("tab"))
		state.Update(key("enter"))
		for _, status := range []string{"Applying change", testReviewCancelling} {
			if status == testReviewCancelling {
				state.Update(key("esc"))
			}
			content := state.View().Content
			t.Logf("CAPTURE %dx%d %s\n%s\nEND CAPTURE", size[0], size[1], status, content)
			if !strings.Contains(content, status) || strings.Contains(content, statusReady) ||
				strings.Contains(content, "No runtime change has started") ||
				strings.Contains(content, "Continue to confirmation") || strings.Contains(content, "? Help") {
				t.Errorf("%dx%d %s has misleading review:\n%s", size[0], size[1], status, content)
			}
		}
		state.finishOperation()
	}
}

func TestSettledUnchangedRefreshesInsteadOfConfirming(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	operations.snapshot.Plan.Kind = application.PlanUnchanged
	deliver(t, state, state.startSnapshot(application.Request{}))
	content := state.View().Content
	if !strings.Contains(content, "Refresh") || strings.Contains(content, "Continue to confirmation") {
		t.Errorf("settled no-op actions:\n%s", content)
	}
	state.Update(key(keyTab))
	state.Update(key(keyEnter))
	if _, valid := state.page.(detailsPage); !valid {
		t.Fatal("the settled secondary action did not open Details")
	}
	state.Update(key(keyEscape))
	state.Update(key("o"))
	if _, valid := state.page.(reviewOptionsPage); !valid {
		t.Fatal("settled review lost deployment editing and LLM access")
	}
	state.Update(key(keyEscape))
	state.Update(key(keyTab))
	operations.snapshot.Plan.Kind = application.PlanUpgrade
	_, command := state.Update(key("enter"))
	deliver(t, state, command)
	if reviewPageValue(t, state).plan.kind != string(application.PlanUpgrade) {
		t.Fatal("Enter did not re-evaluate the source/runtime snapshot")
	}
	operations.snapshot.Plan.Kind = application.PlanUnchanged
	operations.snapshot.HasTransaction = true
	deliver(t, state, state.startSnapshot(application.Request{}))
	if reviewPageValue(t, state).plan.settled {
		t.Fatal("unresolved transaction was classified as settled")
	}
}

func TestWarningDetailsDoNotTrustMessageOrCode(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	secret := "/private/customer/token"
	untrusted := secret + "\x1b[2J"
	operations.snapshot.Plan.Warnings = []application.Warning{
		{Code: application.WarningDaemonMountProbeUnavailable, Message: untrusted},
		{Code: application.WarningCode(untrusted), Message: untrusted},
	}
	deliver(t, state, state.startSnapshot(application.Request{}))
	review := reviewPageValue(t, state)
	text := state.detailProjection(review).plain()
	if !strings.Contains(text, "daemon-side") || !strings.Contains(text, "unavailable") ||
		strings.Contains(text, secret) {
		t.Fatalf("unsafe or unhelpful warnings: %q", text)
	}
	state.Update(key("enter"))
	if !strings.Contains(state.View().Content, "2 warning") || !strings.Contains(state.View().Content, "Details") {
		t.Fatal("confirmation lost warning count or Details access")
	}
	state.Update(key("d"))
	if _, valid := state.page.(detailsPage); !valid {
		t.Fatal("confirmation Details shortcut did not open Details")
	}
	if lines := strings.Join(state.detailsLines(review, 56), "\n"); !strings.Contains(lines, "daemon-side") {
		t.Fatal("warning summary is absent from scrollable Details")
	}
}

func TestRepeatedWarningDetailsRemainBounded(t *testing.T) {
	t.Parallel()

	_, _, operations := newTestModel(t)
	operations.snapshot.Plan.Warnings = []application.Warning{{Code: testDeploymentUnknownField}}
	for range 100 {
		operations.snapshot.Plan.Warnings = append(operations.snapshot.Plan.Warnings,
			application.Warning{Code: application.WarningDaemonMountProbeUnavailable})
	}
	view, err := projectPlan(operations.snapshot)
	if err != nil || len(view.warnings) != 2 {
		t.Fatalf("warning projection must remain bounded: %d, %v", len(view.warnings), err)
	}
}

func TestReviewDisclosesReadOnlyRefresh(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	deliver(t, state, state.startSnapshot(application.Request{}))
	_, command := state.Update(key("r"))
	content := state.View().Content
	if !strings.Contains(content, statusRefreshing) || strings.Contains(content, statusReady) {
		t.Fatalf("refresh status: %s", content)
	}
	deliver(t, state, command)
}

func TestCompactCommitKeepsBothActionsVisible(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	for _, unicode := range []bool{false, true} {
		state.options.Unicode = unicode
		for _, focus := range []confirmationFocus{confirmationBack, confirmationApply} {
			state.resize(56, 16)
			state.page = commitPage{
				kind:  commitKindService,
				focus: focus, message: strings.Repeat("message", 30),
				staged: StagedService{ComposePath: registeredAPIID, Diff: strings.Repeat("+changed line\n", 20)},
			}
			content := state.View().Content
			t.Logf("CAPTURE compact commit unicode=%t focus=%d\n%s\nEND CAPTURE", unicode, focus, content)
			assertViewContains(t, content, "compact commit", "Back and save draft", "Create signed commit", registeredAPIID)
			assertBoundedView(t, content, 56, 16)
		}
	}
}

func TestReviewSettledEffectAndReadbackStatus(t *testing.T) {
	t.Parallel()

	for _, size := range [][2]int{{80, 24}, {56, 16}} {
		for _, outcome := range []struct {
			applyErr, snapshotErr error
			want                  string
		}{
			{want: statusApplyCompleted},
			{applyErr: errTestSecret, want: statusOperationFailed},
			{applyErr: context.Canceled, want: statusCancelled},
			{snapshotErr: errTestSecret, want: statusApplyCompleted},
		} {
			state, _, operations := newTestModel(t)
			deliver(t, state, state.startSnapshot(application.Request{}))
			state.resize(size[0], size[1])
			state.Update(key("enter"))
			state.Update(key("tab"))
			operations.applyErr = outcome.applyErr
			operations.snapshotErr = outcome.snapshotErr
			_, command := state.Update(key("enter"))
			deliver(t, state, command)
			content := state.View().Content
			if !strings.Contains(content, outcome.want) || strings.Contains(content, "No runtime change has started") ||
				strings.Contains(content, errTestSecret.Error()) {
				t.Errorf("%v %s: %s", size, outcome.want, content)
			}
		}
	}
}

func TestCompactCommitKeyboardRequiresVisibleConfirmation(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	workspace.staged.Diff = "+first changed line\n" + strings.Repeat("+changed line\n", 20) + "+last changed line"
	commit := commitPage{kind: commitKindService, staged: workspace.staged, message: workspace.staged.CommitMessage}
	state.resize(56, 16)
	state.page = commit
	state.Update(key("d"))
	assertViewContains(t, state.View().Content, "opened full diff", "Staged diff", "+first changed line")
	if strings.Contains(state.View().Content, "+last changed line") {
		t.Fatal("fixture does not require scrolling to read the full diff")
	}
	for range 20 {
		state.Update(key(keyDown))
	}
	assertViewContains(t, state.View().Content, "scrolled full diff", "+last changed line")
	state.Update(key("esc"))
	state.Update(key("tab"))
	state.Update(tea.WindowSizeMsg{Width: 32, Height: 8})
	state.Update(key("tab"))
	_, command := state.Update(key("enter"))
	deliver(t, state, command)
	if len(workspace.recordedCalls()) != 0 {
		t.Fatalf("hidden Enter called the workspace: %q", workspace.recordedCalls())
	}
	state.Update(tea.WindowSizeMsg{Width: 56, Height: 16})
	state.Update(key("tab"))
	assertViewContains(t, state.View().Content, "visible commit target",
		"Create signed commit", workspace.staged.ComposePath)
	_, command = state.Update(key("enter"))
	deliver(t, state, command)
	if !slices.Equal(workspace.recordedCalls(), []string{"commit:false:" + workspace.staged.CommitMessage}) {
		t.Fatalf("explicit commit calls: %q", workspace.recordedCalls())
	}
}

func TestNarrowCommitAllowsBackAndQuit(t *testing.T) {
	t.Parallel()

	for _, input := range []string{keyEscape, keyQuit} {
		state, _, _ := newTestModel(t)
		state.resize(32, 8)
		state.page = commitPage{kind: commitKindService}
		_, command := state.Update(key(input))
		if command == nil {
			t.Fatalf("%s was blocked below Compact", input)
		}
		deliver(t, state, command)
		want := []string(nil)
		if input == keyEscape {
			want = []string{testSuspendCall}
		}
		if calls := workspaceFixtureValue(t, state).recordedCalls(); !slices.Equal(calls, want) {
			t.Fatalf("%s: calls %q", input, calls)
		}
	}
}
