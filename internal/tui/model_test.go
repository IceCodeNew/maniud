//nolint:cyclop,funlen // These interaction tests intentionally verify complete message and viewport state matrices.
package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
)

//nolint:cyclop // This lifecycle test keeps the safety-critical confirmation and readback sequence contiguous.
func TestModelNavigatesHomeReviewConfirmationAndApply(t *testing.T) {
	t.Parallel()

	state, catalog, operations := newTestModel(t)
	deliver(t, state, state.startCatalog())
	if _, valid := state.page.(homePage); !valid || state.status != "Choose a service" {
		t.Fatalf("catalog state = %#v", state)
	}

	deliver(t, state, state.handleKey(key("enter")))
	review, valid := state.page.(reviewPage)
	if !valid || review.plan.status != statusReady || state.busy {
		t.Fatalf("review state = %#v", state)
	}
	if !slices.Equal(catalog.recordedCalls(), []string{snapshotCall, "registered:" + registeredAPIID}) ||
		!slices.Equal(operations.recordedCalls(), []string{snapshotCall, evidenceCall}) {
		t.Fatalf("calls = %q / %q", catalog.recordedCalls(), operations.recordedCalls())
	}

	for _, shortcut := range []string{"a", "e", "h"} {
		state.handleKey(key(shortcut))
	}
	if _, valid = state.page.(reviewPage); !valid || len(operations.recordedCalls()) != 2 {
		t.Fatal("removed review shortcut changed state")
	}
	state.handleKey(key("enter"))
	confirmation, valid := state.page.(confirmationPage)
	if !valid || confirmation.focus != confirmationBack {
		t.Fatalf("initial confirmation = %#v", state.page)
	}
	state.handleKey(key("enter"))
	if _, valid = state.page.(reviewPage); !valid || len(operations.recordedCalls()) != 2 {
		t.Fatal("default confirmation focus performed a mutation")
	}

	state.handleKey(key("enter"))
	state.handleKey(key("tab"))
	deliver(t, state, state.handleKey(key("enter")))
	if _, valid = state.page.(reviewPage); !valid || state.mutationOutcome != testApplyCompleted ||
		!slices.Equal(operations.recordedCalls(), []string{
			snapshotCall, evidenceCall, applyCall, snapshotCall, evidenceCall,
		}) {
		t.Fatalf("post-apply state = %#v, calls = %q", state, operations.recordedCalls())
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo // Keeps the complete chooser workflow together.
func TestReviewExploreOptionsAndDeterministicExplanation(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	review := reviewPage{
		request: application.Request{Service: testService},
		plan: planView{
			project: testProject, service: testService, runtime: testRuntime,
			kind: string(application.PlanUpgrade), current: testCurrentImage, proposed: testProposedImage,
			status: statusReady,
		},
	}
	state.page = review
	state.status = statusReady

	for _, navigationKey := range []string{"up", "k", keyDown, "j", keyTab, keyShiftTab} {
		state.page = review
		state.handleReviewKey(review, navigationKey)
		if focused := reviewPageValue(t, state); focused.focus != reviewExplore {
			t.Fatalf("review focus after %q = %d, want Explore options", navigationKey, focused.focus)
		}
	}
	state.page = review
	state.handleKey(key(keyTab))
	focused := reviewPageValue(t, state)
	if focused.focus != reviewExplore {
		t.Fatalf("review focus = %d, want Explore options", focused.focus)
	}
	state.handleKey(key(keyEnter))
	options, valid := state.page.(reviewOptionsPage)
	if !valid || options.cursor != 0 || !strings.Contains(state.View().Content, "Explain this change") {
		t.Fatalf("review options = %#v", state.page)
	}
	state.handleReviewOptionsKey(options, "k")
	wrapped, wrappedValid := state.page.(reviewOptionsPage)
	if !wrappedValid || wrapped.cursor != reviewHistoryOption {
		t.Fatalf("k-wrapped review option = %#v, want history", state.page)
	}
	for _, navigationKey := range []string{"j", keyTab} {
		state.handleReviewOptionsKey(options, navigationKey)
		next, nextValid := state.page.(reviewOptionsPage)
		if !nextValid || next.cursor != reviewLLMOption {
			t.Fatalf("review option after %q = %#v, want LLM", navigationKey, state.page)
		}
	}
	state.handleReviewOptionsKey(reviewOptionsPage{review: review, cursor: reviewOptionCount}, keyEnter)
	unmatched, unmatchedValid := state.page.(reviewOptionsPage)
	if !unmatchedValid || unmatched.cursor != reviewOptionCount {
		t.Fatalf("unmatched review option = %#v", state.page)
	}
	state.page = options
	state.handleKey(key("up"))
	options, valid = state.page.(reviewOptionsPage)
	if !valid || options.cursor != reviewHistoryOption {
		t.Fatalf("wrapped review option = %#v, want history", state.page)
	}
	state.handleKey(key(keyDown))
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("review options quit command is nil")
	}
	state.handleKey(key(keyEscape))
	if returned := reviewPageValue(t, state); returned.focus != reviewExplore || state.status != statusReady {
		t.Fatalf("returned review = %#v, status = %q", returned, state.status)
	}
	state.handleKey(key(keyTab))
	if returned := reviewPageValue(t, state); returned.focus != reviewContinue {
		t.Fatalf("review focus after second toggle = %d, want Continue", returned.focus)
	}
	state.handleKey(key("o"))
	state.handleKey(key(keyDown))
	state.handleKey(key(keyEnter))
	if _, valid = state.page.(reviewOptionsPage); !valid || state.status != "LLM assistance is unavailable" {
		t.Fatalf("unavailable LLM option = %T, status = %q", state.page, state.status)
	}
	state.handleKey(key(keyEscape))

	state.handleKey(key("o"))
	state.handleKey(key(testUnknownValue))
	state.handleKey(key(keyEnter))
	if _, valid = state.page.(explainPage); !valid ||
		!strings.Contains(state.View().Content, "derived this explanation") ||
		len(operations.recordedCalls()) != 0 {
		t.Fatalf("deterministic explanation = %T, calls = %q", state.page, operations.recordedCalls())
	}
	state.handleKey(key("a"))
	if _, valid = state.page.(explainPage); !valid || len(operations.recordedCalls()) != 0 {
		t.Fatalf("explanation accepted a removed shortcut: %T, calls = %q", state.page, operations.recordedCalls())
	}
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("explanation quit command is nil")
	}
	explanation, explanationValid := state.page.(explainPage)
	if !explanationValid {
		t.Fatalf("explanation page = %T", state.page)
	}
	deliver(t, state, state.handleExplainKey(explanation, keyEscape))
	if _, valid = state.page.(reviewPage); !valid ||
		!slices.Equal(operations.recordedCalls(), []string{snapshotCall, evidenceCall}) {
		t.Fatalf("fresh review after Escape = %T, calls = %q", state.page, operations.recordedCalls())
	}
	state.page = explainPage{review: review}
	deliver(t, state, state.handleKey(key(keyEnter)))
	if _, valid = state.page.(reviewPage); !valid ||
		!slices.Equal(operations.recordedCalls(), []string{
			snapshotCall, evidenceCall, snapshotCall, evidenceCall,
		}) {
		t.Fatalf("fresh review = %T, calls = %q", state.page, operations.recordedCalls())
	}
}

func TestModelOpensPathAndSelectsService(t *testing.T) {
	t.Parallel()

	state, catalog, operations := newTestModel(t)
	catalog.pathResult = OpenResult{Targets: []Target{
		{Project: testProject, Service: testAPI, Runtime: testRuntime,
			Request: application.Request{Service: testAPI}},
		{Project: testProject, Service: testWorker, Runtime: testRuntime,
			Request: application.Request{Service: testWorker}},
	}}
	state.page = openPathPage{}
	for _, character := range []string{"c", "o", "m", "p", "o", "s", "e", ".", "y", "m", "l"} {
		state.handleKey(key(character))
	}
	state.handleKey(key("backspace"))
	state.handleKey(key("l"))
	state.handleKey(tea.KeyPressMsg(tea.Key{Text: "\n", Code: '\n'}))
	path := openPathPageValue(t, state).value
	if path != "compose.yml" {
		t.Fatalf("path input = %q", path)
	}

	deliver(t, state, state.handleKey(key("enter")))
	selection, valid := state.page.(selectServicePage)
	if !valid || len(selection.choices) != 2 || selection.cursor != 0 {
		t.Fatalf("service selection = %#v", state.page)
	}
	state.handleKey(key("up"))
	state.handleKey(key("down"))
	state.handleKey(key("j"))
	deliver(t, state, state.handleKey(key("enter")))
	review := reviewPageValue(t, state)
	if review.request.Service != testWorker || !slices.Contains(catalog.recordedCalls(), "path:compose.yml") ||
		!slices.Equal(operations.recordedCalls(), []string{snapshotCall, evidenceCall}) {
		t.Fatalf("selected review = %#v, calls %q / %q", review, catalog.recordedCalls(), operations.recordedCalls())
	}
}

//nolint:cyclop,funlen // The Add service confirmations and post-commit validation must stay in order.
func TestModelAddsServiceWithExplicitUnsignedFallback(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	workspace.commit = CommitResult{Outcome: CommitNeedsUnsignedApproval}
	deliver(t, state, state.startCatalog())
	state.handleKey(key("down"))
	state.handleKey(key("enter"))
	if _, valid := state.page.(addServicePage); !valid {
		t.Fatalf("add service page = %T", state.page)
	}

	const input = "docker://registry.example/api@sha256:aaaaaaaa"
	state.handleKey(key(input))
	deliver(t, state, state.handleKey(key("enter")))
	preview, valid := state.page.(servicePreviewPage)
	if !valid || preview.input != input || preview.draft.Service != testAPI ||
		!slices.Equal(workspace.recordedCalls(), []string{"preview:" + input}) {
		t.Fatalf("preview = %#v, calls = %q", state.page, workspace.recordedCalls())
	}

	state.handleKey(key("enter"))
	confirmation, valid := state.page.(stageServiceConfirmationPage)
	if !valid || confirmation.focus != confirmationBack {
		t.Fatalf("stage confirmation = %#v", state.page)
	}
	state.handleKey(key("enter"))
	if _, valid = state.page.(servicePreviewPage); !valid || len(workspace.recordedCalls()) != 1 {
		t.Fatal("default stage confirmation performed an effect")
	}
	state.handleKey(key("enter"))
	state.handleKey(key("tab"))
	deliver(t, state, state.handleKey(key("enter")))
	commit, valid := state.page.(commitPage)
	if !valid || commit.focus != confirmationBack || commit.message != "Add api service" ||
		commit.diffWidth != state.commitDiffWidth() || len(commit.diffLines) == 0 {
		t.Fatalf("commit review = %#v", state.page)
	}
	fullWidth := commit.diffWidth
	deliver(t, state, state.resize(compactMinimum, compactMinHeight))
	commit = commitPageValue(t, state)
	if commit.diffWidth != state.commitDiffWidth() || commit.diffWidth == fullWidth || len(commit.diffLines) == 0 {
		t.Fatalf("resized commit diff = %#v", commit)
	}
	deliver(t, state, state.resize(defaultWidth, defaultHeight))

	state.handleKey(key("e"))
	state.handleKey(key("!"))
	state.handleKey(key("enter"))
	state.handleKey(key("d"))
	state.handleKey(key("down"))
	state.handleKey(key("d"))
	commit = commitPageValue(t, state)
	if commit.message != "Add api service!" || commit.editing {
		t.Fatalf("edited commit = %#v", commit)
	}
	state.handleKey(key("tab"))
	deliver(t, state, state.handleKey(key("enter")))
	unsigned, valid := state.page.(unsignedCommitConfirmationPage)
	if !valid || unsigned.focus != confirmationBack {
		t.Fatalf("unsigned confirmation = %#v", state.page)
	}
	deliver(t, state, state.resize(compactMinimum, compactMinHeight))
	unsigned, valid = state.page.(unsignedCommitConfirmationPage)
	if !valid || unsigned.commit.diffWidth != state.commitDiffWidth() {
		t.Fatalf("resized unsigned confirmation = %#v", state.page)
	}
	deliver(t, state, state.resize(defaultWidth, defaultHeight))
	state.handleKey(key("enter"))
	if _, valid = state.page.(commitPage); !valid {
		t.Fatalf("unsigned default back page = %T", state.page)
	}

	deliver(t, state, state.handleKey(key("enter")))
	workspace.commit = CommitResult{
		Request: application.Request{Service: testAPI}, Outcome: CommitSucceeded,
	}
	state.handleKey(key("tab"))
	deliver(t, state, state.handleKey(key("enter")))
	if _, valid = state.page.(reviewPage); !valid || state.mutationOutcome != "Compose commit created" {
		t.Fatalf("validated commit state = %#v", state)
	}
	if !slices.Equal(workspace.recordedCalls(), []string{
		"preview:" + input,
		stageCall,
		"commit:false:Add api service!",
		"commit:false:Add api service!",
		"commit:true:Add api service!",
	}) || !slices.Equal(operations.recordedCalls(), []string{dryRunCall, snapshotCall, evidenceCall}) {
		t.Fatalf("calls = %q / %q", workspace.recordedCalls(), operations.recordedCalls())
	}
}

