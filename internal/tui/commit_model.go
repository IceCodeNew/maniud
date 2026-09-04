package tui

import (
	"context"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const commitDiffResizeDelay = 50 * time.Millisecond

type commitDiffResizeMsg struct {
	width int
}

type commitKind uint8

const (
	commitKindService commitKind = iota + 1
	commitKindDeployment
)

func validCommitKind(kind commitKind) bool {
	return kind == commitKindService || kind == commitKindDeployment
}

type commitPage struct {
	kind       commitKind
	preview    servicePreviewPage
	staged     StagedService
	message    string
	focus      confirmationFocus
	editing    bool
	scroll     int
	diffWidth  int
	diffLines  []string
	deployment deploymentPreviewPage
}

func (commitPage) isPage() {}

func (current commitPage) acceptsTextInput() bool { return current.editing }

type stagedDiffPage struct {
	commit commitPage
	scroll int
}

func (stagedDiffPage) isPage() {}

type unsignedCommitConfirmationPage struct {
	commit commitPage
	focus  confirmationFocus
}

func (unsignedCommitConfirmationPage) isPage() {}

func (state *model) rewrapCommitDiff() {
	switch current := state.page.(type) {
	case commitPage:
		state.page = state.wrapCommitDiff(current)
	case stagedDiffPage:
		current.commit = state.wrapCommitDiff(current.commit)
		state.page = current
	case unsignedCommitConfirmationPage:
		current.commit = state.wrapCommitDiff(current.commit)
		state.page = current
	case stageDeploymentConfirmationPage:
		current.preview = state.wrapDeploymentPreviewDiff(current.preview)
		state.page = current
	case deploymentDiffPage:
		current.confirmation.preview = state.wrapDeploymentPreviewDiff(current.confirmation.preview)
		state.page = current
	}
}

func (state *model) scheduleCommitDiffResize() tea.Cmd {
	width := state.commitDiffWidth()
	var currentWidth int
	var currentLines []string
	switch page := state.page.(type) {
	case commitPage:
		currentWidth, currentLines = page.diffWidth, page.diffLines
	case stagedDiffPage:
		currentWidth, currentLines = page.commit.diffWidth, page.commit.diffLines
	case unsignedCommitConfirmationPage:
		currentWidth, currentLines = page.commit.diffWidth, page.commit.diffLines
	case stageDeploymentConfirmationPage:
		currentWidth, currentLines = page.preview.diffWidth, page.preview.diffLines
	case deploymentDiffPage:
		preview := page.confirmation.preview
		currentWidth, currentLines = preview.diffWidth, preview.diffLines
	default:
		return nil
	}
	if currentWidth == width && currentLines != nil {
		return nil
	}

	return tea.Tick(commitDiffResizeDelay, func(time.Time) tea.Msg {
		return commitDiffResizeMsg{width: width}
	})
}

func (state *model) wrapCommitDiff(current commitPage) commitPage {
	width := state.commitDiffWidth()
	if current.diffWidth == width && current.diffLines != nil {
		return current
	}
	current.diffWidth = width
	current.diffLines = terminaltext.Wrap(current.staged.Diff, width)

	return current
}

func (state *model) wrapDeploymentPreviewDiff(current deploymentPreviewPage) deploymentPreviewPage {
	width := state.commitDiffWidth()
	if current.diffWidth == width && current.diffLines != nil {
		return current
	}
	current.diffWidth = width
	current.diffLines = terminaltext.Wrap(current.preview.Diff, width)

	return current
}

func (state *model) commitDiffWidth() int {
	width := state.width
	if width == 0 {
		width = defaultWidth
	}
	height := state.height
	if height == 0 {
		height = defaultHeight
	}
	layout := layoutFor(width, height)
	if layout == layoutFull {
		return fullMinimumWidth - fullBodyOffset - detailsPadding
	}
	if layout == layoutCompact {
		return compactMinimum - detailsPadding
	}

	return hardMinimumWidth - detailsPadding
}

func (state *model) handleCommitPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case commitPage:
		if validCommitKind(current.kind) {
			return state.handleCommitKey(current, message), true
		}
	case stagedDiffPage:
		if validCommitKind(current.commit.kind) {
			return state.handleStagedDiffKey(current, message.String()), true
		}
	case unsignedCommitConfirmationPage:
		if validCommitKind(current.commit.kind) {
			return state.handleUnsignedCommitConfirmationKey(current, message.String()), true
		}
	}

	return nil, false
}

