package docker

import (
	"context"
	"errors"
	"math"
	"net/http"
	"net/netip"
	"strings"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func testCreateOptions() application.WorkloadCreateOptions {
	return application.WorkloadCreateOptions{CopyImageVolumes: true}
}

//nolint:gocognit,gocyclo,cyclop,funlen // This test intentionally aggregates the invalid configuration boundary matrix.
func TestDockerConfigurationValidationBoundaries(t *testing.T) {
	t.Parallel()

	badText := string([]byte{0xff})
	tooLong := strings.Repeat("x", maximumDockerTextBytes+1)
	if validDockerStringMap(map[string]string{"": "x"}) || validDockerEnvironment([]string{"missing"}) ||
		validDockerEnvironment([]string{"A=1", "A=2"}) {
		t.Fatal("invalid map or environment accepted")
	}
	for _, labels := range [][]string{{""}, {badText + "=x"}, {"a=" + badText}, {"a=1", "a=2"}} {
		if _, valid := dockerLabels(labels, nil); valid {
			t.Fatalf("dockerLabels(%q) accepted", labels)
		}
	}

	for _, cpu := range []string{".1", "+1", "-1", "1.0000000000", "x", "9223372037", "0", "1.x"} {
		if _, valid := dockerNanoCPUs(cpu); valid {
			t.Fatalf("dockerNanoCPUs(%q) accepted", cpu)
		}
	}
	if got, valid := dockerNanoCPUs("1"); !valid || got != dockerNanoCPUsPerCPU {
		t.Fatalf("dockerNanoCPUs(1) = %d, %v", got, valid)
	}
	badWeight := 9
	if _, valid := dockerBlkioWeight(&badWeight); valid {
		t.Fatal("dockerBlkioWeight(9) accepted")
	}
	if _, valid := dockerRestartPolicy("on-failure:0"); valid {
		t.Fatal("zero restart retries accepted")
	}

	badExposed := [][]domain.ExposedPort{
		{{TargetPort: 0, Protocol: dockerProtocolTCP}}, {{TargetPort: 1, Protocol: "bad"}},
	}
	for _, value := range badExposed {
		if _, _, valid := dockerPorts(value, nil); valid {
			t.Fatalf("dockerPorts(%v) accepted", value)
		}
	}
	badPorts := [][]domain.PortBinding{
		{{TargetPort: 1, PublishedPort: 0, Protocol: dockerProtocolTCP}},
		{{TargetPort: 0, PublishedPort: 1, Protocol: dockerProtocolTCP}},
		{{TargetPort: 1, PublishedPort: 1, Protocol: "sctp"}},
		{{TargetPort: 1, PublishedPort: 1, Protocol: dockerProtocolTCP, HostIP: "invalid"}},
		{{TargetPort: 1, PublishedPort: 1, Protocol: dockerProtocolTCP},
			{TargetPort: 1, PublishedPort: 2, Protocol: dockerProtocolTCP}},
	}
	for _, value := range badPorts {
		if _, _, valid := dockerPorts(nil, value); valid {
			t.Fatalf("dockerPorts(%v) accepted", value)
		}
	}
	for _, ip := range []string{"2001:db8::1", "192.0.2.1"} {
		bindings, _, valid := dockerPorts(nil, []domain.PortBinding{{
			HostIP: ip, PublishedPort: 2, TargetPort: 1, Protocol: dockerProtocolUDP,
		}})
		if !valid || len(bindings) != 1 {
			t.Fatalf("dockerPorts(%s) rejected", ip)
		}
	}
	if _, valid := dockerDNS([]string{"01.1.1.1"}); valid {
		t.Fatal("noncanonical DNS accepted")
	}

	if _, valid := dockerTmpfs([]domain.TmpfsMount{{Target: ""}}); valid {
		t.Fatal("empty tmpfs target accepted")
	}
	if _, valid := dockerTmpfs([]domain.TmpfsMount{{Target: "/x"}, {Target: "/x"}}); valid {
		t.Fatal("duplicate tmpfs target accepted")
	}
	if _, valid := dockerDevices([]domain.DeviceMapping{{Permissions: "z"}}); valid {
		t.Fatal("invalid device accepted")
	}
	if _, valid := dockerUlimits([]domain.Ulimit{{Name: "x", Soft: 2, Hard: 1}}); valid {
		t.Fatal("invalid ulimit accepted")
	}
	for _, value := range [][]domain.Mount{
		{{Target: ""}}, {{Kind: domain.MountBind, Target: "/x"}},
		{{Kind: domain.MountVolume, Source: "named", Target: "/x"}}, {{Kind: domain.MountKind(99), Target: "/x"}},
	} {
		if _, _, valid := dockerMounts(value); valid {
			t.Fatalf("dockerMounts(%v) accepted", value)
		}
	}
	volumes, _, valid := dockerMounts([]domain.Mount{{Kind: domain.MountBind, Source: "/a", Target: "/b"}})
	if !valid || volumes != nil {
		t.Fatal("bind-only mounts rejected")
	}
	workload := validApplicationWorkload(t)
	workload.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: "/data"}}
	request, valid := dockerCreateConfiguration(
		workload, testTransaction, application.WorkloadCreateOptions{CopyImageVolumes: false},
	)
	config := request.Config
	host := request.HostConfig
	if !valid || config == nil || host == nil ||
		len(config.Volumes) != 0 || len(host.Mounts) != 1 ||
		host.Mounts[0].VolumeOptions == nil || !host.Mounts[0].VolumeOptions.NoCopy {
		t.Fatalf("no-copy volume create = %#v", request)
	}

	retry := 0
	for _, value := range []*domain.Healthcheck{
		{Disabled: true, Test: []string{"CMD", "true"}}, {Interval: "bad"}, {Retries: &retry},
	} {
		if _, valid := dockerHealthcheck(value); valid {
			t.Fatalf("dockerHealthcheck(%v) accepted", value)
		}
	}
	disabledHealthcheck, valid := dockerHealthcheck(&domain.Healthcheck{Disabled: true})
	if !valid || disabledHealthcheck == nil || len(disabledHealthcheck.Test) != 1 ||
		disabledHealthcheck.Test[0] != dockerHealthcheckNone {
		t.Fatal("disabled healthcheck rejected")
	}
	if _, valid := dockerDuration("0s"); valid {
		t.Fatal("zero duration accepted")
	}
	badTimeout := int64(0)
	if _, valid := dockerStopTimeout(&badTimeout); valid {
		t.Fatal("zero stop timeout accepted")
	}
	if strconvIntSize() == 32 {
		tooBig := int64(math.MaxInt32) + 1
		if _, valid := dockerStopTimeout(&tooBig); valid {
			t.Fatal("oversized stop timeout accepted")
		}
	}

	spec := completeDockerWorkloadSpec()
	reserved := validApplicationWorkload(t)
	reserved.WorkloadSpec = spec
	reserved.Labels = []string{domain.LabelService + "=reserved"}
	if _, accepted := dockerCreateConfiguration(reserved, testTransaction, testCreateOptions()); accepted {
		t.Fatal("create accepted reserved label")
	}
	invalidHost := validApplicationWorkload(t)
	invalidHost.Hostname = tooLong
	if _, accepted := dockerCreateConfiguration(invalidHost, testTransaction, testCreateOptions()); accepted {
		t.Fatal("create accepted invalid spec")
	}
}