func TestModelCachesServiceDiffAtDefaultDimensions(t *testing.T) {
	t.Parallel()

	state := &model{}
	if width := state.bodyWidth(); width != defaultWidth-fullBodyOffset {
		t.Fatalf("default body width = %d", width)
	}
	commit := state.wrapCommitDiff(commitPage{
		kind: commitKindService, staged: StagedService{Diff: "+image: pinned\n"},
	})
	cached := state.wrapCommitDiff(commit)
	if commit.diffWidth != state.commitDiffWidth() || cached.diffWidth != commit.diffWidth ||
		!slices.Equal(cached.diffLines, commit.diffLines) {
		t.Fatalf("cached service diff = %#v, want %#v", cached, commit)
	}
}

func TestModelDefersAndDeduplicatesCommitDiffResize(t *testing.T) {
	t.Parallel()

	state := &model{}
	commit := state.wrapCommitDiff(commitPage{
		kind: commitKindService, staged: StagedService{Diff: strings.Repeat("wide diff value\n", 128)},
	})
	state.page = commit
	command := state.resize(compactMinimum, compactMinHeight)
	before := commitPageValue(t, state)
	if command == nil || before.diffWidth != commit.diffWidth || !slices.Equal(before.diffLines, commit.diffLines) {
		t.Fatalf("deferred resize = %#v, command nil = %t", before, command == nil)
	}
	if currentCommand := state.resize(defaultWidth, defaultHeight); currentCommand != nil {
		t.Fatal("resize back to cached tier scheduled another diff wrap")
	}
	deliver(t, state, command)
	if current := commitPageValue(t, state); current.diffWidth != commit.diffWidth ||
		!slices.Equal(current.diffLines, commit.diffLines) {
		t.Fatalf("stale compact resize changed current cache = %#v", current)
	}
	command = state.resize(compactMinimum, compactMinHeight)
	deliver(t, state, command)
	compact := commitPageValue(t, state)
	if compact.diffWidth != compactMinimum-detailsPadding || compact.diffWidth == commit.diffWidth {
		t.Fatalf("settled compact diff = %#v", compact)
	}
	if command = state.resize(compactMinimum+10, compactMinHeight); command != nil {
		t.Fatal("same-tier resize scheduled another diff wrap")
	}
	command = state.resize(defaultWidth, defaultHeight)
	if command == nil {
		t.Fatal("full-tier resize command is nil")
	}
	state.page = homePage{}
	deliver(t, state, command)
	if _, valid := state.page.(homePage); !valid {
		t.Fatalf("resize message changed non-commit page: %T", state.page)
	}
}

func TestModelDefersAndCachesDeploymentDiffResize(t *testing.T) {
	t.Parallel()

	state := &model{}
	preview := state.wrapDeploymentPreviewDiff(deploymentPreviewPage{
		preview: DeploymentEditPreview{Diff: strings.Repeat("wide deployment diff value\n", 128)},
	})
	cached := state.wrapDeploymentPreviewDiff(preview)
	if preview.diffWidth != state.commitDiffWidth() || cached.diffWidth != preview.diffWidth ||
		!slices.Equal(cached.diffLines, preview.diffLines) {
		t.Fatalf("cached deployment diff = %#v, want %#v", cached, preview)
	}
	state.page = stageDeploymentConfirmationPage{preview: preview}
	command := state.resize(compactMinimum, compactMinHeight)
	before := mustLLMPage[stageDeploymentConfirmationPage](state.page).preview
	if command == nil || before.diffWidth != preview.diffWidth ||
		!slices.Equal(before.diffLines, preview.diffLines) {
		t.Fatalf("deferred deployment resize = %#v, command nil = %t", before, command == nil)
	}
	deliver(t, state, command)
	compact := mustLLMPage[stageDeploymentConfirmationPage](state.page).preview
	if compact.diffWidth != compactMinimum-detailsPadding || compact.diffWidth == preview.diffWidth {
		t.Fatalf("settled deployment confirmation diff = %#v", compact)
	}
	state.page = deploymentDiffPage{confirmation: stageDeploymentConfirmationPage{preview: compact}}
	command = state.resize(defaultWidth, defaultHeight)
	if command == nil {
		t.Fatal("full deployment diff resize command is nil")
	}
	deliver(t, state, command)
	full := mustLLMPage[deploymentDiffPage](state.page).confirmation.preview
	if full.diffWidth != state.commitDiffWidth() || full.diffWidth == compact.diffWidth || len(full.diffLines) == 0 {
		t.Fatalf("settled full deployment diff = %#v", full)
	}
	if command = state.resize(defaultWidth+10, defaultHeight); command != nil {
		t.Fatal("same-tier deployment resize scheduled another diff wrap")
	}
}

func TestModelKeepsDeploymentDiffOnTheLastFullViewport(t *testing.T) {
	t.Parallel()

	state := &model{}
	preview := state.wrapDeploymentPreviewDiff(deploymentPreviewPage{
		preview: DeploymentEditPreview{Diff: strings.Repeat("deployment diff line\n", 128)},
	})
	state.page = deploymentDiffPage{
		confirmation: stageDeploymentConfirmationPage{preview: preview},
		scroll:       len(preview.diffLines) + deploymentDiffLeadRows,
	}
	state.clampPageScroll()
	current := mustLLMPage[deploymentDiffPage](state.page)
	total := len(preview.diffLines) + deploymentDiffLeadRows
	visible := state.deploymentDiffViewportHeight()
	if current.scroll != total-visible {
		t.Fatalf("full deployment diff scroll = %d, want %d", current.scroll, total-visible)
	}
	if lines := state.deploymentDiffBody(current, state.bodyWidth(), visible); len(lines) != visible {
		t.Fatalf("full deployment diff viewport has %d lines, want %d", len(lines), visible)
	}

	command := state.resize(compactMinimum, compactMinHeight)
	if command == nil {
		t.Fatal("compact deployment diff resize command is nil")
	}
	deliver(t, state, command)
	current = mustLLMPage[deploymentDiffPage](state.page)
	total = len(current.confirmation.preview.diffLines) + deploymentDiffLeadRows
	visible = state.deploymentDiffViewportHeight()
	current.scroll = total
	state.page = current
	state.clampPageScroll()
	current = mustLLMPage[deploymentDiffPage](state.page)
	if current.scroll != total-visible {
		t.Fatalf("compact deployment diff scroll = %d, want %d", current.scroll, total-visible)
	}
	if lines := state.deploymentDiffBody(current, state.bodyWidth(), visible); len(lines) != visible {
		t.Fatalf("compact deployment diff viewport has %d lines, want %d", len(lines), visible)
	}
	for _, dimensions := range [][2]int{
		{hardMinimumWidth, hardMinimumHeight},
		{hardMinimumWidth - 1, hardMinimumHeight - 1},
	} {
		state.width, state.height = dimensions[0], dimensions[1]
		if height := state.deploymentDiffViewportHeight(); height != 0 {
			t.Fatalf("deployment diff viewport at %dx%d = %d", dimensions[0], dimensions[1], height)
		}
	}
}

func TestModelPersistsScrollablePageBounds(t *testing.T) {
	t.Parallel()

	commit := commitPage{kind: commitKindService, staged: StagedService{
		Diff: strings.Repeat("+image: registry.example/namespace/service@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa\n", 8),
	}}
	review := reviewPage{plan: planView{
		current:  strings.Repeat("registry.example/current/", 8),
		proposed: strings.Repeat("registry.example/proposed/", 8),
	}}
	pages := []struct {
		name string
		page page
	}{
		{name: "source diagnostic", page: sourceDiagnosticPage{
			diagnostic: SourceDiagnostic{
				File: strings.Repeat("services/nested/", 8) + "compose.yaml", Reason: DiagnosticComposeValidation,
			},
			scroll: 1000,
		}},
		{name: "commit", page: commitPage{kind: commitKindService, staged: commit.staged, scroll: 1000}},
		{name: "staged diff", page: stagedDiffPage{commit: commit, scroll: 1000}},
		{name: "details", page: detailsPage{review: review, scroll: 1000}},
	}

	for _, test := range pages {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, _, _ := newTestModel(t)
			state.page = test.page
			deliver(t, state, state.resize(compactMinimum, compactMinHeight))
			bounded := scrollPosition(t, state.page)
			if bounded >= 1000 {
				t.Fatalf("scroll was not bounded: %d", bounded)
			}
			state.handleKey(key(keyDown))
			if got := scrollPosition(t, state.page); got != bounded {
				t.Fatalf("scroll advanced past final line: %d, want %d", got, bounded)
			}

			deliver(t, state, state.resize(defaultWidth*2, defaultHeight))
			if got := scrollPosition(t, state.page); got > bounded {
				t.Fatalf("resized scroll = %d, want at most %d", got, bounded)
			}
			if content := state.View().Content; content == "" {
				t.Fatal("resized scroll page is empty")
			}
		})
	}
}

func scrollPosition(t *testing.T, current page) int {
	t.Helper()

	switch current := current.(type) {
	case sourceDiagnosticPage:
		return current.scroll
	case commitPage:
		return current.scroll
	case stagedDiffPage:
		return current.scroll
	case detailsPage:
		return current.scroll
	default:
		t.Fatalf("page %T is not scrollable", current)

		return 0
	}
}

func TestModelRejectsCommittedPlanDrift(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	operations.dryRunPlan.Service = "changed"
	deliver(t, state, state.startCommittedSnapshot(application.Request{Service: testAPI}))
	if !errors.Is(state.err, errInvalidInput) || state.status != "Committed service changed during validation" ||
		!slices.Equal(operations.recordedCalls(), []string{dryRunCall, snapshotCall, evidenceCall}) {
		t.Fatalf("drift state = %#v, calls = %q", state, operations.recordedCalls())
	}
}

func TestModelContainsPartialAndInvalidCommitResults(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	preview := servicePreviewPage{input: testRuntimeCommand, draft: workspace.draft}
	commit := commitPage{
		kind: commitKindService, preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage,
	}

	state.sequence++
	state.busy = true
	state.Update(serviceCommitResultMsg{
		sequence: state.sequence,
		commit:   commit,
		result:   CommitResult{Outcome: CommitValidationUnavailable},
	})
	home, valid := state.page.(homePage)
	if !valid || home.catalog.State != CatalogUnavailable ||
		state.status != "Commit created; validation is unavailable" ||
		state.mutationOutcome != "Compose commit created" || state.err != nil {
		t.Fatalf("partial commit result = %#v", state)
	}

	for _, result := range []CommitResult{
		{},
		{Outcome: CommitOutcome(255)},
	} {
		state.sequence++
		state.busy = true
		state.err = nil
		state.Update(serviceCommitResultMsg{sequence: state.sequence, commit: commit, result: result})
		if !errors.Is(state.err, errInvalidInput) || state.status != "Commit result could not be verified" {
			t.Fatalf("contradictory commit result %#v = %#v", result, state)
		}
	}
}

func TestModelContainsCommittedSnapshotFailures(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	request := application.Request{Service: testAPI}
	operations.dryRunErr = errTestTUI
	deliver(t, state, state.startCommittedSnapshot(request))
	if !errors.Is(state.err, errTestTUI) || !slices.Equal(operations.recordedCalls(), []string{dryRunCall}) {
		t.Fatalf("committed dry-run failure = %#v, calls %q", state, operations.recordedCalls())
	}

	operations.dryRunErr = nil
	operations.snapshotErr = errTestTUI
	deliver(t, state, state.startCommittedSnapshot(request))
	if !errors.Is(state.err, errTestTUI) || !slices.Equal(
		operations.recordedCalls(),
		[]string{dryRunCall, dryRunCall, snapshotCall},
	) {
		t.Fatalf("committed snapshot failure = %#v, calls %q", state, operations.recordedCalls())
	}
}

