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
	if _, valid = state.page.(reviewPage); !valid || state.mutationOutcome != testApplyCompleted ||
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
	if !valid || commit.focus != confirmationBack || commit.message != "Add api service" ||
		commit.diffWidth != state.serviceDiffWidth() || len(commit.diffLines) == 0 {
		t.Fatalf("commit review = %#v", state.page)
	}
	fullWidth := commit.diffWidth
	state.resize(compactMinimum, compactMinHeight)
	commit = commitServicePageValue(t, state)
	if commit.diffWidth != state.serviceDiffWidth() || commit.diffWidth == fullWidth || len(commit.diffLines) == 0 {
		t.Fatalf("resized commit diff = %#v", commit)
	}
	state.resize(defaultWidth, defaultHeight)

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
	state.resize(compactMinimum, compactMinHeight)
	unsigned, valid = state.page.(unsignedCommitConfirmationPage)
	if !valid || unsigned.commit.diffWidth != state.serviceDiffWidth() {
		t.Fatalf("resized unsigned confirmation = %#v", state.page)
	}
	state.resize(defaultWidth, defaultHeight)
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
	}) || !slices.Equal(operations.recordedCalls(), []string{dryRunCall, snapshotCall, evidenceCall}) {
		t.Fatalf("calls = %q / %q", workspace.recordedCalls(), operations.recordedCalls())
	}
}

func TestModelCachesServiceDiffAtDefaultDimensions(t *testing.T) {
	t.Parallel()

	state := &model{}
	commit := state.wrapServiceDiff(commitServicePage{staged: StagedService{Diff: "+image: pinned\n"}})
	cached := state.wrapServiceDiff(commit)
	if commit.diffWidth != state.serviceDiffWidth() || cached.diffWidth != commit.diffWidth ||
		!slices.Equal(cached.diffLines, commit.diffLines) {
		t.Fatalf("cached service diff = %#v, want %#v", cached, commit)
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

func TestModelContainsPartialAndContradictoryCommitResults(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	preview := servicePreviewPage{input: testRuntimeCommand, draft: workspace.draft}
	commit := commitServicePage{preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage}

	state.sequence++
	state.busy = true
	state.Update(serviceCommitResultMsg{
		sequence: state.sequence,
		commit:   commit,
		result: ServiceCommitResult{
			Committed: true, ValidationUnavailable: true,
		},
	})
	home, valid := state.page.(homePage)
	if !valid || home.catalog.State != CatalogUnavailable ||
		state.status != "Commit created; validation is unavailable" ||
		state.mutationOutcome != "Compose commit created" || state.err != nil {
		t.Fatalf("partial commit result = %#v", state)
	}

	for _, result := range []ServiceCommitResult{
		{Committed: true, NeedsUnsignedApproval: true},
		{ValidationUnavailable: true},
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
	commit := commitServicePage{preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage}
	state.sequence++
	state.busy = true
	state.Update(serviceCommitResultMsg{
		sequence: state.sequence,
		commit:   commit,
		result: ServiceCommitResult{
			Committed: true, PreparationRequired: true,
		},
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
	if state.busy || state.applying || state.status != "Cancelled" || state.err != nil {
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
		Runtime: testRuntime, Image: testImage, Service: testService, ComposePath: testServicePath,
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
	state.handleKey(key(keyRight))
	state.handleKey(key(keyLeft))
	state.handleKey(key(keyShiftTab))
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

//nolint:cyclop,funlen,gocognit,gocyclo,maintidx // One contract covers every page-specific keyboard boundary.
func TestModelKeyboardBoundaryContracts(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	workspace := workspaceFixtureValue(t, state)
	preview := servicePreviewPage{input: testRuntimeCommand, draft: workspace.draft}
	commit := commitServicePage{
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
		stageServiceConfirmationPage{}, commitServicePage{}, stagedDiffPage{},
		unsignedCommitConfirmationPage{}, preparationRequiredPage{}, selectServicePage{},
		reviewPage{}, detailsPage{}, confirmationPage{},
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
		review,
		detailsPage{review: review},
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
	state.busy = true
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
	if command := state.handleKey(key("q")); command == nil {
		t.Fatal("Add service quit command is nil")
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
		current := commitServicePageValue(t, state)
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
	if current := commitServicePageValue(t, state); current.editing {
		t.Fatal("commit message remained in edit mode")
	}
	current := commitServicePageValue(t, state)
	current.editing = true
	current.message = strings.Repeat("x", maximumCommitMessageBytes)
	state.page = current
	state.handleKey(key("x"))
	if got := commitServicePageValue(t, state).message; len(got) != maximumCommitMessageBytes {
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
	if _, valid := state.page.(commitServicePage); !valid || state.status != statusReviewLarger {
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
	commit := commitServicePage{preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage}

	state.sequence = 10
	state.status = testStableStatus
	for _, message := range []tea.Msg{
		servicePreviewResultMsg{sequence: 9, err: errTestTUI},
		serviceStageResultMsg{sequence: 9, err: errTestTUI},
		serviceCommitResultMsg{sequence: 9, err: errTestTUI},
		serviceSuspendResultMsg{sequence: 9, err: errTestTUI},
		registrationResultMsg{sequence: 9, result: RegistrationResult{Blocker: BlockerInvalid}},
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
		result:   RegistrationResult{Blocker: BlockerUnavailable},
	})
	if state.status != "Compose source is unavailable" {
		t.Fatalf("registration blocker status = %q", state.status)
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
		{State: CatalogState("unknown")},
	} {
		if catalogMessage(snapshot) == "" {
			t.Fatalf("catalogMessage(%#v) is empty", snapshot)
		}
	}
	if got := blockerMessage(SourceBlocker("unknown")); got != testBlockerMessage {
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
	commit := commitServicePage{preview: preview, staged: workspace.staged, message: workspace.staged.CommitMessage}
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
	if current := commitServicePageValue(t, state); current.editing || current.focus != confirmationBack {
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
