package tui

import (
	"fmt"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	testAddServiceMessage = "Add service"
	testCurrentLabel      = "CURRENT"
	testProposedLabel     = "PROPOSED"
	testCurrentImage      = "current"
	testProposedImage     = "proposed"
	testPreparationPath   = "services/service.prepare.sh"
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
		{width: 100, height: 30, contains: []string{"FLOW", testCurrentLabel, testProposedLabel, "Continue to confirmation"}},
		{width: 60, height: 20, contains: []string{"project / service / docker", testCurrentLabel, testProposedLabel}},
		{width: 56, height: 16, contains: []string{
			testCurrentLabel, testProposedLabel, statusReady, "Continue to confirmation",
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
		testProposedLabel, testProposedImage, statusApplyCompleted, "Continue to confirmation", "Operation failed.")
	if strings.Contains(content, "No runtime change has started") || strings.Contains(content, errTestSecret.Error()) {
		t.Fatalf("compact completed review contains stale or unsafe copy: %q", content)
	}

	state.mutationOutcome = ""
	content = state.View().Content
	assertViewContains(t, content, "compact constrained review", testCurrentLabel, testCurrentImage,
		testProposedLabel, testProposedImage, statusReady, "Continue to confirmation", "Operation failed.")
	if strings.Contains(content, "Review image change") || strings.Contains(content, errTestSecret.Error()) {
		t.Fatalf("compact constrained review kept expendable or unsafe copy: %q", content)
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
		current: "old", proposed: "new", status: statusReady,
	}}
	state.options = Options{Color: true, Unicode: true}
	colored := state.View().Content
	if !strings.Contains(colored, "\x1b[") || !strings.Contains(colored, "⬟") || !strings.Contains(colored, "│") {
		t.Fatalf("colored Unicode view = %q", colored)
	}
	state.options = Options{}
	plain := state.View().Content
	if strings.Contains(plain, "\x1b[") || strings.ContainsAny(plain, "⬟✓●○›│─…▌") ||
		!strings.Contains(plain, "[OK] ") || !strings.Contains(plain, "[*] Review") ||
		!strings.Contains(plain, "[ ] Confirm") {
		t.Fatalf("plain ASCII view = %q", plain)
	}
}

//nolint:funlen // The page table keeps the complete rendering contract visible.
func TestViewRendersEveryPageWithoutLeakingErrors(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{
		kind: testUpgrade, project: testProject, service: testService, runtime: testRuntime, platform: testPlatform,
		current: "old", proposed: "new", status: statusReady, warningText: "1 warning(s) require review",
	}}
	preview := servicePreviewPage{input: testRuntimeCommand, draft: ServiceDraft{
		Runtime: testRuntime, Image: strings.Repeat("registry.example/image@sha256:a", 4),
		Service: testService, ComposePath: testServicePath, Preparation: testPreparationPath,
		WarningCount: 1,
	}}
	commit := commitServicePage{
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
		{page: registrationPage{step: ^registrationStep(0)}, contains: "Repository setup is unavailable"},
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
		{page: detailsPage{review: review, scroll: 100}, contains: "Up/Down Scroll"},
		{page: confirmationPage{review: review, focus: confirmationApply}, contains: "Confirm apply"},
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
	if lines := stagedServiceDiffLines(commitServicePage{
		diffWidth: 4, diffLines: cached,
	}, 4+detailsPadding); !slices.Equal(lines, cached) {
		t.Fatalf("stagedServiceDiffLines(cached) = %q", lines)
	}
	if start, end := visibleHomeServices(20, 15, 4); start != 12 || end != 16 {
		t.Fatalf("visibleHomeServices() = %d:%d, want 12:16", start, end)
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
		"unknown",
	} {
		message, action := sourceDiagnosticText(reason)
		if message == "" || action == "" {
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
		addServicePage{},
		servicePreviewPage{},
		stageServiceConfirmationPage{},
		commitServicePage{},
		stagedDiffPage{},
		unsignedCommitConfirmationPage{},
		preparationRequiredPage{},
		selectServicePage{},
		reviewPage{},
		detailsPage{},
		confirmationPage{},
	} {
		state.page = current
		if lines := state.hardFloorView(hardMinimumWidth); len(lines) != 4 {
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
		{step: registrationRemoteStep, mode: RepositorySetupCreateGitHub, remote: "owner/repository"},
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
	commit := commitServicePage{
		editing: true,
		staged: StagedService{
			Diff: "diff\n", ComposePath: testServicePath,
		},
	}
	if lines := state.commitServiceBody(commit, defaultWidth); !strings.Contains(strings.Join(lines, "\n"), "_") {
		t.Fatalf("editing commitServiceBody() = %q", lines)
	}
	lines = state.openPathBody(openPathPage{}, defaultWidth)
	if !strings.Contains(strings.Join(lines, "\n"), testComposePath) {
		t.Fatalf("empty openPathBody() = %q", lines)
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
