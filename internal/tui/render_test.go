//nolint:cyclop // The presentation matrix intentionally covers all health states in one scenario.
package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	testAddServiceMessage = "Add service"
	testCurrentLabel      = "CURRENT"
	testProposedLabel     = "PROPOSED"
	testCurrentImage      = "current"
	testProposedImage     = "proposed"
	testPreparationPath   = "services/service.prepare.sh"
	testContinueAction    = "Continue to confirmation"
	testHelpAction        = "? Help"
)

func TestViewsMatchFullCompactHardAndResizeContracts(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.page = reviewPage{plan: planView{
		kind: testUpgrade, project: testProject, service: testService, runtime: testRuntime, platform: testPlatform,
		current: strings.Repeat("current/", 20), proposed: strings.Repeat("proposed/", 20), status: statusReady,
	}}
	state.status = statusReady
	for _, test := range []struct {
		width, height int
		contains      []string
	}{
		{width: 100, height: 30, contains: []string{"FLOW", testCurrentLabel, testProposedLabel, testContinueAction}},
		{width: 80, height: 24, contains: []string{"FLOW", testCurrentLabel, testProposedLabel, testContinueAction}},
		{width: 60, height: 20, contains: []string{"project / service / docker", testCurrentLabel, testProposedLabel}},
		{width: 56, height: 16, contains: []string{
			testCurrentLabel, testProposedLabel, statusReady, testContinueAction,
		}},
		{width: 40, height: 10, contains: []string{"Status: Ready for confirmation", "r Review"}},
		{width: 20, height: 5, contains: []string{"Resize to at least"}},
	} {
		state.resize(test.width, test.height)
		content := state.View().Content
		assertBoundedView(t, content, test.width, test.height)
		for _, part := range test.contains {
			if !strings.Contains(content, part) {
				t.Fatalf("%dx%d view misses %q: %q", test.width, test.height, part, content)
			}
		}
	}
	assertFullReviewFooterFits(t, state)

	state.page = reviewPage{plan: planView{
		project: testProject, service: testService, runtime: testRuntime,
		current: testCurrentImage, proposed: testProposedImage, status: statusReady,
		warningText: "1 warning requires review",
	}}
	state.mutationOutcome = statusApplyCompleted
	state.err = errTestSecret
	state.resize(compactMinimum, compactMinHeight)
	content := state.View().Content
	assertViewContains(t, content, "compact completed review", testCurrentLabel, testCurrentImage,
		testProposedLabel, testProposedImage, statusApplyCompleted, testContinueAction, "Operation failed.",
		"maniud --debug tui")
	if strings.Contains(content, "No runtime change has started") || strings.Contains(content, errTestSecret.Error()) {
		t.Fatalf("compact completed review contains stale or unsafe copy: %q", content)
	}

	state.mutationOutcome = ""
	content = state.View().Content
	assertViewContains(t, content, "compact constrained review", testCurrentLabel, testCurrentImage,
		testProposedLabel, testProposedImage, statusReady, testContinueAction, "Operation failed.")
	if strings.Contains(content, "Review image change") || strings.Contains(content, errTestSecret.Error()) {
		t.Fatalf("compact constrained review kept expendable or unsafe copy: %q", content)
	}
}

func assertFullReviewFooterFits(t *testing.T, state *model) {
	t.Helper()

	state.resize(100, 30)
	content := state.View().Content
	_, footer, found := strings.CutLast(content, "\n")
	want := "? Help   Tab Focus  Enter Choose  o Explore  d Details  x Export  r Refresh  Esc Back  q Quit"
	if !found || strings.Count(content, "d Details") != 1 || footer != want {
		t.Fatalf("full review footer is repeated or clipped: %q", content)
	}
}

func assertViewContains(t *testing.T, content, description string, parts ...string) {
	t.Helper()
	for _, part := range parts {
		if !strings.Contains(content, part) {
			t.Fatalf("%s misses %q: %q", description, part, content)
		}
	}
}

func TestViewUsesColorUnicodeAndASCIICapabilities(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.page = reviewPage{plan: planView{
		kind: testUpgrade, project: testProject, service: testService, runtime: testRuntime, platform: testPlatform,
		current: testOldProjection, proposed: testNewProjection, status: statusReady,
	}}
	state.err = errTestSecret
	state.options = Options{Color: true, Unicode: true}
	colored := state.View().Content
	assertViewContains(t, colored, "colored Unicode view", "\x1b[", "⬟", "│", "× Operation failed.")
	state.options = Options{}
	plain := state.View().Content
	if strings.Contains(plain, "\x1b[") || strings.ContainsAny(plain, "⬟✓●○›│─…▌") {
		t.Fatalf("plain ASCII view = %q", plain)
	}
	assertViewContains(t, plain, "plain ASCII view", "[OK] ", "[*] Review", "[ ] Confirm", "[FAIL] Operation failed.")
}

