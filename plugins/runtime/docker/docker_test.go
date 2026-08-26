package docker

import (
	"errors"
	"io"
	"reflect"
	"testing"

	dockerruntime "github.com/IceCodeNew/maniud/internal/runtime/docker"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const testPlainHost = "tcp://10.0.0.1:2375"

//nolint:funlen // The table keeps every supported and rejected endpoint form together.
func TestEndpointSelection(t *testing.T) {
	t.Parallel()

	for _, environment := range []map[string]string{
		{},
		{dockerHostEnvironment: "unix:///tmp/docker.sock"},
	} {
		selected, err := endpoint(testEnvironment(environment), func(runtimeplugin.Warning) error { return nil })
		if err != nil || reflect.ValueOf(selected).IsZero() {
			t.Fatalf("endpoint(%v) = %#v, %v", environment, selected, err)
		}
	}

	var warning runtimeplugin.Warning
	selected, err := endpoint(
		testEnvironment(map[string]string{dockerHostEnvironment: testPlainHost}),
		func(got runtimeplugin.Warning) error {
			warning = got

			return nil
		},
	)
	if err != nil || reflect.ValueOf(selected).IsZero() || warning.Code != "insecure_remote_engine" ||
		warning.Message == "" {
		t.Fatalf("endpoint(VPN) = %#v, %v, warning %#v", selected, err, warning)
	}

	tests := []struct {
		name        string
		environment map[string]string
		warnings    runtimeplugin.WarningSink
		want        error
	}{
		{
			name:        "invalid Unix",
			environment: map[string]string{dockerHostEnvironment: "unix://relative.sock"},
			warnings:    func(runtimeplugin.Warning) error { return nil },
			want:        dockerruntime.ErrInvalidEndpoint,
		},
		{
			name:        "SSH without explicit authentication",
			environment: map[string]string{dockerHostEnvironment: "ssh://engine.example"},
			warnings:    func(runtimeplugin.Warning) error { return nil },
			want:        dockerruntime.ErrInvalidEndpoint,
		},
		{
			name:        "VPN without warning transport",
			environment: map[string]string{dockerHostEnvironment: testPlainHost},
			warnings:    nil,
			want:        dockerruntime.ErrWarningDelivery,
		},
		{
			name:        "VPN warning failure",
			environment: map[string]string{dockerHostEnvironment: testPlainHost},
			warnings:    func(runtimeplugin.Warning) error { return io.ErrClosedPipe },
			want:        dockerruntime.ErrWarningDelivery,
		},
		{
			name: "TLS not configured",
			environment: map[string]string{
				dockerHostEnvironment:      "tcp://engine.example:2376",
				dockerTLSVerifyEnvironment: "1",
			},
			warnings: func(runtimeplugin.Warning) error { return nil },
			want:     dockerruntime.ErrInvalidEndpoint,
		},
		{
			name:        "unknown scheme",
			environment: map[string]string{dockerHostEnvironment: "http://engine.example:2375"},
			warnings:    func(runtimeplugin.Warning) error { return nil },
			want:        dockerruntime.ErrInvalidEndpoint,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			_, err := endpoint(testEnvironment(test.environment), test.warnings)
			if !errors.Is(err, test.want) {
				t.Fatalf("endpoint() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestPluginClassifiesDockerAvailability(t *testing.T) {
	t.Parallel()

	plugin := New()
	if plugin.Open == nil || !plugin.Unavailable(dockerruntime.ErrUnavailable) ||
		plugin.Unavailable(dockerruntime.ErrInvalidEndpoint) {
		t.Fatalf("New() = %#v", plugin)
	}
}

func testEnvironment(values map[string]string) runtimeplugin.Environment {
	return func(name string) string { return values[name] }
}