//nolint:cyclop // This lifecycle test keeps both effect confirmations and their default focus contiguous.
func TestModelConfirmsFirstRunRepositorySetup(t *testing.T) {
	t.Parallel()

	state, catalog, _ := newTestModel(t)
	const repository = "/home/user/maniud-desired-state"
	catalog.snapshot = CatalogSnapshot{State: CatalogMissing, SuggestedRepository: repository}
	catalog.registration = RegistrationResult{Snapshot: CatalogSnapshot{State: CatalogReady}}
	deliver(t, state, state.startCatalog())
	registration, valid := state.page.(registrationPage)
	if !valid || registration.step != registrationModeStep || registration.suggestedPath != repository {
		t.Fatalf("first-run page = %#v", state.page)
	}

	state.handleKey(key("enter"))
	registration = registrationPageValue(t, state)
	registration.remote = testGitHubRepository
	state.page = registration
	state.handleKey(key("enter"))
	state.handleKey(key("enter"))
	confirmation, valid := state.page.(registrationConfirmationPage)
	if !valid || confirmation.focus != confirmationBack {
		t.Fatalf("registration confirmation = %#v", state.page)
	}
	state.handleKey(key("enter"))
	if _, valid = state.page.(registrationPage); !valid || len(catalog.recordedCalls()) != 1 {
		t.Fatal("default registration confirmation performed an effect")
	}

	state.handleKey(key("enter"))
	state.handleKey(key("tab"))
	deliver(t, state, state.handleKey(key("enter")))
	if home, homeValid := state.page.(homePage); !homeValid || home.catalog.State != CatalogReady ||
		state.mutationOutcome != "Private GitHub repository created" ||
		!slices.Equal(catalog.recordedCalls(), []string{
			snapshotCall, "register:1:" + testGitHubRepository + ":" + repository,
		}) {
		t.Fatalf("registered state = %#v, calls = %q", state, catalog.recordedCalls())
	}
}

func TestModelCanSkipAndReopenRepositorySetup(t *testing.T) {
	t.Parallel()

	state, catalog, _ := newTestModel(t)
	const repository = "/home/user/maniud-desired-state"
	catalog.snapshot = CatalogSnapshot{State: CatalogMissing, SuggestedRepository: repository}
	deliver(t, state, state.startCatalog())
	state.handleKey(key("esc"))
	home := homePageValue(t, state)
	if home.catalog.SuggestedRepository != repository {
		t.Fatalf("skipped home = %#v", home.catalog)
	}
	state.handleKey(key("down"))
	state.handleKey(key("down"))
	state.handleKey(key("enter"))
	if registration, valid := state.page.(registrationPage); !valid ||
		registration.step != registrationModeStep || registration.suggestedPath != repository {
		t.Fatalf("reopened setup = %#v", state.page)
	}
}

func TestModelRecoversGitHubRepositoryAfterCloneFailure(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.sequence++
	state.busy = true
	state.page = registrationConfirmationPage{}
	state.Update(registrationResultMsg{
		sequence: state.sequence,
		request: RepositorySetupRequest{
			Mode: RepositorySetupCreateGitHub, Remote: testGitHubRepository, Checkout: testRepositoryPath,
		},
		result: RegistrationResult{
			Failure: RepositorySetupCloneFailed, RecoveryRepository: testExistingRemote,
		},
	})
	registration := registrationPageValue(t, state)
	if registration.step != registrationCheckoutStep || registration.mode != RepositorySetupCreateGitHub ||
		!registration.created ||
		registration.remote != testExistingRemote || registration.checkout != testRepositoryPath ||
		state.status != "GitHub repository created, but checkout failed. Review the path, then retry" {
		t.Fatalf("clone recovery state = %#v, status = %q", registration, state.status)
	}
}

//nolint:cyclop // Each stable repository failure code maps to its own recovery message.
func TestModelMapsRepositorySetupFailuresWithoutComposeCopy(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		failure RepositorySetupFailure
		want    string
	}{
		{RepositorySetupInvalidInput, "Repository setup values are invalid; review the repository and checkout path"},
		{RepositorySetupGitHubFailed, "GitHub repository was not created; check gh authentication, then retry"},
		{RepositorySetupCloneFailed, "Repository clone failed; review the remote and checkout path"},
		{RepositorySetupRegistrationFailed, "Repository checkout could not be registered; inspect it before retrying"},
		{RepositorySetupUnavailable, repositorySetupUnavailableStatus},
		{RepositorySetupFailure("unknown"), repositorySetupUnavailableStatus},
	} {
		state, _, _ := newTestModel(t)
		state.sequence++
		state.busy = true
		state.Update(registrationResultMsg{
			sequence: state.sequence, result: RegistrationResult{Failure: test.failure},
		})
		if state.status != test.want || strings.Contains(state.status, "Compose") {
			t.Fatalf("repository failure %q status = %q", test.failure, state.status)
		}
	}

	state, _, _ := newTestModel(t)
	state.sequence++
	state.busy = true
	state.Update(registrationResultMsg{
		sequence: state.sequence,
		request: RepositorySetupRequest{
			Mode: RepositorySetupCreateGitHub, Remote: testGitHubRepository, Checkout: testRepositoryPath,
		},
		result: RegistrationResult{
			Failure: RepositorySetupRegistrationFailed, RecoveryRepository: testExistingRemote,
		},
	})
	if state.status != "Repository checkout exists, but registration failed. Inspect it, then retry" {
		t.Fatalf("registration recovery status = %q", state.status)
	}

	state.sequence++
	state.busy = true
	state.Update(registrationResultMsg{
		sequence: state.sequence,
		result: RegistrationResult{
			Failure:            RepositorySetupCloneFailed,
			RecoveryRepository: strings.Repeat("x", displayLimits().Bytes+1),
		},
	})
	if state.status != repositorySetupUnavailableStatus {
		t.Fatalf("unsafe recovery status = %q", state.status)
	}

	if repositorySetupRecoveryStatus(RepositorySetupInvalidInput) !=
		"GitHub repository exists, but local setup did not finish. Review the path, then retry" {
		t.Fatal("unexpected generic repository recovery status")
	}
}

//nolint:cyclop,funlen // One state-machine scenario checks each setup slide and back-navigation boundary.
func TestModelNavigatesRepositorySetupSlidesAndPreparationExit(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	for _, name := range []string{"up", "k", "j", keyTab} {
		state.page = newRegistrationPage(testRepositoryPath)
		state.handleKey(key(name))
		registration := registrationPageValue(t, state)
		if registration.cursor != 1 {
			t.Fatalf("registration mode key %q cursor = %d", name, registration.cursor)
		}
	}
	state.page = newRegistrationPage(testRepositoryPath)
	state.handleKey(key("down"))
	state.handleKey(key("enter"))
	registration := registrationPageValue(t, state)
	if registration.mode != RepositorySetupExisting || registration.step != registrationRemoteStep {
		t.Fatalf("existing repository mode = %#v", registration)
	}
	registration.remote = testExistingRemote
	state.page = registration
	state.handleKey(key("enter"))
	registration = registrationPageValue(t, state)
	if registration.step != registrationCheckoutStep || registration.checkout != testRepositoryPath {
		t.Fatalf("checkout slide = %#v", registration)
	}
	state.handleKey(key("esc"))
	registration = registrationPageValue(t, state)
	if registration.step != registrationRemoteStep {
		t.Fatalf("checkout back = %#v", registration)
	}
	registration.checkout = "/tmp/custom-checkout"
	state.page = registration
	state.handleKey(key("enter"))
	registration = registrationPageValue(t, state)
	if registration.step != registrationCheckoutStep || registration.checkout != "/tmp/custom-checkout" {
		t.Fatalf("revisited checkout slide = %#v", registration)
	}
	state.handleKey(key("esc"))
	state.handleKey(key("esc"))
	registration = registrationPageValue(t, state)
	if registration.step != registrationModeStep {
		t.Fatalf("remote back = %#v", registration)
	}
	state.handleKey(key("x"))
	if _, valid := state.page.(registrationPage); !valid {
		t.Fatalf("unknown mode key changed page to %T", state.page)
	}
	state.page = registrationPage{
		step: registrationCheckoutStep, mode: RepositorySetupCreateGitHub,
		remote: testGitHubRepository, checkout: testRepositoryPath, created: true,
	}
	state.handleKey(key(keyEscape))
	registration = registrationPageValue(t, state)
	if registration.step != registrationCheckoutStep ||
		state.status != "GitHub repository already exists; change only the local checkout path" {
		t.Fatalf("created repository back = %#v, status = %q", registration, state.status)
	}

	state.page = registrationPage{
		step: registrationCheckoutStep, mode: RepositorySetupExisting,
		remote: testExistingRemote,
	}
	state.handleKey(key("enter"))
	if state.status != "Enter a local checkout path" {
		t.Fatalf("empty checkout status = %q", state.status)
	}
	state.handleKey(key("x"))
	registration = registrationPageValue(t, state)
	if registration.checkout != "x" {
		t.Fatalf("edited checkout = %#v", registration)
	}
	state.sequence++
	state.busy = true
	state.Update(registrationResultMsg{
		sequence: state.sequence,
		request:  registrationRequest(registration),
		result: RegistrationResult{Snapshot: CatalogSnapshot{
			State: CatalogReady,
		}},
	})
	if state.mutationOutcome != "Repository registered" {
		t.Fatalf("existing repository result = %#v", state)
	}

	state.page = preparationRequiredPage{draft: ServiceDraft{Service: testService}}
	if command := state.handleKey(key("x")); command != nil {
		t.Fatal("unknown preparation key quit")
	}
	for _, name := range []string{keyEnter, keyEscape, keyQuit} {
		state.page = preparationRequiredPage{draft: ServiceDraft{Service: testService}}
		if command := state.handleKey(key(name)); command == nil {
			t.Fatalf("preparation key %q did not quit", name)
		}
	}
}

func TestModelBlocksApplyAfterPreparationCommit(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	preview := servicePreviewPage{input: testRuntimeCommand, draft: workspace.draft}
	commit := commitPage{
		kind: commitKindService, preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage,
	}
	state.sequence++
	state.busy = true
	state.Update(serviceCommitResultMsg{
		sequence: state.sequence,
		commit:   commit,
		result:   CommitResult{Outcome: CommitPreparationRequired},
	})
	if _, valid := state.page.(preparationRequiredPage); !valid || !state.applyBlocked ||
		state.status != "Preparation is required before validation" {
		t.Fatalf("preparation commit state = %#v", state)
	}
	if command := state.startApply(reviewPage{}); command != nil ||
		state.status != "Exit, run the preparation step, then start maniud tui again" {
		t.Fatalf("blocked apply = %#v", state)
	}
}

func TestModelContainsCatalogBlockersAndUnsafeDisplayValues(t *testing.T) {
	t.Parallel()

	state, catalog, _ := newTestModel(t)
	catalog.snapshot.Services[0].Location = "services/\x1b[31mapi.yaml"
	catalog.snapshot.Services[0].Name = strings.Repeat("x", displayLimits().Bytes+1)
	deliver(t, state, state.startCatalog())
	home := homePageValue(t, state)
	if home.catalog.Services[0].Blocker != BlockerInvalid || strings.Contains(state.View().Content, "\x1b[31m") {
		t.Fatalf("unsafe catalog = %#v, view %q", home.catalog, state.View().Content)
	}

	catalog.registeredResult = OpenResult{Blocker: BlockerNotFound}
	home.catalog.Services[0] = Service{ID: "gone", Location: "gone", Name: testService}
	state.page = home
	deliver(t, state, state.handleKey(key("enter")))
	if state.status != "Compose source is no longer registered" {
		t.Fatalf("missing source status = %q", state.status)
	}

	for blocker, message := range map[SourceBlocker]string{
		BlockerInvalid:     testBlockerMessage,
		BlockerUnavailable: "Compose source is unavailable",
	} {
		state.handleOpenResult(openResultMsg{sequence: state.sequence, result: OpenResult{Blocker: blocker}})
		if state.status != message {
			t.Fatalf("blocker %q status = %q", blocker, state.status)
		}
	}
}

