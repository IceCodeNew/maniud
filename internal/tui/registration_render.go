package tui

import "github.com/IceCodeNew/maniud/internal/terminaltext"

func (state *model) registrationLocation() (string, bool) {
	switch state.page.(type) {
	case registrationPage, registrationConfirmationPage:
		return "Home / Repository setup", true
	default:
		return "", false
	}
}

func (state *model) registrationPageBody(width int) ([]string, bool) {
	switch current := state.page.(type) {
	case registrationPage:
		return state.registrationBody(current, width), true
	case registrationConfirmationPage:
		return state.registrationConfirmationBody(current, width), true
	default:
		return nil, false
	}
}

func (state *model) registrationBody(current registrationPage, width int) []string {
	switch current.step {
	case registrationModeStep:
		return []string{
			state.title("Set up repository"),
			"Choose where the desired-state repository comes from.",
			"",
			state.choice(current.cursor == 0, "Create private GitHub repository", width),
			state.choice(current.cursor == 1, "Use existing Git repository", width),
		}
	case registrationRemoteStep:
		title := "GitHub repository"
		description := "Enter OWNER/REPOSITORY. Maniud will invoke gh and create a private repository."
		label := "Name  "
		if current.mode == RepositorySetupExisting {
			title = "Existing Git repository"
			description = "Enter an HTTPS or file remote. This path uses git and does not invoke gh."
			label = "Remote  "
		}
		line := label + terminaltext.Middle(current.remote, max(width-terminaltext.Width(label), 1), "…")

		return []string{state.title(title), description, "", state.accent(line + state.symbol("▌", "_"))}
	case registrationCheckoutStep:
		label := "Path  "
		line := label + terminaltext.Middle(current.checkout, max(width-terminaltext.Width(label), 1), "…")

		return []string{
			state.title("Local checkout"),
			"Choose an absolute path for the desired-state checkout.",
			"",
			state.accent(line + state.symbol("▌", "_")),
		}
	default:
		return []string{state.title("Set up repository"), "", repositorySetupUnavailableStatus}
	}
}

func (state *model) registrationConfirmationBody(
	current registrationConfirmationPage,
	width int,
) []string {
	request := registrationRequest(current.registration)
	remote := terminaltext.Middle(request.Remote, max(width-pathLabelWidth, 1), "…")
	path := terminaltext.Middle(request.Checkout, max(width-pathLabelWidth, 1), "…")
	description := "Create the private GitHub repository, clone it, and register the checkout?"
	action := "Create and register"
	if request.Created {
		description = "Retry the local clone or registration without creating another GitHub repository?"
		action = "Retry local setup"
	} else if request.Mode == RepositorySetupExisting {
		description = "Clone or reuse this Git repository and register the clean checkout?"
		action = "Clone and register"
	}

	return []string{
		state.title("Confirm repository setup"),
		description,
		"",
		"Source  " + remote,
		"Path    " + path,
		"",
		state.choice(current.focus == confirmationBack, "Back", width),
		state.choice(current.focus == confirmationApply, action, width),
	}
}

func (state *model) registrationFooter() (string, bool) {
	switch current := state.page.(type) {
	case registrationPage:
		switch current.step {
		case registrationModeStep:
			return "Up/Down Choose   Enter Continue   Esc Skip", true
		case registrationRemoteStep:
			return "Type source   Enter Continue   Esc Back", true
		case registrationCheckoutStep:
		default:
			return "", false
		}
		if current.created {
			return "Type path   Enter Review", true
		}

		return "Type path   Enter Review   Esc Back", true
	case registrationConfirmationPage:
		return confirmationKeys, true
	default:
		return "", false
	}
}
