package tui

import (
	"context"

	tea "charm.land/bubbletea/v2"
)

const (
	repositorySetupModeCount         = 2
	repositorySetupUnavailableStatus = "Repository setup is unavailable"
)

type registrationResultMsg struct {
	sequence uint64
	request  RepositorySetupRequest
	result   RegistrationResult
}

type registrationStep uint8

const (
	registrationModeStep registrationStep = iota
	registrationRemoteStep
	registrationCheckoutStep
)

type registrationPage struct {
	step          registrationStep
	mode          RepositorySetupMode
	remote        string
	checkout      string
	suggestedPath string
	cursor        int
	created       bool
}

func (registrationPage) isPage() {}

func (current registrationPage) acceptsTextInput() bool {
	return current.step != registrationModeStep
}

type registrationConfirmationPage struct {
	registration registrationPage
	focus        confirmationFocus
}

func (registrationConfirmationPage) isPage() {}

func (state *model) handleRegistrationPageKey(message tea.KeyPressMsg) (tea.Cmd, bool) {
	switch current := state.page.(type) {
	case registrationPage:
		state.handleRegistrationKey(current, message)

		return nil, true
	case registrationConfirmationPage:
		return state.handleRegistrationConfirmationKey(current, message.String()), true
	default:
		return nil, false
	}
}

func (state *model) handleRegistrationKey(current registrationPage, message tea.KeyPressMsg) {
	if current.step == registrationModeStep {
		state.handleRegistrationModeKey(current, message.String())

		return
	}

	switch message.String() {
	case keyEnter:
		if current.step == registrationRemoteStep {
			if current.remote == "" {
				state.status = "Enter a repository name or remote URL"

				return
			}
			current.step = registrationCheckoutStep
			if current.checkout == "" {
				current.checkout = current.suggestedPath
			}
			state.page = current
			state.status = "Choose where to store the local checkout"

			return
		}
		if current.checkout == "" {
			state.status = "Enter a local checkout path"

			return
		}
		state.page = registrationConfirmationPage{registration: current, focus: confirmationBack}
		state.status = "Confirm repository setup or go back"

		return
	case keyEscape:
		state.handleRegistrationBack(current)

		return
	}

	if current.step == registrationRemoteStep {
		current.remote = editSingleLine(current.remote, message)
	} else {
		current.checkout = editSingleLine(current.checkout, message)
	}
	state.page = current
}

func (state *model) handleRegistrationBack(current registrationPage) {
	if current.step != registrationCheckoutStep {
		current.step = registrationModeStep
		state.page = current
		state.status = "Choose how to set up the desired-state repository"

		return
	}
	if current.created {
		state.status = "GitHub repository already exists; change only the local checkout path"

		return
	}
	current.step = registrationRemoteStep
	state.page = current
	state.status = "Enter the repository source"
}

func (state *model) handleRegistrationModeKey(current registrationPage, key string) {
	switch key {
	case "up", "k", keyDown, "j", keyTab:
		current.cursor = (current.cursor + 1) % repositorySetupModeCount
	case keyEnter:
		current.mode = RepositorySetupCreateGitHub
		if current.cursor == 1 {
			current.mode = RepositorySetupExisting
		}
		current.step = registrationRemoteStep
		state.page = current
		state.status = "Enter the repository source"

		return
	case keyEscape:
		state.registrationSeen = true
		state.page = homePage{catalog: CatalogSnapshot{
			State: CatalogMissing, SuggestedRepository: current.suggestedPath,
		}}
		state.status = "No registered repository"

		return
	}
	state.page = current
}

func newRegistrationPage(suggestedPath string) registrationPage {
	return registrationPage{step: registrationModeStep, suggestedPath: suggestedPath}
}

func registrationRequest(current registrationPage) RepositorySetupRequest {
	return RepositorySetupRequest{
		Mode: current.mode, Remote: current.remote, Checkout: current.checkout, Created: current.created,
	}
}

