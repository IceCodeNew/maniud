package tui

import "github.com/IceCodeNew/maniud/internal/terminaltext"

const (
	commitBaseRows     = 10
	stagedDiffLeadRows = 3
)

func (state *model) deploymentCommitPage() bool {
	switch current := state.page.(type) {
	case commitPage:
		return current.kind == commitKindDeployment
	case stagedDiffPage:
		return current.commit.kind == commitKindDeployment
	case unsignedCommitConfirmationPage:
		return current.commit.kind == commitKindDeployment
	default:
		return false
	}
}

func (state *model) commitPageBody(width, height int) ([]string, bool) {
	switch current := state.page.(type) {
	case commitPage:
		if validCommitKind(current.kind) {
			return state.commitBody(current, width), true
		}
	case stagedDiffPage:
		if validCommitKind(current.commit.kind) {
			return state.stagedDiffBody(current, width, height), true
		}
	case unsignedCommitConfirmationPage:
		if validCommitKind(current.commit.kind) {
			return state.unsignedCommitConfirmationBody(current, width), true
		}
	}

	return nil, false
}

func (state *model) commitBody(current commitPage, width int) []string {
	message := terminaltext.Middle(current.message, max(width-serviceFieldWidth, 1), "…")
	if current.editing {
		message += state.symbol("▌", "_")
	}
	diff := stagedDiffLines(current, width)
	start := boundedScroll(current.scroll, len(diff))
	diff = diff[start:min(start+diffSummaryRows, len(diff))]
	lines := make([]string, 0, commitBaseRows+len(diff))
	lines = append(lines,
		state.title("Review and commit"),
		"Target    "+terminaltext.Middle(current.staged.ComposePath, max(width-serviceFieldWidth, 1), "…"),
		"Message   "+message,
		"",
		state.muted("STAGED DIFF"),
	)
	available := max(width-detailsPadding, hardMinimumWidth-detailsPadding)
	for _, line := range diff {
		lines = append(lines, terminaltext.Clip(line, available))
	}
	lines = append(lines, "", state.choice(current.focus == confirmationBack, "Back and save draft", width),
		state.choice(current.focus == confirmationApply, "Create signed commit", width))

	return lines
}

func (state *model) stagedDiffBody(current stagedDiffPage, width, height int) []string {
	lead := []string{state.title("Staged diff"), "This page is read-only.", ""}
	diff := stagedDiffLines(current.commit, width)
	total := len(lead) + len(diff)
	start := boundedScroll(current.scroll, total)
	count := min(max(height, 0), total-start)
	end := start + count
	lines := make([]string, 0, count)
	if leadEnd := min(end, len(lead)); start < leadEnd {
		lines = append(lines, lead[start:leadEnd]...)
	}
	diffStart := max(start-len(lead), 0)
	diffEnd := max(end-len(lead), 0)
	if diffStart < diffEnd {
		available := max(width-detailsPadding, hardMinimumWidth-detailsPadding)
		for _, line := range diff[diffStart:diffEnd] {
			lines = append(lines, terminaltext.Clip(line, available))
		}
	}

	return lines
}

func stagedDiffLineCount(commit commitPage, width int) int {
	return stagedDiffLeadRows + len(stagedDiffLines(commit, width))
}

func stagedDiffLines(current commitPage, width int) []string {
	if current.diffLines != nil {
		return current.diffLines
	}
	available := max(width-detailsPadding, hardMinimumWidth-detailsPadding)

	return terminaltext.Wrap(current.staged.Diff, available)
}

func (state *model) unsignedCommitConfirmationBody(
	current unsignedCommitConfirmationPage,
	width int,
) []string {
	message := terminaltext.Middle(current.commit.message, max(width-serviceFieldWidth, 1), "…")

	return []string{
		state.title("Confirm unsigned commit"),
		"Git could not create a signed commit with the configured identity and signing key.",
		"The staged files are unchanged. Continue only if an unsigned history entry is acceptable.",
		"",
		"Message   " + message,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, "Create unsigned commit", width),
	}
}

func (state *model) commitFooter() (string, bool) {
	switch current := state.page.(type) {
	case commitPage:
		if current.editing {
			return "Type message   Enter/Esc Done", validCommitKind(current.kind)
		}

		return "e Edit   d Diff   Tab Focus   Enter Choose   q Quit", validCommitKind(current.kind)
	case stagedDiffPage:
		return scrollBackKeys, validCommitKind(current.commit.kind)
	case unsignedCommitConfirmationPage:
		return confirmationKeys, validCommitKind(current.commit.kind)
	default:
		return "", false
	}
}
