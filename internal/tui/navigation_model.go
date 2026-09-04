package tui

import (
	"strings"
	"unicode"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/terminaltext"
)

const (
	reviewExplainOption = iota
	reviewLLMOption
	reviewEditOption
	reviewHistoryOption
	reviewOptionCount
)

func (state *model) handleHomeKey(current homePage, key string) tea.Cmd {
	setupIndex := registrationSetupIndex(current.catalog)
	items := homeItemCount(current.catalog)
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + items) % items
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % items
	case keyEnter:
		return state.activateHomeItem(current, setupIndex)
	case "r":
		return state.startCatalog()
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) activateHomeItem(current homePage, setupIndex int) tea.Cmd {
	switch current.cursor {
	case addServiceIndex(current.catalog):
		return state.activateAddService(current.catalog)
	case openComposeIndex(current.catalog):
		state.page = openPathPage{}
		state.status = "Enter a committed Compose path"

		return nil
	case setupIndex:
		state.page = newRegistrationPage(current.catalog.SuggestedRepository)
		state.status = "Choose how to set up the desired-state repository"

		return nil
	}
	service := current.catalog.Services[current.cursor]
	if service.Blocker != BlockerNone {
		if diagnostic, valid := canonicalSourceDiagnostic(service.Diagnostic); valid {
			state.page = sourceDiagnosticPage{previous: current, diagnostic: diagnostic}
			state.status = "Review the Compose source issue"

			return nil
		}
		state.status = blockerMessage(service.Blocker)

		return nil
	}

	return state.startOpenRegistered(service.ID)
}

func (state *model) handleSourceDiagnosticKey(current sourceDiagnosticPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case keyEnter, keyEscape:
		state.page = current.previous
		state.status = "Fix the Compose source, exit, and rerun maniud tui"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current
	state.clampPageScroll()

	return nil
}

func homeItemCount(catalog CatalogSnapshot) int {
	count := len(catalog.Services) + homeActionCount
	if registrationSetupIndex(catalog) >= 0 {
		count++
	}

	return count
}

func registrationSetupIndex(catalog CatalogSnapshot) int {
	if catalog.State == CatalogReady || catalog.SuggestedRepository == "" {
		return -1
	}

	return len(catalog.Services) + homeActionCount
}

func openComposeIndex(catalog CatalogSnapshot) int {
	return len(catalog.Services) + 1
}

func (state *model) handleOpenPathKey(current openPathPage, message tea.KeyPressMsg) tea.Cmd {
	key := message.String()
	switch key {
	case keyEnter:
		if current.value == "" {
			state.status = "Enter a Compose path"

			return nil
		}

		return state.startOpenPath(current.value)
	case "backspace":
		characters := []rune(current.value)
		if len(characters) > 0 {
			current.value = string(characters[:len(characters)-1])
		}
	case keyEscape:
		return state.startCatalog()
	default:
		text := message.Key().Text
		if printableSingleLine(text) {
			candidate := current.value + text
			if _, err := terminaltext.Canonicalize(candidate, displayLimits()); err == nil {
				current.value = candidate
			}
		}
	}
	state.page = current

	return nil
}

func editSingleLine(value string, message tea.KeyPressMsg) string {
	if message.String() == "backspace" {
		characters := []rune(value)
		if len(characters) > 0 {
			return string(characters[:len(characters)-1])
		}

		return value
	}
	text := message.Key().Text
	if !printableSingleLine(text) {
		return value
	}
	candidate := value + text
	if _, err := terminaltext.Canonicalize(candidate, displayLimits()); err != nil {
		return value
	}

	return candidate
}

func toggledConfirmationFocus(focus confirmationFocus) confirmationFocus {
	if focus == confirmationBack {
		return confirmationApply
	}

	return confirmationBack
}

func printableSingleLine(value string) bool {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return false
	}
	for _, character := range value {
		if !unicode.IsPrint(character) {
			return false
		}
	}

	return true
}

