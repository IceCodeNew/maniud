//nolint:goconst // Compose contract matrices keep complete source values readable in place.
package compose

import (
	"context"
	"errors"
	"math"
	"reflect"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
)

//nolint:funlen // One complete Spec proves that every portable field round-trips together.
func TestEncodeRoundTripsPortableSpecAndAddsExplicitSyntax(t *testing.T) {
	t.Parallel()

	integer, integer64, retries, truth := 10, int64(3), 2, true
	spec := containerconfig.Spec{
		ServiceName: "api", ContainerName: "example-api",
		Platform:   containerconfig.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"},
		Entrypoint: []string{"/entrypoint"}, Command: []string{"serve"}, NetworkMode: bridgeNetwork,
		BlkioWeight: &integer, CgroupParent: "parent", Cgroup: "private", CPUs: "1.5", Hostname: "api",
		MemoryBytes: 1024, OOMScoreAdj: &integer, PidsLimit: &integer64, Restart: "always",
		SharedMemoryBytes: 2048, StopSignal: "SIGTERM", StopTimeout: &integer64,
		User: "1000", WorkingDirectory: "/work", CapAdd: []string{"NET_ADMIN"}, CapDrop: []string{"MKNOD"},
		DNS: []string{"1.1.1.1"}, DNSOptions: []string{"rotate"}, DNSSearch: []string{"example.test"},
		Devices:    []containerconfig.DeviceMapping{{Source: "/dev/a", Target: "/dev/b", Permissions: "rw"}},
		ExtraHosts: []string{"host=127.0.0.1"}, GroupAdd: []string{"1000"},
		Sysctls:     map[string]string{"net.ipv4.ip_forward": "1"},
		Tmpfs:       []containerconfig.TmpfsMount{{Target: "/run"}, {Target: "/tmp", Options: []string{"ro"}}},
		Ulimits:     []containerconfig.Ulimit{{Name: "core", Soft: 1, Hard: 1}, {Name: "nofile", Soft: 2, Hard: 3}},
		Environment: []string{"A=1", "EMPTY"}, Labels: []string{"team=platform"},
		ExposedPorts: []containerconfig.ExposedPort{{TargetPort: 53, Protocol: protocolUDP}},
		Ports: []containerconfig.PortBinding{
			{PublishedPort: 8080, TargetPort: 80, Protocol: protocolTCP},
			{HostIP: "::1", PublishedPort: 5353, TargetPort: 53, Protocol: protocolUDP},
		},
		NoNewPrivileges: true,
		Mounts: []containerconfig.Mount{
			{Kind: containerconfig.MountBind, Source: "/host/data", Target: "/data", ReadOnly: true},
			{Kind: containerconfig.MountVolume, Target: "/cache"},
		},
		Init: &truth, StdinOpen: &truth, OOMKillDisable: &truth, ReadOnly: &truth, TTY: &truth,
		Healthcheck: &containerconfig.Healthcheck{
			Test: []string{"CMD", "true"}, Interval: "30s", Timeout: "2s", Retries: &retries,
			StartPeriod: "5s", StartInterval: "1s",
		},
	}
	spec.ExposedPorts = append(spec.ExposedPorts, containerconfig.ExposedPort{TargetPort: 80, Protocol: protocolTCP})
	rendered, err := Encode(context.Background(), spec, EncodeOptions{
		Image: "example.test/api@sha256:" + strings.Repeat("a", 64), WorkingDirectory: "/work",
		EnvironmentFiles: []string{"api.env"}, PullPolicy: "never",
		Extensions: map[string]any{"x-example": map[string]any{
			"enabled": true, "values": []any{float32(1.5), uint8(2), nil},
		}},
	})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	text := string(rendered)
	for _, fragment := range []string{
		"platform: linux/arm64/v8", "env_file:", "pull_policy: never", "x-example:",
		"[::1]:5353:53/udp", "stop_grace_period: 3s",
	} {
		if !strings.Contains(text, fragment) {
			t.Fatalf("Encode() output missing %q:\n%s", fragment, text)
		}
	}

	portable, err := Encode(context.Background(), spec, EncodeOptions{
		Image: "example.test/api:1", WorkingDirectory: "/work",
	})
	if err != nil {
		t.Fatal(err)
	}
	decoded, err := Decode(context.Background(), portable, DecodeOptions{
		WorkingDirectory: "/work", Service: "api", Platform: spec.Platform,
	})
	if err != nil || !containerconfig.Equivalent(decoded, spec) {
		t.Fatalf("round trip = %#v, %v, want %#v", decoded, err, spec)
	}
}

