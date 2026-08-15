package cli

import "slices"

const (
	helpOption      = "--help"
	shortHelpOption = "-h"
	versionOption   = "--version"
)

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
