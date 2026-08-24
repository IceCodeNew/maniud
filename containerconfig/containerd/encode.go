package containerd

import (
	"math"
	"slices"
	"strconv"
	"strings"

	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	rootfsPath              = "rootfs"
	defaultWorkingDir       = "/"
	defaultSharedMemorySize = int64(64 << 20)
	cpuPeriod               = uint64(100_000)
)

// Encode validates spec and returns a complete owned containerd projection.
func Encode(spec containerconfig.Spec) (Configuration, error) {
	canonical, err := canonicalSpec(spec)
	if err != nil {
		return Configuration{}, err
	}

	configuration := Configuration{
		OCI:     encodeOCI(canonical),
		Control: encodeControl(canonical),
	}

	return configuration, nil
}

// Validate reports whether spec can be represented without a lossy fallback.
func Validate(spec containerconfig.Spec) error {
	_, err := Encode(spec)

	return err
}

func encodeOCI(spec containerconfig.Spec) specs.Spec {
	workingDirectory := spec.WorkingDirectory
	if workingDirectory == "" {
		workingDirectory = defaultWorkingDir
	}
	user, _ := parsedUser(spec.User)
	user.AdditionalGids = parsedGroups(spec.GroupAdd)
	capabilities := effectiveCapabilities(spec.CapAdd, spec.CapDrop)
	process := &specs.Process{
		Terminal: valueOrZero(spec.TTY), User: user,
		Args: append(slices.Clone(spec.Entrypoint), spec.Command...), Env: slices.Clone(spec.Environment),
		Cwd: workingDirectory, Capabilities: capabilitySets(capabilities),
		Rlimits: encodedRlimits(spec.Ulimits), NoNewPrivileges: spec.NoNewPrivileges,
		OOMScoreAdj: cloneValue(spec.OOMScoreAdj),
	}
	linux := &specs.Linux{
		Sysctl: cloneMap(spec.Sysctls), Resources: encodedResources(spec),
		Namespaces: encodedNamespaces(spec.Cgroup),
		MaskedPaths: []string{
			"/proc/acpi", "/proc/asound", "/proc/kcore", "/proc/keys", "/proc/latency_stats",
			"/proc/sched_debug", "/proc/scsi", "/proc/timer_list", "/proc/timer_stats",
			"/sys/devices/virtual/powercap", "/sys/firmware",
		},
		ReadonlyPaths: []string{"/proc/bus", "/proc/fs", "/proc/irq", "/proc/sys", "/proc/sysrq-trigger"},
	}

	return specs.Spec{
		Version: specs.Version, Process: process,
		Root:     &specs.Root{Path: rootfsPath, Readonly: valueOrZero(spec.ReadOnly)},
		Hostname: spec.Hostname, Mounts: encodedMounts(spec), Linux: linux,
	}
}

func encodeControl(spec containerconfig.Spec) Control {
	control := Control{
		ServiceName: spec.ServiceName, ContainerName: spec.ContainerName, Platform: spec.Platform,
		EntrypointLength: len(spec.Entrypoint), EntrypointDefined: spec.Entrypoint != nil,
		CommandDefined: spec.Command != nil, NetworkMode: spec.NetworkMode,
		CgroupParent: spec.CgroupParent, CPUs: spec.CPUs, Restart: spec.Restart,
		SharedMemoryBytes: spec.SharedMemoryBytes, StopSignal: spec.StopSignal,
		StopTimeout: cloneValue(spec.StopTimeout), User: spec.User, WorkingDirectory: spec.WorkingDirectory,
		CapAdd: slices.Clone(spec.CapAdd), CapDrop: slices.Clone(spec.CapDrop),
		DNS: slices.Clone(spec.DNS), DNSOptions: slices.Clone(spec.DNSOptions),
		DNSSearch: slices.Clone(spec.DNSSearch), Devices: slices.Clone(spec.Devices),
		ExtraHosts: slices.Clone(spec.ExtraHosts), GroupAdd: slices.Clone(spec.GroupAdd),
		Labels:       slices.Clone(spec.Labels),
		ExposedPorts: slices.Clone(spec.ExposedPorts), Ports: slices.Clone(spec.Ports),
		Tmpfs: cloneTmpfs(spec.Tmpfs), Mounts: slices.Clone(spec.Mounts),
		Init: cloneValue(spec.Init), StdinOpen: cloneValue(spec.StdinOpen),
		OOMKillDisable: cloneValue(spec.OOMKillDisable), ReadOnly: cloneValue(spec.ReadOnly),
		TTY: cloneValue(spec.TTY), Healthcheck: cloneHealthcheck(spec.Healthcheck),
	}

	return control
}

