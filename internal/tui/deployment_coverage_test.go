package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"testing"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

const (
	testDeploymentCPUInput        = "2.5"
	testDeploymentDefaultDuration = "30s"
	testDeploymentTrueValue       = "true"
	testDeploymentUnknownField    = "unknown"
)

//nolint:cyclop,funlen // One table covers each malformed projection boundary for the closed value catalog.
func TestDeploymentProjectionsRejectEveryMalformedShape(t *testing.T) {
	t.Parallel()

	fixture := newDeploymentWorkflowFixture()
	if fields, err := canonicalDeploymentFields(fixture.fields); err != nil || len(fields) != len(fixture.fields) {
		t.Fatalf("canonicalDeploymentFields(valid) = %#v, %v", fields, err)
	}
	fieldCases := [][]DeploymentFieldState{
		nil,
		append(slices.Clone(fixture.fields), fixture.fields[0]),
		replaceDeploymentField(fixture.fields, 0, DeploymentFieldState{
			ID: testDeploymentUnknownField, AllowsUnset: true, Available: true,
		}),
		replaceDeploymentField(fixture.fields, 1, fixture.fields[0]),
		replaceDeploymentField(fixture.fields, 0, DeploymentFieldState{
			ID: application.DeploymentCPUs.ID(), Value: string([]byte{0xff}), Present: true,
			AllowsUnset: true, Available: true,
		}),
		replaceDeploymentField(fixture.fields, 0, DeploymentFieldState{
			ID: application.DeploymentCPUs.ID(), AllowsUnset: false, Available: true,
		}),
	}
	for _, fields := range fieldCases {
		if _, err := canonicalDeploymentFields(fields); err == nil {
			t.Fatalf("canonicalDeploymentFields(%#v) succeeded", fields)
		}
	}

	validRevisionValue := strings.Repeat("a", 40)
	cpuChange := deploymentChange(application.DeploymentCPUs, "1", "2")
	validPreviews := []DeploymentEditPreview{
		{ComposePath: testServicePath, Changes: []DeploymentFieldChange{cpuChange}, Diff: testDiff},
		{ComposePath: testServicePath, Changes: []DeploymentFieldChange{{
			FieldID: application.DeploymentCPUs.ID(), ProposedValue: "2", ProposedPresent: true,
		}}, Diff: testDiff},
		{ComposePath: testServicePath, Restore: validRevisionValue, Diff: testDiff},
		{
			ComposePath: testServicePath, Changes: []DeploymentFieldChange{cpuChange},
			Restore: validRevisionValue, Diff: testDiff,
		},
		{NoChanges: true},
	}
	for _, preview := range validPreviews {
		if _, err := canonicalDeploymentPreview(preview); err != nil {
			t.Fatalf("canonicalDeploymentPreview(%#v) error = %v", preview, err)
		}
	}
	previewCases := []DeploymentEditPreview{
		{},
		{NoChanges: true, Diff: testDiff},
		{ComposePath: "/absolute", Changes: []DeploymentFieldChange{cpuChange}},
		{ComposePath: testServicePath},
		{
			ComposePath: testServicePath,
			Changes: []DeploymentFieldChange{{
				FieldID: testDeploymentUnknownField, CurrentValue: "1", CurrentPresent: true,
				ProposedValue: "2", ProposedPresent: true,
			}},
		},
		{
			ComposePath: testServicePath,
			Changes:     []DeploymentFieldChange{cpuChange, cpuChange},
		},
		{
			ComposePath: testServicePath,
			Changes: []DeploymentFieldChange{
				deploymentChange(application.DeploymentMemory, "1", "2"), cpuChange,
			},
		},
		{ComposePath: testServicePath, Changes: []DeploymentFieldChange{{
			FieldID: application.DeploymentCPUs.ID(), CurrentValue: "1", CurrentPresent: true,
			ProposedValue: "invalid", ProposedPresent: true,
		}}},
		{ComposePath: testServicePath, Changes: []DeploymentFieldChange{{
			FieldID: application.DeploymentCPUs.ID(), CurrentValue: "1", CurrentPresent: true,
			ProposedValue: "1", ProposedPresent: true,
		}}},
		{ComposePath: testServicePath, Changes: []DeploymentFieldChange{{
			FieldID: application.DeploymentCPUs.ID(), CurrentValue: "1", CurrentPresent: false,
			ProposedValue: "2", ProposedPresent: true,
		}}},
		{ComposePath: testServicePath, Changes: []DeploymentFieldChange{{
			FieldID: application.DeploymentCPUs.ID(), CurrentValue: "", CurrentPresent: true,
			ProposedValue: "2", ProposedPresent: true,
		}}},
		{ComposePath: testServicePath, Restore: "bad"},
		{
			ComposePath: testServicePath, Changes: []DeploymentFieldChange{cpuChange},
			Diff: string([]byte{0xff}),
		},
	}
	for _, preview := range previewCases {
		if _, err := canonicalDeploymentPreview(preview); err == nil {
			t.Fatalf("canonicalDeploymentPreview(%#v) succeeded", preview)
		}
	}

	if _, err := canonicalStagedDeployment(fixture.staged); err != nil {
		t.Fatalf("canonicalStagedDeployment(valid) error = %v", err)
	}
	if _, err := canonicalStagedDeployment(StagedDeploymentEdit{}); err == nil {
		t.Fatal("canonicalStagedDeployment(empty) succeeded")
	}

	validHistory := fixture.history
	if history, err := canonicalDeploymentHistory(validHistory); err != nil || len(history) != len(validHistory) {
		t.Fatalf("canonicalDeploymentHistory(valid) = %#v, %v", history, err)
	}
	historyCases := [][]DeploymentHistoryEntry{
		nil,
		make([]DeploymentHistoryEntry, 101),
		{{Revision: "bad", Subject: "subject"}},
		{validHistory[0], validHistory[0]},
		{{Revision: validRevisionValue}},
		{{Revision: validRevisionValue, Subject: "bad\nsubject"}},
	}
	for _, history := range historyCases {
		if _, err := canonicalDeploymentHistory(history); err == nil {
			t.Fatalf("canonicalDeploymentHistory(%#v) succeeded", history)
		}
	}
	for _, revision := range []string{
		"", strings.Repeat("a", 39), strings.Repeat("a", 41), strings.Repeat("A", 40),
		strings.Repeat("g", 64),
	} {
		if validRevision(revision) {
			t.Fatalf("validRevision(%q) = true", revision)
		}
	}
	if !validRevision(validRevisionValue) || !validRevision(strings.Repeat("b", 64)) {
		t.Fatal("valid revisions were rejected")
	}
}