func (state *model) handleRegistrationConfirmationKey(
	current registrationConfirmationPage,
	key string,
) tea.Cmd {
	if layoutFor(state.width, state.height) < layoutCompact {
		state.page = current.registration
		state.status = statusReviewLarger

		return nil
	}

	switch key {
	case keyTab, keyLeft, keyRight, keyShiftTab:
		current.focus = toggledConfirmationFocus(current.focus)
	case keyEnter:
		if current.focus == confirmationBack {
			state.page = current.registration
			state.status = "Review repository setup"

			return nil
		}

		return state.startRegistration(registrationRequest(current.registration))
	case keyEscape:
		state.page = current.registration
		state.status = "Review repository setup"

		return nil
	case keyQuit:
		return tea.Quit
	}
	state.page = current

	return nil
}

func (state *model) startRegistration(request RepositorySetupRequest) tea.Cmd {
	return state.begin("Setting up repository", func(ctx context.Context, sequence uint64) tea.Cmd {
		catalog := state.catalog

		return func() tea.Msg {
			return registrationResultMsg{
				sequence: sequence, request: request, result: catalog.Register(ctx, request),
			}
		}
	})
}

func (state *model) handleRegistrationResult(result registrationResultMsg) tea.Cmd {
	accepted, command := state.completeOperation(result.sequence, nil)
	if !accepted {
		return command
	}
	if result.result.Failure != RepositorySetupReady {
		failure := canonicalRepositorySetupFailure(result.result.Failure)
		recovery, err := canonicalDisplay(result.result.RecoveryRepository)
		if err != nil {
			recovery = ""
			failure = RepositorySetupUnavailable
		}
		if recovery != "" {
			state.page = registrationPage{
				step: registrationCheckoutStep, mode: RepositorySetupCreateGitHub,
				remote: recovery, checkout: result.request.Checkout, created: true,
			}
			state.status = repositorySetupRecoveryStatus(failure)

			return command
		}
		state.status = repositorySetupFailureStatus(failure)

		return command
	}
	snapshot := canonicalCatalog(result.result.Snapshot)
	state.page = homePage{catalog: snapshot}
	state.status = "Repository ready"
	state.mutationOutcome = "Repository registered"
	if result.request.Mode == RepositorySetupCreateGitHub {
		state.mutationOutcome = "Private GitHub repository created"
	}

	return command
}

//nolint:exhaustive // Ready is handled before this failure-only canonicalizer.
func canonicalRepositorySetupFailure(failure RepositorySetupFailure) RepositorySetupFailure {
	switch failure {
	case RepositorySetupInvalidInput, RepositorySetupGitHubFailed, RepositorySetupCloneFailed,
		RepositorySetupRegistrationFailed, RepositorySetupUnavailable:
		return failure
	default:
		return RepositorySetupUnavailable
	}
}

//nolint:exhaustive // The private fallback contains malformed adapter output.
func repositorySetupFailureStatus(failure RepositorySetupFailure) string {
	switch failure {
	case RepositorySetupInvalidInput:
		return "Repository setup values are invalid; review the repository and checkout path"
	case RepositorySetupGitHubFailed:
		return "GitHub repository was not created; check gh authentication, then retry"
	case RepositorySetupCloneFailed:
		return "Repository clone failed; review the remote and checkout path"
	case RepositorySetupRegistrationFailed:
		return "Repository checkout could not be registered; inspect it before retrying"
	default:
		return repositorySetupUnavailableStatus
	}
}

//nolint:exhaustive // Recovery is available only after a repository was created.
func repositorySetupRecoveryStatus(failure RepositorySetupFailure) string {
	switch failure {
	case RepositorySetupCloneFailed:
		return "GitHub repository created, but checkout failed. Review the path, then retry"
	case RepositorySetupRegistrationFailed:
		return "Repository checkout exists, but registration failed. Inspect it, then retry"
	default:
		return "GitHub repository exists, but local setup did not finish. Review the path, then retry"
	}
}
