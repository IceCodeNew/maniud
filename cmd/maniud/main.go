// Command maniud creates and deploys Compose services or reconciles them from Git.
package main

import (
	"context"
	"fmt"
	"os"

	"github.com/IceCodeNew/maniud/internal/cli"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
	containerdplugin "github.com/IceCodeNew/maniud/plugins/runtime/containerd"
	dockerplugin "github.com/IceCodeNew/maniud/plugins/runtime/docker"
	podmanplugin "github.com/IceCodeNew/maniud/plugins/runtime/podman"
)

func main() {
	os.Exit(run())
}

func run() int {
	ctx, stop := cli.CommandContext(context.Background())
	defer stop()
	runtimes, err := runtimeplugin.NewSet(
		dockerplugin.New(),
		podmanplugin.New(),
		containerdplugin.New(),
	)
	if err != nil {
		_, _ = fmt.Fprintln(os.Stderr, "maniud:", err)

		return 1
	}

	return cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, runtimes)
}