func replaceDeploymentField(
	fields []DeploymentFieldState,
	index int,
	replacement DeploymentFieldState,
) []DeploymentFieldState {
	result := slices.Clone(fields)
	result[index] = replacement

	return result
}

//nolint:cyclop,funlen,gocognit,gocyclo // This table covers the closed rendering catalog and page projections.
func TestDeploymentRendererCoversPagesCatalogAndNavigationChrome(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.resize(100, 24)
	fixture := newDeploymentWorkflowFixture()
	review := deploymentReviewPage()
	fields := deploymentFieldsPage{review: review, fields: fixture.fields, cursor: len(fixture.fields) - 1}
	value := deploymentValuePage{fields: fields, field: fixture.fields[0]}
	preview := deploymentPreviewPage{review: review, previous: fields, preview: fixture.preview}
	restorePreview := deploymentPreviewPage{review: review, previous: fields, preview: fixture.restore}
	history := deploymentHistoryPage{review: review, history: fixture.history, cursor: 1}
	pages := []page{
		fields,
		value,
		deploymentValuePage{fields: fields, field: fixture.fields[0], value: testDeploymentCPUInput},
		preview,
		restorePreview,
		deploymentDetailsPage{preview: preview, scroll: 1},
		stageDeploymentConfirmationPage{preview: preview, focus: confirmationBack},
		stageDeploymentConfirmationPage{preview: preview, focus: confirmationApply},
		deploymentDiffPage{confirmation: stageDeploymentConfirmationPage{preview: preview}},
		deploymentDraftConfirmationPage{previous: preview},
		deploymentDraftConfirmationPage{previous: preview, focus: confirmationApply, quit: true},
		history,
		restoreDeploymentConfirmationPage{history: history, entry: fixture.history[1], focus: confirmationBack},
		restoreDeploymentConfirmationPage{history: history, entry: fixture.history[1], focus: confirmationApply},
	}
	for _, current := range pages {
		state.page = current
		body, valid := state.deploymentPageBody(80)
		if !valid || len(body) == 0 || state.locationLine() == "" || len(state.rail()) == 0 || state.footer(80) == "" {
			t.Fatalf("deployment page %T did not render safely", current)
		}
		if hardFloor := state.hardFloorView(80); len(hardFloor) == 0 {
			t.Fatalf("deployment hard floor %T was empty", current)
		}
		if auxiliary, valid := state.auxiliaryPageBody(80, 24); !valid || len(auxiliary) == 0 {
			t.Fatalf("auxiliary deployment page %T = %#v, %t", current, auxiliary, valid)
		}
	}
	state.page = homePage{}
	if _, valid := state.deploymentPageBody(80); valid {
		t.Fatal("home page was treated as a deployment page")
	}
	if _, valid := state.deploymentWorkspaceLocation(); valid || state.deploymentCommitPage() {
		t.Fatal("home page was treated as deployment chrome")
	}
	if _, valid := state.deploymentWorkspaceRail(); valid {
		t.Fatal("home page received deployment rail")
	}
	if _, valid := state.deploymentWorkspaceFooter(); valid {
		t.Fatal("home page received deployment footer")
	}
	if lines := state.deploymentDiffBody(
		deploymentDiffPage{confirmation: stageDeploymentConfirmationPage{preview: preview}}, 80, 1,
	); len(lines) != 1 {
		t.Fatalf("deployment diff lead-only window = %q", lines)
	}
	if lines := state.deploymentDiffBody(
		deploymentDiffPage{confirmation: stageDeploymentConfirmationPage{preview: preview}, scroll: 4}, 80, 1,
	); len(lines) != 1 {
		t.Fatalf("deployment diff body-only window = %q", lines)
	}

	for _, field := range fixture.fields {
		if deploymentFieldLabel(field.ID) == "" || deploymentFieldPlaceholder(field.ID) == "" ||
			deploymentFieldHint(field.ID) == "" {
			t.Fatalf("field rendering metadata missing for %q", field.ID)
		}
	}
	if deploymentFieldLabel(testDeploymentUnknownField) != testDeploymentUnknownField ||
		deploymentFieldPlaceholder(testDeploymentUnknownField) != testDeploymentDefaultDuration ||
		deploymentFieldHint(testDeploymentUnknownField) == "" {
		t.Fatal("unknown field rendering fallback changed")
	}

	fieldVariants := slices.Clone(fixture.fields)
	fieldVariants[6].Present = true
	fieldVariants[6].Value = testDeploymentTrueValue
	fieldVariants[7].Available = false
	state.height = 12
	if body := state.deploymentFieldsBody(
		deploymentFieldsPage{fields: fieldVariants}, 80,
	); len(body) == 0 {
		t.Fatal("deployment fields at the first row were empty")
	}
	fieldBody := strings.Join(state.deploymentFieldsBody(
		deploymentFieldsPage{fields: fieldVariants, cursor: len(fieldVariants) / 2}, 80,
	), "\n")
	if !strings.Contains(fieldBody, "earlier field") || !strings.Contains(fieldBody, "later field") ||
		!strings.Contains(fieldBody, "Unavailable") {
		t.Fatalf("bounded field body = %q", fieldBody)
	}
	dirtyFields := fields
	dirtyFields.draft = deploymentValueDraft{
		fieldID: fields.fields[0].ID, initial: "1", value: testDeploymentCPUInput,
	}
	if body := strings.Join(state.deploymentFieldsBody(dirtyFields, 80), "\n"); !strings.Contains(body, "Unsaved") {
		t.Fatalf("dirty fields body = %q", body)
	}
	if start, end := visibleSelection(2, 0, 4); start != 0 || end != 2 {
		t.Fatalf("visibleSelection(short) = %d, %d", start, end)
	}
	if body := state.deploymentValueBody(deploymentValuePage{
		field: DeploymentFieldState{ID: application.DeploymentCPUs.ID()},
	}, 80); len(body) == 0 {
		t.Fatal("deployment value without a current value was empty")
	}

	longHistory := make([]DeploymentHistoryEntry, 20)
	for index := range longHistory {
		longHistory[index] = DeploymentHistoryEntry{
			Revision:         strings.Repeat(string(rune('a'+index%6)), 40),
			Subject:          "Revision",
			SignaturePresent: index%2 == 0,
		}
	}
	historyBody := strings.Join(state.deploymentHistoryBody(
		deploymentHistoryPage{history: longHistory, cursor: 10}, 80,
	), "\n")
	if !strings.Contains(historyBody, "newer revision") || !strings.Contains(historyBody, "older revision") ||
		!strings.Contains(historyBody, "signature present") || !strings.Contains(historyBody, "no signature") {
		t.Fatalf("bounded history body = %q", historyBody)
	}

	commit := commitPage{kind: commitKindDeployment, deployment: preview}
	commitPages := []page{
		commit,
		stagedDiffPage{commit: commit},
		unsignedCommitConfirmationPage{commit: commit},
	}
	for _, current := range commitPages {
		state.page = current
		if !state.deploymentCommitPage() || state.locationLine() == "" || len(state.rail()) == 0 || state.footer(80) == "" {
			t.Fatalf("deployment commit page %T did not receive deployment chrome", current)
		}
	}
	state.page = commitPage{kind: commitKindService}
	if state.deploymentCommitPage() {
		t.Fatal("service commit page was treated as deployment commit")
	}
}

