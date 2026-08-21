package cli

import "slices"

const (
	helpOption      = "--help"
	shortHelpOption = "-h"
	versionOption   = "--version"
)

const rootHelp = `Usage: maniud [--debug] COMMAND [ARGUMENTS]

Create and deploy Compose services, or reconcile them from Git.

Commands:
  gen          create a deployable Compose file
  apply        deploy one Compose service
  gitops init  register a tracked desired-state repository
  daemon       reconcile the registered repository
  doctor       inspect or rebuild maniud's internal backup index

Options:
  --debug      include internal diagnostic context in command failures

Run 'maniud COMMAND --help' for command-specific syntax.
`

const genHelp = `Usage:
  maniud gen [--name SERVICE] [--output PATH] SOURCE
  maniud gen [--name SERVICE] [--output PATH] -- RUNTIME {create|run} [ARGUMENTS]

Create a Compose file from an image reference, a Docker archive member, or a
supported docker, podman, or nerdctl create/run command. Runtime arguments are
parsed as input and are never executed. The command refuses to replace an
existing output file.
`

const applyHelp = `Usage: maniud apply [--dry-run] COMPOSE [SERVICE]

Validate and apply one selected Compose service through the journaled transaction.
--dry-run validates Compose and selected-runtime support without runtime effects
or persistent writes.
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

const doctorHelp = `Usage: maniud doctor --reindex-backups [--confirm] [--state PATH]

Validate complete maniud backup manifests and report candidate entries for its
internal index. The command is read-only unless --confirm is supplied;
confirmation only rebuilds maniud's internal backup index and does not infer
applied commits or transactions.
`

func requestedHelp(args []string) (string, bool) {
	if len(args) > 0 && args[0] == debugOption {
		args = args[1:]
	}

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
	case string(commandDoctor):
		return doctorHelp, true
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
