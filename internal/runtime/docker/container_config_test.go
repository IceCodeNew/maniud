package docker

import (
	"slices"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	dockerTestBindSource   = "/host/data"
	dockerTestDataTarget   = "/data"
	dockerTestHealthCMD    = "CMD"
	dockerTestStateTarget  = "/state"
	dockerTestTmpfsTarget  = "/tmp"
	dockerTestVolumeName   = "anonymous-volume"
	dockerTestVolumeSource = "/var/lib/docker/volumes/anonymous/_data"
)

func TestDockerConfigurationRoundTripsCompleteWorkloadSpec(t *testing.T) {
	t.Parallel()

	spec := completeDockerWorkloadSpec()
	labels, valid := dockerLabels(spec.Labels, map[string]string{domain.LabelService: spec.ServiceName})
	if !valid {
		t.Fatal("dockerLabels() = false")
	}
	config, host, valid := dockerConfiguration(spec, testContainerImage, labels)
	if !valid {
		t.Fatal("dockerConfiguration() = false")
	}
	observed, valid := dockerWorkloadFromInspect(spec.ContainerName, config, host)
	if !valid {
		t.Fatal("dockerWorkloadFromInspect() = false")
	}
	if !dockerConfigurationMatches(observed, spec) {
		t.Fatalf("round trip = %#v, want %#v", observed, spec)
	}

	mutations := []struct {
		name   string
		mutate func(*domain.WorkloadSpec)
	}{
		{name: "process", mutate: func(value *domain.WorkloadSpec) { value.Command = []string{testOtherValue} }},
		{name: "network", mutate: func(value *domain.WorkloadSpec) { value.NetworkMode = "host" }},
		{name: "resources", mutate: func(value *domain.WorkloadSpec) { value.MemoryBytes++ }},
		{name: "identity", mutate: func(value *domain.WorkloadSpec) { value.User = testOtherValue }},
		{name: "capabilities", mutate: func(value *domain.WorkloadSpec) { value.CapAdd = nil }},
		{name: "dns", mutate: func(value *domain.WorkloadSpec) { value.DNS = nil }},
		{name: "device", mutate: func(value *domain.WorkloadSpec) { value.Devices = nil }},
		{name: "sysctl", mutate: func(value *domain.WorkloadSpec) { value.Sysctls = nil }},
		{name: "tmpfs", mutate: func(value *domain.WorkloadSpec) { value.Tmpfs = nil }},
		{name: "ulimit", mutate: func(value *domain.WorkloadSpec) { value.Ulimits = nil }},
		{name: "environment", mutate: func(value *domain.WorkloadSpec) { value.Environment = nil }},
		{name: "label", mutate: func(value *domain.WorkloadSpec) { value.Labels = nil }},
		{name: "port", mutate: func(value *domain.WorkloadSpec) { value.Ports = nil }},
		{name: "security", mutate: func(value *domain.WorkloadSpec) { value.NoNewPrivileges = false }},
		{name: "mount", mutate: func(value *domain.WorkloadSpec) { value.Mounts = nil }},
		{name: "healthcheck", mutate: func(value *domain.WorkloadSpec) { value.Healthcheck = nil }},
	}
	for _, test := range mutations {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			changed := spec.Clone()
			test.mutate(&changed)
			if dockerConfigurationMatches(observed, changed) {
				t.Fatalf("dockerConfigurationMatches(%s) = true", test.name)
			}
		})
	}
}

func TestDockerWorkloadInspectRejectsUnsupportedConfiguration(t *testing.T) {
	t.Parallel()

	spec := completeDockerWorkloadSpec()
	config, host, valid := dockerConfiguration(spec, testContainerImage, map[string]string{})
	if !valid {
		t.Fatal("dockerConfiguration() = false")
	}

	config.Domainname = "unsupported.example"
	if _, valid := dockerWorkloadFromInspect(spec.ContainerName, config, host); valid {
		t.Fatal("dockerWorkloadFromInspect(domain name) = true")
	}
	config.Domainname = ""
	host.Privileged = true
	if _, valid := dockerWorkloadFromInspect(spec.ContainerName, config, host); valid {
		t.Fatal("dockerWorkloadFromInspect(privileged) = true")
	}
	host.Privileged = false
	host.CPUShares = 1024
	if _, valid := dockerWorkloadFromInspect(spec.ContainerName, config, host); valid {
		t.Fatal("dockerWorkloadFromInspect(CPU shares) = true")
	}
}

func TestDockerRuntimeMountsProvePersistentSources(t *testing.T) {
	t.Parallel()

	spec := completeDockerWorkloadSpec()
	values := dockerRuntimeMountPoints()
	observed, valid := dockerRuntimeMounts(values, spec)
	if !valid || len(observed) != 2 || observed[0] != (domain.RuntimeMount{
		Kind: domain.MountBind, Source: dockerTestBindSource, Target: dockerTestDataTarget, ReadOnly: true,
	}) || observed[1] != (domain.RuntimeMount{
		Kind: domain.MountVolume, Name: dockerTestVolumeName,
		Source: dockerTestVolumeSource, Target: dockerTestStateTarget,
	}) {
		t.Fatalf("dockerRuntimeMounts() = %#v, %t", observed, valid)
	}

	values[1].Name = ""
	if _, valid := dockerRuntimeMounts(values, spec); valid {
		t.Fatal("dockerRuntimeMounts(volume without name) = true")
	}
	values[1].Name = dockerTestVolumeName
	values[1].Source = "/var/lib/docker/volumes/anonymous/../other/_data"
	if _, valid := dockerRuntimeMounts(values, spec); valid {
		t.Fatal("dockerRuntimeMounts(unclean volume source) = true")
	}
	values[1].Source = dockerTestVolumeSource
}

