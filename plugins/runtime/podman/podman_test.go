package podman

import (
	"errors"
	"testing"

	podmanruntime "github.com/IceCodeNew/maniud/internal/runtime/podman"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

func TestSocketPathUsesOnlyLocalUnixEndpoints(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name        string
		environment map[string]string
		want        string
		wantErr     bool
	}{
		{
			name: "explicit", environment: map[string]string{containerHostEnvironment: "unix:///tmp/podman.sock"},
			want: "/tmp/podman.sock",
		},
		{
			name: "XDG runtime", environment: map[string]string{"XDG_RUNTIME_DIR": "/run/user/1000"},
			want: "/run/user/1000/podman/podman.sock",
		},
		{
			name: "remote", environment: map[string]string{containerHostEnvironment: "ssh://host/run/podman.sock"},
			wantErr: true,
		},
		{
			name: "relative", environment: map[string]string{containerHostEnvironment: "unix://podman.sock"},
			wantErr: true,
		},
		{name: "unclean", environment: map[string]string{"XDG_RUNTIME_DIR": "/run/../tmp"}, wantErr: true},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := socketPath(testEnvironment(test.environment), 1000)
			if (err != nil) != test.wantErr || got != test.want {
				t.Fatalf("socketPath() = %q, %v", got, err)
			}
		})
	}

	got, err := socketPath(testEnvironment(nil), 0)
	if err != nil || got != defaultRootfulSocket {
		t.Fatalf("socketPath(root) = %q, %v", got, err)
	}
	got, err = socketPath(testEnvironment(nil), 1000)
	if err != nil || got != "/run/user/1000/podman/podman.sock" {
		t.Fatalf("socketPath(rootless default) = %q, %v", got, err)
	}
	got, err = socketPath(testEnvironment(nil), -1)
	if got != "" || !errors.Is(err, podmanruntime.ErrInvalidEndpoint) {
		t.Fatalf("socketPath(invalid user) = %q, %v", got, err)
	}
}

func TestPluginClassifiesPodmanAvailability(t *testing.T) {
	t.Parallel()

	plugin := New()
	if plugin.Open == nil || !plugin.Unavailable(podmanruntime.ErrUnavailable) ||
		plugin.Unavailable(podmanruntime.ErrInvalidEndpoint) {
		t.Fatalf("New() = %#v", plugin)
	}
}

func testEnvironment(values map[string]string) runtimeplugin.Environment {
	return func(name string) string { return values[name] }
}
