package containerd

import (
	"testing"

	containerdruntime "github.com/IceCodeNew/maniud/internal/runtime/containerd"
)

func TestPluginProvidesContainerdCapabilities(t *testing.T) {
	t.Parallel()

	plugin := New()
	if plugin.Open == nil || plugin.ResolveLocalImage == nil ||
		!plugin.Unavailable(containerdruntime.ErrUnavailable) ||
		plugin.Unavailable(containerdruntime.ErrInvalidEndpoint) {
		t.Fatalf("New() = %#v", plugin)
	}
}