func TestDockerRuntimeMountsRejectAmbiguousIdentity(t *testing.T) {
	t.Parallel()

	spec := completeDockerWorkloadSpec()
	values := dockerRuntimeMountPoints()

	invalidSpecs := []domain.WorkloadSpec{spec, spec, spec, spec, spec}
	for index := range invalidSpecs {
		invalidSpecs[index].Mounts = slices.Clone(spec.Mounts)
	}
	invalidValues := [][]containertypes.MountPoint{values, values, values, values, values[:2]}
	invalidSpecs[0].Mounts = append(slices.Clone(spec.Mounts), spec.Mounts[0])
	invalidValues[1] = append(slices.Clone(values), containertypes.MountPoint{
		Type: mount.TypeBind, Source: "/extra", Destination: "/extra", RW: true,
	})
	invalidValues[2] = slices.Clone(values)
	invalidValues[2][2].Name = "unexpected"
	invalidSpecs[3].Mounts[0].Kind = domain.MountKind(255)
	for index, invalidSpec := range invalidSpecs {
		if _, valid := dockerRuntimeMounts(invalidValues[index], invalidSpec); valid {
			t.Fatalf("dockerRuntimeMounts(invalid %d) = true", index)
		}
	}
	missingPropagation := slices.Clone(values)
	missingPropagation[2].Propagation = ""
	if _, valid := dockerRuntimeMounts(missingPropagation, spec); valid {
		t.Fatal("dockerRuntimeMounts(bind without propagation) = true")
	}
}

func dockerRuntimeMountPoints() []containertypes.MountPoint {
	return []containertypes.MountPoint{
		{Type: mount.TypeTmpfs, Destination: dockerTestTmpfsTarget, RW: true},
		{
			Type: mount.TypeVolume, Name: dockerTestVolumeName, Source: dockerTestVolumeSource,
			Destination: dockerTestStateTarget, Driver: dockerVolumeDriverLocal, RW: true,
		},
		{
			Type: mount.TypeBind, Source: dockerTestBindSource, Destination: dockerTestDataTarget, RW: false,
			Propagation: mount.PropagationRPrivate,
		},
	}
}

func completeDockerWorkloadSpec() domain.WorkloadSpec {
	weight := 500
	oomScore := 100
	pids := int64(128)
	stopTimeout := int64(15)
	truth := true
	retries := 3

	return domain.WorkloadSpec{
		ServiceName: testContainerService, ContainerName: testContainerService,
		Platform:   domain.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint: []string{"/init"}, Command: []string{"serve"}, NetworkMode: dockerNetworkMode,
		BlkioWeight: &weight, CgroupParent: "parent", Cgroup: "private", CPUs: "1.5", Hostname: "api",
		MemoryBytes: 512 << 20, OOMScoreAdj: &oomScore, PidsLimit: &pids, Restart: "on-failure:3",
		SharedMemoryBytes: 32 << 20, StopSignal: "SIGTERM", StopTimeout: &stopTimeout,
		User: "1000:1000", WorkingDirectory: "/work", CapAdd: []string{"NET_ADMIN"},
		CapDrop: []string{"MKNOD"}, DNS: []string{"1.1.1.1"}, DNSOptions: []string{"rotate"},
		DNSSearch: []string{"example.test"}, Devices: []domain.DeviceMapping{{
			Source: "/dev/fuse", Target: "/dev/fuse", Permissions: "rw",
		}}, ExtraHosts: []string{"host=192.0.2.1"}, GroupAdd: []string{"100"},
		Sysctls:     map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
		Tmpfs:       []domain.TmpfsMount{{Target: dockerTestTmpfsTarget, Options: []string{"rw", "size=1048576"}}},
		Ulimits:     []domain.Ulimit{{Name: "nofile", Soft: 1024, Hard: 2048}},
		Environment: []string{"A=one", "B=two"}, Labels: []string{"team=platform"},
		ExposedPorts: []domain.ExposedPort{
			{TargetPort: 80, Protocol: dockerProtocolTCP},
			{TargetPort: 53, Protocol: dockerProtocolUDP},
		},
		Ports:           []domain.PortBinding{{PublishedPort: 8080, TargetPort: 80, Protocol: dockerProtocolTCP}},
		NoNewPrivileges: true, Mounts: []domain.Mount{
			{Kind: domain.MountBind, Source: dockerTestBindSource, Target: dockerTestDataTarget, ReadOnly: true},
			{Kind: domain.MountVolume, Target: dockerTestStateTarget},
		},
		Init: &truth, StdinOpen: &truth, OOMKillDisable: &truth, ReadOnly: &truth, TTY: &truth,
		Healthcheck: &domain.Healthcheck{
			Test: []string{dockerTestHealthCMD, dockerQueryTrue}, Interval: "30s", Timeout: "5s", Retries: &retries,
			StartPeriod: "10s", StartInterval: "1s",
		},
	}
}
