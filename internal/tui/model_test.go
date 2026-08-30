package tui

import (
	"context"
	"errors"
	"slices"
	"strings"
	"sync"
	"testing"

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

	state.handleKey(key("a"))
	if len(operations.recordedCalls()) != 2 {
		t.Fatal("review shortcut bypassed confirmation")
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
	if _, valid = state.page.(reviewPage); !valid || state.mutationOutcome != "Apply completed" ||
		!slices.Equal(operations.recordedCalls(), []string{
			snapshotCall, evidenceCall, "apply", snapshotCall, evidenceCall,
		}) {
		t.Fatalf("post-apply state = %#v, calls = %q", state, operations.recordedCalls())
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
	workspace.commit = ServiceCommitResult{NeedsUnsignedApproval: true}
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
	commit, valid := state.page.(commitServicePage)
	if !valid || commit.focus != confirmationBack || commit.message != "Add api service" {
		t.Fatalf("commit review = %#v", state.page)
	}

	state.handleKey(key("e"))
	state.handleKey(key("!"))
	state.handleKey(key("enter"))
	state.handleKey(key("d"))
	state.handleKey(key("down"))
	state.handleKey(key("d"))
	commit = commitServicePageValue(t, state)
	if commit.message != "Add api service!" || commit.editing {
		t.Fatalf("edited commit = %#v", commit)
	}
	state.handleKey(key("tab"))
	deliver(t, state, state.handleKey(key("enter")))
	unsigned, valid := state.page.(unsignedCommitConfirmationPage)
	if !valid || unsigned.focus != confirmationBack {
		t.Fatalf("unsigned confirmation = %#v", state.page)
	}
	state.handleKey(key("enter"))
	if _, valid = state.page.(commitServicePage); !valid {
		t.Fatalf("unsigned default back page = %T", state.page)
	}

	deliver(t, state, state.handleKey(key("enter")))
	workspace.commit = ServiceCommitResult{
		Request: application.Request{Service: testAPI}, Committed: true,
	}
	state.handleKey(key("tab"))
	deliver(t, state, state.handleKey(key("enter")))
	if _, valid = state.page.(reviewPage); !valid || state.mutationOutcome != "Compose commit created" {
		t.Fatalf("validated commit state = %#v", state)
	}
	if !slices.Equal(workspace.recordedCalls(), []string{
		"preview:" + input,
		"stage",
		"commit:false:Add api service!",
		"commit:false:Add api service!",
		"commit:true:Add api service!",
	}) || !slices.Equal(operations.recordedCalls(), []string{"dry-run", snapshotCall, evidenceCall}) {
		t.Fatalf("calls = %q / %q", workspace.recordedCalls(), operations.recordedCalls())
	}
}

func TestModelRejectsCommittedPlanDrift(t *testing.T) {
	t.Parallel()

	state, _, operations := newTestModel(t)
	operations.dryRunPlan.Service = "changed"
	deliver(t, state, state.startCommittedSnapshot(application.Request{Service: testAPI}))
	if !errors.Is(state.err, errInvalidInput) || state.status != "Committed service changed during validation" ||
		!slices.Equal(operations.recordedCalls(), []string{"dry-run", snapshotCall, evidenceCall}) {
		t.Fatalf("drift state = %#v, calls = %q", state, operations.recordedCalls())
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
	if !valid || registration.value != repository {
		t.Fatalf("first-run page = %#v", state.page)
	}

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
		state.mutationOutcome != "Repository created" ||
		!slices.Equal(catalog.recordedCalls(), []string{snapshotCall, "register:" + repository}) {
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
	if registration, valid := state.page.(registrationPage); !valid || registration.value != repository {
		t.Fatalf("reopened setup = %#v", state.page)
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
		BlockerInvalid:     "Compose source did not pass validation",
		BlockerUnavailable: "Compose source is unavailable",
	} {
		state.handleOpenResult(openResultMsg{sequence: state.sequence, result: OpenResult{Blocker: blocker}})
		if state.status != message {
			t.Fatalf("blocker %q status = %q", blocker, state.status)
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
	if state.busy || state.status != "Cancelled" || state.err != nil {
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
	state.status = "stable"
	state.sequence = 10
	state.Update(snapshotResultMsg{sequence: 9, err: errTestTUI})
	state.Update(applyResultMsg{sequence: 9, err: errTestTUI})
	state.Update(openResultMsg{sequence: 9, result: OpenResult{Blocker: BlockerInvalid}})
	state.Update(catalogResultMsg{sequence: 9, snapshot: CatalogSnapshot{State: CatalogUnavailable}})
	if state.err != nil || state.status != "stable" {
		t.Fatalf("stale results changed state = %#v", state)
	}

	state.sequence = 11
	state.busy = true
	state.Update(snapshotResultMsg{sequence: 11, err: context.Canceled})
	if state.err != nil || state.status != "Cancelled" {
		t.Fatalf("cancelled result = %#v", state)
	}
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

	state.resize(defaultWidth, defaultHeight)
	preview := servicePreviewPage{draft: ServiceDraft{
		Runtime: testRuntime, Image: "image", Service: testService, ComposePath: testServicePath,
	}}
	state.page = stageServiceConfirmationPage{preview: preview, focus: confirmationApply}
	state.resize(hardMinimumWidth, hardMinimumHeight)
	if _, valid := state.page.(servicePreviewPage); !valid || state.status != statusReviewLarger {
		t.Fatalf("stage hard-floor state = %#v", state)
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
	state.handleKey(key("right"))
	state.handleKey(key("left"))
	state.handleKey(key("shift+tab"))
	state.handleKey(key("esc"))
	if _, valid := state.page.(reviewPage); !valid {
		t.Fatalf("page navigation ended at %T", state.page)
	}
	state.Update(eventMsg(application.Event{}))
	state.Update(struct{}{})
	_, quit := state.Update(contextDoneMsg{})
	if quit == nil {
		t.Fatal("context completion did not quit")
	}
	if state.handleKey(key("ctrl+c")) == nil || state.handleKey(key("q")) == nil {
		t.Fatal("idle quit keys did not quit")
	}
}