func TestDeploymentResultProjectionErrorsRemainContained(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := deploymentReviewPage()
	fixture := newDeploymentWorkflowFixture()
	state.sequence = 10

	if command := state.handleDeploymentFieldsResult(deploymentFieldsResultMsg{
		sequence: 9, review: review, fields: fixture.fields,
	}); command != nil {
		t.Fatal("stale fields result returned command")
	}
	state.sequence++
	state.handleDeploymentFieldsResult(deploymentFieldsResultMsg{
		sequence: state.sequence, review: review, fields: fixture.fields[:1],
	})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Deployment fields could not be displayed safely" {
		t.Fatalf("invalid fields result = %v, %q", state.err, state.status)
	}

	state.sequence++
	state.handleDeploymentPreviewResult(deploymentPreviewResultMsg{
		sequence: state.sequence, review: review, previous: review, preview: DeploymentEditPreview{},
	})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Deployment preview could not be displayed safely" {
		t.Fatalf("invalid preview result = %v, %q", state.err, state.status)
	}

	state.sequence++
	state.handleDeploymentStageResult(deploymentStageResultMsg{
		sequence: state.sequence, preview: deploymentPreviewPage{review: review}, staged: StagedDeploymentEdit{},
	})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Staged deployment edit could not be displayed safely" {
		t.Fatalf("invalid stage result = %v, %q", state.err, state.status)
	}

	state.sequence++
	state.handleDeploymentHistoryResult(deploymentHistoryResultMsg{sequence: state.sequence, review: review})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Deployment history could not be displayed safely" {
		t.Fatalf("invalid history result = %v, %q", state.err, state.status)
	}
}