func strconvIntSize() int { return 32 << (^uint(0) >> 63) }

func TestDockerInspectValidationBoundaries(t *testing.T) {
	t.Parallel()

	falseValue := false
	zero := 0
	spec := completeDockerWorkloadSpec()
	config, host, valid := dockerConfiguration(spec, testContainerImage, map[string]string{})
	if !valid {
		t.Fatal("configuration rejected")
	}
	observed, valid := dockerWorkloadFromInspect(spec.ContainerName, config, host)
	if !valid {
		t.Fatal("inspection rejected")
	}
	expected := observed.Clone()
	observed.SharedMemoryBytes = dockerDefaultSharedMemoryBytes
	expected.SharedMemoryBytes = 0
	observed.Cgroup = "private"
	expected.Cgroup = ""
	observed.Restart = "no"
	expected.Restart = ""
	if !dockerConfigurationMatches(observed, expected) {
		t.Fatal("Docker defaults did not canonicalize")
	}
	canonical := canonicalDockerPointers(domain.WorkloadSpec{
		StdinOpen: &falseValue, ReadOnly: &falseValue, TTY: &falseValue,
		OOMScoreAdj: &zero, Init: &falseValue, OOMKillDisable: &falseValue,
	})
	if canonical.Init == nil || *canonical.Init {
		t.Fatal("canonicalDockerPointers() discarded explicit init=false")
	}
	explicitInit := expected.Clone()
	explicitInit.Init = &falseValue
	if dockerConfigurationMatches(observed, explicitInit) {
		t.Fatal("dockerConfigurationMatches() treated daemon-default init as explicit false")
	}
	canonicalDockerOrder(&domain.WorkloadSpec{
		Devices: []domain.DeviceMapping{{Target: "/b"}, {Target: "/a"}},
		Tmpfs:   []domain.TmpfsMount{{Target: "/b"}, {Target: "/a"}},
		Ulimits: []domain.Ulimit{{Name: "b"}, {Name: "a"}},
	})
}