func TestModelShowsSafeComposeSourceDiagnostic(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.page = openPathPage{value: testServicePath}
	state.handleOpenResult(openResultMsg{sequence: state.sequence, result: OpenResult{
		Blocker: BlockerInvalid,
		Diagnostic: SourceDiagnostic{
			File: testServicePath, Reason: DiagnosticYAMLStructure, Line: 4, Column: 5,
		},
	}})
	if _, valid := state.page.(sourceDiagnosticPage); !valid {
		t.Fatalf("diagnostic page = %#v", state.page)
	}
	content := state.View().Content
	for _, value := range []string{
		testServicePath, "line 4, column 5", "YAML mapping is invalid", "Remove duplicate keys",
	} {
		if !strings.Contains(content, value) {
			t.Fatalf("diagnostic view misses %q: %q", value, content)
		}
	}
	state.handleKey(key("esc"))
	if _, valid := state.page.(openPathPage); !valid {
		t.Fatalf("diagnostic back page = %#v", state.page)
	}

	diagnostic := sourceDiagnosticPage{previous: openPathPage{}, scroll: 1}
	state.page = diagnostic
	state.handleKey(key("up"))
	state.handleKey(key("down"))
	if current, valid := state.page.(sourceDiagnosticPage); !valid || current.scroll != 1 {
		t.Fatalf("diagnostic scroll page = %#v", state.page)
	}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("diagnostic q command is nil")
	}
}

func TestModelRejectsUnsafeComposeSourceDiagnostic(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.page = openPathPage{value: testServicePath}
	state.handleOpenResult(openResultMsg{sequence: state.sequence, result: OpenResult{
		Blocker: BlockerInvalid,
		Diagnostic: SourceDiagnostic{
			File: "/private/secret.yaml", Reason: DiagnosticYAMLSyntax, Line: 1, Column: 1,
		},
	}})
	if _, valid := state.page.(sourceDiagnosticPage); valid ||
		strings.Contains(state.View().Content, "/private/secret.yaml") {
		t.Fatalf("unsafe diagnostic page = %#v, view %q", state.page, state.View().Content)
	}

	for _, diagnostic := range []SourceDiagnostic{
		{File: "", Reason: DiagnosticYAMLSyntax},
		{File: "../secret.yaml", Reason: DiagnosticYAMLSyntax, Line: 1, Column: 1},
		{File: "services/../secret.yaml", Reason: DiagnosticYAMLSyntax, Line: 1, Column: 1},
		{File: "services/\xff.yaml", Reason: DiagnosticYAMLSyntax, Line: 1, Column: 1},
		{File: testServicePath, Reason: "vendor_message", Line: 1, Column: 1},
		{File: testServicePath, Reason: DiagnosticYAMLSyntax, Line: 0, Column: 1},
		{File: testServicePath, Reason: DiagnosticYAMLSyntax, Line: -1, Column: 0},
		{File: testServicePath, Reason: DiagnosticYAMLSyntax, Line: 1, Column: -1},
	} {
		if _, valid := canonicalSourceDiagnostic(diagnostic); valid {
			t.Fatalf("canonicalSourceDiagnostic(%#v) accepted", diagnostic)
		}
	}
	for _, reason := range []SourceDiagnosticReason{
		DiagnosticYAMLSyntax, DiagnosticYAMLStructure, DiagnosticYAMLUnsupported, DiagnosticComposeValidation,
	} {
		if _, valid := canonicalSourceDiagnostic(SourceDiagnostic{File: testServicePath, Reason: reason}); !valid {
			t.Fatalf("canonicalSourceDiagnostic(%q) rejected", reason)
		}
	}
}

//nolint:cyclop // One test covers both explicit cancellation paths through the same in-flight operation.
func TestModelCancelsAndWaitsForBusyOperation(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	operations.blockApply = true
	operations.cancelObserved = make(chan struct{})
	review := reviewPage{request: application.Request{Service: testService}, plan: planView{status: statusReady}}
	command := state.startApply(review)
	if command == nil || !state.applying {
		t.Fatalf("started apply state = %#v", state)
	}
	startedReview, valid := state.page.(reviewPage)
	if !valid || startedReview.correlation.sequence != state.sequence ||
		state.events.sequence.Load() != state.sequence {
		t.Fatalf("apply correlation generation = %#v / %d", startedReview.correlation, state.events.sequence.Load())
	}
	if state.startApply(review) != nil || !state.applying {
		t.Fatalf("busy apply changed state = %#v", state)
	}
	result := make(chan tea.Msg, 1)
	go func() { result <- command() }()

	if next := state.handleKey(key("esc")); next != nil || state.status != "Cancelling" || !state.busy {
		t.Fatalf("cancel state = %#v", state)
	}
	if state.startCatalog() != nil {
		t.Fatal("busy model started a second operation")
	}
	<-operations.cancelObserved
	state.Update(<-result)
	if state.busy || state.applying || state.status != testLLMCancelled || state.err != nil {
		t.Fatalf("cancelled state = %#v", state)
	}

	operations.blockApply = true
	operations.cancelObserved = make(chan struct{})
	operations.cancelOnce = sync.Once{}
	command = state.startApply(review)
	result = make(chan tea.Msg, 1)
	go func() { result <- command() }()
	if next := state.handleKey(key("q")); next != nil || !state.quitAfterOperation {
		t.Fatalf("busy quit state = %#v", state)
	}
	<-operations.cancelObserved
	_, quit := state.Update(<-result)
	if quit == nil || state.busy || state.quitAfterOperation {
		t.Fatalf("completed quit state = %#v", state)
	}
}

func TestModelCancellationRejectsLateCatalogResults(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		start func(*model) tea.Cmd
	}{
		{name: "catalog", start: func(state *model) tea.Cmd { return state.startCatalog() }},
		{name: "registered service", start: func(state *model) tea.Cmd {
			return state.startOpenRegistered(registeredAPIID)
		}},
		{name: "Compose path", start: func(state *model) tea.Cmd { return state.startOpenPath(testComposePath) }},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			state, _, _ := newTestModel(t)
			state.page = openPathPage{value: testStableStatus}
			command := test.start(state)
			if command == nil {
				t.Fatal("operation command is nil")
			}
			state.handleKey(key(keyEscape))
			state.Update(command())
			current, valid := state.page.(openPathPage)
			if !valid || current.value != testStableStatus || state.busy || state.status != "Cancelled" {
				t.Fatalf("late result changed cancelled operation: %#v", state)
			}
		})
	}
}

func TestModelContainsOperationFailuresAndStaleResults(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	state.page = reviewPage{request: application.Request{}, plan: planView{status: statusReady}}
	operations.applyErr = errTestSecret
	deliver(t, state, state.startApply(reviewPageValue(t, state)))
	if !errors.Is(state.err, errTestSecret) || strings.Contains(state.View().Content, errTestSecret.Error()) {
		t.Fatalf("operation failure = %#v, view %q", state, state.View().Content)
	}

	state.err = nil
	state.status = testStableStatus
	state.sequence = 10
	state.Update(snapshotResultMsg{sequence: 9, err: errTestTUI})
	state.Update(applyResultMsg{sequence: 9, err: errTestTUI})
	state.Update(openResultMsg{sequence: 9, result: OpenResult{Blocker: BlockerInvalid}})
	state.Update(catalogResultMsg{sequence: 9, snapshot: CatalogSnapshot{State: CatalogUnavailable}})
	if state.err != nil || state.status != testStableStatus {
		t.Fatalf("stale results changed state = %#v", state)
	}

	state.sequence = 11
	state.busy = true
	state.Update(snapshotResultMsg{sequence: 11, err: context.Canceled})
	if state.err != nil || state.status != "Cancelled" {
		t.Fatalf("cancelled result = %#v", state)
	}
}

func TestModelRefreshesHealthPendingApplyWithoutFailure(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	review := reviewPage{
		request: application.Request{Service: testService},
		plan:    planView{status: statusReady},
	}
	state.page = review
	operations.applyErr = application.ErrHealthPending

	deliver(t, state, state.startApply(review))
	if state.err != nil || state.status == statusOperationFailed {
		t.Fatalf("pending apply state = %#v", state)
	}
	if !slices.Equal(operations.recordedCalls(), []string{applyCall, snapshotCall, evidenceCall}) {
		t.Fatalf("pending apply calls = %q", operations.recordedCalls())
	}
}

func TestModelRefreshesDegradedPredecessorAfterHealthRollback(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	review := reviewPage{
		request: application.Request{Service: testService},
		plan:    planView{status: statusReady},
	}
	state.page = review
	operations.resolutionErr = application.ErrHealthDegraded

	deliver(t, state, state.startHealthResolution(healthConfirmationPage{
		review: reviewPage{
			request: review.request,
			plan: planView{
				status: statusReady, transaction: strings.Repeat("a", 32),
				resolution: application.HealthResolutionRollback,
			},
		},
		focus: confirmationApply,
	}))
	if state.err != nil || state.status == statusOperationFailed {
		t.Fatalf("degraded predecessor state = %#v", state)
	}
	if !slices.Equal(operations.recordedCalls(), []string{
		"resolve-health", snapshotCall, evidenceCall,
	}) {
		t.Fatalf("degraded predecessor calls = %q", operations.recordedCalls())
	}
}

//nolint:funlen // One table-free boundary test keeps related health message routing together.
func TestModelRoutesHealthMessagesAndRejectsStaleResults(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{
		request: application.Request{Service: testService},
		plan: planView{
			health: application.HealthConvergencePending, healthPoll: time.Nanosecond,
			status: statusReady,
		},
	}
	state.page = review
	sequence := state.sequence
	_, command := state.Update(healthPollMsg{sequence: sequence})
	if command == nil {
		t.Fatal("current health poll did not start a snapshot")
	}
	state.finishOperation()

	poll := state.waitForHealth(review)
	if poll == nil {
		t.Fatal("pending health wait command is nil")
	}
	message, valid := poll().(healthPollMsg)
	if !valid || message.sequence != state.sequence {
		t.Fatalf("health poll message = %#v", message)
	}

	state.page = healthConfirmationPage{review: review}
	state.status = statusReady
	state.invalidateConfirmation()
	if _, valid = state.page.(reviewPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("invalidated health confirmation = %#v", state)
	}
	state.page = healthConfirmationPage{review: review}
	state.handleKey(key(testUnknownValue))
	if _, valid = state.page.(healthConfirmationPage); !valid {
		t.Fatalf("health confirmation routing = %T", state.page)
	}

	if state.startHealthResolution(healthConfirmationPage{}) != nil {
		t.Fatal("invalid health resolution started")
	}
	state.page = review
	if command = state.handleApplyResult(applyResultMsg{
		sequence: state.sequence + 1, err: application.ErrHealthPending,
	}); command != nil {
		t.Fatal("stale pending apply returned a command")
	}
	state.page = homePage{}
	state.busy = true
	state.handleApplyResult(applyResultMsg{sequence: state.sequence, err: application.ErrHealthDegraded})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Health result could not be refreshed" {
		t.Fatalf("health apply without review = %#v", state)
	}

	state.err = nil
	state.page = review
	state.busy = true
	if command = state.handleHealthResolutionResult(healthResolutionResultMsg{
		sequence: state.sequence + 1,
	}); command != nil {
		t.Fatal("stale health resolution returned a command")
	}
	state.busy = true
	state.handleHealthResolutionResult(healthResolutionResultMsg{
		sequence: state.sequence, err: errTestTUI,
	})
	if !errors.Is(state.err, errTestTUI) || state.status != statusOperationFailed {
		t.Fatalf("failed health resolution = %#v", state)
	}
	state.err = nil
	state.page = homePage{}
	state.busy = true
	state.handleHealthResolutionResult(healthResolutionResultMsg{
		sequence: state.sequence, err: application.ErrSnapshotStale,
	})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Health decision could not be refreshed" {
		t.Fatalf("health resolution without review = %#v", state)
	}
}

