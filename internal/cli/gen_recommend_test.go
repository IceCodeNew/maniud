package cli

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

func TestRecommendImageWorkloadAddsSafeDefaults(t *testing.T) {
	t.Parallel()

	workload := domain.WorkloadSpec{
		Environment: []string{"KEEP=yes"},
		ExposedPorts: []domain.ExposedPort{
			{TargetPort: 8001, Protocol: protocolTCP},
			{TargetPort: 5353, Protocol: protocolUDP},
			{TargetPort: 7000, Protocol: "sctp"},
		},
	}
	publish := func(exposed []domain.ExposedPort) ([]domain.PortBinding, error) {
		if !slices.Equal(exposed, workload.ExposedPorts[:2]) {
			t.Fatalf("publish input = %#v", exposed)
		}

		return []domain.PortBinding{
			{HostIP: recommendedLoopbackAddress, PublishedPort: 49152, TargetPort: 8001, Protocol: protocolTCP},
			{HostIP: recommendedLoopbackAddress, PublishedPort: 5353, TargetPort: 5353, Protocol: protocolUDP},
		}, nil
	}
	got, warnings, err := recommendImageWorkload(workload, imageRecommendationOptions{
		publishPorts: publish, localtimeAvailable: true, timezone: testTimezone, timezoneSet: true,
	})
	if err != nil {
		t.Fatalf("recommendImageWorkload() error = %v", err)
	}
	assertRecommendedWorkloadDefaults(t, got)
	assertRecommendedWorkloadWarnings(t, warnings)
	if workload.Restart != "" || workload.NoNewPrivileges || len(workload.Mounts) != 0 || len(workload.Ports) != 0 ||
		!slices.Equal(workload.Environment, []string{"KEEP=yes"}) {
		t.Fatalf("source workload was modified: %#v", workload)
	}
}

func assertRecommendedWorkloadDefaults(t *testing.T, workload domain.WorkloadSpec) {
	t.Helper()

	if workload.Restart != recommendedRestartPolicy || !workload.NoNewPrivileges || len(workload.Mounts) != 1 ||
		workload.Mounts[0] != (domain.Mount{
			Kind: domain.MountBind, Source: recommendedLocaltimePath,
			Target: recommendedLocaltimePath, ReadOnly: true,
		}) || len(workload.Ports) != 2 ||
		!slices.Contains(workload.Environment, testTimezoneAssignment) ||
		!slices.Contains(workload.Environment, "KEEP=yes") {
		t.Fatalf("recommended workload = %#v", workload)
	}
}

func assertRecommendedWorkloadWarnings(t *testing.T, warnings []runtimeargv.Warning) {
	t.Helper()

	if len(warnings) != 2 || warnings[0].Code != warningImagePortSkipped ||
		warnings[1].Code != warningImagePortReassigned ||
		!strings.Contains(warnings[1].Reason, "127.0.0.1:49152") {
		t.Fatalf("warnings = %#v", warnings)
	}
}

func TestRecommendImageWorkloadPreservesExplicitConfiguration(t *testing.T) {
	t.Parallel()

	workload := domain.WorkloadSpec{
		Restart: "always", NoNewPrivileges: true,
		Environment:  []string{"TZ=UTC"},
		Mounts:       []domain.Mount{{Kind: domain.MountVolume, Target: recommendedLocaltimePath}},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 8080, Protocol: protocolTCP}},
		Ports: []domain.PortBinding{{
			HostIP: "0.0.0.0", PublishedPort: 80, TargetPort: 8080, Protocol: protocolTCP,
		}},
	}
	got, warnings, err := recommendImageWorkload(workload, imageRecommendationOptions{
		localtimeAvailable: true, timezone: testTimezone, timezoneSet: true,
		publishPorts: func([]domain.ExposedPort) ([]domain.PortBinding, error) {
			t.Fatal("publisher called for an already published port")

			return nil, nil
		},
	})
	if err != nil || len(warnings) != 0 ||
		got.Restart != workload.Restart ||
		!slices.Equal(got.Environment, workload.Environment) ||
		!slices.Equal(got.Mounts, workload.Mounts) || !slices.Equal(got.Ports, workload.Ports) {
		t.Fatalf("recommendImageWorkload() = %#v, %#v, %v", got, warnings, err)
	}
}

func TestRecommendImageWorkloadRejectsInvalidPublisherResult(t *testing.T) {
	t.Parallel()

	workload := domain.WorkloadSpec{
		ExposedPorts: []domain.ExposedPort{{TargetPort: 8080, Protocol: protocolTCP}},
	}
	for _, publish := range []generatedPortPublisher{
		func([]domain.ExposedPort) ([]domain.PortBinding, error) { return nil, errGeneratedComposeTest },
		func([]domain.ExposedPort) ([]domain.PortBinding, error) { return nil, nil },
		func([]domain.ExposedPort) ([]domain.PortBinding, error) {
			return []domain.PortBinding{{
				HostIP: "0.0.0.0", PublishedPort: 8080, TargetPort: 8080, Protocol: protocolTCP,
			}}, nil
		},
	} {
		if _, _, err := recommendImageWorkload(workload, imageRecommendationOptions{
			publishPorts: publish, localtimeAvailable: true,
		}); !errors.Is(err, runtimeargv.ErrInvalid) {
			t.Fatalf("recommendImageWorkload() error = %v", err)
		}
	}
}