func TestDockerInspectRoundTripsPortsAndTimeouts(t *testing.T) {
	t.Parallel()

	spec := completeDockerWorkloadSpec()
	second := spec.Clone()
	second.ExposedPorts = append(second.ExposedPorts,
		domain.ExposedPort{TargetPort: 1, Protocol: "sctp"},
		domain.ExposedPort{TargetPort: 1, Protocol: dockerProtocolUDP})
	second.Ports = append(second.Ports,
		domain.PortBinding{
			HostIP: "2001:db8::1", PublishedPort: 2, TargetPort: 1, Protocol: dockerProtocolUDP,
		})
	labels, valid := dockerLabels(second.Labels, nil)
	if !valid {
		t.Fatal("labels rejected")
	}
	cfg2, host2, valid := dockerConfiguration(second, testContainerImage, labels)
	if !valid {
		t.Fatal("IPv6/SCTP configuration rejected")
	}
	got, valid := dockerWorkloadFromInspect(second.ContainerName, cfg2, host2)
	if !valid || !dockerConfigurationMatches(got, second) {
		t.Fatal("IPv6/SCTP inspection did not round trip")
	}
	zeroTimeout := 0
	cfg2.StopTimeout = &zeroTimeout
	if _, valid := dockerWorkloadFromInspect(second.ContainerName, cfg2, host2); valid {
		t.Fatal("zero observed stop timeout accepted")
	}
}

func TestDockerObservedConfigurationBoundaries(t *testing.T) {
	t.Parallel()

	badLabels := map[string]string{"": "x"}
	if labels, valid := dockerObservedLabels(nil); !valid || labels != nil {
		t.Fatal("nil observed labels rejected")
	}
	if _, valid := dockerObservedLabels(badLabels); valid {
		t.Fatal("bad observed label accepted")
	}
	badPort, _ := network.PortFrom(1, network.TCP)
	if _, _, valid := dockerObservedPorts(network.PortSet{network.Port{}: {}}, nil); valid {
		t.Fatal("invalid exposed port accepted")
	}
	for _, bindings := range []network.PortMap{
		{badPort: nil}, {badPort: {{HostPort: "0"}}},
	} {
		if _, _, valid := dockerObservedPorts(nil, bindings); valid {
			t.Fatal("bad observed port accepted")
		}
	}
}