func (state *model) handleServiceChoiceKey(current selectServicePage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + len(current.choices)) % len(current.choices)
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % len(current.choices)
	case keyEnter:
		return state.startSnapshot(current.choices[current.cursor].request)
	case keyEscape:
		state.page = openPathPage{}
		state.status = "Enter a committed Compose path"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleReviewKey(current reviewPage, key string) tea.Cmd {
	switch key {
	case "up", "k", keyDown, "j", keyTab, keyShiftTab:
		if current.focus == reviewContinue {
			current.focus = reviewExplore
		} else {
			current.focus = reviewContinue
		}
		state.page = current
	case keyEnter:
		return state.activateReview(current)
	case "d":
		state.page = detailsPage{review: current}
		state.status = "Full image references"
	case "r":
		return state.startSnapshot(current.request)
	case "o":
		if current.plan.health != "" {
			state.page = detailsPage{review: current}
			state.status = statusHealthDetails

			return nil
		}
		state.page = reviewOptionsPage{review: current}
		state.status = "Choose an option"
	case keyEscape:
		return state.startCatalog()
	case keyQuit:
		return tea.Quit
	}

	return nil
}

func (state *model) activateReview(current reviewPage) tea.Cmd {
	if current.focus == reviewExplore {
		if current.plan.health != "" {
			state.page = detailsPage{review: current}
			state.status = statusHealthDetails

			return nil
		}
		state.page = reviewOptionsPage{review: current}
		state.status = "Choose an option"

		return nil
	}
	if state.configReloadNeeded {
		return state.startLLMConfiguration(current)
	}
	if current.plan.resolution != "" {
		if layoutFor(state.width, state.height) < layoutCompact {
			state.status = "Resize to continue to confirmation"

			return nil
		}
		state.page = healthConfirmationPage{review: current, focus: confirmationBack}
		state.status = "Confirm the health decision or go back"

		return nil
	}
	if current.plan.health != "" {
		return state.startSnapshot(current.request)
	}
	if layoutFor(state.width, state.height) < layoutCompact {
		state.status = "Resize to continue to confirmation"

		return nil
	}
	state.page = confirmationPage{review: current, focus: confirmationBack}
	state.status = "Confirm or go back"

	return nil
}

func (state *model) handleReviewOptionsKey(current reviewOptionsPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.cursor = (current.cursor - 1 + reviewOptionCount) % reviewOptionCount
	case keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % reviewOptionCount
	case keyEnter:
		switch current.cursor {
		case reviewExplainOption:
			state.clearRecoverableDeploymentFailure()
			state.page = explainPage{review: current.review}
			state.status = "Deterministic explanation"

			return nil
		case reviewLLMOption:
			state.clearRecoverableDeploymentFailure()

			return state.startLLMConfiguration(current.review)
		case reviewEditOption:
			return state.startDeploymentFields(current.review)
		case reviewHistoryOption:
			return state.startDeploymentHistory(current.review)
		}
	case keyEscape:
		state.clearRecoverableDeploymentFailure()
		state.page = current.review
		state.status = current.review.plan.status

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleExplainKey(current explainPage, key string) tea.Cmd {
	switch key {
	case keyEnter, keyEscape:
		return state.startSnapshot(current.review.request)
	case keyQuit:
		return tea.Quit
	default:
		return nil
	}
}

func (state *model) handleDetailsKey(current detailsPage, key string) tea.Cmd {
	switch key {
	case "up", "k":
		current.scroll = max(current.scroll-1, 0)
	case keyDown, "j":
		current.scroll++
	case "d", keyEscape:
		state.page = current.review
		state.status = current.review.plan.status
		if current.review.plan.health == application.HealthConvergencePending {
			return state.startSnapshot(current.review.request)
		}

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current
	state.clampPageScroll()

	return nil
}

func (state *model) handleConfirmationKey(current confirmationPage, key string) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.review
		state.status = "Review again at a larger terminal"

		return nil
	}

	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.review
			state.status = current.review.plan.status

			return nil
		}

		return state.startApply(current.review)
	case keyEscape:
		state.page = current.review
		state.status = current.review.plan.status

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) handleHealthConfirmationKey(
	current healthConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.review
		state.status = statusReviewLarger

		return nil
	}

	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.review
			state.status = current.review.plan.status

			return state.waitForHealth(current.review)
		}

		return state.startHealthResolution(current)
	case keyEscape:
		state.page = current.review
		state.status = current.review.plan.status

		return state.waitForHealth(current.review)
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}