func (state *model) handleCommitKey(current commitPage, message tea.KeyPressMsg) tea.Cmd {
	if current.editing {
		return state.handleCommitMessageKey(current, message)
	}

	return state.handleCommitReviewKey(current, message.String())
}

func (state *model) handleCommitReviewKey(current commitPage, key string) tea.Cmd {
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d":
		state.page = stagedDiffPage{commit: current}
		state.status = "Full staged diff"

		return nil
	case "e":
		current.editing = true
		state.status = "Edit the commit message"
	case keyEnter:
		if current.focus == confirmationBack {
			return state.startCommitDiscard(current)
		}

		return state.startCommit(current, false)
	case keyEscape:
		return state.startCommitDiscard(current)
	case keyQuit:
		return state.quitCommit(current, current)
	}
	state.page = current
	state.clampPageScroll()

	return nil
}

func (state *model) startCommitDiscard(current commitPage) tea.Cmd {
	switch current.kind {
	case commitKindService:
		return state.startServiceSuspend(current.preview)
	case commitKindDeployment:
		return state.startDeploymentDiscard(current.deployment, false, true)
	default:
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return nil
	}
}

func (state *model) quitCommit(previous page, current commitPage) tea.Cmd {
	if current.kind != commitKindDeployment {
		return tea.Quit
	}
	state.page = deploymentDraftConfirmationPage{
		previous: previous, focus: confirmationBack, quit: true, staged: true,
	}
	state.status = "Discard unsaved deployment edit?"

	return nil
}

func (state *model) handleCommitMessageKey(current commitPage, message tea.KeyPressMsg) tea.Cmd {
	switch message.String() {
	case keyEnter, keyEscape:
		current.editing = false
		state.status = statusReviewStaged
	default:
		candidate := editSingleLine(current.message, message)
		if len(candidate) <= maximumCommitMessageBytes {
			current.message = candidate
		}
	}
	state.page = current

	return nil
}

func (state *model) handleStagedDiffKey(current stagedDiffPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d", keyEscape:
		state.page = current.commit
		state.status = statusReviewStaged

		return nil
	case keyQuit:
		return state.quitCommit(current, current.commit)
	}
	state.page = current
	state.clampPageScroll()

	return nil
}

func (state *model) handleUnsignedCommitConfirmationKey(
	current unsignedCommitConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.commit
		state.status = statusReviewLarger

		return nil
	}
	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.commit
			state.status = statusSignedCommitMissing

			return nil
		}

		return state.startCommit(current.commit, true)
	case keyEscape:
		state.page = current.commit
		state.status = statusSignedCommitMissing

		return nil
	case keyQuit:
		return state.quitCommit(current, current.commit)
	}
	state.page = current

	return nil
}

func (state *model) startCommit(commit commitPage, unsigned bool) tea.Cmd {
	if commit.kind == commitKindDeployment {
		return state.startDeploymentCommit(commit, unsigned)
	}
	if commit.kind != commitKindService {
		state.err = errInvalidInput
		state.status = statusCommitUnverified

		return nil
	}
	if !state.deploymentOperationReady() {
		return nil
	}
	state.page = commit

	return state.begin("Creating commit", func(ctx context.Context, sequence uint64) tea.Cmd {
		workspace := state.workspace

		return func() tea.Msg {
			result, err := workspace.Commit(ctx, commit.message, unsigned)

			return serviceCommitResultMsg{sequence: sequence, commit: commit, result: result, err: err}
		}
	})
}
