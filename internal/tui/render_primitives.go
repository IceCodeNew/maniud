package tui

import (
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

func (state *model) statusTitle(status string) string {
	if status == statusApplyCompleted {
		return state.success(state.symbol("✓ ", "[x] ") + status)
	}

	return state.accent(state.symbol("⬟ ", "[OK] ") + status)
}

func (state *model) statusCard(status string, width int) []string {
	pending := "No runtime change has started."
	if status == statusApplyCompleted {
		pending = ""
	}

	return state.statusCardWith(
		status, "Compose validation and read-only runtime checks passed.", pending, width,
	)
}

func (state *model) healthStatusCard(plan planView, width int) []string {
	detail := healthStatusDetail(plan.health)

	return state.statusCardWith(plan.status, detail, "", width)
}

func healthStatusDetail(health application.HealthConvergence) string {
	if health == application.HealthConvergencePending {
		return "Runtime health is still pending. Maniud is polling read-only."
	}
	if health == application.HealthConvergenceDegraded {
		return "The workload was left in place. Choose a health action or refresh."
	}
	if health == application.HealthConvergenceHealthy {
		return "Runtime health passed. Maniud is resuming the existing transaction."
	}

	return "Runtime health is unavailable. Refresh to inspect the current state."
}

func (state *model) statusCardWith(status, detail, pending string, width int) []string {
	horizontal := strings.Repeat(state.symbol("─", "-"), width-statusCardBorders)
	lines := []string{
		state.accent(state.symbol("┌", "+") + horizontal + state.symbol("┐", "+")),
		state.statusCardLine(state.statusTitle(status), width),
		state.statusCardLine(detail, width),
	}
	if pending != "" {
		lines = append(lines, state.statusCardLine(pending, width))
	}
	lines = append(lines, state.accent(state.symbol("└", "+")+horizontal+state.symbol("┘", "+")))

	return lines
}

func (state *model) statusCardLine(value string, width int) string {
	contentWidth := width - statusCardBorders - statusCardPadding
	border := state.accent(state.symbol("│", "|"))

	return border + " " + padCells(value, contentWidth) + " " + border
}

func (state *model) choice(selected bool, label string, width int) string {
	marker := "  "
	if selected {
		marker = state.accent(state.symbol("› ", "> "))
	}

	return marker + terminaltext.Middle(label, max(width-terminaltext.Width(marker), 1), "…")
}

func (state *model) symbol(unicodeValue, asciiValue string) string {
	if state.options.Unicode {
		return unicodeValue
	}

	return asciiValue
}

func (state *model) title(value string) string {
	return state.accent(value)
}

func (state *model) accent(value string) string {
	return state.color(colorAmber, value)
}

func (state *model) muted(value string) string {
	return state.color(colorMuted, value)
}

func (state *model) success(value string) string {
	return state.color(colorSuccess, value)
}

func (state *model) failure(value string) string {
	return state.color(colorFailure, value)
}

func (state *model) color(code, value string) string {
	if !state.options.Color {
		return value
	}

	return code + value + colorReset
}

func padCells(value string, width int) string {
	clipped := terminaltext.Clip(value, width)

	return clipped + strings.Repeat(" ", max(width-terminaltext.Width(clipped), 0))
}

func fitView(lines []string, width, height int) string {
	visible := lines[:min(len(lines), height)]
	for index, line := range visible {
		visible[index] = terminaltext.Clip(line, width)
	}

	return strings.Join(visible, "\n")
}