func TestStatusCardUsesBoundedUnicodeAndASCIIChrome(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.options = Options{Color: true, Unicode: true}
	unicodeCard := state.statusCard(statusReady, fullMinimumWidth-fullBodyOffset)
	if len(unicodeCard) != 5 {
		t.Fatalf("Unicode statusCard() rows = %d, want 5", len(unicodeCard))
	}
	for _, line := range unicodeCard {
		if width := terminaltext.Width(line); width != fullMinimumWidth-fullBodyOffset {
			t.Fatalf("Unicode statusCard() line width = %d: %q", width, line)
		}
	}
	assertViewContains(t, strings.Join(unicodeCard, "\n"), "Unicode status card",
		"┌", "┘", "⬟ "+statusReady, "Compose validation and read-only runtime checks passed.",
		"No runtime change has started.")

	completed := strings.Join(state.statusCard(statusApplyCompleted, fullMinimumWidth-fullBodyOffset), "\n")
	if strings.Contains(completed, "No runtime change has started.") {
		t.Fatalf("completed status card contains pre-mutation copy: %q", completed)
	}

	state.options = Options{}
	asciiCard := state.statusCard(statusReady, fullMinimumWidth-fullBodyOffset)
	ascii := strings.Join(asciiCard, "\n")
	assertViewContains(t, ascii, "ASCII status card", "+", "| [OK] "+statusReady)
	if strings.ContainsAny(ascii, "┌┐└┘─│⬟") {
		t.Fatalf("ASCII status card contains Unicode chrome: %q", ascii)
	}
}

func TestFullReviewPreservesStatusCardRightBorder(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.options = Options{Unicode: true}
	state.page = reviewPage{plan: planView{
		kind: testUpgrade, project: testProject, service: testService, runtime: testRuntime, platform: testPlatform,
		current: testCurrentImage, proposed: testProposedImage, status: statusReady,
	}}
	state.resize(fullMinimumWidth, fullMinimumHeight)
	content := state.View().Content
	assertBoundedView(t, content, fullMinimumWidth, fullMinimumHeight)
	if !strings.Contains(content, "┐") || !strings.Contains(content, "┘") {
		t.Fatalf("full Review clipped the status card right border: %q", content)
	}
}

//nolint:funlen // The page table keeps the complete rendering contract visible.
func TestViewRendersEveryPageWithoutLeakingErrors(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{
		kind: testUpgrade, project: testProject, service: testService, runtime: testRuntime, platform: testPlatform,
		current: testOldProjection, proposed: testNewProjection,
		status: statusReady, warningText: "1 warning(s) require review",
	}}
	preview := servicePreviewPage{input: testRuntimeCommand, draft: ServiceDraft{
		Runtime: testRuntime, Image: strings.Repeat("registry.example/image@sha256:a", 4),
		Service: testService, ComposePath: testServicePath, Preparation: testPreparationPath,
		WarningCount: 1,
	}}
	commit := commitPage{
		kind:    commitKindService,
		preview: preview,
		staged: StagedService{
			Diff:        "diff --git a/services/service.yaml b/services/service.yaml\n+image: pinned\n",
			ComposePath: testServicePath, CommitMessage: testAddServiceMessage,
		},
		message: testAddServiceMessage, focus: confirmationBack,
	}
	pages := []struct {
		page     page
		contains string
	}{
		{page: homePage{catalog: CatalogSnapshot{State: CatalogMissing}}, contains: "Services"},
		{page: openPathPage{value: strings.Repeat("path/", 40)}, contains: "Open Compose file"},
		{page: sourceDiagnosticPage{
			previous: openPathPage{},
			diagnostic: SourceDiagnostic{
				File: testServicePath, Reason: DiagnosticComposeValidation,
			},
		}, contains: "Compose validation failed"},
		{page: newRegistrationPage("/home/user/maniud-desired-state"), contains: "Set up repository"},
		{page: registrationPage{step: ^registrationStep(0)}, contains: repositorySetupUnavailableStatus},
		{page: registrationConfirmationPage{
			registration: registrationPage{
				step: registrationCheckoutStep, mode: RepositorySetupCreateGitHub,
				remote: testGitHubRepository, checkout: "/home/user/maniud-desired-state",
			},
			focus: confirmationBack,
		}, contains: "Confirm repository setup"},
		{page: addServicePage{value: testRuntimeCommand}, contains: testAddServiceMessage},
		{page: preview, contains: "parsed, not run"},
		{page: stageServiceConfirmationPage{preview: preview, focus: confirmationBack},
			contains: "Confirm file mutation"},
		{page: commit, contains: "STAGED DIFF"},
		{page: stagedDiffPage{commit: commit}, contains: "This page is read-only"},
		{page: unsignedCommitConfirmationPage{commit: commit, focus: confirmationBack},
			contains: "Confirm unsigned commit"},
		{page: preparationRequiredPage{draft: ServiceDraft{
			Service: testService, Preparation: testPreparationPath,
		}}, contains: "Preparation required"},
		{page: selectServicePage{choices: []serviceChoice{{
			project: testProject, service: testService, runtime: testRuntime,
		}}},
			contains: "Select service"},
		{page: review, contains: "1 warning(s) require review"},
		{page: detailsPage{review: review}, contains: "Dropped events"},
		{page: contextualHelpPage{
			previous: review, location: "Home / Review", keys: "Enter Continue   q Quit",
		}, contains: "Keyboard help"},
		{page: confirmationPage{review: review, focus: confirmationApply}, contains: "Confirm apply"},
		{page: healthConfirmationPage{review: review, focus: confirmationBack},
			contains: "Confirm health decision"},
	}
	for _, test := range pages {
		state.page = test.page
		state.err = errTestSecret
		state.mutationOutcome = testApplyCompleted
		content := state.View().Content
		if !strings.Contains(content, test.contains) || strings.Contains(content, errTestSecret.Error()) {
			t.Fatalf("%T view = %q", test.page, content)
		}
	}
}