//nolint:cyclop,funlen // Each stable failure and blocked mutating route is an independent recovery contract.
func TestDeploymentTypedFailuresRemainRecoverableAndBoundUnknownWorktree(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.resize(100, 24)
	state.deployments = newDeploymentWorkflowFixture()
	review := deploymentReviewPage()
	for _, test := range []struct {
		code DeploymentFailure
		want string
	}{
		{DeploymentPreconditionFailed, "Deployment source changed; reload it before editing"},
		{DeploymentUnsupportedSource, "This Compose source cannot be edited safely"},
		{DeploymentValidationFailed, "Deployment value failed local validation; edit it before retrying"},
		{DeploymentPublishFailed, "Staging failed; the original Compose file was restored. Reload before retrying"},
		{DeploymentWorktreeUnknown, deploymentWorktreeUnknownStatus},
		{DeploymentCommitFailed, "Commit failed; the staged deployment edit is unchanged and can be retried"},
		{DeploymentHistoryUnavailable, "Deployment history could not be read; the worktree is unchanged"},
		{DeploymentHistoryEntryInvalid, "That history entry can no longer be restored; reload history"},
		{DeploymentFailure("unknown"), "Deployment source changed; reload it before editing"},
	} {
		state.deploymentFailure = ""
		state.sequence++
		state.busy = true
		state.handleDeploymentFieldsResult(deploymentFieldsResultMsg{
			sequence: state.sequence, review: review, err: &DeploymentActionError{Code: test.code},
		})
		if state.err != nil || state.status != test.want || state.deploymentFailure == "" {
			t.Fatalf("deployment failure %q = %v, %q, %q", test.code, state.err, state.status, state.deploymentFailure)
		}
	}
	fields := deploymentFieldsPage{review: review}
	state.deploymentFailure = DeploymentValidationFailed
	state.handleDeploymentFieldsKey(fields, keyEscape)
	if _, valid := state.page.(reviewPage); !valid || state.deploymentFailure != "" {
		t.Fatalf("leaving recoverable deployment failure = %T, %q", state.page, state.deploymentFailure)
	}
	state.deploymentFailure = DeploymentValidationFailed
	state.handleDeploymentValueKey(deploymentValuePage{
		fields: fields, value: "1",
	}, key("2"))
	if state.deploymentFailure != "" {
		t.Fatalf("editing deployment value retained failure = %q", state.deploymentFailure)
	}

	state.deploymentFailure = DeploymentWorktreeUnknown
	preview := deploymentPreviewPage{review: review}
	history := deploymentHistoryPage{review: review}
	commit := commitPage{kind: commitKindDeployment, deployment: preview}
	for name, command := range map[string]tea.Cmd{
		"fields":          state.startDeploymentFields(review),
		"preview":         state.startDeploymentPreview(review, review, application.DeploymentCPUs.ID(), "2", false),
		stageCall:         state.startDeploymentStage(preview),
		"commit":          state.startDeploymentCommit(commit, false),
		"discard":         state.startDeploymentDiscard(review, false, true),
		"history":         state.startDeploymentHistory(review),
		"restore":         state.startDeploymentRestore(history, strings.Repeat("a", 40)),
		"service-stage":   state.startServiceStage(servicePreviewPage{}),
		"service-suspend": state.startServiceSuspend(servicePreviewPage{}),
		"service-commit":  state.startCommit(commitPage{kind: commitKindService}, false),
		applyCall:         state.startApply(review),
	} {
		if command != nil {
			t.Fatalf("manual-rescue %s returned a command", name)
		}
	}
	if view := state.View().Content; !strings.Contains(view, "Action did not finish") ||
		!strings.Contains(view, "run git status") {
		t.Fatalf("manual-rescue view = %q", view)
	}
	state.clearRecoverableDeploymentFailure()
	if state.deploymentFailure != DeploymentWorktreeUnknown {
		t.Fatalf("navigation cleared manual-rescue failure = %q", state.deploymentFailure)
	}

	state.sequence++
	state.busy = true
	state.handleDeploymentFieldsResult(deploymentFieldsResultMsg{
		sequence: state.sequence, review: review, err: errors.Join(
			&DeploymentActionError{Code: DeploymentCommitFailed}, context.Canceled,
		),
	})
	if state.status != testLLMCancelled {
		t.Fatalf("cancelled deployment failure status = %q", state.status)
	}
	state.sequence++
	state.busy = true
	state.handleDeploymentFieldsResult(deploymentFieldsResultMsg{
		sequence: state.sequence, review: review, err: errors.Join(
			&DeploymentActionError{Code: DeploymentWorktreeUnknown}, context.Canceled,
		),
	})
	if state.status != deploymentWorktreeUnknownStatus {
		t.Fatalf("cancelled unknown worktree status = %q", state.status)
	}
	state.deploymentFailure = ""
	state.sequence++
	state.busy = true
	state.handleDeploymentFieldsResult(deploymentFieldsResultMsg{
		sequence: state.sequence, review: review, fields: newDeploymentWorkflowFixture().fields,
	})
	if state.deploymentFailure != "" {
		t.Fatalf("successful fresh result retained failure = %q", state.deploymentFailure)
	}

	var empty *DeploymentActionError
	if empty.Error() != "deployment action failed" ||
		(&DeploymentActionError{Code: DeploymentCommitFailed}).Error() != string(DeploymentCommitFailed) {
		t.Fatal("deployment action error did not preserve its stable code")
	}
}