func TestProjectPlanUsesApplicationHealthResolutionProjection(t *testing.T) {
	t.Parallel()

	operations := newOperationsFixture()
	snapshot := operations.snapshot
	snapshot.HasTransaction = true
	snapshot.Transaction.ID = strings.Repeat("a", 32)
	snapshot.Plan.Health = application.HealthConvergenceDegraded
	snapshot.Plan.Observation.Health = application.WorkloadHealth{
		Status: application.WorkloadHealthUnhealthy,
	}
	snapshot.AvailableHealthResolution = application.HealthResolutionRetryRestoreStart
	view, err := projectPlan(snapshot)
	if err != nil {
		t.Fatalf("projectPlan(retry) error = %v", err)
	}
	if view.resolution != application.HealthResolutionRetryRestoreStart {
		t.Fatalf("projected resolution = %q", view.resolution)
	}

	snapshot.AvailableHealthResolution = application.HealthResolutionRollback
	snapshot.HealthResolutionRestoresPrevious = true
	view, err = projectPlan(snapshot)
	if err != nil || view.resolution != application.HealthResolutionRollback || !view.restoresPrevious {
		t.Fatalf("projectPlan(rollback) = %#v, %v", view, err)
	}

	snapshot.AvailableHealthResolution = application.HealthResolutionCancelAdoption
	snapshot.HealthResolutionRestoresPrevious = false
	snapshot.Plan.Health = application.HealthConvergencePending
	snapshot.Plan.Observation.Lifecycle = application.WorkloadLifecycleRestarting
	view, err = projectPlan(snapshot)
	if err != nil {
		t.Fatalf("projectPlan(adoption) error = %v", err)
	}
	if view.resolution != application.HealthResolutionCancelAdoption || view.healthState != "restarting" {
		t.Fatalf("pending adoption projection = %#v", view)
	}

	for _, invalid := range []application.OperationSnapshot{
		{AvailableHealthResolution: application.HealthResolutionRollback},
		{HealthResolutionRestoresPrevious: true},
		{
			HasTransaction: true, Transaction: application.SnapshotTransaction{ID: strings.Repeat("a", 32)},
			AvailableHealthResolution:        application.HealthResolutionCancelAdoption,
			HealthResolutionRestoresPrevious: true,
		},
		{
			HasTransaction: true, Transaction: application.SnapshotTransaction{ID: strings.Repeat("a", 32)},
			AvailableHealthResolution: application.HealthResolutionAction(testUnknownValue),
		},
	} {
		invalid.Plan = snapshot.Plan
		if _, invalidErr := projectPlan(invalid); !errors.Is(invalidErr, errInvalidInput) {
			t.Fatalf("projectPlan(invalid projection %#v) error = %v", invalid, invalidErr)
		}
	}
}

func TestModelLeavesDegradedHealthForOperatorDecision(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	operations.snapshot.Plan.Health = application.HealthConvergenceDegraded
	operations.snapshot.Plan.Observation.Health = application.WorkloadHealth{
		Status: application.WorkloadHealthUnhealthy,
	}

	_, followup := state.Update(state.startSnapshot(application.Request{Service: testService})())
	review := reviewPageValue(t, state)
	if followup != nil || review.plan.health != application.HealthConvergenceDegraded ||
		state.status != "Workload health requires a decision" {
		t.Fatalf("degraded review = %#v, followup = %#v", state, followup)
	}
	if got := operations.recordedCalls(); !slices.Equal(got, []string{snapshotCall, evidenceCall}) {
		t.Fatalf("degraded review calls = %q", got)
	}
}

func TestModelPollsPendingHealthOnlyFromCurrentIdleReview(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	request := application.Request{Service: testService}
	operations.snapshot.Plan.Health = application.HealthConvergencePending
	operations.snapshot.Plan.HealthPoll = time.Second
	operations.snapshot.Plan.Observation.Lifecycle = application.WorkloadLifecycleRunning
	operations.snapshot.Plan.Observation.Health = application.WorkloadHealth{
		Status: application.WorkloadHealthStarting,
	}

	command := state.startSnapshot(request)
	if command == nil {
		t.Fatal("pending snapshot command is nil")
	}
	_, poll := state.Update(command())
	review := reviewPageValue(t, state)
	if poll == nil || review.plan.health != application.HealthConvergencePending {
		t.Fatalf("pending review = %#v, poll = %#v", review, poll)
	}
	sequence := state.sequence
	before := len(operations.recordedCalls())
	if state.handleHealthPoll(healthPollMsg{sequence: sequence - 1}) != nil ||
		len(operations.recordedCalls()) != before {
		t.Fatal("stale health poll started an operation")
	}
	state.page = detailsPage{review: review}
	if state.handleHealthPoll(healthPollMsg{sequence: sequence}) != nil ||
		len(operations.recordedCalls()) != before {
		t.Fatal("health poll outside review started an operation")
	}
	state.page = review
	state.busy = true
	if state.handleHealthPoll(healthPollMsg{sequence: sequence}) != nil {
		t.Fatal("health poll started while busy")
	}
	state.busy = false
	operations.snapshot.Plan.Health = application.HealthConvergenceNone
	operations.snapshot.Plan.HealthPoll = 0
	deliver(t, state, state.handleHealthPoll(healthPollMsg{sequence: sequence}))
	if got := operations.recordedCalls(); !slices.Equal(got, []string{
		snapshotCall, evidenceCall, snapshotCall, evidenceCall,
	}) {
		t.Fatalf("current health poll calls = %q", got)
	}
}

func TestPendingHealthNavigationReturnsThroughFreshSnapshot(t *testing.T) {
	t.Parallel()

	newPendingReview := func() reviewPage {
		return reviewPage{
			request: application.Request{Service: testService},
			plan: planView{
				health: application.HealthConvergencePending,
				status: statusReady,
			},
		}
	}

	state, _, operations := newTestModel(t)
	review := newPendingReview()
	if command := state.handleDetailsKey(detailsPage{review: review}, keyEscape); command == nil {
		t.Fatal("returning from pending health details did not refresh")
	} else {
		command()
	}
	if got := operations.recordedCalls(); !slices.Equal(got, []string{snapshotCall, evidenceCall}) {
		t.Fatalf("details return calls = %q", got)
	}

	state, _, operations = newTestModel(t)
	review = newPendingReview()
	if command := state.handleContextualHelpKey(
		contextualHelpPage{previous: review}, keyEscape,
	); command == nil {
		t.Fatal("returning from pending health help did not refresh")
	} else {
		command()
	}
	if got := operations.recordedCalls(); !slices.Equal(got, []string{snapshotCall, evidenceCall}) {
		t.Fatalf("help return calls = %q", got)
	}

	state, _, _ = newTestModel(t)
	review.plan.health = application.HealthConvergenceNone
	if command := state.handleDetailsKey(detailsPage{review: review}, keyEscape); command != nil {
		t.Fatal("settled health details return refreshed")
	}
	if command := state.handleContextualHelpKey(
		contextualHelpPage{previous: review}, keyEscape,
	); command != nil {
		t.Fatal("settled health help return refreshed")
	}
}

func TestModelResumesHealthyTransactionOnce(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	request := application.Request{Service: testService}
	operations.snapshot.Plan.Health = application.HealthConvergenceHealthy
	operations.snapshot.Plan.Observation.Lifecycle = application.WorkloadLifecycleRunning
	operations.snapshot.Plan.Observation.Health = application.WorkloadHealth{
		Status: application.WorkloadHealthHealthy,
	}

	command := state.startSnapshot(request)
	_, resume := state.Update(command())
	if resume == nil || !state.applying {
		t.Fatalf("healthy snapshot did not resume = %#v", state)
	}
	operations.snapshot.Plan.Health = application.HealthConvergenceNone
	deliver(t, state, resume)
	calls := operations.recordedCalls()
	if count := strings.Count(strings.Join(calls, ","), applyCall); count != 1 {
		t.Fatalf("healthy resume calls = %q", calls)
	}
}

func TestModelConfirmsExactHealthResolutionWithBackAsDefault(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		health application.HealthConvergence
		action application.HealthResolutionAction
	}{
		{
			name: "upgrade rollback", health: application.HealthConvergenceDegraded,
			action: application.HealthResolutionRollback,
		},
		{
			name: "adoption cancel", health: application.HealthConvergencePending,
			action: application.HealthResolutionCancelAdoption,
		},
		{
			name: "restore start retry", health: application.HealthConvergenceDegraded,
			action: application.HealthResolutionRetryRestoreStart,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, _, operations := newTestModel(t)
			transactionID := strings.Repeat("a", 32)
			review := reviewPage{
				request: application.Request{Service: testService},
				plan: planView{
					project: testProject, service: testService, status: statusReady,
					health: test.health, healthPoll: time.Second,
					transaction: transactionID, resolution: test.action,
				},
			}
			state.page = review
			state.handleReviewKey(review, keyEnter)
			confirmation, valid := state.page.(healthConfirmationPage)
			if !valid || confirmation.focus != confirmationBack {
				t.Fatalf("initial health confirmation = %#v", state.page)
			}
			state.handleHealthConfirmationKey(confirmation, keyEnter)
			if len(operations.recordedResolutions()) != 0 {
				t.Fatal("default Back applied a health resolution")
			}

			state.page = review
			state.handleReviewKey(review, keyEnter)
			confirmation, valid = state.page.(healthConfirmationPage)
			if !valid {
				t.Fatalf("health confirmation page = %T", state.page)
			}
			for _, navigation := range []string{keyLeft, keyRight, keyShiftTab} {
				state.page = confirmation
				state.handleHealthConfirmationKey(confirmation, navigation)
				focused, focusedValid := state.page.(healthConfirmationPage)
				if !focusedValid || focused.focus != confirmationApply {
					t.Fatalf("health confirmation %s did not change focus: %#v", navigation, state.page)
				}
			}
			state.page = confirmation
			state.handleHealthConfirmationKey(confirmation, keyTab)
			confirmation, valid = state.page.(healthConfirmationPage)
			if !valid {
				t.Fatalf("focused health confirmation page = %T", state.page)
			}
			deliver(t, state, state.handleHealthConfirmationKey(confirmation, keyEnter))
			want := application.HealthResolution{
				Transaction: transactionID, Action: test.action,
				Observation: review.plan.healthProof,
			}
			if got := operations.recordedResolutions(); !slices.Equal(got, []application.HealthResolution{want}) {
				t.Fatalf("health resolutions = %#v, want %#v", got, want)
			}
		})
	}
}

func TestModelHealthRecoveryDoesNotOpenMutationOptions(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	review := reviewPage{
		request: application.Request{Service: testService},
		plan: planView{
			health: application.HealthConvergencePending, healthPoll: time.Second,
			status: statusReady,
		},
	}
	state.page = review
	state.handleReviewKey(review, "o")
	if _, valid := state.page.(detailsPage); !valid {
		t.Fatalf("health options shortcut opened %T", state.page)
	}
	state.page = review
	review.focus = reviewExplore
	state.handleReviewKey(review, keyEnter)
	if _, valid := state.page.(detailsPage); !valid {
		t.Fatalf("health secondary action opened %T", state.page)
	}
	state.page = review
	if command := state.handleReviewKey(review, keyQuit); command == nil ||
		len(operations.recordedResolutions()) != 0 {
		t.Fatal("quitting health review applied a resolution")
	}

	state.page = review
	review.focus = reviewContinue
	if command := state.handleReviewKey(review, keyEnter); command == nil {
		t.Fatal("health review without a resolution did not refresh")
	}
	state.finishOperation()
}

func TestModelRequiresEvidenceAndSafePlanProjection(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	request := application.Request{Service: testService}
	operations.evidence.Version = 0
	deliver(t, state, state.startSnapshot(request))
	if !errors.Is(state.err, errInvalidInput) || state.status != "Review evidence is unavailable" {
		t.Fatalf("invalid evidence state = %#v", state)
	}

	operations.evidence.Version = application.EvidenceBundleVersion
	operations.snapshot.Plan.Project = strings.Repeat("x", displayLimits().Bytes+1)
	deliver(t, state, state.startSnapshot(request))
	if !errors.Is(state.err, errInvalidInput) || state.status != "Review content could not be displayed safely" {
		t.Fatalf("unsafe plan state = %#v", state)
	}

	operations.snapshot.Plan.Project = testProject
	operations.snapshot.Plan.Health = application.HealthConvergence("new")
	deliver(t, state, state.startSnapshot(request))
	if !errors.Is(state.err, errInvalidInput) || state.status != "Review content could not be displayed safely" {
		t.Fatalf("invalid health convergence = %#v", state)
	}

	operations.snapshotErr = errTestTUI
	before := len(operations.recordedCalls())
	deliver(t, state, state.startSnapshot(request))
	if !errors.Is(state.err, errTestTUI) || len(operations.recordedCalls()) != before+1 {
		t.Fatalf("snapshot failure = %#v, calls %q", state, operations.recordedCalls())
	}
}

