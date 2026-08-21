package containerd

import (
	"maps"
	"math"
	"reflect"
	"slices"
	"strings"

	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/containerconfig"
)

// Decode validates a complete containerd projection and reconstructs its
// portable configuration. OCI fields that differ from the encoded control
// contract fail closed.
func Decode(configuration Configuration) (containerconfig.Spec, error) {
	spec, valid := decodedSpec(configuration)
	if !valid {
		return containerconfig.Spec{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}
	canonical, err := canonicalSpec(spec)
	if err != nil {
		return containerconfig.Spec{}, err
	}
	expected := Configuration{OCI: encodeOCI(canonical), Control: encodeControl(canonical)}
	if !reflect.DeepEqual(expected, configuration) {
		return containerconfig.Spec{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}

	return canonical, nil
}

func decodedSpec(configuration Configuration) (containerconfig.Spec, bool) {
	oci := configuration.OCI
	control := configuration.Control
	if oci.Process == nil || oci.Root == nil || oci.Linux == nil || control.EntrypointLength < 0 ||
		control.EntrypointLength > len(oci.Process.Args) {
		return containerconfig.Spec{}, false
	}
	resources := oci.Linux.Resources
	if resources == nil {
		return containerconfig.Spec{}, false
	}
	entrypoint := slices.Clone(oci.Process.Args[:control.EntrypointLength])
	command := slices.Clone(oci.Process.Args[control.EntrypointLength:])
	if !control.EntrypointDefined {
		entrypoint = nil
	}
	if !control.CommandDefined {
		command = nil
	}

	return containerconfig.Spec{
		ServiceName: control.ServiceName, ContainerName: control.ContainerName, Platform: control.Platform,
		Entrypoint: entrypoint, Command: command, NetworkMode: control.NetworkMode,
		BlkioWeight: decodedWeight(resources.BlockIO), CgroupParent: control.CgroupParent,
		Cgroup: decodedCgroup(oci.Linux.Namespaces), CPUs: control.CPUs, Hostname: oci.Hostname,
		MemoryBytes: decodedMemory(resources.Memory), OOMScoreAdj: cloneValue(oci.Process.OOMScoreAdj),
		PidsLimit: decodedPids(resources.Pids), Restart: control.Restart,
		SharedMemoryBytes: control.SharedMemoryBytes, StopSignal: control.StopSignal,
		StopTimeout: cloneValue(control.StopTimeout), User: control.User,
		WorkingDirectory: control.WorkingDirectory, CapAdd: slices.Clone(control.CapAdd),
		CapDrop: slices.Clone(control.CapDrop), DNS: slices.Clone(control.DNS),
		DNSOptions: slices.Clone(control.DNSOptions), DNSSearch: slices.Clone(control.DNSSearch),
		Devices: slices.Clone(control.Devices), ExtraHosts: slices.Clone(control.ExtraHosts),
		GroupAdd: slices.Clone(control.GroupAdd), Sysctls: maps.Clone(oci.Linux.Sysctl),
		Tmpfs: cloneTmpfs(control.Tmpfs), Ulimits: decodedRlimits(oci.Process.Rlimits),
		Environment: slices.Clone(oci.Process.Env), Labels: slices.Clone(control.Labels),
		ExposedPorts: slices.Clone(control.ExposedPorts), Ports: slices.Clone(control.Ports),
		NoNewPrivileges: oci.Process.NoNewPrivileges, Mounts: slices.Clone(control.Mounts),
		Init: cloneValue(control.Init), StdinOpen: cloneValue(control.StdinOpen),
		OOMKillDisable: cloneValue(control.OOMKillDisable), ReadOnly: cloneValue(control.ReadOnly),
		TTY: cloneValue(control.TTY), Healthcheck: cloneHealthcheck(control.Healthcheck),
	}, true
}

func decodedWeight(value *specs.LinuxBlockIO) *int {
	if value == nil || value.Weight == nil {
		return nil
	}
	result := int(*value.Weight)

	return &result
}

func decodedCgroup(namespaces []specs.LinuxNamespace) string {
	if slices.ContainsFunc(namespaces, func(value specs.LinuxNamespace) bool {
		return value.Type == specs.CgroupNamespace
	}) {
		return cgroupPrivate
	}

	return ""
}

func decodedMemory(value *specs.LinuxMemory) int64 {
	if value == nil || value.Limit == nil {
		return 0
	}

	return *value.Limit
}

func decodedPids(value *specs.LinuxPids) *int64 {
	if value == nil {
		return nil
	}

	return cloneValue(value.Limit)
}

func decodedRlimits(values []specs.POSIXRlimit) []containerconfig.Ulimit {
	if values == nil {
		return nil
	}
	result := make([]containerconfig.Ulimit, len(values))
	for index, value := range values {
		result[index] = containerconfig.Ulimit{
			Name: strings.ToLower(strings.TrimPrefix(value.Type, "RLIMIT_")),
			Soft: decodedUlimit(value.Soft), Hard: decodedUlimit(value.Hard),
		}
	}

	return result
}

func decodedUlimit(value uint64) int64 {
	if value == math.MaxUint64 {
		return -1
	}
	if value > math.MaxInt64 {
		return math.MinInt64
	}

	return int64(value)
}