//nolint:funlen,gocognit,cyclop,gocyclo,maintidx // This table drives every bounded deployment keyboard transition.
func TestDeploymentKeyboardCatalog(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.resize(100, 24)
	fixture := newDeploymentWorkflowFixture()
	state.deployments = fixture
	review := deploymentReviewPage()
	fields := deploymentFieldsPage{review: review, fields: fixture.fields}
	preview := deploymentPreviewPage{review: review, previous: fields, preview: fixture.preview}
	history := deploymentHistoryPage{review: review, history: fixture.history, cursor: 1}

	deploymentFieldsPage{}.isPage()
	deploymentValuePage{}.isPage()
	deploymentPreviewPage{}.isPage()
	deploymentDetailsPage{}.isPage()
	stageDeploymentConfirmationPage{}.isPage()
	deploymentDiffPage{}.isPage()
	deploymentDraftConfirmationPage{}.isPage()
	deploymentHistoryPage{}.isPage()
	restoreDeploymentConfirmationPage{}.isPage()

	state.page = fields
	for _, input := range []string{"up", "k"} {
		state.handleDeploymentFieldsKey(fields, input)
	}
	fields.cursor = len(fields.fields) - 1
	for _, input := range []string{keyDown, "j", keyTab, "?"} {
		state.handleDeploymentFieldsKey(fields, input)
	}
	if command := state.handleDeploymentFieldsKey(fields, keyEscape); command != nil {
		t.Fatal("fields escape was not handled")
	}
	unavailable := fields
	unavailable.fields = slices.Clone(fields.fields)
	unavailable.cursor = 0
	unavailable.fields[0].Available = false
	state.handleDeploymentFieldsKey(unavailable, keyEnter)
	if state.status != "This field is unavailable for the selected service" {
		t.Fatalf("unavailable field status = %q", state.status)
	}
	state.handleDeploymentFieldsKey(fields, keyEnter)
	if _, valid := state.page.(deploymentValuePage); !valid {
		t.Fatalf("enter field page = %T", state.page)
	}
	dirtyFields := fields
	dirtyFields.cursor = 0
	dirtyFields.draft = deploymentValueDraft{
		fieldID: fields.fields[0].ID, initial: "", value: testDeploymentCPUInput,
	}
	state.handleDeploymentFieldsKey(dirtyFields, keyEnter)
	if current, valid := state.page.(deploymentValuePage); !valid || current.value != testDeploymentCPUInput {
		t.Fatalf("restored field draft = %#v", state.page)
	}
	state.handleDeploymentFieldsKey(dirtyFields, "u")
	if state.status != "Discard the unsaved value before removing a field" {
		t.Fatalf("remove with dirty field status = %q", state.status)
	}
	if command := state.handleDeploymentFieldsKey(dirtyFields, keyQuit); command != nil {
		t.Fatal("dirty fields quit bypassed discard confirmation")
	}
	noNewPrivileges := fields
	noNewPrivileges.cursor = 8
	state.handleDeploymentFieldsKey(noNewPrivileges, keyEnter)
	if current, valid := state.page.(deploymentValuePage); !valid || current.value != testDeploymentTrueValue {
		t.Fatalf("no-new-privileges field page = %#v", state.page)
	}
	unsettable := fields
	unsettable.fields = slices.Clone(fields.fields)
	unsettable.cursor = 0
	unsettable.fields[0].Present = true
	state.busy = false
	if command := state.handleDeploymentFieldsKey(unsettable, "u"); command == nil {
		t.Fatal("unset field did not begin preview")
	}
	state.busy = false
	state.handleDeploymentFieldsKey(fields, "u")
	if state.status != "This field cannot be removed" {
		t.Fatalf("absent field status = %q", state.status)
	}
	locked := fields
	locked.cursor = 8
	state.handleDeploymentFieldsKey(locked, "u")
	if state.status != "This field cannot be removed" {
		t.Fatalf("locked field status = %q", state.status)
	}
	if state.handleDeploymentFieldsKey(fields, keyQuit) == nil {
		t.Fatal("fields quit did not return a command")
	}

	value := deploymentValuePage{fields: fields, field: fields.fields[0]}
	for _, input := range []tea.KeyPressMsg{key("2"), key("x"), key("backspace")} {
		state.handleDeploymentValueKey(value, input)
	}
	if command := state.handleDeploymentValueKey(value, key(keyEscape)); command != nil {
		t.Fatal("value escape was not handled")
	}
	state.handleDeploymentValueKey(value, key(keyEnter))
	if state.status != "Enter a deployment value" {
		t.Fatalf("empty value status = %q", state.status)
	}
	value.value = "2"
	state.busy = false
	if command := state.handleDeploymentValueKey(value, key(keyEnter)); command == nil {
		t.Fatal("value enter did not begin preview")
	}
	state.handleDeploymentValueKey(value, key(keyQuit))
	currentValue := mustLLMPage[deploymentValuePage](state.page)
	state.handleDeploymentValueKey(currentValue, key("c"))
	if current, valid := state.page.(deploymentValuePage); !valid || current.value != "2qc" {
		t.Fatalf("value letter input = %#v", state.page)
	}

	deliver(t, state, state.handleDeploymentPreviewKey(preview, keyEscape))
	state.handleDeploymentPreviewKey(preview, "up")
	if current, valid := state.page.(deploymentPreviewPage); !valid || current.scroll != 0 {
		t.Fatalf("preview up state = %#v", state.page)
	}
	state.handleDeploymentPreviewKey(preview, "k")
	if current, valid := state.page.(deploymentPreviewPage); !valid || current.scroll != 0 {
		t.Fatalf("preview k state = %#v", state.page)
	}
	state.handleDeploymentPreviewKey(preview, keyDown)
	if current, valid := state.page.(deploymentPreviewPage); !valid || current.scroll != 0 {
		t.Fatalf("preview down state = %#v", state.page)
	}
	state.handleDeploymentPreviewKey(preview, "j")
	if current, valid := state.page.(deploymentPreviewPage); !valid || current.scroll != 0 {
		t.Fatalf("preview j state = %#v", state.page)
	}
	state.handleDeploymentPreviewKey(preview, "d")
	details, valid := state.page.(deploymentDetailsPage)
	if !valid || details.preview.preview.ComposePath != preview.preview.ComposePath {
		t.Fatalf("preview details state = %#v", state.page)
	}
	for _, input := range []string{"up", "k", keyDown, "j", "?"} {
		state.handleDeploymentDetailsKey(details, input)
	}
	state.handleDeploymentDetailsKey(details, "d")
	if _, valid := state.page.(deploymentPreviewPage); !valid {
		t.Fatalf("details d returned to %T", state.page)
	}
	state.handleDeploymentDetailsKey(details, keyEscape)
	if _, valid := state.page.(deploymentPreviewPage); !valid {
		t.Fatalf("details escape returned to %T", state.page)
	}
	if command := state.handleDeploymentDetailsKey(details, keyQuit); command != nil {
		t.Fatal("details quit bypassed discard confirmation")
	}
	if discard, valid := state.page.(deploymentDraftConfirmationPage); !valid || !discard.quit {
		t.Fatalf("details discard confirmation = %#v", state.page)
	}
	if command := state.handleDeploymentPreviewKey(preview, keyEnter); command != nil {
		t.Fatal("preview enter did not open confirmation")
	}
	state.handleDeploymentPreviewKey(preview, "?")
	if state.handleDeploymentPreviewKey(preview, keyQuit) != nil {
		t.Fatal("preview quit bypassed discard confirmation")
	}
	if discard, valid := state.page.(deploymentDraftConfirmationPage); !valid || !discard.quit {
		t.Fatalf("preview discard confirmation = %#v", state.page)
	}
	state.resize(1, 1)
	state.handleDeploymentPreviewKey(preview, keyEnter)
	if state.status != "Resize to review the file mutation" {
		t.Fatalf("small preview status = %q", state.status)
	}
	state.resize(100, 24)

	for _, input := range []string{keyTab, keyLeft, keyRight, keyShiftTab} {
		state.handleDeploymentStageConfirmationKey(stageDeploymentConfirmationPage{preview: preview}, input)
	}
	for _, input := range []string{keyEscape, keyEnter} {
		state.handleDeploymentStageConfirmationKey(stageDeploymentConfirmationPage{preview: preview}, input)
	}
	state.busy = false
	if command := state.handleDeploymentStageConfirmationKey(
		stageDeploymentConfirmationPage{preview: preview, focus: confirmationApply}, keyEnter,
	); command == nil {
		t.Fatal("stage confirmation did not begin stage")
	}
	state.handleDeploymentStageConfirmationKey(stageDeploymentConfirmationPage{preview: preview}, "?")
	if state.handleDeploymentStageConfirmationKey(
		stageDeploymentConfirmationPage{preview: preview}, keyQuit,
	) != nil {
		t.Fatal("stage confirmation quit bypassed discard confirmation")
	}
	stageConfirmation := stageDeploymentConfirmationPage{preview: preview}
	state.handleDeploymentStageConfirmationKey(stageConfirmation, "d")
	diffPage, valid := state.page.(deploymentDiffPage)
	if !valid || diffPage.confirmation.preview.preview.Diff != fixture.preview.Diff {
		t.Fatalf("deployment diff page = %#v", state.page)
	}
	state.handleDeploymentDiffKey(diffPage, keyDown)
	diffPage = mustLLMPage[deploymentDiffPage](state.page)
	if diffPage.scroll != 0 {
		t.Fatalf("deployment diff scroll = %d", diffPage.scroll)
	}
	state.handleDeploymentDiffKey(diffPage, "d")
	if current, valid := state.page.(stageDeploymentConfirmationPage); !valid ||
		current.preview.preview.Diff != fixture.preview.Diff || current.focus != stageConfirmation.focus {
		t.Fatalf("deployment diff return = %#v", state.page)
	}
	diffPage.scroll = 0
	for _, input := range []string{"up", "k", "j", "?", keyEscape} {
		state.handleDeploymentDiffKey(diffPage, input)
	}
	if state.handleDeploymentDiffKey(diffPage, keyQuit) != nil {
		t.Fatal("deployment diff quit bypassed discard confirmation")
	}
	state.resize(1, 1)
	state.handleDeploymentStageConfirmationKey(stageDeploymentConfirmationPage{preview: preview}, keyEnter)
	state.resize(100, 24)

	discard := deploymentDraftConfirmationPage{previous: fields, destination: review}
	state.resize(1, 1)
	state.handleDeploymentDraftConfirmationKey(discard, keyEnter)
	if _, valid := state.page.(deploymentFieldsPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("small deployment discard confirmation = %T, %q", state.page, state.status)
	}
	state.resize(100, 24)
	for _, input := range []string{keyTab, keyLeft, keyRight, keyShiftTab, "?"} {
		state.handleDeploymentDraftConfirmationKey(discard, input)
	}
	state.handleDeploymentDraftConfirmationKey(discard, keyEnter)
	state.handleDeploymentDraftConfirmationKey(discard, keyEscape)
	discard.focus = confirmationApply
	state.busy = false
	if command := state.handleDeploymentDraftConfirmationKey(discard, keyEnter); command == nil {
		t.Fatal("deployment discard confirmation did not begin discard")
	}

	for _, input := range []string{"up", "k"} {
		state.handleDeploymentHistoryKey(history, input)
	}
	for _, input := range []string{keyDown, "j", keyTab} {
		state.handleDeploymentHistoryKey(history, input)
	}
	state.handleDeploymentHistoryKey(history, keyEscape)
	currentHistory := history
	currentHistory.cursor = 0
	state.handleDeploymentHistoryKey(currentHistory, keyEnter)
	if state.status != "This revision is already current" {
		t.Fatalf("current history status = %q", state.status)
	}
	if command := state.handleDeploymentHistoryKey(history, keyEnter); command != nil {
		t.Fatal("history enter returned unexpected command")
	}
	state.handleDeploymentHistoryKey(history, "?")
	if state.handleDeploymentHistoryKey(history, keyQuit) == nil {
		t.Fatal("history quit did not return a command")
	}

	confirmation := restoreDeploymentConfirmationPage{history: history, entry: history.history[1]}
	for _, input := range []string{keyTab, keyLeft, keyRight, keyShiftTab} {
		state.handleRestoreDeploymentConfirmationKey(confirmation, input)
	}
	for _, input := range []string{keyEscape, keyEnter} {
		state.handleRestoreDeploymentConfirmationKey(confirmation, input)
	}
	confirmation.focus = confirmationApply
	state.busy = false
	if command := state.handleRestoreDeploymentConfirmationKey(confirmation, keyEnter); command == nil {
		t.Fatal("restore confirmation did not begin restore")
	}
	state.handleRestoreDeploymentConfirmationKey(confirmation, "?")
	if state.handleRestoreDeploymentConfirmationKey(confirmation, keyQuit) == nil {
		t.Fatal("restore confirmation quit did not return a command")
	}
	state.resize(1, 1)
	state.handleRestoreDeploymentConfirmationKey(confirmation, keyEnter)
	state.resize(100, 24)

	state.page = stageDeploymentConfirmationPage{preview: preview}
	state.invalidateConfirmation()
	state.page = deploymentDiffPage{confirmation: stageDeploymentConfirmationPage{preview: preview}}
	state.invalidateConfirmation()
	state.page = deploymentDraftConfirmationPage{previous: preview}
	state.invalidateConfirmation()
	state.page = confirmation
	state.invalidateConfirmation()
	state.busy = false
	if state.startCommitDiscard(commitPage{kind: commitKindDeployment, deployment: preview}) == nil {
		t.Fatal("deployment commit discard did not return a command")
	}

	for _, current := range []page{
		fields, value, preview, details, stageDeploymentConfirmationPage{}, deploymentDiffPage{},
		deploymentDraftConfirmationPage{}, history, confirmation,
	} {
		state.page = current
		if _, handled := state.handleDeploymentPageKey(key("?")); !handled {
			t.Fatalf("deployment page key was not handled for %T", current)
		}
	}
	state.page = homePage{}
	if _, handled := state.handleDeploymentPageKey(key("?")); handled {
		t.Fatal("home page was handled as deployment page")
	}
}