func TestModelInvalidatesConfirmationBelowCompactFloor(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{status: statusReady}}
	state.page = review
	state.handleKey(key("enter"))
	if _, valid := state.page.(confirmationPage); !valid {
		t.Fatal("full layout did not enter confirmation")
	}
	state.resize(hardMinimumWidth, hardMinimumHeight)
	if _, valid := state.page.(reviewPage); !valid || state.status != "Review again at a larger terminal" {
		t.Fatalf("hard-floor state = %#v", state)
	}
	state.handleKey(key("enter"))
	if _, valid := state.page.(confirmationPage); valid || state.status != "Resize to continue to confirmation" {
		t.Fatalf("hard-floor confirmation = %#v", state)
	}

	healthReview := reviewPage{plan: planView{
		status: statusReady, health: application.HealthConvergenceDegraded,
		resolution: application.HealthResolutionRollback,
	}}
	state.page = healthReview
	state.handleReviewKey(healthReview, keyEnter)
	if _, valid := state.page.(healthConfirmationPage); valid ||
		state.status != "Resize to continue to confirmation" {
		t.Fatalf("hard-floor health confirmation = %#v", state)
	}
	state.page = healthConfirmationPage{review: healthReview}
	state.handleHealthConfirmationKey(healthConfirmationPage{review: healthReview}, keyEnter)
	if _, valid := state.page.(reviewPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("active hard-floor health confirmation = %#v", state)
	}

	state.resize(defaultWidth, defaultHeight)
	preview := servicePreviewPage{draft: ServiceDraft{
		Runtime: testRuntime, Image: testImage, Service: testService, ComposePath: testServicePath,
	}}
	state.page = stageServiceConfirmationPage{preview: preview, focus: confirmationApply}
	state.resize(hardMinimumWidth, hardMinimumHeight)
	if _, valid := state.page.(servicePreviewPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("stage hard-floor state = %#v", state)
	}
}

func TestModelDismissesHealthConfirmationWithoutMutation(t *testing.T) {
	t.Parallel()

	review := reviewPage{plan: planView{
		status: statusReady, health: application.HealthConvergencePending,
		healthPoll: time.Second, resolution: application.HealthResolutionCancelAdoption,
	}}
	for _, tt := range []struct {
		name string
		key  string
		quit bool
	}{
		{name: "escape", key: keyEscape},
		{name: "quit", key: keyQuit, quit: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			state, _, operations := newTestModel(t)
			current := healthConfirmationPage{review: review, focus: confirmationBack}
			state.page = current

			command := state.handleHealthConfirmationKey(current, tt.key)
			if tt.quit {
				if command == nil {
					t.Fatal("quit command is nil")
				}
			} else if _, valid := state.page.(reviewPage); !valid || command == nil {
				t.Fatalf("dismissed health confirmation = %#v, command = %#v", state.page, command)
			}
			if len(operations.recordedResolutions()) != 0 {
				t.Fatal("dismissing health confirmation applied a resolution")
			}
		})
	}
}

//nolint:cyclop // One contract keeps every allowed and denied export boundary together.
func TestModelExportsOnlyIdleReviewDetailsAtSafeLayout(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{current: "current-image", proposed: "proposed-image"}}
	state.page = review
	if command := state.handleKey(key("z")); command != nil {
		t.Fatalf("unbound review key command = %#v", command)
	}
	if command := state.handleKey(key("x")); command == nil ||
		!strings.Contains(state.result.Export, "CURRENT\ncurrent-image\n\nPROPOSED\nproposed-image") {
		t.Fatalf("review export = %q, command = %#v", state.result.Export, command)
	}

	state.result = Result{}
	state.page = detailsPage{review: review}
	if command := state.handleKey(key("x")); command == nil || state.result.Export == "" {
		t.Fatalf("details export = %q, command = %#v", state.result.Export, command)
	}

	state.result = Result{}
	state.busy = true
	if command := state.handleKey(key("x")); command != nil || state.result.Export != "" ||
		state.status != "Wait for the current operation before exporting" {
		t.Fatalf("busy export = %q, status = %q, command = %#v", state.result.Export, state.status, command)
	}
	state.busy = false
	state.resize(hardMinimumWidth, hardMinimumHeight)
	if command := state.handleKey(key("x")); command != nil || state.result.Export != "" ||
		state.status != "Resize to export session details" {
		t.Fatalf("hard-floor export = %q, status = %q, command = %#v", state.result.Export, state.status, command)
	}

	state.resize(defaultWidth, defaultHeight)
	state.page = confirmationPage{review: review}
	state.status = "confirmation"
	if command := state.handleKey(key("x")); command != nil || state.result.Export != "" ||
		state.status != "confirmation" {
		t.Fatalf("confirmation export = %q, status = %q, command = %#v", state.result.Export, state.status, command)
	}
}

//nolint:cyclop // One contract compares observation behavior across each correlated page.
func TestModelObservesEventsWithoutChangingFlowState(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{
		plan: planView{status: statusReady},
		correlation: eventCorrelation{
			sequence: 7,
			project:  testProject, service: testService, plan: application.PlanUpgrade,
			runtime: testRuntime,
		},
	}
	event := application.Event{
		Kind: application.EventPlanPrepared, Plan: application.PlanUpgrade,
		Project: testProject, Service: testService, Runtime: testRuntime,
	}
	state.page = review
	state.status = testStableStatus
	state.mutationOutcome = testApplyCompleted
	state.observeApplicationEvent(eventMsg{sequence: 7, event: event})
	observedReview, valid := state.page.(reviewPage)
	if !valid || observedReview.plan.status != statusReady || state.status != testStableStatus ||
		state.mutationOutcome != testApplyCompleted ||
		len(state.timeline.entries) != 1 ||
		state.timeline.latestCorrelated(review.correlation) != string(application.EventPlanPrepared) {
		t.Fatalf("review observation changed flow state: %#v", state)
	}

	state.page = detailsPage{review: review}
	state.observeApplicationEvent(eventMsg{sequence: 7, event: event})
	state.page = confirmationPage{review: review}
	state.observeApplicationEvent(eventMsg{sequence: 7, event: event})
	state.page = healthConfirmationPage{review: review}
	state.observeApplicationEvent(eventMsg{sequence: 7, event: event})
	state.page = homePage{}
	state.observeApplicationEvent(eventMsg{sequence: 7, event: event})
	if len(state.timeline.entries) != 5 || state.timeline.entries[1].outcome != observationCorrelated ||
		state.timeline.entries[2].outcome != observationCorrelated ||
		state.timeline.entries[3].outcome != observationCorrelated ||
		state.timeline.entries[4].outcome != observationStale {
		t.Fatalf("page correlations = %#v", state.timeline.entries)
	}
}

func TestModelRoutesPageNavigationAndContext(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	deliver(t, state, state.startCatalog())
	state.handleKey(key("down"))
	state.handleKey(key("up"))
	state.handleKey(key("k"))
	state.handleKey(key("j"))
	state.handleKey(key("tab"))
	state.handleKey(key("down"))
	state.handleKey(key("enter"))
	if _, valid := state.page.(openPathPage); !valid {
		t.Fatalf("home navigation page = %T", state.page)
	}
	state.handleKey(key("esc"))
	state.finishOperation()
	state.page = openPathPage{}
	state.handleKey(key("enter"))
	if state.status != "Enter a Compose path" {
		t.Fatalf("empty path status = %q", state.status)
	}

	review := reviewPage{plan: planView{status: statusReady}}
	state.page = review
	state.handleKey(key("d"))
	state.handleKey(key("down"))
	state.handleKey(key("up"))
	state.handleKey(key("d"))
	state.handleKey(key("enter"))
	state.handleKey(key(keyRight))
	state.handleKey(key(keyLeft))
	state.handleKey(key(keyShiftTab))
	state.handleKey(key("esc"))
	if _, valid := state.page.(reviewPage); !valid {
		t.Fatalf("page navigation ended at %T", state.page)
	}
	state.Update(eventMsg{event: application.Event{}})
	state.Update(struct{}{})
	_, quit := state.Update(contextDoneMsg{})
	if quit == nil {
		t.Fatal("context completion did not quit")
	}
	if state.handleKey(key("ctrl+c")) == nil || state.handleKey(key("q")) == nil {
		t.Fatal("idle quit keys did not quit")
	}
}