func TestContextualHelpFitsResponsiveLayouts(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	for _, test := range []struct {
		width, height int
		affordance    string
		helpContains  string
	}{
		{width: fullMinimumWidth, height: fullMinimumHeight, affordance: testHelpAction, helpContains: "o Explore"},
		{width: compactMinimum, height: compactMinHeight, affordance: testHelpAction, helpContains: "o Explore"},
		{width: hardMinimumWidth, height: hardMinimumHeight, affordance: testHelpAction, helpContains: "?/Esc Back"},
	} {
		state.page = reviewPage{plan: planView{
			project: testProject, service: testService, runtime: testRuntime, status: statusReady,
		}}
		state.resize(test.width, test.height)
		content := state.View().Content
		if !strings.Contains(content, test.affordance) {
			t.Fatalf("%dx%d review does not advertise contextual help: %q", test.width, test.height, content)
		}
		state.handleKey(key("?"))
		content = state.View().Content
		assertBoundedView(t, content, test.width, test.height)
		if !strings.Contains(content, test.helpContains) {
			t.Fatalf("%dx%d contextual help misses %q: %q", test.width, test.height, test.helpContains, content)
		}
		rail := strings.Join(state.rail(), "\n")
		for _, duplicate := range []string{"?    Close", "Esc  Back", "q    Quit"} {
			if strings.Contains(rail, duplicate) {
				t.Fatalf("%dx%d contextual rail duplicates footer action %q: %q",
					test.width, test.height, duplicate, rail)
			}
		}
	}
}

func TestTextInputViewsDoNotAdvertiseReservedShortcuts(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	pages := []page{
		openPathPage{},
		addServicePage{},
		deploymentValuePage{},
		llmQuestionPage{},
		llmConfigurationPage{step: llmModelStep},
		registrationPage{step: registrationRemoteStep},
		commitPage{kind: commitKindService, editing: true},
	}
	for _, dimensions := range [][2]int{
		{fullMinimumWidth, fullMinimumHeight},
		{compactMinimum, compactMinHeight},
		{hardMinimumWidth, hardMinimumHeight},
	} {
		state.resize(dimensions[0], dimensions[1])
		for _, current := range pages {
			state.page = current
			content := state.View().Content
			if strings.Contains(content, testHelpAction) || strings.Contains(content, "q Quit") {
				t.Fatalf("%T at %dx%d advertises a text key as a shortcut: %q",
					current, dimensions[0], dimensions[1], content)
			}
		}
	}
}

func TestDiscardConfirmationViewsDoNotAdvertiseDirectQuit(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	pages := []page{
		deploymentDraftConfirmationPage{},
		llmDiscardConfirmationPage{},
	}
	for _, dimensions := range [][2]int{
		{fullMinimumWidth, fullMinimumHeight},
		{compactMinimum, compactMinHeight},
		{hardMinimumWidth, hardMinimumHeight},
	} {
		state.resize(dimensions[0], dimensions[1])
		for _, current := range pages {
			state.page = current
			if content := state.View().Content; strings.Contains(content, "q Quit") {
				t.Fatalf("%T at %dx%d advertises unsupported direct quit: %q",
					current, dimensions[0], dimensions[1], content)
			}
		}
	}
}

