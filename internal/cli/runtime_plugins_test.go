package cli

import (
	"testing"

	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
	containerdplugin "github.com/IceCodeNew/maniud/plugins/runtime/containerd"
	dockerplugin "github.com/IceCodeNew/maniud/plugins/runtime/docker"
	podmanplugin "github.com/IceCodeNew/maniud/plugins/runtime/podman"
)

func testRuntimePlugins(tb testing.TB) runtimeplugin.Set {
	tb.Helper()

	plugins, err := runtimeplugin.NewSet(
		dockerplugin.New(),
		podmanplugin.New(),
		containerdplugin.New(),
	)
	if err != nil {
		tb.Fatalf("compose test runtime plugins: %v", err)
	}

	return plugins
}