//nolint:cyclop,funlen // The table covers every text-editing page variant that suppresses letter shortcuts.
func TestModelOpensContextualHelpOutsideTextEditing(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{
		project: testProject, service: testService, runtime: testRuntime, status: statusReady,
	}}
	state.page = review
	state.status = testStableStatus
	state.handleKey(key("?"))
	help, valid := state.page.(contextualHelpPage)
	if !valid || help.location != testProject+" / "+testService+" / "+testRuntime ||
		!strings.Contains(help.keys, "o Explore") || state.status != testStableStatus {
		t.Fatalf("contextual help = %#v, status %q", state.page, state.status)
	}
	state.handleKey(key(testUnknownValue))
	if _, valid := state.page.(contextualHelpPage); !valid {
		t.Fatalf("unknown help key changed page to %T", state.page)
	}
	state.handleKey(key("?"))
	if _, valid := state.page.(reviewPage); !valid {
		t.Fatalf("closing help returned to %T", state.page)
	}
	state.handleKey(key("?"))
	state.handleKey(key(keyEscape))
	if _, valid := state.page.(reviewPage); !valid {
		t.Fatalf("escaping help returned to %T", state.page)
	}
	state.handleKey(key("?"))
	if command := state.handleKey(key(keyQuit)); command == nil {
		t.Fatal("contextual help quit command is nil")
	}

	for _, test := range []struct {
		page page
		want bool
	}{
		{page: openPathPage{}, want: true},
		{page: addServicePage{}, want: true},
		{page: deploymentValuePage{}, want: true},
		{page: llmQuestionPage{}, want: true},
		{page: registrationPage{step: registrationRemoteStep}, want: true},
		{page: registrationPage{step: registrationModeStep}},
		{page: llmConfigurationPage{step: llmModelStep}, want: true},
		{page: llmConfigurationPage{step: llmProviderStep}},
		{page: commitPage{kind: commitKindService, editing: true}, want: true},
		{page: commitPage{kind: commitKindService}},
		{page: reviewPage{}},
	} {
		if got := pageAcceptsText(test.page); got != test.want {
			t.Fatalf("pageAcceptsText(%T) = %t, want %t", test.page, got, test.want)
		}
	}
	if pageAcceptsText(nil) {
		t.Fatal("nil page accepts text")
	}

	state.page = openPathPage{}
	state.handleKey(key("?"))
	if current := openPathPageValue(t, state); current.value != "?" {
		t.Fatalf("text input after ? = %q", current.value)
	}

	state.resize(fullMinimumWidth, fullMinimumHeight)
	state.page = confirmationPage{review: review, focus: confirmationApply}
	state.handleKey(key("?"))
	state.resize(hardMinimumWidth, hardMinimumHeight)
	if _, valid := state.page.(reviewPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("narrow help confirmation = %T, status %q", state.page, state.status)
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo,maintidx // One contract covers every page-specific keyboard boundary.
func TestModelKeyboardBoundaryContracts(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	preview := servicePreviewPage{input: testRuntimeCommand, draft: workspace.draft}
	commit := commitPage{
		kind:    commitKindService,
		preview: preview,
		staged:  workspace.staged,
		message: workspace.staged.CommitMessage,
	}
	review := reviewPage{
		request: application.Request{Service: testService},
		plan:    planView{status: statusReady},
	}

	pages := []page{
		homePage{}, openPathPage{}, sourceDiagnosticPage{}, registrationPage{},
		registrationConfirmationPage{}, addServicePage{}, servicePreviewPage{},
		stageServiceConfirmationPage{}, commitPage{}, stagedDiffPage{},
		unsignedCommitConfirmationPage{}, preparationRequiredPage{}, selectServicePage{},
		reviewPage{}, reviewOptionsPage{}, explainPage{}, detailsPage{}, confirmationPage{}, llmConfigurationPage{},
		llmSaveConfirmationPage{}, llmDiscardConfirmationPage{}, llmSaveOutcomeUnknownPage{}, llmQuestionPage{},
		llmNetworkConfirmationPage{}, llmChoicesPage{}, contextualHelpPage{},
	}
	for _, current := range pages {
		current.isPage()
	}
	ignoredPages := []page{
		homePage{catalog: CatalogSnapshot{State: CatalogReady}},
		sourceDiagnosticPage{},
		preview,
		stageServiceConfirmationPage{preview: preview},
		commit,
		stagedDiffPage{commit: commit},
		unsignedCommitConfirmationPage{commit: commit},
		preparationRequiredPage{},
		registrationConfirmationPage{},
		selectServicePage{choices: []serviceChoice{{request: application.Request{Service: testAPI}}}},
		confirmationPage{review: review},
	}
	for _, current := range ignoredPages {
		state.page = current
		if command := state.handleKey(key("x")); command != nil {
			t.Fatalf("%T handled an unknown key", current)
		}
	}
	for _, name := range []string{"k", "j", keyEnter} {
		state.page = sourceDiagnosticPage{previous: homePage{}}
		state.handleKey(key(name))
	}
	state.handleDetailsKey(detailsPage{}, testUnknownValue)
	state.busy = true
	if command, handled := state.handleSessionKey(testUnknownValue); command != nil || !handled {
		t.Fatalf("busy non-control key = %#v, %t", command, handled)
	}
	if command, handled := state.handleSessionKey("x"); command != nil || !handled {
		t.Fatalf("busy unknown key = %#v, %t", command, handled)
	}
	state.cancel = nil
	state.requestCancellation()
	state.busy = false

	state.page = nil
	if command := state.handleKey(key("x")); command != nil {
		t.Fatal("unknown page handled a key")
	}

	state.page = homePage{catalog: CatalogSnapshot{State: CatalogReady}}
	if command := state.handleKey(key("r")); command == nil {
		t.Fatal("home refresh command is nil")
	}
	state.finishOperation()
	state.page = homePage{catalog: CatalogSnapshot{State: CatalogReady}}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("home quit command is nil")
	}
	state.activateAddService(CatalogSnapshot{State: CatalogMissing})
	if state.status != "Set up a desired-state repository before adding a service" {
		t.Fatalf("missing-repository Add service status = %q", state.status)
	}
	state.activateAddService(CatalogSnapshot{State: CatalogMissing, SuggestedRepository: testRepositoryPath})
	if _, valid := state.page.(registrationPage); !valid {
		t.Fatalf("suggested repository page = %T", state.page)
	}

	state.page = openPathPage{}
	state.handleKey(key("backspace"))
	state.handleKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "\x00"}))
	if got := openPathPageValue(t, state).value; got != "" {
		t.Fatalf("rejected path input = %q", got)
	}
	state.page = openPathPage{value: strings.Repeat("x", maximumDisplayBytes)}
	state.handleKey(key("x"))
	if got := openPathPageValue(t, state).value; len(got) != maximumDisplayBytes {
		t.Fatalf("oversized path input length = %d", len(got))
	}
	state.page = openPathPage{}
	if command := state.handleKey(key("esc")); command == nil {
		t.Fatal("open-path back command is nil")
	}
	state.finishOperation()

	state.page = registrationPage{step: registrationRemoteStep, mode: RepositorySetupExisting}
	state.handleKey(key("enter"))
	if state.status != "Enter a repository name or remote URL" {
		t.Fatalf("empty registration status = %q", state.status)
	}
	state.handleKey(key("backspace"))
	state.handleKey(key("x"))
	state.handleKey(key("backspace"))
	registration, valid := state.page.(registrationPage)
	if !valid || registration.remote != "" {
		t.Fatalf("edited registration page = %#v", state.page)
	}

	state.page = addServicePage{}
	state.handleKey(key("enter"))
	if state.status != "Enter a fixed image URI or a complete runtime command" {
		t.Fatalf("empty Add service status = %q", state.status)
	}
	if command := state.handleKey(key("q")); command != nil {
		t.Fatal("Add service text input returned a command")
	}
	inputPage, valid := state.page.(addServicePage)
	if !valid || inputPage.value != "q" {
		t.Fatalf("Add service text input = %#v", state.page)
	}
	state.page = addServicePage{}
	if command := state.handleKey(key("esc")); command == nil {
		t.Fatal("Add service back command is nil")
	}
	state.finishOperation()

	state.resize(hardMinimumWidth, hardMinimumHeight)
	state.page = preview
	state.handleKey(key("enter"))
	if state.status != "Resize to review the file mutation" {
		t.Fatalf("small preview status = %q", state.status)
	}
	state.resize(defaultWidth, defaultHeight)
	state.page = preview
	state.handleKey(key("esc"))
	addService, valid := state.page.(addServicePage)
	if !valid || addService.value != preview.input {
		t.Fatalf("restored Add service page = %#v", state.page)
	}
	state.page = preview
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("service preview quit command is nil")
	}

	state.resize(hardMinimumWidth, hardMinimumHeight)
	state.page = stageServiceConfirmationPage{preview: preview, focus: confirmationApply}
	state.handleKey(key("enter"))
	if _, valid := state.page.(servicePreviewPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("small stage confirmation = %#v", state)
	}
	state.resize(defaultWidth, defaultHeight)
	for _, name := range []string{keyLeft, keyRight, keyShiftTab} {
		state.page = stageServiceConfirmationPage{preview: preview}
		state.handleKey(key(name))
		confirmation, valid := state.page.(stageServiceConfirmationPage)
		if !valid || confirmation.focus != confirmationApply {
			t.Fatalf("stage confirmation %s did not change focus", name)
		}
	}
	state.page = stageServiceConfirmationPage{preview: preview}
	state.handleKey(key("esc"))
	state.page = stageServiceConfirmationPage{preview: preview}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("stage confirmation quit command is nil")
	}

	for _, name := range []string{keyLeft, keyRight, keyShiftTab} {
		state.page = commit
		state.handleKey(key(name))
		current := commitPageValue(t, state)
		if current.focus != confirmationApply {
			t.Fatalf("commit review %s did not change focus", name)
		}
	}
	state.page = commit
	state.handleKey(key("up"))
	state.handleKey(key("down"))
	state.handleKey(key("k"))
	state.handleKey(key("j"))
	state.handleKey(key("e"))
	state.handleKey(key("esc"))
	if current := commitPageValue(t, state); current.editing {
		t.Fatal("commit message remained in edit mode")
	}
	current := commitPageValue(t, state)
	current.editing = true
	current.message = strings.Repeat("x", maximumCommitMessageBytes)
	state.page = current
	state.handleKey(key("x"))
	if got := commitPageValue(t, state).message; len(got) != maximumCommitMessageBytes {
		t.Fatalf("oversized commit message length = %d", len(got))
	}
	state.page = commit
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("commit review quit command is nil")
	}
	state.page = commit
	deliver(t, state, state.handleKey(key("esc")))
	if _, valid := state.page.(servicePreviewPage); !valid {
		t.Fatalf("saved-draft commit page = %T", state.page)
	}

	state.page = stagedDiffPage{commit: commit, scroll: 1}
	state.handleKey(key("up"))
	state.handleKey(key("down"))
	state.handleKey(key("k"))
	state.handleKey(key("j"))
	state.handleKey(key("esc"))
	state.page = stagedDiffPage{commit: commit}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("staged diff quit command is nil")
	}

	state.resize(hardMinimumWidth, hardMinimumHeight)
	state.page = unsignedCommitConfirmationPage{commit: commit, focus: confirmationApply}
	state.handleKey(key("enter"))
	if _, valid := state.page.(commitPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("small unsigned confirmation = %#v", state)
	}
	state.resize(defaultWidth, defaultHeight)
	for _, name := range []string{keyLeft, keyRight, keyShiftTab} {
		state.page = unsignedCommitConfirmationPage{commit: commit}
		state.handleKey(key(name))
		confirmation, valid := state.page.(unsignedCommitConfirmationPage)
		if !valid || confirmation.focus != confirmationApply {
			t.Fatalf("unsigned confirmation %s did not change focus", name)
		}
	}
	state.page = unsignedCommitConfirmationPage{commit: commit}
	state.handleKey(key("esc"))
	state.page = unsignedCommitConfirmationPage{commit: commit}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("unsigned confirmation quit command is nil")
	}

	registration = registrationPage{
		step: registrationCheckoutStep, mode: RepositorySetupExisting,
		remote: testExistingRemote, checkout: testRepositoryPath,
	}
	state.resize(hardMinimumWidth, hardMinimumHeight)
	state.page = registrationConfirmationPage{registration: registration, focus: confirmationApply}
	state.handleKey(key("enter"))
	if _, valid := state.page.(registrationPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("small registration confirmation = %#v", state)
	}
	state.resize(defaultWidth, defaultHeight)
	for _, name := range []string{keyLeft, keyRight, keyShiftTab} {
		state.page = registrationConfirmationPage{registration: registration}
		state.handleKey(key(name))
		registrationConfirmation, valid := state.page.(registrationConfirmationPage)
		if !valid || registrationConfirmation.focus != confirmationApply {
			t.Fatalf("registration confirmation %s did not change focus", name)
		}
	}
	state.page = registrationConfirmationPage{registration: registration, focus: confirmationApply}
	state.handleKey(key("tab"))
	state.page = registrationConfirmationPage{registration: registration}
	state.handleKey(key("esc"))
	state.page = registrationConfirmationPage{registration: registration}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("registration confirmation quit command is nil")
	}

	choices := selectServicePage{choices: []serviceChoice{{request: application.Request{Service: testAPI}}}}
	state.page = choices
	state.handleKey(key("k"))
	state.handleKey(key("j"))
	state.handleKey(key("tab"))
	state.handleKey(key("esc"))
	state.page = choices
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("service choice quit command is nil")
	}

	state.page = review
	if command := state.handleKey(key("r")); command == nil {
		t.Fatal("review refresh command is nil")
	}
	state.finishOperation()
	state.page = review
	if command := state.handleKey(key("esc")); command == nil {
		t.Fatal("review back command is nil")
	}
	state.finishOperation()
	state.page = review
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("review quit command is nil")
	}

	state.page = detailsPage{review: review, scroll: 1}
	state.handleKey(key("k"))
	state.handleKey(key("j"))
	state.handleKey(key("esc"))
	state.page = detailsPage{review: review}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("details quit command is nil")
	}

	state.resize(hardMinimumWidth, hardMinimumHeight)
	state.page = confirmationPage{review: review, focus: confirmationApply}
	state.handleKey(key("enter"))
	if _, valid := state.page.(reviewPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("small apply confirmation = %#v", state)
	}
	state.resize(defaultWidth, defaultHeight)
	state.page = confirmationPage{review: review, focus: confirmationApply}
	state.handleKey(key("tab"))
	confirmation, valid := state.page.(confirmationPage)
	if !valid || confirmation.focus != confirmationBack {
		t.Fatal("apply confirmation did not restore Back focus")
	}
	state.page = confirmationPage{review: review}
	state.handleKey(key("esc"))
	state.page = confirmationPage{review: review}
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("apply confirmation quit command is nil")
	}
}