func TestFullLayoutUsesApprovedBodyOffsetAndBodyOmitsFooterHints(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{
		project: testProject, service: testService, runtime: testRuntime,
		current: testCurrentImage, proposed: testProposedImage, status: statusReady,
	}}
	state.page = review
	lines := state.fullView(fullMinimumWidth, fullMinimumHeight)
	if index := strings.Index(lines[2], "Review image change"); index != fullBodyOffset {
		t.Fatalf("full body starts at cell %d, want %d: %q", index, fullBodyOffset, lines[2])
	}
	for _, duplicate := range []string{"? Help", "Esc Back", "q Quit"} {
		if rail := strings.Join(state.rail(), "\n"); strings.Contains(rail, duplicate) {
			t.Fatalf("linear rail duplicates footer action %q: %q", duplicate, rail)
		}
	}

	for _, current := range []page{
		review,
		detailsPage{review: review},
		confirmationPage{review: review},
		sourceDiagnosticPage{diagnostic: SourceDiagnostic{
			File: testServicePath, Reason: DiagnosticComposeValidation,
		}},
		llmConfigurationPage{step: llmProviderStep},
	} {
		state.page = current
		body := strings.Join(state.body(defaultWidth), "\n")
		for _, hint := range []string{"Tab Focus", "Esc Back", "Up/Down Scroll", "Enter Choose", testHelpAction} {
			if strings.Contains(body, hint) {
				t.Fatalf("%T body duplicates footer hint %q: %q", current, hint, body)
			}
		}
	}
}

func TestRenderHelpersRespectCellsAndLayoutBoundaries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		width, height int
		want          layoutTier
	}{
		{width: 80, height: 24, want: layoutFull},
		{width: 56, height: 16, want: layoutCompact},
		{width: 32, height: 8, want: layoutHardFloor},
		{width: 31, height: 8, want: layoutResize},
	} {
		if got := layoutFor(test.width, test.height); got != test.want {
			t.Fatalf("layoutFor(%d, %d) = %d, want %d", test.width, test.height, got, test.want)
		}
	}
	if got := padCells("\u754c", 4); terminaltext.Width(got) != 4 {
		t.Fatalf("padCells() = %q, width %d", got, terminaltext.Width(got))
	}
	if got := fitView([]string{"\u754c\u754c\u754c", "second", "third"}, 4, 2); got != "\u754c\u754c\nseco" {
		t.Fatalf("fitView() = %q", got)
	}
	comparison := imageComparison("current", "proposed", 3)
	if len(comparison) != 2 {
		t.Fatalf("imageComparison() = %q", comparison)
	}
	cached := []string{"cached diff"}
	if lines := stagedDiffLines(commitPage{
		diffWidth: hardMinimumWidth - detailsPadding, diffLines: cached,
	}, hardMinimumWidth); !slices.Equal(lines, cached) {
		t.Fatalf("stagedDiffLines(cached) = %q", lines)
	}
	if start, end := visibleHomeServices(20, 15, 4); start != 12 || end != 16 {
		t.Fatalf("visibleHomeServices() = %d:%d, want 12:16", start, end)
	}
}

func TestStagedDiffRenderingBoundsNarrowTerminalWork(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	state.width = 1
	state.height = 1
	if width := state.commitDiffWidth(); width != hardMinimumWidth-detailsPadding {
		t.Fatalf("commitDiffWidth() = %d, want %d", width, hardMinimumWidth-detailsPadding)
	}
	commit := state.wrapCommitDiff(commitPage{
		kind:   commitKindService,
		staged: StagedService{Diff: strings.Repeat("x", maximumStagedDiffBytes)},
	})
	if len(commit.diffLines) > maximumStagedDiffBytes/(hardMinimumWidth-detailsPadding)+1 {
		t.Fatalf("small-terminal wrapped lines = %d", len(commit.diffLines))
	}
	page := stagedDiffPage{commit: commit}
	if lines := state.stagedDiffBody(page, 1, 2); len(lines) != 2 {
		t.Fatalf("stagedDiffBody(header) returned %d visible lines, want 2", len(lines))
	}
	if lines := state.stagedDiffBody(page, 1, 5); len(lines) != 5 {
		t.Fatalf("stagedDiffBody() returned %d visible lines, want 5", len(lines))
	}
	if lines := state.commitBody(commitPage{
		kind: commitKindService, staged: commit.staged, scroll: 1000,
	}, 1); len(lines) == 0 {
		t.Fatal("commitBody(stale scroll) is empty")
	}
	page.scroll = 4
	lines := state.stagedDiffBody(page, 1, 5)
	if len(lines) != 5 || slices.Equal(lines, state.stagedDiffBody(stagedDiffPage{commit: commit}, 1, 5)) {
		t.Fatalf("scrolled stagedDiffBody() = %q", lines)
	}
	staleLine := strings.Repeat("y", fullMinimumWidth-fullBodyOffset-detailsPadding)
	stalePage := stagedDiffPage{commit: commitPage{
		kind: commitKindService, diffWidth: len(staleLine), diffLines: []string{staleLine},
	}}
	staleLines := state.stagedDiffBody(stalePage, 1, 4)
	if terminaltext.Width(staleLines[3]) != hardMinimumWidth-detailsPadding ||
		stalePage.commit.diffLines[0] != staleLine {
		t.Fatalf("stale cached diff was not clipped without mutation: %q", staleLines)
	}
}

