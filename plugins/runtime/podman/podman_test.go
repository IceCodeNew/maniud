package podman

import (
	"errors"
	"testing"

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
	if got != "" || !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("socketPath(invalid user) = %q, %v", got, err)
	}
}

func TestPluginClassifiesPodmanAvailability(t *testing.T) {
	t.Parallel()

	plugin := New()
	if plugin.Open == nil || !plugin.Unavailable(ErrUnavailable) ||
		plugin.Unavailable(ErrInvalidEndpoint) {
		t.Fatalf("New() = %#v", plugin)
	}
	if got := environmentValue(nil, containerHostEnvironment); got != "" {
		t.Fatalf("environmentValue(nil) = %q", got)
	}
}

func TestPluginOpenContainsConfigurationAndConnectionFailures(t *testing.T) {
	t.Parallel()

	if _, err := open(t.Context(), testEnvironment(map[string]string{
		containerHostEnvironment: "invalid://engine",
	}), nil); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("open(invalid endpoint) error = %v", err)
	}
	if _, err := open(t.Context(), testEnvironment(map[string]string{
		containerHostEnvironment: "unix:///missing/podman.sock",
	}), nil); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("open(missing endpoint) error = %v", err)
	}

	path := startPodmanTestServer(
		t,
		podmanNegotiationHandler(maximumLibpodAPIVersion, testLibpodServerMinimum, maximumLibpodAPIVersion),
	)
	runtime, err := open(t.Context(), testEnvironment(map[string]string{
		containerHostEnvironment: "unix://" + path,
	}), nil)
	if err != nil {
		t.Fatalf("open() error = %v", err)
	}
	runtime.CloseIdleConnections()
}

func testEnvironment(values map[string]string) runtimeplugin.Environment {
	return func(name string) string { return values[name] }
}
