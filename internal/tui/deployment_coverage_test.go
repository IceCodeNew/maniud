package tui

import (
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
	validPreviews := []DeploymentEditPreview{
		{ComposePath: testServicePath, FieldIDs: []string{application.DeploymentCPUs.ID()}},
		{ComposePath: testServicePath, Restore: validRevisionValue},
	}
	for _, preview := range validPreviews {
		if _, err := canonicalDeploymentPreview(preview); err != nil {
			t.Fatalf("canonicalDeploymentPreview(%#v) error = %v", preview, err)
		}
	}
	previewCases := []DeploymentEditPreview{
		{},
		{ComposePath: "/absolute", FieldIDs: []string{application.DeploymentCPUs.ID()}},
		{ComposePath: testServicePath},
		{
			ComposePath: testServicePath, FieldIDs: []string{application.DeploymentCPUs.ID()},
			Restore: validRevisionValue,
		},
		{ComposePath: testServicePath, FieldIDs: []string{testDeploymentUnknownField}},
		{
			ComposePath: testServicePath,
			FieldIDs:    []string{application.DeploymentCPUs.ID(), application.DeploymentCPUs.ID()},
		},
		{ComposePath: testServicePath, Restore: "bad"},
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
		stageDeploymentConfirmationPage{preview: preview, focus: confirmationBack},
		stageDeploymentConfirmationPage{preview: preview, focus: confirmationApply},
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
		if auxiliary, valid := state.auxiliaryPageBody(80); !valid || len(auxiliary) == 0 {
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

	commit := commitServicePage{deployment: &preview}
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
	state.page = commitServicePage{}
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
	stageDeploymentConfirmationPage{}.isPage()
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
	if state.handleDeploymentValueKey(value, key(keyQuit)) == nil {
		t.Fatal("value quit did not return a command")
	}

	if command := state.handleDeploymentPreviewKey(preview, keyEscape); command != nil {
		t.Fatal("preview escape was not handled")
	}
	if command := state.handleDeploymentPreviewKey(preview, keyEnter); command != nil {
		t.Fatal("preview enter did not open confirmation")
	}
	state.handleDeploymentPreviewKey(preview, "?")
	if state.handleDeploymentPreviewKey(preview, keyQuit) == nil {
		t.Fatal("preview quit did not return a command")
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
	if state.handleDeploymentStageConfirmationKey(stageDeploymentConfirmationPage{preview: preview}, keyQuit) == nil {
		t.Fatal("stage confirmation quit did not return a command")
	}
	state.resize(1, 1)
	state.handleDeploymentStageConfirmationKey(stageDeploymentConfirmationPage{preview: preview}, keyEnter)
	state.resize(100, 24)

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
	state.page = confirmation
	state.invalidateConfirmation()
	state.busy = false
	if state.startCommitDiscard(commitServicePage{deployment: &preview}) == nil {
		t.Fatal("deployment commit discard did not return a command")
	}

	for _, current := range []page{fields, value, preview, stageDeploymentConfirmationPage{}, history, confirmation} {
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
	commit := commitServicePage{deployment: &preview, message: "Tune deployment"}
	deliver(t, state, state.startDeploymentCommit(commit, true))
	deliver(t, state, state.startDeploymentDiscard(preview))

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
		sequence: state.sequence, preview: preview, err: errTestTUI,
	})
	if !errors.Is(state.err, errTestTUI) {
		t.Fatalf("discard error = %v", state.err)
	}
	state.sequence++
	state.handleDeploymentDiscardResult(deploymentDiscardResultMsg{
		sequence: state.sequence, preview: preview,
	})
	if _, valid := state.page.(deploymentPreviewPage); !valid || state.status != "Staged deployment edit discarded" {
		t.Fatalf("discard success = %T, %q", state.page, state.status)
	}

	staged := fixture.staged
	commit = commitServicePage{
		staged: StagedService{
			Diff: staged.Diff, ComposePath: staged.ComposePath, CommitMessage: staged.CommitMessage,
		},
		deployment: &preview,
		message:    "Tune deployment",
	}
	commitCases := []struct {
		result DeploymentCommitResult
		err    error
	}{
		{err: errTestTUI},
		{result: DeploymentCommitResult{NeedsUnsignedApproval: true}},
		{result: DeploymentCommitResult{Committed: true, ValidationUnavailable: true}},
		{result: DeploymentCommitResult{}},
		{result: DeploymentCommitResult{Committed: true, NeedsUnsignedApproval: true}},
		{result: DeploymentCommitResult{ValidationUnavailable: true}},
	}
	for _, test := range commitCases {
		state.sequence++
		state.handleDeploymentCommitResult(deploymentCommitResultMsg{
			sequence: state.sequence, commit: commit, result: test.result, err: test.err,
		})
	}
	state.sequence++
	state.handleDeploymentCommitResult(deploymentCommitResultMsg{
		sequence: state.sequence, commit: commitServicePage{}, result: DeploymentCommitResult{Committed: true},
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
	if _, valid := state.page.(commitServicePage); !valid {
		t.Fatalf("stage success page = %T", state.page)
	}
}
