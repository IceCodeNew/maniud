package tui

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

const (
	defaultWidth  = 80
	defaultHeight = 24
)

func (state *model) View() tea.View {
	width := state.width
	if width == 0 {
		width = defaultWidth
	}
	height := state.height
	if height == 0 {
		height = defaultHeight
	}

	lines := []string{
		"maniud",
		"Status: " + state.status,
	}
	if state.hasPlan {
		lines = append(lines,
			fmt.Sprintf("Service: %s/%s", state.plan.Project, state.plan.Service),
			fmt.Sprintf("Plan: %s with %s", state.plan.Kind, state.plan.Runtime),
			fmt.Sprintf(
				"Platform: %s/%s",
				state.plan.Platform.OS,
				state.plan.Platform.Architecture,
			),
		)
	}
	if state.hasSnapshot {
		lines = append(lines, fmt.Sprintf(
			"Snapshot: transaction=%t actions=%d dropped-events=%d",
			state.snapshot.HasTransaction,
			len(state.snapshot.Actions),
			state.snapshot.DroppedEvents,
		))
	}
	if state.hasEvidence {
		lines = append(lines, fmt.Sprintf(
			"Evidence: %d items, truncated=%t",
			len(state.evidence.Items),
			state.evidence.Truncated,
		))
	}
	if len(state.recent) > 0 {
		lines = append(lines, "Recent events:")
		for _, event := range state.recent {
			lines = append(lines, fmt.Sprintf(
				"  %s action=%s sequence=%d",
				event.Kind,
				event.Action,
				event.Sequence,
			))
		}
	}
	if state.err != nil {
		lines = append(lines, "The operation failed. Exit and run the command again with --debug for details.")
	}
	lines = append(lines, "d check  a apply  r refresh  esc cancel  q quit")

	view := tea.NewView(fitView(lines, width, height))
	view.AltScreen = true

	return view
}

func fitView(lines []string, width, height int) string {
	visible := lines[:min(len(lines), height)]
	for index, line := range visible {
		characters := []rune(line)
		if len(characters) > width {
			visible[index] = string(characters[:width])
		}
	}

	return strings.Join(visible, "\n")
}
