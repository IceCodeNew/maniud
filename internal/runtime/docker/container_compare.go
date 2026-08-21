package docker

import (
	"reflect"
	"slices"
	"strconv"
	"strings"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func dockerConfigurationMatches(observed domain.WorkloadSpec, expected domain.WorkloadSpec) bool {
	observed = observed.Clone()
	expected = expected.Clone()
	observed.ServiceName = expected.ServiceName
	observed.Platform = expected.Platform
	if expected.SharedMemoryBytes == 0 && observed.SharedMemoryBytes == dockerDefaultSharedMemoryBytes {
		observed.SharedMemoryBytes = 0
	}
	if expected.Cgroup == "" && observed.Cgroup == string(containertypes.CgroupnsModePrivate) {
		observed.Cgroup = ""
	}
	if expected.Restart == "" && observed.Restart == string(containertypes.RestartPolicyDisabled) {
		observed.Restart = ""
	}
	observed = canonicalDockerSpec(observed)
	expected = canonicalDockerSpec(expected)

	return reflect.DeepEqual(observed, expected)
}

func canonicalDockerSpec(spec domain.WorkloadSpec) domain.WorkloadSpec {
	spec = canonicalDockerPointers(spec)
	canonicalDockerOrder(&spec)
	canonicalDockerCollections(&spec)

	return spec
}

//nolint:cyclop // Docker inspect collapses each explicit false or zero pointer independently.
func canonicalDockerPointers(spec domain.WorkloadSpec) domain.WorkloadSpec {
	if spec.StdinOpen != nil && !*spec.StdinOpen {
		spec.StdinOpen = nil
	}
	if spec.ReadOnly != nil && !*spec.ReadOnly {
		spec.ReadOnly = nil
	}
	if spec.TTY != nil && !*spec.TTY {
		spec.TTY = nil
	}
	if spec.OOMScoreAdj != nil && *spec.OOMScoreAdj == 0 {
		spec.OOMScoreAdj = nil
	}
	if spec.OOMKillDisable != nil && !*spec.OOMKillDisable {
		spec.OOMKillDisable = nil
	}

	return spec
}

func canonicalDockerOrder(spec *domain.WorkloadSpec) {
	slices.Sort(spec.CapAdd)
	slices.Sort(spec.CapDrop)
	slices.Sort(spec.DNS)
	slices.Sort(spec.DNSOptions)
	slices.Sort(spec.DNSSearch)
	slices.Sort(spec.ExtraHosts)
	slices.Sort(spec.GroupAdd)
	slices.Sort(spec.Environment)
	slices.Sort(spec.Labels)
	slices.SortFunc(spec.ExposedPorts, func(left, right domain.ExposedPort) int {
		if left.TargetPort != right.TargetPort {
			return int(left.TargetPort) - int(right.TargetPort)
		}

		return strings.Compare(left.Protocol, right.Protocol)
	})
	slices.SortFunc(spec.Devices, func(left, right domain.DeviceMapping) int {
		return strings.Compare(left.Target+"\x00"+left.Source, right.Target+"\x00"+right.Source)
	})
	slices.SortFunc(spec.Tmpfs, func(left, right domain.TmpfsMount) int {
		return strings.Compare(left.Target, right.Target)
	})
	slices.SortFunc(spec.Ulimits, func(left, right domain.Ulimit) int {
		return strings.Compare(left.Name, right.Name)
	})
	slices.SortFunc(spec.Ports, func(left, right domain.PortBinding) int {
		return strings.Compare(dockerPortSortKey(left), dockerPortSortKey(right))
	})
	slices.SortFunc(spec.Mounts, func(left, right domain.Mount) int {
		return strings.Compare(left.Target+"\x00"+left.Source, right.Target+"\x00"+right.Source)
	})
}

//nolint:cyclop // Docker omits every empty collection from inspect responses.
func canonicalDockerCollections(spec *domain.WorkloadSpec) {
	if len(spec.CapAdd) == 0 {
		spec.CapAdd = nil
	}
	if len(spec.CapDrop) == 0 {
		spec.CapDrop = nil
	}
	if len(spec.DNS) == 0 {
		spec.DNS = nil
	}
	if len(spec.DNSOptions) == 0 {
		spec.DNSOptions = nil
	}
	if len(spec.DNSSearch) == 0 {
		spec.DNSSearch = nil
	}
	if len(spec.Devices) == 0 {
		spec.Devices = nil
	}
	if len(spec.ExtraHosts) == 0 {
		spec.ExtraHosts = nil
	}
	if len(spec.GroupAdd) == 0 {
		spec.GroupAdd = nil
	}
	if len(spec.Sysctls) == 0 {
		spec.Sysctls = nil
	}
	if len(spec.Tmpfs) == 0 {
		spec.Tmpfs = nil
	}
	if len(spec.Ulimits) == 0 {
		spec.Ulimits = nil
	}
	if len(spec.Environment) == 0 {
		spec.Environment = nil
	}
	if len(spec.Labels) == 0 {
		spec.Labels = nil
	}
	if len(spec.ExposedPorts) == 0 {
		spec.ExposedPorts = nil
	}
	if len(spec.Ports) == 0 {
		spec.Ports = nil
	}
	if len(spec.Mounts) == 0 {
		spec.Mounts = nil
	}
}

func dockerPortSortKey(value domain.PortBinding) string {
	return value.Protocol + "\x00" + strconv.FormatUint(uint64(value.TargetPort), 10) + "\x00" +
		value.HostIP + "\x00" + strconv.FormatUint(uint64(value.PublishedPort), 10)
}