func TestHomeServiceWindowFollowsCursor(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	services := make([]Service, 20)
	for index := range services {
		services[index] = Service{
			ID:       fmt.Sprintf("services/service-%02d.yaml", index),
			Location: fmt.Sprintf("services/service-%02d.yaml", index),
			Name:     fmt.Sprintf("service-%02d", index), Runtime: testRuntime,
		}
	}
	state.page = homePage{catalog: CatalogSnapshot{State: CatalogReady, Services: services}, cursor: 15}
	state.resize(compactMinimum, compactMinHeight)
	content := state.View().Content
	if !strings.Contains(content, "service-15") || strings.Contains(content, "service-00") {
		t.Fatalf("cursor-following home window = %q", content)
	}
}

func TestSourceDiagnosticWrapsPathWithoutEllipsis(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	path := "services/" + strings.Repeat("long-directory/", 8) + "api.yaml"
	state.page = sourceDiagnosticPage{
		previous: openPathPage{},
		diagnostic: SourceDiagnostic{
			File: path, Reason: DiagnosticYAMLSyntax, Line: 12, Column: 7,
		},
	}
	state.resize(compactMinimum, compactMinHeight)
	content := state.View().Content
	if strings.Contains(content, "…") || !strings.Contains(content, "services/") ||
		!strings.Contains(content, "api.yaml") {
		t.Fatalf("wrapped diagnostic path = %q", content)
	}
}

func TestSourceDiagnosticTextUsesFixedCopy(t *testing.T) {
	t.Parallel()

	for _, reason := range []SourceDiagnosticReason{
		DiagnosticYAMLSyntax,
		DiagnosticYAMLStructure,
		DiagnosticYAMLUnsupported,
		DiagnosticComposeValidation,
		SourceDiagnosticReason(testUnknownValue),
	} {
		message, action := sourceDiagnosticText(reason)
		if message == "" || action == "" || strings.Contains(action, "retry") {
			t.Fatalf("sourceDiagnosticText(%q) = %q, %q", reason, message, action)
		}
	}
}

