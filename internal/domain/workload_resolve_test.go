package domain_test

import (
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testImageEntrypoint = "/image-entrypoint"
	testTCPProtocol     = "tcp"
	testDataPath        = "/data"
	testStopSignal      = "SIGTERM"
	testHealthCommand   = "CMD"
	testTrueCommand     = "true"
	testChangedValue    = "changed"
)

func TestResolveWorkloadSpecAppliesImageDefaultsAndServicePrecedence(t *testing.T) {
	t.Parallel()

	retries := 3
	image := domain.ImageIdentity{
		User: "1000", Environment: []string{"DROP=image", "KEEP=image", "OVERRIDE=image"},
		Entrypoint: []string{testImageEntrypoint}, Command: []string{"image-command"},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 443, Protocol: testTCPProtocol}},
		Volumes:      []string{testDataPath, "/image-only"}, WorkingDirectory: "/work",
		Labels:     []string{"image=yes", domain.LabelService + "=forged", "override=image"},
		StopSignal: testStopSignal, Healthcheck: &domain.Healthcheck{
			Test: []string{testHealthCommand, testTrueCommand}, Interval: "30s", Retries: &retries,
		},
	}
	spec := domain.WorkloadSpec{
		Command: []string{}, Environment: []string{"DROP", "OVERRIDE=service", "SERVICE=yes"},
		Labels:       []string{"override=service", "service=yes"},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 53, Protocol: "udp"}},
		Ports:        []domain.PortBinding{{PublishedPort: 8080, TargetPort: 80, Protocol: testTCPProtocol}},
		Mounts:       []domain.Mount{{Kind: domain.MountBind, Source: "/host/data", Target: testDataPath}},
	}

	got := domain.ResolveWorkloadSpec(spec, image)
	want := domain.WorkloadSpec{
		User: "1000", Entrypoint: []string{testImageEntrypoint}, Command: []string{},
		WorkingDirectory: "/work", StopSignal: testStopSignal,
		Environment: []string{"KEEP=image", "OVERRIDE=service", "SERVICE=yes"},
		Labels:      []string{"image=yes", "override=service", "service=yes"},
		ExposedPorts: []domain.ExposedPort{
			{TargetPort: 53, Protocol: "udp"}, {TargetPort: 80, Protocol: testTCPProtocol},
			{TargetPort: 443, Protocol: testTCPProtocol},
		},
		Ports: []domain.PortBinding{{PublishedPort: 8080, TargetPort: 80, Protocol: testTCPProtocol}},
		Mounts: []domain.Mount{
			{Kind: domain.MountBind, Source: "/host/data", Target: testDataPath},
			{Kind: domain.MountVolume, Target: "/image-only"},
		},
		Healthcheck: &domain.Healthcheck{
			Test: []string{testHealthCommand, testTrueCommand}, Interval: "30s", Retries: &retries,
		},
	}
	if !reflect.DeepEqual(got, want) {
		t.Fatalf("ResolveWorkloadSpec() = %#v, want %#v", got, want)
	}

	got.Entrypoint[0] = testChangedValue
	got.Healthcheck.Test[0] = testChangedValue
	if image.Entrypoint[0] != testImageEntrypoint || image.Healthcheck.Test[0] != testHealthCommand {
		t.Fatal("resolved workload aliases image identity")
	}
}

func TestResolveWorkloadSpecKeepsExplicitProcessAndConfiguration(t *testing.T) {
	t.Parallel()

	spec := domain.WorkloadSpec{
		Entrypoint: []string{}, Command: []string{"service"}, User: "2000", WorkingDirectory: "/service",
		StopSignal: "SIGINT", Healthcheck: &domain.Healthcheck{Disabled: true},
	}
	image := domain.ImageIdentity{
		Entrypoint: []string{"image"}, Command: []string{"image"}, User: "1000",
		WorkingDirectory: "/image", StopSignal: testStopSignal,
		Healthcheck: &domain.Healthcheck{Test: []string{testHealthCommand, testTrueCommand}},
	}

	got := domain.ResolveWorkloadSpec(spec, image)
	if !reflect.DeepEqual(got.Entrypoint, []string{}) || !reflect.DeepEqual(got.Command, []string{"service"}) ||
		got.User != "2000" || got.WorkingDirectory != "/service" || got.StopSignal != "SIGINT" ||
		got.Healthcheck == nil || !got.Healthcheck.Disabled {
		t.Fatalf("ResolveWorkloadSpec() = %#v", got)
	}
}

func TestResolveWorkloadSpecPreservesNilAndEmptyDefaults(t *testing.T) {
	t.Parallel()

	got := domain.ResolveWorkloadSpec(domain.WorkloadSpec{}, domain.ImageIdentity{})
	if got.Entrypoint != nil || got.Command != nil || got.Healthcheck != nil {
		t.Fatalf("nil image defaults became explicit values: %#v", got)
	}
	if got.Environment != nil || got.Labels != nil || got.ExposedPorts != nil || got.Mounts != nil {
		t.Fatalf("nil merged image defaults became explicit values: %#v", got)
	}

	got = domain.ResolveWorkloadSpec(domain.WorkloadSpec{
		Environment: []string{domain.LabelService + "=forged"},
		Labels:      []string{domain.LabelTransaction + "=forged"},
	}, domain.ImageIdentity{
		Environment: []string{domain.LabelService + "=forged"},
		Labels:      []string{domain.LabelTransaction + "=forged"},
	})
	if len(got.Environment) != 0 || len(got.Labels) != 0 {
		t.Fatalf("ownership labels survived resolution: %#v", got)
	}
}

func TestImageIdentityCloneIsDeep(t *testing.T) {
	t.Parallel()

	retries := 2
	original := domain.ImageIdentity{
		Environment: []string{"A=1"}, Entrypoint: []string{"entry"}, Command: []string{"cmd"},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 80, Protocol: "tcp"}},
		Volumes:      []string{"/data"}, Labels: []string{"a=b"},
		Healthcheck: &domain.Healthcheck{Test: []string{"CMD", "true"}, Retries: &retries},
	}
	clone := original.Clone()
	clone.Environment[0], clone.Entrypoint[0], clone.Command[0] = testChangedValue, testChangedValue, testChangedValue
	clone.ExposedPorts[0].TargetPort, clone.Volumes[0], clone.Labels[0] = 81, "/changed", "changed=yes"
	clone.Healthcheck.Test[0], *clone.Healthcheck.Retries = testChangedValue, 9
	if reflect.DeepEqual(original, clone) || original.Environment[0] != "A=1" ||
		original.Entrypoint[0] != "entry" || original.Command[0] != "cmd" ||
		original.ExposedPorts[0].TargetPort != 80 || original.Volumes[0] != "/data" ||
		original.Labels[0] != "a=b" || original.Healthcheck.Test[0] != "CMD" ||
		*original.Healthcheck.Retries != 2 {
		t.Fatalf("Clone() aliases original: original=%#v clone=%#v", original, clone)
	}
}