func TestDeploymentPreviewGroupsChangedValuesAndKeepsActionsFixedWhileScrolling(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.resize(100, 24)
	changes := []DeploymentFieldChange{
		deploymentChange(application.DeploymentCPUs, "1", "2.5"),
		{
			FieldID:       application.DeploymentMemory.ID(),
			ProposedValue: "1073741824", ProposedPresent: true,
		},
		deploymentChange(application.DeploymentRestart, "always", "unless-stopped"),
		deploymentChange(application.DeploymentReadOnly, "false", "true"),
	}
	page := deploymentPreviewPage{preview: DeploymentEditPreview{
		ComposePath: testServicePath, Changes: changes,
	}}
	body := strings.Join(state.deploymentPreviewBody(page, 80), "\n")
	for _, value := range []string{
		"RESOURCES", "LIFECYCLE", "HEALTH & SAFETY", "CURRENT", "PROPOSED",
		"CPU limit", "Compose default", "1073741824", "Restart policy", "Read-only root",
	} {
		if !strings.Contains(body, value) {
			t.Fatalf("deployment preview misses %q: %q", value, body)
		}
	}
	resources := strings.Index(body, "RESOURCES")
	lifecycle := strings.Index(body, "LIFECYCLE")
	safety := strings.Index(body, "HEALTH & SAFETY")
	if resources < 0 || resources >= lifecycle || lifecycle >= safety {
		t.Fatalf("deployment groups are out of order: %q", body)
	}

	allChanges := make([]DeploymentFieldChange, 0, len(application.DeploymentFields()))
	for _, field := range application.DeploymentFields() {
		allChanges = append(allChanges, DeploymentFieldChange{
			FieldID: field.ID(), CurrentValue: testCurrentImage, CurrentPresent: true,
			ProposedValue: testProposedImage, ProposedPresent: true,
		})
	}
	state.resize(compactMinimum, compactMinHeight)
	page.preview.Changes = allChanges
	page.scroll = 7
	scrolled := strings.Join(state.deploymentPreviewBody(page, compactMinimum), "\n")
	for _, fixed := range []string{deploymentReviewTitle, deploymentReviewReady, deploymentReviewAction} {
		if !strings.Contains(scrolled, fixed) {
			t.Fatalf("scrolled deployment preview lost %q: %q", fixed, scrolled)
		}
	}
}

