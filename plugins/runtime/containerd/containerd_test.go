package containerd

import (
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

func TestPluginProvidesContainerdCapabilities(t *testing.T) {
	t.Parallel()

	plugin := New()
	if plugin.Open == nil || plugin.ResolveLocalImage == nil ||
		!plugin.Unavailable(ErrUnavailable) ||
		plugin.Unavailable(ErrInvalidEndpoint) {
		t.Fatalf("New() = %#v", plugin)
	}
	if got := environmentValue(nil, addressEnvironment); got != "" {
		t.Fatalf("environmentValue(nil) = %q", got)
	}
	if got := environmentValue(func(name string) string { return name }, addressEnvironment); got != addressEnvironment {
		t.Fatalf("environmentValue(configured) = %q", got)
	}
	if _, err := plugin.Open(t.Context(), nil, nil); err == nil {
		t.Fatal("Open(unconfigured) succeeded")
	}
	if _, err := plugin.ResolveLocalImage(
		t.Context(), nil, imageref.Source{}, domain.Platform{},
	); err == nil {
		t.Fatal("ResolveLocalImage(unconfigured) succeeded")
	}
}
