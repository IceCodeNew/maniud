package tui

import (
	"context"
	"fmt"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

const deploymentFixtureDiff = "diff --git a/services/service.yaml b/services/service.yaml\n+    cpus: 2.5\n"

type deploymentWorkflowFixture struct {
	fields      []DeploymentFieldState
	preview     DeploymentEditPreview
	restore     DeploymentEditPreview
	staged      StagedDeploymentEdit
	history     []DeploymentHistoryEntry
	commit      []CommitResult
	commitIndex int
	calls       []string
}

func (fixture *deploymentWorkflowFixture) Fields(
	context.Context,
	application.Request,
) ([]DeploymentFieldState, error) {
	fixture.calls = append(fixture.calls, "fields")

	return slices.Clone(fixture.fields), nil
}

func (fixture *deploymentWorkflowFixture) Preview(
	_ context.Context,
	_ application.Request,
	field string,
	value string,
	unset bool,
) (DeploymentEditPreview, error) {
	fixture.calls = append(fixture.calls, fmt.Sprintf("preview:%s:%s:%t", field, value, unset))

	return fixture.preview, nil
}

func (fixture *deploymentWorkflowFixture) PreviewRestore(
	_ context.Context,
	_ application.Request,
	revision string,
) (DeploymentEditPreview, error) {
	fixture.calls = append(fixture.calls, "restore:"+revision)

	return fixture.restore, nil
}

func (fixture *deploymentWorkflowFixture) Stage(context.Context) (StagedDeploymentEdit, error) {
	fixture.calls = append(fixture.calls, stageCall)

	return fixture.staged, nil
}

func (fixture *deploymentWorkflowFixture) Commit(
	_ context.Context,
	message string,
	unsigned bool,
) (CommitResult, error) {
	fixture.calls = append(fixture.calls, fmt.Sprintf("commit:%t:%s", unsigned, message))
	result := fixture.commit[min(fixture.commitIndex, len(fixture.commit)-1)]
	fixture.commitIndex++

	return result, nil
}

func (fixture *deploymentWorkflowFixture) Discard(context.Context) error {
	fixture.calls = append(fixture.calls, "discard")

	return nil
}

//nolint:cyclop // The test covers each commit presentation plus the help overlay in one flow.
func TestDeploymentCommitQuitRequiresDiscardConfirmation(t *testing.T) {
	t.Parallel()

	deployments := newDeploymentWorkflowFixture()
	state, _, _ := newTestModel(t)
	state.deployments = deployments
	preview := deploymentPreviewPage{preview: deployments.preview}
	commit := commitPage{kind: commitKindDeployment, deployment: preview}
	for _, current := range []page{
		commit,
		stagedDiffPage{commit: commit},
		unsignedCommitConfirmationPage{commit: commit},
	} {
		state.page = current
		if command := state.handleKey(key(keyQuit)); command != nil {
			t.Fatalf("%T quit started an effect before confirmation", current)
		}
		confirmation := mustLLMPage[deploymentDraftConfirmationPage](state.page)
		if fmt.Sprintf("%T", confirmation.previous) != fmt.Sprintf("%T", current) ||
			!confirmation.quit || !confirmation.staged ||
			confirmation.focus != confirmationBack {
			t.Fatalf("%T quit confirmation = %#v", current, confirmation)
		}
		if content := state.View().Content; !strings.Contains(content, "staged Compose candidate") {
			t.Fatalf("%T quit confirmation view = %q", current, content)
		}
		state.handleKey(key(keyEnter))
		if fmt.Sprintf("%T", state.page) != fmt.Sprintf("%T", current) {
			t.Fatalf("%T continue editing returned to %T", current, state.page)
		}
	}
	state.page = commit
	state.handleKey(key("?"))
	if command := state.handleKey(key(keyQuit)); command != nil {
		t.Fatal("help quit started an effect before deployment discard confirmation")
	}
	confirmation := mustLLMPage[deploymentDraftConfirmationPage](state.page)
	if !confirmation.quit || !confirmation.staged {
		t.Fatalf("help quit confirmation = %#v", confirmation)
	}
	state.page = commit
	state.handleKey(key(keyQuit))
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	if !slices.Contains(deployments.calls, "discard") {
		t.Fatalf("confirmed commit quit calls = %q", deployments.calls)
	}
}

func (fixture *deploymentWorkflowFixture) History(
	context.Context,
	application.Request,
) ([]DeploymentHistoryEntry, error) {
	fixture.calls = append(fixture.calls, "history")

	return slices.Clone(fixture.history), nil
}

func newDeploymentWorkflowFixture() *deploymentWorkflowFixture {
	fields := make([]DeploymentFieldState, 0, len(application.DeploymentFields()))
	for _, field := range application.DeploymentFields() {
		fields = append(fields, DeploymentFieldState{
			ID: field.ID(), AllowsUnset: field.AllowsUnset(), Available: true,
		})
	}
	fields[0].Present = true
	fields[0].Value = "1"
	request := application.Request{Service: testService}

	return &deploymentWorkflowFixture{
		fields: fields,
		preview: DeploymentEditPreview{
			ComposePath: testServicePath,
			Diff:        deploymentFixtureDiff,
			Changes: []DeploymentFieldChange{
				deploymentChange(application.DeploymentCPUs, "1", "2.5"),
			},
		},
		restore: DeploymentEditPreview{
			ComposePath: testServicePath,
			Diff:        deploymentFixtureDiff,
			Changes: []DeploymentFieldChange{
				deploymentChange(application.DeploymentCPUs, "2.5", "1"),
			},
			Restore: strings.Repeat("b", 40),
		},
		staged: StagedDeploymentEdit{
			Diff:        deploymentFixtureDiff,
			ComposePath: testServicePath, CommitMessage: "Update service deployment",
		},
		history: []DeploymentHistoryEntry{
			{Revision: strings.Repeat("a", 40), Subject: "Current deployment", SignaturePresent: true},
			{Revision: strings.Repeat("b", 40), Subject: "Previous deployment"},
		},
		commit: []CommitResult{
			{Outcome: CommitNeedsUnsignedApproval},
			{Request: request, Outcome: CommitSucceeded},
		},
	}
}

func deploymentChange(
	field application.DeploymentField,
	current string,
	proposed string,
) DeploymentFieldChange {
	return DeploymentFieldChange{
		FieldID: field.ID(), CurrentValue: current, CurrentPresent: true,
		ProposedValue: proposed, ProposedPresent: true,
	}
}

func chooseReviewOption(state *model, option int) tea.Cmd {
	state.handleKey(key("o"))
	for range option {
		state.handleKey(key(keyDown))
	}

	return state.handleKey(key(keyEnter))
}

func deploymentReviewPage() reviewPage {
	return reviewPage{
		request: application.Request{Service: testService},
		plan: planView{
			project: testProject, service: testService, runtime: testRuntime, status: statusReady,
		},
	}
}

//nolint:cyclop // The test preserves the user-visible edit, stage, and explicit unsigned fallback sequence.
func TestModelEditsDeploymentWithExplicitUnsignedFallback(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	deployments := newDeploymentWorkflowFixture()
	state.deployments = deployments
	state.page = deploymentReviewPage()
	state.status = statusReady

	deliver(t, state, chooseReviewOption(state, 2))
	fields, valid := state.page.(deploymentFieldsPage)
	if !valid || len(fields.fields) != len(application.DeploymentFields()) {
		t.Fatalf("fields page = %#v", state.page)
	}
	state.handleKey(key(keyEnter))
	value, valid := state.page.(deploymentValuePage)
	if !valid || value.value != "" || value.field.Value != "1" {
		t.Fatalf("value page = %#v", state.page)
	}
	state.handleKey(key("2.5"))
	deliver(t, state, state.handleKey(key(keyEnter)))
	if _, valid = state.page.(deploymentPreviewPage); !valid {
		t.Fatalf("preview page = %T", state.page)
	}
	state.handleKey(key(keyEnter))
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	commit, valid := state.page.(commitPage)
	if !valid || commit.kind != commitKindDeployment || commit.focus != confirmationBack {
		t.Fatalf("commit page = %#v", state.page)
	}
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	unsigned, valid := state.page.(unsignedCommitConfirmationPage)
	if !valid || unsigned.focus != confirmationBack || unsigned.commit.kind != commitKindDeployment {
		t.Fatalf("unsigned confirmation = %#v", state.page)
	}
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	if _, valid = state.page.(reviewPage); !valid || state.mutationOutcome != "Deployment commit created" {
		t.Fatalf("post-commit state = %#v", state)
	}
	wantCalls := []string{
		"fields", "preview:cpus:2.5:false", stageCall,
		"commit:false:Update service deployment", "commit:true:Update service deployment",
	}
	if !slices.Equal(deployments.calls, wantCalls) ||
		!slices.Equal(operations.recordedCalls(), []string{dryRunCall, snapshotCall, evidenceCall}) {
		t.Fatalf("calls = %q / %q", deployments.calls, operations.recordedCalls())
	}
}

//nolint:cyclop // The test keeps the field draft lifecycle in one contiguous interaction.
func TestDeploymentValueDraftSurvivesBackAndRequiresDiscardBeforeLeaving(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	deployments := newDeploymentWorkflowFixture()
	state.deployments = deployments
	review := deploymentReviewPage()
	fields := deploymentFieldsPage{review: review, fields: deployments.fields}
	state.page = fields

	state.handleKey(key(keyEnter))
	state.handleKey(key("2.5"))
	if !strings.Contains(state.View().Content, "Unsaved") {
		t.Fatalf("dirty value view = %q", state.View().Content)
	}
	state.handleKey(key(keyEscape))
	fields = mustLLMPage[deploymentFieldsPage](state.page)
	if !fields.draft.dirty() || fields.draft.value != "2.5" {
		t.Fatalf("preserved deployment draft = %#v", fields.draft)
	}
	state.handleKey(key(keyEnter))
	if value := mustLLMPage[deploymentValuePage](state.page); value.value != "2.5" {
		t.Fatalf("restored deployment value = %#v", value)
	}
	state.handleKey(key(keyEscape))
	fields = mustLLMPage[deploymentFieldsPage](state.page)
	fields.cursor++
	state.page = fields
	state.handleKey(key(keyEnter))
	if _, valid := state.page.(deploymentFieldsPage); !valid ||
		state.status != "Return to the unsaved field or discard it before editing another field" {
		t.Fatalf("different field with draft = %T, %q", state.page, state.status)
	}
	state.handleKey(key(keyEscape))
	discard := mustLLMPage[deploymentDraftConfirmationPage](state.page)
	if discard.quit || discard.focus != confirmationBack {
		t.Fatalf("deployment discard confirmation = %#v", discard)
	}
	state.handleKey(key(keyEscape))
	if current := mustLLMPage[deploymentFieldsPage](state.page); !current.draft.dirty() {
		t.Fatal("continue editing discarded the deployment value")
	}
	state.handleKey(key(keyEscape))
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	if _, valid := state.page.(reviewPage); !valid ||
		!slices.Equal(deployments.calls, []string{"discard"}) {
		t.Fatalf("discarded deployment draft = %T, %q", state.page, deployments.calls)
	}
}

func TestModelRestoresPriorDeploymentRevision(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	deployments := newDeploymentWorkflowFixture()
	state.deployments = deployments
	state.page = deploymentReviewPage()
	state.status = statusReady

	deliver(t, state, chooseReviewOption(state, 3))
	if _, valid := state.page.(deploymentHistoryPage); !valid {
		t.Fatalf("history page = %T", state.page)
	}
	state.handleKey(key(keyEnter))
	if _, valid := state.page.(deploymentHistoryPage); !valid || state.status != "This revision is already current" {
		t.Fatalf("current history selection = %#v, %q", state.page, state.status)
	}
	state.handleKey(key(keyDown))
	state.handleKey(key(keyEnter))
	confirmation, valid := state.page.(restoreDeploymentConfirmationPage)
	if !valid || confirmation.entry.Revision != strings.Repeat("b", 40) {
		t.Fatalf("restore confirmation = %#v", state.page)
	}
	state.handleKey(key(keyTab))
	deliver(t, state, state.handleKey(key(keyEnter)))
	preview, valid := state.page.(deploymentPreviewPage)
	if !valid || preview.preview.Restore != strings.Repeat("b", 40) ||
		!slices.Equal(deployments.calls, []string{"history", "restore:" + strings.Repeat("b", 40)}) {
		t.Fatalf("restore preview = %#v, calls = %q", state.page, deployments.calls)
	}
}

func TestDeploymentCommitReadbackFailureRequiresCatalogReload(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	preview := deploymentPreviewPage{review: deploymentReviewPage()}
	state.sequence++
	state.handleDeploymentCommitResult(deploymentCommitResultMsg{
		sequence: state.sequence,
		commit:   commitPage{kind: commitKindDeployment, deployment: preview},
		result:   CommitResult{Outcome: CommitValidationUnavailable},
	})
	home, valid := state.page.(homePage)
	if !valid || home.catalog.State != CatalogUnavailable ||
		state.status != "Commit created; validation is unavailable" ||
		state.mutationOutcome != "Deployment commit created" {
		t.Fatalf("post-commit readback state = %#v", state)
	}
}

func TestDeploymentProjectionAndCursorWindowFailClosed(t *testing.T) {
	t.Parallel()

	fixture := newDeploymentWorkflowFixture()
	if _, err := canonicalDeploymentFields(fixture.fields[:len(fixture.fields)-1]); err == nil {
		t.Fatal("partial deployment field projection was accepted")
	}
	reordered := slices.Clone(fixture.fields)
	reordered[0], reordered[1] = reordered[1], reordered[0]
	if _, err := canonicalDeploymentFields(reordered); err == nil {
		t.Fatal("reordered deployment field projection was accepted")
	}

	state, _, _ := newTestModel(t)
	state.height = 18
	page := deploymentFieldsPage{fields: fixture.fields, cursor: len(fixture.fields) - 1}
	fields := strings.Join(state.deploymentFieldsBody(page, 80), "\n")
	if !strings.Contains(fields, "Health start interval") || strings.Contains(fields, "CPU limit") {
		t.Fatalf("field cursor window = %q", fields)
	}
	history := make([]DeploymentHistoryEntry, 20)
	for index := range history {
		history[index] = DeploymentHistoryEntry{
			Revision: fmt.Sprintf("%040x", index+1), Subject: fmt.Sprintf("Revision %d", index+1),
		}
	}
	historyBody := strings.Join(state.deploymentHistoryBody(
		deploymentHistoryPage{history: history, cursor: len(history) - 1}, 80,
	), "\n")
	if !strings.Contains(historyBody, "Revision 20") || strings.Contains(historyBody, "Revision 1  ") {
		t.Fatalf("history cursor window = %q", historyBody)
	}
}