//nolint:funlen // The table keeps the complete EncodeOptions rejection contract in one matrix.
func TestEncodeRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	valid := containerconfig.Spec{
		ServiceName: "api", ContainerName: "api", NetworkMode: bridgeNetwork,
		Platform: containerconfig.Platform{OS: "linux", Architecture: "amd64"},
	}
	tests := []struct {
		name    string
		spec    containerconfig.Spec
		options EncodeOptions
	}{
		{"image", valid, EncodeOptions{WorkingDirectory: "/work"}},
		{"working directory", valid, EncodeOptions{Image: "example.test/api:1", WorkingDirectory: "work"}},
		{"invalid spec", containerconfig.Spec{}, EncodeOptions{Image: "example.test/api:1", WorkingDirectory: "/work"}},
		{"invalid service", containerconfig.Spec{}, EncodeOptions{
			Image: "example.test/api:1", WorkingDirectory: "/work", ProjectName: "project",
		}},
		{"invalid platform", func() containerconfig.Spec {
			clone := valid.Clone()
			clone.Platform = containerconfig.Platform{}

			return clone
		}(), EncodeOptions{Image: "example.test/api:1", WorkingDirectory: "/work"}},
		{"duplicate ulimit", func() containerconfig.Spec {
			clone := valid.Clone()
			clone.Ulimits = []containerconfig.Ulimit{
				{Name: "nofile", Soft: 1, Hard: 1},
				{Name: "nofile", Soft: 2, Hard: 2},
			}

			return clone
		}(), EncodeOptions{Image: "example.test/api:1", WorkingDirectory: "/work"}},
		{"extension type", valid, EncodeOptions{
			Image: "example.test/api:1", WorkingDirectory: "/work", Extensions: map[string]any{"x-bad": make(chan int)},
		}},
		{"nested extension type", valid, EncodeOptions{
			Image: "example.test/api:1", WorkingDirectory: "/work",
			Extensions: map[string]any{"x-bad": []any{make(chan int)}},
		}},
		{"extension name", valid, EncodeOptions{
			Image: "example.test/api:1", WorkingDirectory: "/work", Extensions: map[string]any{"bad": true},
		}},
		{"extension number", valid, EncodeOptions{
			Image: "example.test/api:1", WorkingDirectory: "/work", Extensions: map[string]any{"x-bad": math.NaN()},
		}},
		{"pull policy", valid, EncodeOptions{
			Image: "example.test/api:1", WorkingDirectory: "/work", PullPolicy: "invalid",
		}},
		{"document size", valid, EncodeOptions{
			Image: "example.test/api:1", WorkingDirectory: "/work",
			Extensions: map[string]any{"x-large": strings.Repeat("x", maximumDocumentBytes)},
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if _, err := Encode(context.Background(), test.spec, test.options); err == nil {
				t.Fatal("Encode() accepted invalid input")
			}
		})
	}
}

func TestValidateEncodedClassifiesLoaderFailures(t *testing.T) {
	t.Parallel()

	err := validateEncoded(context.Background(), []byte("services: bad\n"), "/work", "project")
	assertValidation(t, err, containerconfig.ValidationInvalidValue, "")

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	err = validateEncoded(ctx, []byte("services: bad\n"), "/work", "project")
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("validateEncoded() error = %v", err)
	}
}

func TestNormalizeExtensionsRejectsCyclesAndUnsupportedMaps(t *testing.T) {
	t.Parallel()
	integers := []any{
		int(1), int8(1), int16(1), int32(1), int64(1),
		uint(1), uint16(1), uint32(1), uint64(1),
	}
	if _, ok := normalizeExtensions(map[string]any{"x-integers": integers}); !ok {
		t.Fatal("normalizeExtensions() rejected integer scalars")
	}

	cycle := make(map[string]any)
	cycle["self"] = cycle
	if _, ok := normalizeExtensions(map[string]any{"x-cycle": cycle}); ok {
		t.Fatal("normalizeExtensions() accepted a cycle")
	}
	if _, ok := normalizeExtensions(map[string]any{"x-map": map[int]string{1: "value"}}); ok {
		t.Fatal("normalizeExtensions() accepted non-string map keys")
	}
}

func TestEncodePreservesContextCancellation(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := Encode(ctx, containerconfig.Spec{}, EncodeOptions{})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("Encode() error = %v", err)
	}
}

func TestEncodedValueMarshalers(t *testing.T) {
	t.Parallel()

	if got, _ := (encodedMount{short: "/data"}).MarshalYAML(); got != "/data" {
		t.Fatal(got)
	}
	gotMount, _ := (encodedMount{bind: &encodedBindMount{Type: "bind"}}).MarshalYAML()
	if reflect.TypeOf(gotMount) != reflect.TypeFor[encodedBindMount]() {
		t.Fatal(gotMount)
	}
	if got, _ := (encodedUlimit{Soft: 2, Hard: 2}).MarshalYAML(); got != int64(2) {
		t.Fatal(got)
	}
	if got, _ := (encodedUlimit{Soft: 1, Hard: 2}).MarshalYAML(); reflect.TypeOf(got).Kind() != reflect.Struct {
		t.Fatal(got)
	}
	if encodedUlimits(nil) != nil {
		t.Fatal("nil values changed shape")
	}
}