//nolint:cyclop,funlen // Assertions cover independent conditional render branches in one state fixture.
func TestRenderConditionalStatePaths(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	for _, current := range []page{
		homePage{},
		openPathPage{},
		sourceDiagnosticPage{},
		registrationPage{},
		registrationConfirmationPage{},
		addServicePage{},
		servicePreviewPage{},
		stageServiceConfirmationPage{},
		commitPage{},
		stagedDiffPage{},
		unsignedCommitConfirmationPage{},
		preparationRequiredPage{},
		selectServicePage{},
		reviewPage{},
		reviewOptionsPage{},
		explainPage{},
		detailsPage{},
		confirmationPage{},
		healthConfirmationPage{},
	} {
		state.page = current
		lines := state.hardFloorView(hardMinimumWidth)
		if len(lines) != 4 {
			t.Fatalf("hardFloorView(%T) rows = %d", current, len(lines))
		}
	}

	state.page = reviewPage{}
	state.busy = true
	state.applying = true
	if lines := state.rail(); !strings.Contains(strings.Join(lines, "\n"), "Apply") {
		t.Fatalf("applying rail = %q", lines)
	}
	state.page = detailsPage{}
	if lines := state.rail(); len(lines) == 0 {
		t.Fatal("non-reviewing apply rail is empty")
	}
	state.page = nil
	if lines := state.hardFloorView(hardMinimumWidth); len(lines) != 4 {
		t.Fatalf("unknown hard-floor rows = %q", lines)
	}
	if lines := state.body(defaultWidth); len(lines) != 0 {
		t.Fatalf("unknown page body = %q", lines)
	}
	if lines := state.sourceDiagnosticBody(sourceDiagnosticPage{diagnostic: SourceDiagnostic{
		Line: 2, Reason: DiagnosticYAMLSyntax,
	}}, defaultWidth); len(lines) == 0 {
		t.Fatal("line-only source diagnostic body is empty")
	}
	if lines := state.servicePreviewBody(servicePreviewPage{draft: ServiceDraft{
		Runtime: testRuntime, Image: testImage, Service: testService, ComposePath: testComposePath,
	}}, defaultWidth); len(lines) == 0 {
		t.Fatal("minimal service preview body is empty")
	}
	if lines := state.stageServiceConfirmationBody(stageServiceConfirmationPage{preview: servicePreviewPage{
		draft: ServiceDraft{ComposePath: testComposePath},
	}}, defaultWidth); len(lines) == 0 {
		t.Fatal("minimal stage confirmation body is empty")
	}
	recovered := servicePreviewPage{draft: ServiceDraft{
		Runtime: testRuntime, Image: testImage, Service: testService, ComposePath: testComposePath,
		Recovered: true,
	}}
	if lines := state.servicePreviewBody(recovered, defaultWidth); !strings.Contains(
		strings.Join(lines, "\n"),
		"Previous draft found",
	) {
		t.Fatalf("recovered service preview = %q", lines)
	}
	if lines := state.stageServiceConfirmationBody(stageServiceConfirmationPage{
		preview: recovered,
	}, defaultWidth); !strings.Contains(strings.Join(lines, "\n"), "Stage saved draft") {
		t.Fatalf("recovered stage confirmation = %q", lines)
	}
	if lines := state.preparationRequiredBody(preparationRequiredPage{draft: ServiceDraft{
		Service: testService, Preparation: testPreparationPath,
	}}, defaultWidth); len(lines) == 0 {
		t.Fatal("preparationRequiredBody() is empty")
	}
	for _, registration := range []registrationPage{
		{step: registrationRemoteStep, mode: RepositorySetupCreateGitHub, remote: testGitHubRepository},
		{step: registrationRemoteStep, mode: RepositorySetupExisting, remote: "https://example.com/repository.git"},
		{step: registrationCheckoutStep, checkout: testRepositoryPath},
	} {
		if lines := state.registrationBody(registration, defaultWidth); len(lines) == 0 {
			t.Fatalf("registrationBody(%#v) is empty", registration)
		}
	}
	if lines := state.registrationConfirmationBody(registrationConfirmationPage{
		registration: registrationPage{
			step: registrationCheckoutStep, mode: RepositorySetupExisting,
			remote: "https://example.com/repository.git", checkout: testRepositoryPath,
		},
	}, defaultWidth); !strings.Contains(strings.Join(lines, "\n"), "Clone and register") {
		t.Fatalf("existing registration confirmation = %q", lines)
	}
	if lines := state.registrationConfirmationBody(registrationConfirmationPage{
		registration: registrationPage{
			step: registrationCheckoutStep, mode: RepositorySetupCreateGitHub,
			remote: testGitHubRepository, checkout: testRepositoryPath, created: true,
		},
	}, defaultWidth); !strings.Contains(strings.Join(lines, "\n"), "Retry local setup") {
		t.Fatalf("created registration confirmation = %q", lines)
	}
	created := registrationPage{
		step: registrationCheckoutStep, mode: RepositorySetupCreateGitHub,
		remote: testGitHubRepository, checkout: testRepositoryPath, created: true,
	}
	if lines := state.registrationBody(created, defaultWidth); strings.Contains(strings.Join(lines, "\n"), "Esc Back") {
		t.Fatalf("created registration checkout can edit remote = %q", lines)
	}
	if lines := state.detailsBody(detailsPage{review: reviewPage{plan: planView{
		current: "current", proposed: "proposed",
	}}}, defaultWidth); len(lines) == 0 {
		t.Fatal("unscrolled details body is empty")
	}

	catalog := CatalogSnapshot{
		State:               CatalogMissing,
		SuggestedRepository: testRepositoryPath,
		Services: []Service{
			{Location: "services/ready.yaml", Name: "ready", Runtime: testRuntime},
			{Location: "services/blocked.yaml", Blocker: BlockerInvalid},
		},
	}
	if lines := state.homeBody(homePage{catalog: catalog}, defaultWidth); len(lines) == 0 {
		t.Fatal("homeBody() is empty")
	}
	lines := state.addServiceBody(addServicePage{}, defaultWidth)
	if !strings.Contains(strings.Join(lines, "\n"), "docker://") {
		t.Fatalf("empty addServiceBody() = %q", lines)
	}
	commit := commitPage{
		kind:    commitKindService,
		editing: true,
		staged: StagedService{
			Diff: "diff\n", ComposePath: testServicePath,
		},
	}
	if lines := state.commitBody(commit, defaultWidth); !strings.Contains(strings.Join(lines, "\n"), "_") {
		t.Fatalf("editing commitBody() = %q", lines)
	}
	lines = state.openPathBody(openPathPage{}, defaultWidth)
	if !strings.Contains(strings.Join(lines, "\n"), testComposePath) {
		t.Fatalf("empty openPathBody() = %q", lines)
	}
}