//nolint:cyclop,funlen,gocognit,gocyclo,maintidx // Result variants define the model's containment boundary.
func TestModelContainsWorkspaceResultsAndCanonicalInputs(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	preview := servicePreviewPage{input: testRuntimeCommand, draft: workspace.draft}
	commit := commitPage{
		kind: commitKindService, preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage,
	}

	state.sequence = 10
	state.status = testStableStatus
	for _, message := range []tea.Msg{
		servicePreviewResultMsg{sequence: 9, err: errTestTUI},
		serviceStageResultMsg{sequence: 9, err: errTestTUI},
		serviceCommitResultMsg{sequence: 9, err: errTestTUI},
		serviceSuspendResultMsg{sequence: 9, err: errTestTUI},
		registrationResultMsg{sequence: 9, result: RegistrationResult{Failure: RepositorySetupInvalidInput}},
	} {
		state.Update(message)
	}
	if state.status != testStableStatus || state.err != nil {
		t.Fatalf("stale workspace results changed state = %#v", state)
	}

	for _, run := range []func(uint64) tea.Msg{
		func(sequence uint64) tea.Msg {
			return servicePreviewResultMsg{sequence: sequence, err: errTestTUI}
		},
		func(sequence uint64) tea.Msg {
			return serviceStageResultMsg{sequence: sequence, err: errTestTUI}
		},
		func(sequence uint64) tea.Msg {
			return serviceCommitResultMsg{sequence: sequence, err: errTestTUI}
		},
		func(sequence uint64) tea.Msg {
			return serviceSuspendResultMsg{sequence: sequence, err: errTestTUI}
		},
	} {
		state.sequence++
		state.busy = true
		state.Update(run(state.sequence))
		if !errors.Is(state.err, errTestTUI) || state.status != "Operation failed" {
			t.Fatalf("workspace failure state = %#v", state)
		}
	}

	state.sequence++
	state.busy = true
	state.Update(servicePreviewResultMsg{sequence: state.sequence, input: preview.input, draft: ServiceDraft{}})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Generated service could not be displayed safely" {
		t.Fatalf("invalid preview result = %#v", state)
	}
	state.sequence++
	state.busy = true
	state.Update(serviceStageResultMsg{sequence: state.sequence, preview: preview, staged: StagedService{}})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Staged change could not be displayed safely" {
		t.Fatalf("invalid stage result = %#v", state)
	}
	state.sequence++
	state.busy = true
	state.Update(serviceCommitResultMsg{sequence: state.sequence, commit: commit})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Commit result could not be verified" {
		t.Fatalf("unverified commit result = %#v", state)
	}

	state.sequence++
	state.busy = true
	state.Update(serviceSuspendResultMsg{sequence: state.sequence, preview: preview})
	if _, valid := state.page.(servicePreviewPage); !valid || state.status != "Draft saved for this service" {
		t.Fatalf("suspend result = %#v", state)
	}
	workspace.suspendErr = errTestTUI
	deliver(t, state, state.startServiceSuspend(preview))
	if !errors.Is(state.err, errTestTUI) || state.status != "Operation failed" {
		t.Fatalf("suspend failure = %#v", state)
	}

	state.sequence++
	state.busy = true
	state.Update(registrationResultMsg{
		sequence: state.sequence,
		result:   RegistrationResult{Failure: RepositorySetupUnavailable},
	})
	if state.status != repositorySetupUnavailableStatus {
		t.Fatalf("registration failure status = %q", state.status)
	}

	state.sequence++
	state.busy = true
	state.page = homePage{}
	state.Update(applyResultMsg{sequence: state.sequence})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Apply result could not be refreshed" {
		t.Fatalf("apply result on invalid page = %#v", state)
	}
	state.sequence++
	state.busy = true
	state.quitAfterOperation = true
	state.page = reviewPage{request: application.Request{Service: testService}}
	_, command := state.Update(applyResultMsg{sequence: state.sequence})
	if command == nil || state.status != testApplyCompleted {
		t.Fatalf("apply-and-quit result = %#v", state)
	}

	unsafe := strings.Repeat("x", displayLimits().Bytes+1)
	for _, draft := range []ServiceDraft{
		{Runtime: unsafe, Image: testImage, Service: testService, ComposePath: testComposePath},
		{Runtime: testRuntime, Image: unsafe, Service: testService, ComposePath: testComposePath},
		{Runtime: testRuntime, Image: testImage, Service: unsafe, ComposePath: testComposePath},
		{Runtime: testRuntime, Image: testImage, Service: testService, ComposePath: unsafe},
		{Runtime: testRuntime, Image: testImage, Service: testService, ComposePath: testComposePath, Preparation: unsafe},
		{Runtime: "", Image: testImage, Service: testService, ComposePath: testComposePath},
		{Runtime: testRuntime, Image: testImage, Service: testService, ComposePath: testComposePath, WarningCount: -1},
	} {
		if _, err := canonicalServiceDraft(draft); err == nil {
			t.Fatalf("canonicalServiceDraft(%#v) succeeded", draft)
		}
	}

	validStaged := workspace.staged
	for _, staged := range []StagedService{
		{ComposePath: unsafe, CommitMessage: testCommitMessage, Diff: testDiff},
		{ComposePath: testComposePath, Preparation: unsafe, CommitMessage: testCommitMessage, Diff: testDiff},
		{ComposePath: testComposePath, CommitMessage: unsafe, Diff: testDiff},
		{ComposePath: testComposePath, CommitMessage: strings.Repeat("x", maximumCommitMessageBytes+1), Diff: testDiff},
		{ComposePath: testComposePath, CommitMessage: testCommitMessage, Diff: unsafe},
		{ComposePath: testComposePath, CommitMessage: testCommitMessage},
	} {
		if _, err := canonicalStagedService(staged); err == nil {
			t.Fatalf("canonicalStagedService(%#v) succeeded", staged)
		}
	}
	if staged, err := canonicalStagedService(validStaged); err != nil || staged != validStaged {
		t.Fatalf("canonicalStagedService(valid) = %#v, %v", staged, err)
	}

	for _, target := range []Target{
		{Project: unsafe, Service: testService, Runtime: testRuntime},
		{Project: testProject, Service: unsafe, Runtime: testRuntime},
		{Project: testProject, Service: testService, Runtime: unsafe},
	} {
		if _, err := canonicalChoices([]Target{target}); err == nil {
			t.Fatalf("canonicalChoices(%#v) succeeded", target)
		}
	}

	operations.snapshot = application.OperationSnapshot{Plan: testPlan()}
	operations.snapshot.Plan.Kind = application.PlanUnchanged
	operations.snapshot.Plan.Platform.Variant = "v8"
	operations.snapshot.Plan.Warnings = []application.Warning{{}}
	view, err := projectPlan(operations.snapshot)
	if err != nil || view.current != "Not deployed" || view.status != "No runtime change needed" ||
		view.platform != "linux/amd64/v8" || view.warningText == "" {
		t.Fatalf("projectPlan(unchanged) = %#v, %v", view, err)
	}

	for _, snapshot := range []CatalogSnapshot{
		{State: CatalogReady},
		{State: CatalogMissing},
		{State: CatalogUnavailable},
		{State: CatalogState(testUnknownValue)},
	} {
		if catalogMessage(snapshot) == "" {
			t.Fatalf("catalogMessage(%#v) is empty", snapshot)
		}
	}
	if got := blockerMessage(SourceBlocker(testUnknownValue)); got != testBlockerMessage {
		t.Fatalf("blockerMessage(unknown) = %q", got)
	}
	if got := blockerMessage(BlockerNone); got != testBlockerMessage {
		t.Fatalf("blockerMessage(none) = %q", got)
	}
}

//nolint:cyclop // Assertions cover each catalog and open-result containment boundary.
func TestModelCanonicalCatalogAndOpenResultBoundaries(t *testing.T) {
	t.Parallel()

	unsafe := strings.Repeat("x", displayLimits().Bytes+1)
	for _, service := range []Service{
		{Location: unsafe, Project: testProject, Name: testService, Runtime: testRuntime},
		{Location: testPlaceholderPath, Project: unsafe, Name: testService, Runtime: testRuntime},
		{Location: testPlaceholderPath, Project: testProject, Name: unsafe, Runtime: testRuntime},
		{Location: testPlaceholderPath, Project: testProject, Name: testService, Runtime: unsafe},
	} {
		catalog := canonicalCatalog(CatalogSnapshot{State: CatalogReady, Services: []Service{service}})
		if catalog.Services[0].Blocker != BlockerInvalid || catalog.Services[0].Location != "Invalid source" {
			t.Fatalf("canonicalCatalog(%#v) = %#v", service, catalog)
		}
	}
	catalog := canonicalCatalog(CatalogSnapshot{State: CatalogReady, SuggestedRepository: unsafe})
	if catalog.State != CatalogUnavailable || catalog.SuggestedRepository != "" {
		t.Fatalf("unsafe suggested repository = %#v", catalog)
	}
	diagnostic := SourceDiagnostic{File: testServicePath, Reason: DiagnosticYAMLSyntax}
	catalog = canonicalCatalog(CatalogSnapshot{State: CatalogReady, Services: []Service{{
		Location: testServicePath, Project: testProject, Name: testService, Runtime: testRuntime,
		Blocker: BlockerInvalid, Diagnostic: diagnostic,
	}}})
	if catalog.Services[0].Diagnostic != diagnostic {
		t.Fatalf("safe source diagnostic = %#v", catalog.Services[0].Diagnostic)
	}

	state, _, _ := newTestModel(t)
	state.page = homePage{catalog: catalog}
	state.handleKey(key("enter"))
	if _, valid := state.page.(sourceDiagnosticPage); !valid {
		t.Fatalf("blocked home diagnostic page = %T", state.page)
	}

	state.sequence++
	state.busy = true
	state.page = openPathPage{}
	state.Update(openResultMsg{sequence: state.sequence, result: OpenResult{}})
	if !errors.Is(state.err, errInvalidInput) || state.status != "Compose source could not be displayed safely" {
		t.Fatalf("empty open result = %#v", state)
	}
	for _, target := range []Target{
		{Project: unsafe, Service: testService, Runtime: testRuntime},
		{Project: testProject, Service: unsafe, Runtime: testRuntime},
		{Project: testProject, Service: testService, Runtime: unsafe},
	} {
		state.sequence++
		state.busy = true
		state.Update(openResultMsg{sequence: state.sequence, result: OpenResult{Targets: []Target{target}}})
		if !errors.Is(state.err, errInvalidInput) {
			t.Fatalf("unsafe open target = %#v", state)
		}
	}
}

//nolint:cyclop // Assertions cover independent page transition boundaries.
func TestModelRemainingStateTransitionBoundaries(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	preview := servicePreviewPage{input: testRuntimeCommand, draft: workspace.draft}
	commit := commitPage{
		kind: commitKindService, preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage,
	}
	registration := registrationPage{
		step: registrationCheckoutStep, mode: RepositorySetupExisting,
		remote: testExistingRemote, checkout: testRepositoryPath,
	}

	state.page = registrationConfirmationPage{registration: registration}
	state.invalidateConfirmation()
	if _, valid := state.page.(registrationPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("invalidated registration = %#v", state)
	}
	commit.editing = true
	commit.focus = confirmationApply
	state.page = commit
	state.invalidateConfirmation()
	if current := commitPageValue(t, state); current.editing || current.focus != confirmationBack {
		t.Fatalf("invalidated commit review = %#v", current)
	}

	state.page = homePage{catalog: CatalogSnapshot{State: CatalogReady, Services: []Service{{
		ID: "blocked", Location: "services/blocked.yaml", Blocker: BlockerInvalid,
	}}}}
	state.handleKey(key("enter"))
	if state.status != testBlockerMessage {
		t.Fatalf("blocked service status = %q", state.status)
	}

	if got := editSingleLine(testStableStatus, tea.KeyPressMsg(tea.Key{Code: 'x', Text: "\n"})); got != testStableStatus {
		t.Fatalf("control edit = %q", got)
	}
	oversized := strings.Repeat("x", maximumDisplayBytes)
	if got := editSingleLine(oversized, key("x")); got != oversized {
		t.Fatalf("oversized edit length = %d", len(got))
	}
	if got := toggledConfirmationFocus(confirmationApply); got != confirmationBack {
		t.Fatalf("toggle Apply = %d", got)
	}

	commit.focus = confirmationBack
	commit.editing = false
	state.page = commit
	command := state.handleKey(key("enter"))
	if command == nil {
		t.Fatalf("Back-and-save command is nil: %#v", state)
	}
	deliver(t, state, command)
	if _, valid := state.page.(servicePreviewPage); !valid {
		t.Fatalf("Back-and-save page = %T", state.page)
	}
}

func TestCommitPageRejectsUnknownOwner(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	invalid := commitPage{kind: commitKind(255)}
	for _, current := range []page{
		invalid,
		stagedDiffPage{commit: invalid},
		unsignedCommitConfirmationPage{commit: invalid},
	} {
		state.page = current
		if _, handled := state.handleCommitPageKey(key(keyEnter)); handled {
			t.Fatalf("handleCommitPageKey(%T) accepted unknown owner", current)
		}
		if _, valid := state.commitPageBody(80, 24); valid {
			t.Fatalf("commitPageBody(%T) accepted unknown owner", current)
		}
		if _, valid := state.commitFooter(); valid {
			t.Fatalf("commitFooter(%T) accepted unknown owner", current)
		}
	}
	if command := state.startCommit(invalid, false); command != nil || !errors.Is(state.err, errInvalidInput) {
		t.Fatalf("startCommit(unknown) = %v, %v", command, state.err)
	}
	state.err = nil
	if command := state.startCommitDiscard(invalid); command != nil || !errors.Is(state.err, errInvalidInput) {
		t.Fatalf("startCommitDiscard(unknown) = %v, %v", command, state.err)
	}
}