func TestDeploymentPreviewScrollStopsAtTheLastComparisonWindow(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.resize(compactMinimum, compactMinHeight)
	changes := make([]DeploymentFieldChange, 0, len(application.DeploymentFields()))
	for _, field := range application.DeploymentFields() {
		changes = append(changes, deploymentChange(field, testCurrentImage, testProposedImage))
	}
	state.page = deploymentPreviewPage{preview: DeploymentEditPreview{
		ComposePath: testServicePath,
		Changes:     changes,
	}}
	for range len(changes) * 3 {
		current := mustLLMPage[deploymentPreviewPage](state.page)
		state.handleDeploymentPreviewKey(current, keyDown)
	}
	current := mustLLMPage[deploymentPreviewPage](state.page)
	lastScroll := current.scroll
	state.handleDeploymentPreviewKey(current, keyDown)
	current = mustLLMPage[deploymentPreviewPage](state.page)
	if lastScroll == 0 || current.scroll != lastScroll {
		t.Fatalf("deployment preview scroll advanced past its final window: %d to %d", lastScroll, current.scroll)
	}
}

func TestDeploymentDetailsPreserveFullCanonicalValues(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	longValue := strings.Repeat("registry.example.com/team/component-", 3)
	details := strings.Join(state.deploymentDetailsLines([]DeploymentFieldChange{{
		FieldID: application.DeploymentRestart.ID(), CurrentValue: longValue, CurrentPresent: true,
		ProposedValue: longValue + "next", ProposedPresent: true,
	}}, 160), "\n")
	if !strings.Contains(details, longValue) || strings.Contains(details, "…") {
		t.Fatalf("deployment details did not preserve full canonical values: %q", details)
	}
	empty := strings.Join(state.deploymentDetailsLines(nil, 80), "\n")
	if !strings.Contains(empty, "No deployment parameters differ") {
		t.Fatalf("empty deployment details = %q", empty)
	}
}

func TestDeploymentPreviewDescribesRestoreWithoutFieldChanges(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.height = 0
	emptyRestore := deploymentPreviewPage{preview: DeploymentEditPreview{
		ComposePath: testServicePath, Restore: strings.Repeat("a", 40),
	}}
	body := strings.Join(state.deploymentPreviewBody(emptyRestore, 80), "\n")
	if !strings.Contains(body, deploymentReviewEmpty) {
		t.Fatalf("empty restore preview = %q", body)
	}
}