func TestRecommendImageWorkloadSkipsUnavailableLocaltime(t *testing.T) {
	t.Parallel()

	workload, warnings, err := recommendImageWorkload(domain.WorkloadSpec{}, imageRecommendationOptions{})
	if err != nil || len(workload.Mounts) != 0 || len(warnings) != 1 ||
		warnings[0].Code != warningLocaltimeUnavailable || warnings[0].Option != recommendedLocaltimePath {
		t.Fatalf("recommendImageWorkload(unavailable localtime) = %#v, %#v, %v", workload, warnings, err)
	}
}

func TestRecommendImageWorkloadRejectsInvalidTimezone(t *testing.T) {
	t.Parallel()

	for _, timezone := range []string{"bad\x00timezone", string([]byte{0xff})} {
		_, _, err := recommendImageWorkload(domain.WorkloadSpec{}, imageRecommendationOptions{
			localtimeAvailable: true, timezone: timezone, timezoneSet: true,
		})
		if !errors.Is(err, runtimeargv.ErrInvalid) {
			t.Fatalf("recommendImageWorkload(timezone %q) error = %v", timezone, err)
		}
	}
}

func TestDefaultImageRecommendationOptionsCapturesHostInputs(t *testing.T) {
	t.Parallel()

	options := defaultImageRecommendationOptions(map[string]string{timezoneEnvironment: testTimezone})
	if options.publishPorts == nil || !options.timezoneSet || options.timezone != testTimezone {
		t.Fatalf("defaultImageRecommendationOptions() = %#v", options)
	}
}

func TestRegularFileExistsFollowsSymlinksAndRejectsOtherPaths(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	file := filepath.Join(directory, "localtime")
	if err := os.WriteFile(file, []byte("timezone"), 0o600); err != nil {
		t.Fatal(err)
	}
	link := filepath.Join(directory, "localtime-link")
	if err := os.Symlink(file, link); err != nil {
		t.Fatal(err)
	}
	if !regularFileExists(file) || !regularFileExists(link) || regularFileExists(directory) ||
		regularFileExists(filepath.Join(directory, "missing")) {
		t.Fatal("regularFileExists() classified a path incorrectly")
	}
}

func TestReserveLoopbackPortsFallsBackWhenTargetIsOccupied(t *testing.T) {
	t.Parallel()

	var config net.ListenConfig
	listener, err := config.Listen(context.Background(), "tcp4", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("Listen() error = %v", err)
	}
	t.Cleanup(func() { _ = listener.Close() })
	address, ok := listener.Addr().(*net.TCPAddr)
	if !ok {
		t.Fatalf("Listen() address = %T", listener.Addr())
	}
	occupied := uint16(address.Port) //nolint:gosec // Ephemeral TCP ports fit uint16.

	ports, err := reserveLoopbackPorts([]domain.ExposedPort{{TargetPort: occupied, Protocol: protocolTCP}})
	if err != nil {
		t.Fatalf("reserveLoopbackPorts() error = %v", err)
	}
	if len(ports) != 1 || ports[0].HostIP != recommendedLoopbackAddress ||
		ports[0].TargetPort != occupied || ports[0].PublishedPort == occupied || ports[0].PublishedPort == 0 {
		t.Fatalf("reserveLoopbackPorts() = %#v", ports)
	}
}

func TestReserveLoopbackPortsSupportsUDPAndRejectsUnknownProtocols(t *testing.T) {
	t.Parallel()

	published, listener, err := reserveLoopbackPort(protocolUDP, 0)
	if err != nil || published == 0 || listener == nil {
		t.Fatalf("reserveLoopbackPort(udp) = %d, %#v, %v", published, listener, err)
	}
	if err := listener.Close(); err != nil {
		t.Fatal(err)
	}
	if _, _, err := reserveLoopbackPort("sctp", 80); !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("reserveLoopbackPort(sctp) error = %v", err)
	}
	if _, err := reserveLoopbackPorts([]domain.ExposedPort{{TargetPort: 80, Protocol: "sctp"}}); !errors.Is(
		err,
		runtimeargv.ErrInvalid,
	) {
		t.Fatalf("reserveLoopbackPorts(sctp) error = %v", err)
	}
}

func TestValidRecommendedPortsRejectsDuplicatePublication(t *testing.T) {
	t.Parallel()

	exposed := []domain.ExposedPort{
		{TargetPort: 80, Protocol: protocolTCP},
		{TargetPort: 81, Protocol: protocolTCP},
	}
	ports := []domain.PortBinding{
		{HostIP: recommendedLoopbackAddress, PublishedPort: 8080, TargetPort: 80, Protocol: protocolTCP},
		{HostIP: recommendedLoopbackAddress, PublishedPort: 8080, TargetPort: 81, Protocol: protocolTCP},
	}
	if validRecommendedPorts(exposed, ports) {
		t.Fatal("validRecommendedPorts() accepted duplicate host publication")
	}
	if hasMountTarget([]domain.Mount{{Target: "/other"}}, recommendedLocaltimePath) {
		t.Fatal("hasMountTarget() matched a different target")
	}
	if hasPublishedPort([]domain.PortBinding{{TargetPort: 81, Protocol: protocolUDP}}, exposed[0]) {
		t.Fatal("hasPublishedPort() matched a different target and protocol")
	}
}