func TestRegistrationFooterMatchesEachStep(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	created := registrationPage{
		step: registrationCheckoutStep, mode: RepositorySetupCreateGitHub,
		remote: testGitHubRepository, checkout: testRepositoryPath, created: true,
	}
	for _, footerTest := range []struct {
		page       registrationPage
		want       string
		forbidText string
	}{
		{page: registrationPage{step: registrationModeStep}, want: "Up/Down Choose"},
		{page: registrationPage{step: registrationRemoteStep}, want: "Type source"},
		{page: registrationPage{step: registrationCheckoutStep}, want: "Type path"},
		{page: created, want: "Type path", forbidText: "Esc"},
	} {
		state.page = footerTest.page
		footer, found := state.registrationFooter()
		if !found || !strings.Contains(footer, footerTest.want) ||
			(footerTest.forbidText != "" && strings.Contains(footer, footerTest.forbidText)) {
			t.Fatalf("registration footer for %#v = %q, %t", footerTest.page, footer, found)
		}
		wantHardFloor := terminaltext.Clip(footer, hardMinimumWidth)
		if !pageAcceptsText(footerTest.page) {
			wantHardFloor = terminaltext.Clip("? Help   "+footer, hardMinimumWidth)
		}
		if hardFloor := state.hardFloorView(hardMinimumWidth); len(hardFloor) != 4 ||
			hardFloor[3] != wantHardFloor {
			t.Fatalf("registration hard-floor footer for %#v = %q", footerTest.page, hardFloor)
		}
	}
	state.page = registrationPage{step: ^registrationStep(0)}
	if footer, found := state.registrationFooter(); found || footer != "" {
		t.Fatalf("invalid registration footer = %q, %t", footer, found)
	}
	state.page = registrationConfirmationPage{}
	wantHardFloor := terminaltext.Clip("? Help   "+confirmationKeys, hardMinimumWidth)
	if hardFloor := state.hardFloorView(hardMinimumWidth); len(hardFloor) != 4 ||
		hardFloor[3] != wantHardFloor {
		t.Fatalf("confirmation hard-floor footer = %q", hardFloor)
	}
}

func TestReviewAndDetailsRenderBoundedObservationSummary(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{
		current: "current", proposed: "proposed", status: statusReady,
	}}
	state.timeline.entries = []timelineEntry{{
		sequence: 1, generation: 7, stage: string(application.EventActionCompleted),
		code: string(application.EventActionCompleted), attempt: 1, outcome: observationCorrelated,
	}}
	state.timeline.truncated = true
	review.correlation.sequence = 7

	reviewContent := strings.Join(state.reviewStatusBody(review, defaultWidth, false), "\n")
	if strings.Count(reviewContent, "Latest observation:") != 1 ||
		!strings.Contains(reviewContent, string(application.EventActionCompleted)) {
		t.Fatalf("review observation summary = %q", reviewContent)
	}
	review.correlation.sequence = 8
	content := strings.Join(state.reviewStatusBody(review, defaultWidth, false), "\n")
	if strings.Contains(content, "Latest observation:") {
		t.Fatalf("review rendered observation from another operation: %q", content)
	}
	detailsContent := strings.Join(state.detailsBody(detailsPage{review: review}, defaultWidth), "\n")
	for _, value := range []string{
		"SESSION TIMELINE", "stage=action_completed", "Dropped events: 0", "Timeline truncated: yes",
	} {
		if !strings.Contains(detailsContent, value) {
			t.Fatalf("details observation summary misses %q: %q", value, detailsContent)
		}
	}
}

