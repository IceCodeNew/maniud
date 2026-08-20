/*
Maniud creates and deploys Compose services or reconciles them from Git.

Usage:

	maniud [--debug] COMMAND [ARGUMENTS]

The commands are:

	gen          create a deployable Compose file
	apply        deploy one Compose service
	gitops init  register a tracked desired-state repository
	daemon       reconcile the registered repository
	doctor       inspect or rebuild maniud's internal backup index

The --debug option includes internal diagnostic context in command failures.

Command syntax:

	maniud gen [--name SERVICE] [--output PATH] SOURCE
	maniud gen [--name SERVICE] [--output PATH] -- RUNTIME {create|run} [ARGUMENTS]
	maniud apply [--dry-run] COMPOSE [SERVICE]
	maniud gitops init [--branch BRANCH] REPOSITORY
	maniud daemon [--once] [--interval SECONDS]
	maniud doctor --reindex-backups [--confirm] [--config PATH] [--state PATH]

Run maniud COMMAND --help for command-specific behavior.
*/
package main

import (
	"context"
	"os"

	"github.com/IceCodeNew/maniud/internal/cli"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := cli.CommandContext(context.Background())
	defer stop()

	return cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr)
}