func encodedNamespaces(cgroup string) []specs.LinuxNamespace {
	result := []specs.LinuxNamespace{
		{Type: specs.PIDNamespace}, {Type: specs.IPCNamespace}, {Type: specs.UTSNamespace},
		{Type: specs.MountNamespace}, {Type: specs.NetworkNamespace},
	}
	if cgroup == cgroupPrivate {
		result = append(result, specs.LinuxNamespace{Type: specs.CgroupNamespace})
	}

	return result
}

func encodedResources(spec containerconfig.Spec) *specs.LinuxResources {
	resources := &specs.LinuxResources{
		Devices: []specs.LinuxDeviceCgroup{{Allow: false, Access: "rwm"}},
	}
	if spec.MemoryBytes != 0 || spec.OOMKillDisable != nil {
		resources.Memory = &specs.LinuxMemory{DisableOOMKiller: cloneValue(spec.OOMKillDisable)}
		if spec.MemoryBytes != 0 {
			resources.Memory.Limit = cloneValue(&spec.MemoryBytes)
		}
	}
	if spec.PidsLimit != nil {
		resources.Pids = &specs.LinuxPids{Limit: cloneValue(spec.PidsLimit)}
	}
	if spec.BlkioWeight != nil {
		// canonicalSpec limits the value to the OCI uint16 range.
		//nolint:gosec // The validated range is 10 through 1000.
		weight := uint16(*spec.BlkioWeight)
		resources.BlockIO = &specs.LinuxBlockIO{Weight: &weight}
	}
	if spec.CPUs != "" {
		quota, _ := cpuQuota(spec.CPUs)
		period := cpuPeriod
		resources.CPU = &specs.LinuxCPU{Period: &period, Quota: &quota}
	}

	return resources
}

func encodedMounts(spec containerconfig.Spec) []specs.Mount {
	mounts := defaultMounts()
	for _, mount := range spec.Mounts {
		if mount.Kind != containerconfig.MountBind {
			continue
		}
		options := []string{"rbind", "rw", "rprivate"}
		if mount.ReadOnly {
			options[1] = "ro"
		}
		mounts = append(mounts, specs.Mount{
			Destination: mount.Target, Type: "bind", Source: mount.Source, Options: options,
		})
	}
	for _, mount := range spec.Tmpfs {
		options := append([]string{mountOptionNoSUID, mountOptionNoDev}, mount.Options...)
		mounts = append(mounts, specs.Mount{
			Destination: mount.Target, Type: mountTypeTmpfs, Source: mountTypeTmpfs, Options: options,
		})
	}
	if !hasMountTarget(spec, sharedMemoryMountPoint) {
		sharedMemorySize := spec.SharedMemoryBytes
		if sharedMemorySize == 0 {
			sharedMemorySize = defaultSharedMemorySize
		}
		mounts = append(mounts, specs.Mount{
			Destination: sharedMemoryMountPoint, Type: mountTypeTmpfs, Source: "shm",
			Options: []string{
				mountOptionNoSUID, mountOptionNoExec, mountOptionNoDev, "mode=1777",
				"size=" + strconv.FormatInt(sharedMemorySize, 10),
			},
		})
	}

	return mounts
}

func hasMountTarget(spec containerconfig.Spec, target string) bool {
	return slices.ContainsFunc(spec.Mounts, func(value containerconfig.Mount) bool {
		return value.Target == target
	}) || slices.ContainsFunc(spec.Tmpfs, func(value containerconfig.TmpfsMount) bool {
		return value.Target == target
	})
}