func TestHealthReviewRendersOnlyBoundedStateAndExplicitDecision(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{
		project: testProject, service: testService, runtime: testRuntime,
		current: testCurrentImage, proposed: testProposedImage,
		status:      "Workload health requires a decision",
		health:      application.HealthConvergenceDegraded,
		healthState: string(application.WorkloadHealthUnhealthy), healthFails: 4,
		resolution: application.HealthResolutionRollback, restoresPrevious: true,
	}}
	reviewContent := strings.Join(state.reviewBody(review, defaultWidth), "\n")
	for _, value := range []string{
		"Review workload health", "Health    unhealthy (4 failing checks)",
		"The workload was left in place", "Rollback candidate", "View details",
	} {
		if !strings.Contains(reviewContent, value) {
			t.Fatalf("health review misses %q: %q", value, reviewContent)
		}
	}
	if strings.Contains(reviewContent, "Explore options") {
		t.Fatalf("health review exposes mutation options: %q", reviewContent)
	}

	details := strings.Join(state.detailsBody(detailsPage{review: review}, defaultWidth), "\n")
	if !strings.Contains(details, "HEALTH\nunhealthy (4 failing checks)") {
		t.Fatalf("health details = %q", details)
	}
	confirmation := strings.Join(state.healthConfirmationBody(healthConfirmationPage{
		review: review, focus: confirmationBack,
	}, defaultWidth), "\n")
	if !strings.Contains(confirmation, "restore the previous workload") ||
		!strings.Contains(confirmation, "> Back") {
		t.Fatalf("health confirmation = %q", confirmation)
	}
	review.plan.restoresPrevious = false
	confirmation = strings.Join(state.healthConfirmationBody(healthConfirmationPage{
		review: review, focus: confirmationBack,
	}, defaultWidth), "\n")
	if strings.Contains(confirmation, "restore the previous workload") {
		t.Fatalf("bootstrap rollback confirmation describes an upgrade restore: %q", confirmation)
	}
}

func TestHealthPresentationCoversPendingHealthyAndAdoptionStates(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	plan := planView{
		project: testProject, service: testService, status: statusReady,
		health:     application.HealthConvergencePending,
		resolution: application.HealthResolutionCancelAdoption,
	}
	if summary := healthSummary(plan); summary != displayUnavailable {
		t.Fatalf("unavailable health summary = %q", summary)
	}
	if action := healthActionLabel(plan); action != "Cancel adoption" {
		t.Fatalf("adoption action = %q", action)
	}
	body := strings.Join(state.healthConfirmationBody(healthConfirmationPage{
		review: reviewPage{plan: plan}, focus: confirmationBack,
	}, defaultWidth), "\n")
	if !strings.Contains(body, "unmanaged workload will remain unchanged") {
		t.Fatalf("adoption confirmation = %q", body)
	}
	plan.resolution = application.HealthResolutionRetryRestoreStart
	if action := healthActionLabel(plan); action != "Retry restore start" {
		t.Fatalf("restore retry action = %q", action)
	}
	retry := strings.Join(state.healthConfirmationBody(healthConfirmationPage{
		review: reviewPage{plan: plan}, focus: confirmationBack,
	}, defaultWidth), "\n")
	if !strings.Contains(retry, "exact stopped predecessor") ||
		!strings.Contains(retry, "rolled-back candidate will remain discarded") {
		t.Fatalf("restore retry confirmation = %q", retry)
	}
	plan.resolution = application.HealthResolutionCancelAdoption
	pending := strings.Join(state.healthStatusCard(plan, defaultWidth), "\n")
	if !strings.Contains(pending, "polling read-only") {
		t.Fatalf("pending health card = %q", pending)
	}

	plan.health = application.HealthConvergenceHealthy
	plan.healthState = string(application.WorkloadHealthHealthy)
	healthy := strings.Join(state.healthStatusCard(plan, defaultWidth), "\n")
	if !strings.Contains(healthy, "resuming the existing transaction") || healthSummary(plan) != "healthy" {
		t.Fatalf("healthy presentation = %q / %q", healthy, healthSummary(plan))
	}
	plan.resolution = ""
	if action := healthActionLabel(plan); action != "Refresh health" {
		t.Fatalf("default health action = %q", action)
	}
	plan.health = application.HealthConvergenceNone
	unavailable := strings.Join(state.healthStatusCard(plan, defaultWidth), "\n")
	if !strings.Contains(unavailable, "Runtime health is unavailable") {
		t.Fatalf("unavailable health presentation = %q", unavailable)
	}
}

func assertBoundedView(t *testing.T, content string, width, height int) {
	t.Helper()

	lines := strings.Split(content, "\n")
	if len(lines) > height {
		t.Fatalf("view has %d lines, want at most %d", len(lines), height)
	}
	for _, line := range lines {
		if terminaltext.Width(line) > width {
			t.Fatalf("view line has %d cells, want at most %d: %q", terminaltext.Width(line), width, line)
		}
	}
}
