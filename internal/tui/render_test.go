package tui

import (
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/terminaltext"
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
		{width: 100, height: 30, contains: []string{"FLOW", "CURRENT", "PROPOSED", "Continue to confirmation"}},
		{width: 60, height: 20, contains: []string{"project / service / docker", "CURRENT", "PROPOSED"}},
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
	if !strings.Contains(colored, "\x1b[") || !strings.Contains(colored, "◆") || !strings.Contains(colored, "│") {
		t.Fatalf("colored Unicode view = %q", colored)
	}
	state.options = Options{}
	plain := state.View().Content
	if strings.Contains(plain, "\x1b[") || strings.ContainsAny(plain, "◆✓›│─…▌") || !strings.Contains(plain, "OK ") {
		t.Fatalf("plain ASCII view = %q", plain)
	}
}

func TestViewRendersEveryPageWithoutLeakingErrors(t *testing.T) {
	t.Parallel()

	state, _, _ := newTestModel(t)
	review := reviewPage{plan: planView{
		kind: testUpgrade, project: testProject, service: testService, runtime: testRuntime, platform: testPlatform,
		current: "old", proposed: "new", status: statusReady, warningText: "1 warning(s) require review",
	}}
	pages := []struct {
		page     page
		contains string
	}{
		{page: homePage{catalog: CatalogSnapshot{State: CatalogMissing}}, contains: "Services"},
		{page: openPathPage{value: strings.Repeat("path/", 40)}, contains: "Open Compose file"},
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
		state.mutationOutcome = "Apply completed"
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
