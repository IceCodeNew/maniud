package cli

import "slices"

const (
	helpOption      = "--help"
	shortHelpOption = "-h"
	versionOption   = "--version"
)

const rootHelp = `Usage: maniud COMMAND [ARGUMENTS]

Create and deploy Compose services, or reconcile them from Git.

Commands:
  gen          create a deployable Compose file
  apply        deploy one Compose service
  gitops init  register a tracked desired-state repository
  daemon       reconcile the registered repository

Run 'maniud COMMAND --help' for command-specific syntax.
`

const genHelp = `Usage: maniud gen [--name SERVICE] [--output PATH] SOURCE

Create a Compose file from an image reference or Docker archive member.
The command refuses runtime create/run argv and never overwrites PATH.
`

const applyHelp = `Usage: maniud apply COMPOSE [SERVICE]

Validate and apply one selected Compose service through the journaled transaction.
`

const gitopsHelp = `Usage: maniud gitops init [--branch BRANCH] REPOSITORY

Register the tracked desired-state repository.
`

const gitopsInitHelp = `Usage: maniud gitops init [--branch BRANCH] REPOSITORY

Register a clean checkout after proving its branch can fast-forward from origin.
`

const daemonHelp = `Usage: maniud daemon [--once] [--interval SECONDS]

Reconcile the registered repository. The default interval is 300 seconds.
`

func requestedHelp(args []string) (string, bool) {
	if len(args) == 1 && isHelp(args[0]) {
		return rootHelp, true
	}

	if len(args) < 2 || !containsHelp(args[1:]) {
		return "", false
	}

	if args[0] == gitOpsCommand {
		return requestedGitOpsHelp(args)
	}

	return requestedLeafHelp(args[0])
}

func requestedLeafHelp(name string) (string, bool) {
	switch name {
	case string(commandGen):
		return genHelp, true
	case string(commandApply):
		return applyHelp, true
	case string(commandDaemon):
		return daemonHelp, true
	default:
		return "", false
	}
}

func requestedGitOpsHelp(args []string) (string, bool) {
	if args[1] == initCommand && containsHelp(args[2:]) {
		return gitopsInitHelp, true
	}

	if isHelp(args[1]) {
		return gitopsHelp, true
	}

	return "", false
}

func containsHelp(args []string) bool {
	return slices.Contains(args, shortHelpOption) || slices.Contains(args, helpOption)
}

func isHelp(value string) bool {
	return value == shortHelpOption || value == helpOption
}