func TestDockerObservedHostResourceBoundaries(t *testing.T) {
	t.Parallel()

	if _, valid := dockerObservedDNS([]netip.Addr{{}}); valid {
		t.Fatal("bad observed DNS accepted")
	}
	if _, valid := dockerObservedDevices([]containertypes.DeviceMapping{{CgroupPermissions: "z"}}); valid {
		t.Fatal("bad observed device accepted")
	}
	if _, valid := dockerObservedTmpfs(map[string]string{"": "x"}); valid {
		t.Fatal("bad observed tmpfs accepted")
	}
	if got, valid := dockerObservedTmpfs(map[string]string{dockerTestTmpfsTarget: ""}); !valid ||
		len(got) != 1 || got[0].Options != nil {
		t.Fatal("empty observed tmpfs options rejected")
	}
	if _, valid := dockerObservedTmpfs(map[string]string{"/b": "", "/a": ""}); !valid {
		t.Fatal("multiple observed tmpfs mounts rejected")
	}
}

func TestDockerObservedLimitAndMountBoundaries(t *testing.T) {
	t.Parallel()

	if _, valid := dockerObservedUlimits([]*containertypes.Ulimit{nil}); valid {
		t.Fatal("nil observed ulimit accepted")
	}
	if _, valid := dockerObservedUlimits([]*containertypes.Ulimit{{Name: "b"}, {Name: "a"}}); !valid {
		t.Fatal("multiple observed ulimits rejected")
	}
	if _, valid := dockerObservedMounts(map[string]struct{}{"": {}}, nil); valid {
		t.Fatal("bad observed volume accepted")
	}
	if _, valid := dockerObservedMounts(nil, []mount.Mount{{Type: mount.TypeVolume}}); valid {
		t.Fatal("unsupported observed mount accepted")
	}
}

func TestDockerObservedPolicyBoundaries(t *testing.T) {
	t.Parallel()

	for _, health := range []*containertypes.HealthConfig{
		{Test: []string{dockerHealthcheckNone}, Interval: time.Second}, {Interval: -1},
	} {
		if _, valid := dockerObservedHealthcheck(health); valid {
			t.Fatalf("bad observed healthcheck accepted: %v", health)
		}
	}
	if got, valid := dockerObservedHealthcheck(&containertypes.HealthConfig{}); !valid || got == nil {
		t.Fatal("empty healthcheck rejected")
	}
	healthcheck, valid := dockerObservedHealthcheck(&containertypes.HealthConfig{
		Test: []string{dockerHealthcheckNone},
	})
	if !valid || healthcheck == nil || !healthcheck.Disabled {
		t.Fatal("disabled observed healthcheck rejected")
	}
	if dockerDurationString(0) != "" || dockerDurationString(time.Second) != "1s" {
		t.Fatal("duration formatting mismatch")
	}
}

func TestDockerObservedRuntimePolicyBoundaries(t *testing.T) {
	t.Parallel()

	if _, valid := dockerObservedRestart(containertypes.RestartPolicy{
		Name: containertypes.RestartPolicyOnFailure, MaximumRetryCount: -1,
	}); valid {
		t.Fatal("bad observed restart accepted")
	}
	if value, valid := dockerObservedSecurity([]string{"no-new-privileges"}); !valid || !value {
		t.Fatal("supported security option rejected")
	}
	if _, valid := dockerObservedSecurity([]string{"seccomp=unconfined"}); valid {
		t.Fatal("unsupported security option accepted")
	}
	if dockerCPUString(dockerNanoCPUsPerCPU) != "1" || dockerCPUString(dockerNanoCPUsPerCPU+1) != "1.000000001" {
		t.Fatal("CPU formatting mismatch")
	}
	if trueDockerPointer(false) != nil {
		t.Fatal("false pointer was not canonicalized")
	}
}

func TestCreateWorkloadFailsClosedWhenDTOCannotBeEncoded(t *testing.T) {
	t.Parallel()

	workload := validApplicationWorkload(t)
	workload.Hostname = strings.Repeat("x", maximumDockerTextBytes+1)
	client := connectedTestClient(t, http.HandlerFunc(func(http.ResponseWriter, *http.Request) {
		t.Fatal("invalid DTO reached Docker")
	}))
	_, err := client.CreateWorkload(context.Background(), workload, testTransaction, testCreateOptions())
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("CreateWorkload() error = %v", err)
	}
}
