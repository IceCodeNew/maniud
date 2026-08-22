package docker

import (
	"math"
	"net/netip"
	"strings"
	"testing"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	testContainerImage    = "docker.io/example/api@sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	dockerTestHealthCMD   = "CMD"
	dockerQueryTrue       = "true"
	dockerTestDataTarget  = "/data"
	dockerTestTmpfsTarget = "/tmp"
)

func testCreateOptions() CreateOptions {
	return CreateOptions{ImageReference: testContainerImage, CopyImageVolumes: true}
}

//nolint:gocognit,gocyclo,cyclop,funlen,maintidx // One matrix covers independent invalid configuration boundaries.
func TestDockerConfigurationValidationBoundaries(t *testing.T) {
	t.Parallel()

	badText := string([]byte{0xff})
	tooLong := strings.Repeat("x", maximumDockerTextBytes+1)
	if validDockerStringMap(map[string]string{"": "x"}) || validDockerEnvironment([]string{"missing"}) ||
		validDockerEnvironment([]string{"A=1", "A=2"}) {
		t.Fatal("invalid map or environment accepted")
	}
	for _, labels := range [][]string{{""}, {badText + "=x"}, {"a=" + badText}, {"a=1", "a=2"}} {
		if _, valid := dockerLabels(labels); valid {
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
	if policy, valid := dockerRestartPolicy("always"); !valid || policy.Name != containertypes.RestartPolicyAlways {
		t.Fatalf("dockerRestartPolicy(always) = %#v, %v", policy, valid)
	}
	badWeight := 9
	if _, valid := dockerBlkioWeight(&badWeight); valid {
		t.Fatal("dockerBlkioWeight(9) accepted")
	}
	if _, valid := dockerRestartPolicy("on-failure:0"); valid {
		t.Fatal("zero restart retries accepted")
	}

	badExposed := [][]containerconfig.ExposedPort{
		{{TargetPort: 0, Protocol: dockerProtocolTCP}}, {{TargetPort: 1, Protocol: "bad"}},
	}
	for _, value := range badExposed {
		if _, _, valid := dockerPorts(value, nil); valid {
			t.Fatalf("dockerPorts(%v) accepted", value)
		}
	}
	badPorts := [][]containerconfig.PortBinding{
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
		bindings, _, valid := dockerPorts(nil, []containerconfig.PortBinding{{
			HostIP: ip, PublishedPort: 2, TargetPort: 1, Protocol: dockerProtocolUDP,
		}})
		if !valid || len(bindings) != 1 {
			t.Fatalf("dockerPorts(%s) rejected", ip)
		}
	}
	if _, valid := dockerDNS([]string{"01.1.1.1"}); valid {
		t.Fatal("noncanonical DNS accepted")
	}

	if _, valid := dockerTmpfs([]containerconfig.TmpfsMount{{Target: ""}}); valid {
		t.Fatal("empty tmpfs target accepted")
	}
	if _, valid := dockerTmpfs([]containerconfig.TmpfsMount{{Target: "/x"}, {Target: "/x"}}); valid {
		t.Fatal("duplicate tmpfs target accepted")
	}
	if _, valid := dockerDevices([]containerconfig.DeviceMapping{{Permissions: "z"}}); valid {
		t.Fatal("invalid device accepted")
	}
	if _, valid := dockerUlimits([]containerconfig.Ulimit{{Name: "x", Soft: 2, Hard: 1}}); valid {
		t.Fatal("invalid ulimit accepted")
	}
	for _, value := range [][]containerconfig.Mount{
		{{Target: ""}}, {{Kind: containerconfig.MountBind, Target: "/x"}},
		{{Kind: containerconfig.MountVolume, Source: "named", Target: "/x"}},
		{{Kind: containerconfig.MountKind(99), Target: "/x"}},
		{{Kind: containerconfig.MountBind, Source: "/a", Target: "/x"},
			{Kind: containerconfig.MountVolume, Target: "/x"}},
	} {
		if _, _, valid := dockerMounts(value); valid {
			t.Fatalf("dockerMounts(%v) accepted", value)
		}
	}
	volumes, _, valid := dockerMounts([]containerconfig.Mount{{
		Kind: containerconfig.MountBind, Source: "/a", Target: "/b",
	}})
	if !valid || volumes != nil {
		t.Fatal("bind-only mounts rejected")
	}
	if mounts := appendNoCopyVolumes(nil, nil); mounts != nil {
		t.Fatalf("appendNoCopyVolumes(nil) = %#v", mounts)
	}
	workload := completeDockerWorkloadSpec()
	workload.Mounts = []containerconfig.Mount{{
		Kind: containerconfig.MountVolume, Target: dockerTestDataTarget,
	}}
	request, err := Encode(workload, CreateOptions{ImageReference: testContainerImage, CopyImageVolumes: false})
	config := request.Config
	host := request.HostConfig
	if err != nil || config == nil || host == nil ||
		len(config.Volumes) != 0 || len(host.Mounts) != 1 ||
		host.Mounts[0].VolumeOptions == nil || !host.Mounts[0].VolumeOptions.NoCopy {
		t.Fatalf("no-copy volume create = %#v", request)
	}

	retry := 0
	for _, value := range []*containerconfig.Healthcheck{
		{Disabled: true, Test: []string{dockerTestHealthCMD, dockerQueryTrue}}, {Interval: "bad"}, {Retries: &retry},
	} {
		if _, valid := dockerHealthcheck(value); valid {
			t.Fatalf("dockerHealthcheck(%v) accepted", value)
		}
	}
	disabledHealthcheck, valid := dockerHealthcheck(&containerconfig.Healthcheck{Disabled: true})
	if !valid || disabledHealthcheck == nil || len(disabledHealthcheck.Test) != 1 ||
		disabledHealthcheck.Test[0] != dockerHealthcheckNone {
		t.Fatal("disabled healthcheck rejected")
	}
	healthcheck, valid := dockerHealthcheck(&containerconfig.Healthcheck{
		Test: []string{dockerTestHealthCMD, dockerQueryTrue},
	})
	if !valid || healthcheck == nil || healthcheck.Retries != 0 {
		t.Fatalf("healthcheck without retries = %#v, %v", healthcheck, valid)
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
	invalidHost := spec.Clone()
	invalidHost.Hostname = tooLong
	if _, err := Encode(invalidHost, testCreateOptions()); err == nil {
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
	if !Equivalent(observed, expected) {
		t.Fatal("Docker defaults did not canonicalize")
	}
	canonical := canonicalDockerPointers(containerconfig.Spec{
		StdinOpen: &falseValue, ReadOnly: &falseValue, TTY: &falseValue,
		OOMScoreAdj: &zero, Init: &falseValue, OOMKillDisable: &falseValue,
	})
	if canonical.Init == nil || *canonical.Init {
		t.Fatal("canonicalDockerPointers() discarded explicit init=false")
	}
	explicitInit := expected.Clone()
	explicitInit.Init = &falseValue
	if Equivalent(observed, explicitInit) {
		t.Fatal("dockerConfigurationMatches() treated daemon-default init as explicit false")
	}
	firstDNS := expected.Clone()
	firstDNS.DNS = []string{"192.0.2.1", "192.0.2.2"}
	secondDNS := firstDNS.Clone()
	secondDNS.DNS[0], secondDNS.DNS[1] = secondDNS.DNS[1], secondDNS.DNS[0]
	if Equivalent(firstDNS, secondDNS) {
		t.Fatal("Docker comparison ignored DNS resolver priority")
	}
	config.Env = []string{"missing-value"}
	if _, valid := dockerWorkloadFromInspect(spec.ContainerName, config, host); valid {
		t.Fatal("bare observed environment entry accepted")
	}
	config.Env = nil
	config.ArgsEscaped = true
	if _, valid := dockerWorkloadFromInspect(spec.ContainerName, config, host); !valid {
		t.Fatal("Linux-irrelevant ArgsEscaped metadata was rejected")
	}
}

func TestDockerInspectRoundTripsPortsAndTimeouts(t *testing.T) {
	t.Parallel()

	spec := completeDockerWorkloadSpec()
	second := spec.Clone()
	second.ExposedPorts = append(second.ExposedPorts,
		containerconfig.ExposedPort{TargetPort: 1, Protocol: "sctp"},
		containerconfig.ExposedPort{TargetPort: 1, Protocol: dockerProtocolUDP})
	second.Ports = append(second.Ports,
		containerconfig.PortBinding{
			HostIP: "2001:db8::1", PublishedPort: 2, TargetPort: 1, Protocol: dockerProtocolUDP,
		})
	labels, valid := dockerLabels(second.Labels)
	if !valid {
		t.Fatal("labels rejected")
	}
	cfg2, host2, valid := dockerConfiguration(second, testContainerImage, labels)
	if !valid {
		t.Fatal("IPv6/SCTP configuration rejected")
	}
	got, valid := dockerWorkloadFromInspect(second.ContainerName, cfg2, host2)
	if !valid || !Equivalent(got, second) {
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

//nolint:cyclop // The test keeps observed limit and mount rejection cases in one matrix.
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
	observed, valid := dockerObservedMounts(nil, []mount.Mount{{
		Type: mount.TypeVolume, Target: dockerTestDataTarget, VolumeOptions: &mount.VolumeOptions{NoCopy: true},
	}})
	if !valid || len(observed) != 1 || observed[0] != (containerconfig.Mount{
		Kind: containerconfig.MountVolume, Target: dockerTestDataTarget,
	}) {
		t.Fatalf("no-copy observed volume = %#v, %t", observed, valid)
	}
	if _, valid := dockerObservedMounts(nil, []mount.Mount{{Type: mount.TypeVolume}}); valid {
		t.Fatal("unsupported observed mount accepted")
	}
	if _, valid := dockerObservedMounts(nil, []mount.Mount{{
		Type: mount.TypeVolume, Target: dockerTestDataTarget, VolumeOptions: &mount.VolumeOptions{},
	}}); valid {
		t.Fatal("copying observed volume accepted")
	}
	if _, valid := dockerObservedMounts(nil, []mount.Mount{{
		Type: mount.TypeBind, Source: "/host", Target: dockerTestDataTarget, TmpfsOptions: &mount.TmpfsOptions{},
	}}); valid {
		t.Fatal("bind mount with foreign options accepted")
	}
	if _, valid := dockerObservedMounts(map[string]struct{}{dockerTestDataTarget: {}}, []mount.Mount{{
		Type: mount.TypeVolume, Target: dockerTestDataTarget, VolumeOptions: &mount.VolumeOptions{NoCopy: true},
	}}); valid {
		t.Fatal("duplicate observed mount target accepted")
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

func TestPublicValidationErrorsRemainValueFree(t *testing.T) {
	t.Parallel()

	workload := completeDockerWorkloadSpec()
	if err := Validate(workload, CreateOptions{}); err == nil || strings.Contains(err.Error(), testContainerImage) {
		t.Fatalf("Validate() error = %v", err)
	}
}

func completeDockerWorkloadSpec() containerconfig.Spec {
	weight := 500
	oomScore := 100
	pids := int64(128)
	stopTimeout := int64(15)
	truth := true
	retries := 3

	return containerconfig.Spec{
		ServiceName: "example-api", ContainerName: "example-api",
		Platform:   containerconfig.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint: []string{"/init"}, Command: []string{"serve"}, NetworkMode: dockerNetworkMode,
		BlkioWeight: &weight, CgroupParent: "parent", Cgroup: "private", CPUs: "1.5", Hostname: testWorkloadName,
		MemoryBytes: 512 << 20, OOMScoreAdj: &oomScore, PidsLimit: &pids, Restart: "on-failure:3",
		SharedMemoryBytes: 32 << 20, StopSignal: "SIGTERM", StopTimeout: &stopTimeout,
		User: "1000:1000", WorkingDirectory: "/work", CapAdd: []string{"NET_ADMIN"},
		CapDrop: []string{"MKNOD"}, DNS: []string{"1.1.1.1"}, DNSOptions: []string{"rotate"},
		DNSSearch: []string{"example.test"}, Devices: []containerconfig.DeviceMapping{{
			Source: "/dev/fuse", Target: "/dev/fuse", Permissions: "rw",
		}}, ExtraHosts: []string{"host=192.0.2.1"}, GroupAdd: []string{"100"},
		Sysctls:     map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
		Tmpfs:       []containerconfig.TmpfsMount{{Target: dockerTestTmpfsTarget, Options: []string{"rw", "size=1048576"}}},
		Ulimits:     []containerconfig.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}},
		Environment: []string{"A=one", "B=two"}, Labels: []string{"team=platform"},
		ExposedPorts: []containerconfig.ExposedPort{
			{TargetPort: 80, Protocol: dockerProtocolTCP},
			{TargetPort: 53, Protocol: dockerProtocolUDP},
		},
		Ports:           []containerconfig.PortBinding{{PublishedPort: 8080, TargetPort: 80, Protocol: dockerProtocolTCP}},
		NoNewPrivileges: true, Mounts: []containerconfig.Mount{
			{Kind: containerconfig.MountBind, Source: "/host/data", Target: dockerTestDataTarget, ReadOnly: true},
			{Kind: containerconfig.MountVolume, Target: "/state"},
		},
		Init: &truth, StdinOpen: &truth, OOMKillDisable: &truth, ReadOnly: &truth, TTY: &truth,
		Healthcheck: &containerconfig.Healthcheck{
			Test: []string{dockerTestHealthCMD, dockerQueryTrue}, Interval: "30s", Timeout: "5s", Retries: &retries,
			StartPeriod: "10s", StartInterval: "1s",
		},
	}
}