func defaultMounts() []specs.Mount {
	return []specs.Mount{
		{Destination: "/proc", Type: "proc", Source: "proc", Options: []string{
			mountOptionNoSUID, mountOptionNoExec, mountOptionNoDev,
		}},
		{Destination: "/dev", Type: mountTypeTmpfs, Source: mountTypeTmpfs, Options: []string{
			mountOptionNoSUID, "strictatime", "mode=755", "size=65536k",
		}},
		{Destination: "/dev/pts", Type: "devpts", Source: "devpts", Options: []string{
			mountOptionNoSUID, mountOptionNoExec, "newinstance", "ptmxmode=0666", "mode=0620", "gid=5",
		}},
		{Destination: "/dev/mqueue", Type: "mqueue", Source: "mqueue", Options: []string{
			mountOptionNoSUID, mountOptionNoExec, mountOptionNoDev,
		}},
		{Destination: "/sys", Type: "sysfs", Source: "sysfs", Options: []string{
			mountOptionNoSUID, mountOptionNoExec, mountOptionNoDev, "ro",
		}},
		{Destination: "/run", Type: mountTypeTmpfs, Source: mountTypeTmpfs, Options: []string{
			mountOptionNoSUID, "strictatime", "mode=755", "size=65536k",
		}},
	}
}

func capabilitySets(values []string) *specs.LinuxCapabilities {
	return &specs.LinuxCapabilities{
		Bounding: slices.Clone(values), Effective: slices.Clone(values), Permitted: slices.Clone(values),
	}
}

func effectiveCapabilities(add, drop []string) []string {
	defaults := defaultCapabilities()
	values := make(map[string]struct{}, len(defaults)+len(add))
	for _, value := range defaults {
		values[value] = struct{}{}
	}
	for _, value := range drop {
		name := capabilityName(value)
		if name == allCapabilities {
			clear(values)
		} else {
			delete(values, name)
		}
	}
	for _, value := range add {
		values[capabilityName(value)] = struct{}{}
	}
	result := make([]string, 0, len(values))
	for value := range values {
		result = append(result, value)
	}
	slices.Sort(result)

	return result
}

func defaultCapabilities() []string {
	return []string{
		"CAP_AUDIT_WRITE", "CAP_CHOWN", "CAP_DAC_OVERRIDE", "CAP_FOWNER", "CAP_FSETID",
		"CAP_KILL", "CAP_MKNOD", "CAP_NET_BIND_SERVICE", capabilityNetRaw, "CAP_SETFCAP",
		"CAP_SETGID", "CAP_SETPCAP", "CAP_SETUID", "CAP_SYS_CHROOT",
	}
}

func capabilityName(value string) string {
	value = strings.ToUpper(value)
	if value == allCapabilities {
		return value
	}
	if !strings.HasPrefix(value, "CAP_") {
		value = "CAP_" + value
	}

	return value
}

func parsedGroups(values []string) []uint32 {
	if values == nil {
		return nil
	}
	result := make([]uint32, len(values))
	for index, value := range values {
		parsed, _ := strconv.ParseUint(value, 10, 32)
		result[index] = uint32(parsed)
	}

	return result
}

func encodedRlimits(values []containerconfig.Ulimit) []specs.POSIXRlimit {
	if values == nil {
		return nil
	}
	result := make([]specs.POSIXRlimit, len(values))
	for index, value := range values {
		result[index] = specs.POSIXRlimit{
			Type: "RLIMIT_" + strings.ToUpper(value.Name),
			Soft: ulimitValue(value.Soft), Hard: ulimitValue(value.Hard),
		}
	}

	return result
}

func ulimitValue(value int64) uint64 {
	if value == -1 {
		return math.MaxUint64
	}

	// canonicalSpec rejects every other negative ulimit.
	//nolint:gosec // The validated value is non-negative.
	return uint64(value)
}

func valueOrZero(value *bool) bool {
	return value != nil && *value
}
