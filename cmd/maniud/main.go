// Command maniud creates and deploys Compose services or reconciles them from Git.
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