//nolint:cyclop,funlen // One state-machine catalog covers every asynchronous deployment result boundary.
func TestDeploymentCommandsAndResultCatalog(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	fixture := newDeploymentWorkflowFixture()
	state.deployments = fixture
	review := deploymentReviewPage()
	fields := deploymentFieldsPage{review: review, fields: fixture.fields}
	preview := deploymentPreviewPage{review: review, previous: fields, preview: fixture.preview}
	state.page = review

	state.deployments = nil
	if command := state.startDeploymentFields(review); command != nil ||
		state.status != "Deployment editing is unavailable" {
		t.Fatal("missing deployment workspace was not contained")
	}
	if command := state.startDeploymentHistory(review); command != nil ||
		state.status != "Deployment history is unavailable" {
		t.Fatal("missing deployment history workspace was not contained")
	}
	state.deployments = fixture
	deliver(t, state, state.startDeploymentFields(review))
	deliver(t, state, state.startDeploymentPreview(
		review, fields, application.DeploymentCPUs.ID(), "2", false,
	))
	deliver(t, state, state.startDeploymentHistory(review))
	history := deploymentHistoryPage{review: review, history: fixture.history, cursor: 1}
	deliver(t, state, state.startDeploymentRestore(history, fixture.history[1].Revision))
	deliver(t, state, state.startDeploymentStage(preview))
	commit := commitPage{kind: commitKindDeployment, deployment: preview, message: "Tune deployment"}
	deliver(t, state, state.startDeploymentCommit(commit, true))
	deliver(t, state, state.startDeploymentDiscard(preview, false, true))

	state.sequence = 100
	staleMessages := []tea.Msg{
		deploymentPreviewResultMsg{sequence: 99}, deploymentStageResultMsg{sequence: 99},
		deploymentCommitResultMsg{sequence: 99}, deploymentDiscardResultMsg{sequence: 99},
		deploymentHistoryResultMsg{sequence: 99},
	}
	for _, message := range staleMessages {
		if command, _ := state.handleDeploymentWorkspaceMessage(message); command != nil {
			t.Fatalf("stale %T returned command", message)
		}
	}
	if command, handled := state.handleDeploymentWorkspaceMessage(struct{}{}); command != nil || handled {
		t.Fatal("unrelated message returned command")
	}

	state.sequence++
	state.handleDeploymentPreviewResult(deploymentPreviewResultMsg{
		sequence: state.sequence, review: review, previous: fields, err: errTestTUI,
	})
	if !errors.Is(state.err, errTestTUI) {
		t.Fatalf("preview error = %v, %T", state.err, state.page)
	}
	state.sequence++
	state.handleDeploymentPreviewResult(deploymentPreviewResultMsg{
		sequence: state.sequence, review: review, previous: fields,
		preview: DeploymentEditPreview{NoChanges: true},
	})
	if _, valid := state.page.(deploymentFieldsPage); !valid || state.status != "Already matches current source" {
		t.Fatalf("no-change preview = %T, %q", state.page, state.status)
	}
	state.sequence++
	state.handleDeploymentStageResult(deploymentStageResultMsg{
		sequence: state.sequence, preview: preview, err: errTestTUI,
	})
	if !errors.Is(state.err, errTestTUI) {
		t.Fatalf("stage error = %v", state.err)
	}
	state.sequence++
	state.handleDeploymentHistoryResult(deploymentHistoryResultMsg{
		sequence: state.sequence, review: review, err: errTestTUI,
	})
	if !errors.Is(state.err, errTestTUI) {
		t.Fatalf("history error = %v, %T", state.err, state.page)
	}

	state.sequence++
	state.handleDeploymentDiscardResult(deploymentDiscardResultMsg{
		sequence: state.sequence, destination: preview, err: errTestTUI,
	})
	if !errors.Is(state.err, errTestTUI) {
		t.Fatalf("discard error = %v", state.err)
	}
	state.sequence++
	state.handleDeploymentDiscardResult(deploymentDiscardResultMsg{
		sequence: state.sequence, destination: preview, staged: true,
	})
	if _, valid := state.page.(deploymentPreviewPage); !valid || state.status != "Staged deployment edit discarded" {
		t.Fatalf("discard success = %T, %q", state.page, state.status)
	}
	state.sequence++
	state.handleDeploymentDiscardResult(deploymentDiscardResultMsg{
		sequence: state.sequence, destination: review,
	})
	if _, valid := state.page.(reviewPage); !valid || state.status != statusDeploymentUnchanged {
		t.Fatalf("discard destination = %T, %q", state.page, state.status)
	}
	state.sequence++
	if command := state.handleDeploymentDiscardResult(deploymentDiscardResultMsg{
		sequence: state.sequence, quit: true,
	}); command == nil {
		t.Fatal("discard and quit returned no command")
	}

	staged := fixture.staged
	commit = commitPage{
		kind: commitKindDeployment,
		staged: StagedService{
			Diff: staged.Diff, ComposePath: staged.ComposePath, CommitMessage: staged.CommitMessage,
		},
		deployment: preview,
		message:    "Tune deployment",
	}
	commitCases := []struct {
		result CommitResult
		err    error
	}{
		{err: errTestTUI},
		{result: CommitResult{Outcome: CommitNeedsUnsignedApproval}},
		{result: CommitResult{Outcome: CommitValidationUnavailable}},
		{},
		{result: CommitResult{Outcome: CommitPreparationRequired}},
		{result: CommitResult{Outcome: CommitOutcome(255)}},
	}
	for _, test := range commitCases {
		state.sequence++
		state.handleDeploymentCommitResult(deploymentCommitResultMsg{
			sequence: state.sequence, commit: commit, result: test.result, err: test.err,
		})
	}
	state.sequence++
	state.handleDeploymentCommitResult(deploymentCommitResultMsg{
		sequence: state.sequence,
		commit:   commitPage{kind: commitKindService},
		result:   CommitResult{Outcome: CommitSucceeded},
	})
	if !errors.Is(state.err, errInvalidInput) {
		t.Fatalf("missing deployment commit metadata error = %v", state.err)
	}

	state.sequence++
	state.handleDeploymentFieldsResult(deploymentFieldsResultMsg{
		sequence: state.sequence, review: review, err: errTestTUI,
	})
	if !errors.Is(state.err, errTestTUI) {
		t.Fatalf("fields error = %v", state.err)
	}

	state.sequence++
	state.handleDeploymentStageResult(deploymentStageResultMsg{
		sequence: state.sequence, preview: preview, staged: staged,
	})
	if _, valid := state.page.(commitPage); !valid {
		t.Fatalf("stage success page = %T", state.page)
	}
}
