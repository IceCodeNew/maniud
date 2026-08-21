package docker

import (
	"maps"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/IceCodeNew/maniud/internal/domain"
)

//nolint:cyclop,funlen,gocyclo // This function is the inverse API-to-WorkloadSpec mapping table.
func dockerWorkloadFromInspect(
	name string,
	config *containertypes.Config,
	host *containertypes.HostConfig,
) (domain.WorkloadSpec, bool) {
	var empty domain.WorkloadSpec
	if config == nil || host == nil || !validObservedDockerConfig(config) || !validObservedDockerHost(host) {
		return empty, false
	}
	labels, labelsValid := dockerObservedLabels(config.Labels)
	exposed, ports, portsValid := dockerObservedPorts(config.ExposedPorts, host.PortBindings)
	dns, dnsValid := dockerObservedDNS(host.DNS)
	devices, devicesValid := dockerObservedDevices(host.Devices)
	tmpfs, tmpfsValid := dockerObservedTmpfs(host.Tmpfs)
	ulimits, ulimitsValid := dockerObservedUlimits(host.Ulimits)
	mounts, mountsValid := dockerObservedMounts(config.Volumes, host.Mounts)
	healthcheck, healthcheckValid := dockerObservedHealthcheck(config.Healthcheck)
	restart, restartValid := dockerObservedRestart(host.RestartPolicy)
	security, securityValid := dockerObservedSecurity(host.SecurityOpt)
	if !labelsValid || !portsValid || !dnsValid || !devicesValid || !tmpfsValid || !ulimitsValid ||
		!mountsValid || !healthcheckValid || !restartValid || !securityValid ||
		!validDockerText(config.Hostname) || !validDockerText(config.User) ||
		!validDockerText(config.WorkingDir) || !validDockerText(config.StopSignal) ||
		!validDockerStringSlice(config.Env) || !validProcessArguments(config.Entrypoint) ||
		!validProcessArguments(config.Cmd) || !validDockerStringSlice(host.CapAdd) ||
		!validDockerStringSlice(host.CapDrop) || !validDockerStringSlice(host.DNSOptions) ||
		!validDockerStringSlice(host.DNSSearch) || !validDockerStringSlice(host.ExtraHosts) ||
		!validDockerStringSlice(host.GroupAdd) || !validDockerStringMap(host.Sysctls) ||
		host.Memory < 0 || host.NanoCPUs < 0 || host.ShmSize < 0 ||
		host.BlkioWeight != 0 && (host.BlkioWeight < 10 || host.BlkioWeight > 1000) ||
		host.OomScoreAdj < -1000 || host.OomScoreAdj > 1000 || !validPidsLimit(host.PidsLimit) ||
		config.StopTimeout != nil && *config.StopTimeout <= 0 {
		return empty, false
	}
	spec := domain.WorkloadSpec{
		ContainerName: name, Entrypoint: slices.Clone(config.Entrypoint), Command: slices.Clone(config.Cmd),
		NetworkMode: string(host.NetworkMode), CgroupParent: host.CgroupParent,
		Cgroup: string(host.CgroupnsMode), CPUs: dockerCPUString(host.NanoCPUs),
		Hostname: config.Hostname, MemoryBytes: host.Memory, Restart: restart,
		SharedMemoryBytes: host.ShmSize, StopSignal: config.StopSignal,
		User: config.User, WorkingDirectory: config.WorkingDir,
		CapAdd: slices.Clone(host.CapAdd), CapDrop: slices.Clone(host.CapDrop), DNS: dns,
		DNSOptions: slices.Clone(host.DNSOptions), DNSSearch: slices.Clone(host.DNSSearch),
		Devices: devices, ExtraHosts: slices.Clone(host.ExtraHosts), GroupAdd: slices.Clone(host.GroupAdd),
		Sysctls: maps.Clone(host.Sysctls), Tmpfs: tmpfs, Ulimits: ulimits,
		Environment: slices.Clone(config.Env), Labels: labels, ExposedPorts: exposed, Ports: ports,
		NoNewPrivileges: security, Mounts: mounts, Init: cloneDockerPointer(host.Init),
		OOMKillDisable: cloneDockerPointer(host.OomKillDisable), Healthcheck: healthcheck,
	}
	if host.BlkioWeight != 0 {
		value := int(host.BlkioWeight)
		spec.BlkioWeight = &value
	}
	if host.OomScoreAdj != 0 {
		value := host.OomScoreAdj
		spec.OOMScoreAdj = &value
	}
	spec.PidsLimit = cloneDockerPointer(host.PidsLimit)
	if config.StopTimeout != nil {
		value := int64(*config.StopTimeout)
		spec.StopTimeout = &value
	}
	spec.StdinOpen = trueDockerPointer(config.OpenStdin)
	spec.ReadOnly = trueDockerPointer(host.ReadonlyRootfs)
	spec.TTY = trueDockerPointer(config.Tty)

	return canonicalDockerSpec(spec), true
}

func validObservedDockerConfig(config *containertypes.Config) bool {
	return config.Domainname == "" && !config.AttachStdin && !config.AttachStdout && !config.AttachStderr &&
		!config.StdinOnce && !config.ArgsEscaped && !config.NetworkDisabled && len(config.OnBuild) == 0
}

//nolint:cyclop,gocyclo // Every rejected field is an unsupported API configuration surface.
func validObservedDockerHost(host *containertypes.HostConfig) bool {
	resources := host.Resources

	return len(host.Binds) == 0 && host.ContainerIDFile == "" && !host.AutoRemove && host.VolumeDriver == "" &&
		len(host.VolumesFrom) == 0 && host.ConsoleSize == [2]uint{} && len(host.Annotations) == 0 &&
		(host.IpcMode == "" || host.IpcMode == dockerCgroupPrivate) && host.Cgroup == "" && len(host.Links) == 0 &&
		host.PidMode == "" && !host.Privileged && !host.PublishAllPorts && len(host.StorageOpt) == 0 &&
		host.UTSMode == "" && host.UsernsMode == "" && host.Isolation == "" &&
		resources.CPUShares == 0 && len(resources.BlkioWeightDevice) == 0 &&
		len(resources.BlkioDeviceReadBps) == 0 && len(resources.BlkioDeviceWriteBps) == 0 &&
		len(resources.BlkioDeviceReadIOps) == 0 && len(resources.BlkioDeviceWriteIOps) == 0 &&
		resources.CPUPeriod == 0 && resources.CPUQuota == 0 && resources.CPURealtimePeriod == 0 &&
		resources.CPURealtimeRuntime == 0 && resources.CpusetCpus == "" && resources.CpusetMems == "" &&
		len(resources.DeviceCgroupRules) == 0 && len(resources.DeviceRequests) == 0 &&
		resources.MemoryReservation == 0 && resources.MemorySwap == 0 && resources.MemorySwappiness == nil &&
		resources.CPUCount == 0 && resources.CPUPercent == 0 && resources.IOMaximumIOps == 0 &&
		resources.IOMaximumBandwidth == 0
}

func dockerObservedLabels(values map[string]string) ([]string, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		if key == "" || !validDockerText(key) || !validDockerText(value) {
			return nil, false
		}
		if !domain.IsOwnershipLabel(key) {
			result = append(result, key+"="+value)
		}
	}
	slices.Sort(result)

	return result, true
}

//nolint:cyclop // Port and exposure sets are decoded as one consistency boundary.
func dockerObservedPorts(
	exposed network.PortSet,
	bindings network.PortMap,
) ([]domain.ExposedPort, []domain.PortBinding, bool) {
	exposedResult := make([]domain.ExposedPort, 0, len(exposed))
	for port := range exposed {
		if !port.IsValid() || port.Num() == 0 || !validExposedProtocol(string(port.Proto())) {
			return nil, nil, false
		}
		exposedResult = append(exposedResult, domain.ExposedPort{
			TargetPort: port.Num(), Protocol: string(port.Proto()),
		})
	}
	ports := make([]domain.PortBinding, 0, len(bindings))
	for port, values := range bindings {
		if !port.IsValid() || port.Num() == 0 || len(values) != 1 ||
			(port.Proto() != network.TCP && port.Proto() != network.UDP) {
			return nil, nil, false
		}
		published, err := strconv.ParseUint(values[0].HostPort, 10, 16)
		if err != nil || published == 0 {
			return nil, nil, false
		}
		host := ""
		if values[0].HostIP.IsValid() {
			host = values[0].HostIP.String()
		}
		ports = append(ports, domain.PortBinding{
			HostIP: host, PublishedPort: uint16(published), TargetPort: port.Num(), Protocol: string(port.Proto()),
		})
	}
	slices.SortFunc(exposedResult, func(left, right domain.ExposedPort) int {
		if left.TargetPort != right.TargetPort {
			return int(left.TargetPort) - int(right.TargetPort)
		}

		return strings.Compare(left.Protocol, right.Protocol)
	})
	slices.SortFunc(ports, func(left, right domain.PortBinding) int {
		return strings.Compare(dockerPortSortKey(left), dockerPortSortKey(right))
	})

	return exposedResult, ports, true
}

func validExposedProtocol(value string) bool {
	return value == dockerProtocolTCP || value == dockerProtocolUDP || value == dockerProtocolSCTP
}

func dockerObservedDNS(values []netip.Addr) ([]string, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]string, len(values))
	for index, value := range values {
		if !value.IsValid() {
			return nil, false
		}
		result[index] = value.String()
	}

	return result, true
}

func dockerObservedDevices(values []containertypes.DeviceMapping) ([]domain.DeviceMapping, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]domain.DeviceMapping, len(values))
	for index, value := range values {
		if !validDockerText(value.PathOnHost) || !validDockerText(value.PathInContainer) ||
			value.CgroupPermissions == "" || strings.Trim(value.CgroupPermissions, "rwm") != "" {
			return nil, false
		}
		result[index] = domain.DeviceMapping{
			Source: value.PathOnHost, Target: value.PathInContainer, Permissions: value.CgroupPermissions,
		}
	}

	return result, true
}

func dockerObservedTmpfs(values map[string]string) ([]domain.TmpfsMount, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]domain.TmpfsMount, 0, len(values))
	for target, options := range values {
		if target == "" || !validDockerText(target) || !validDockerText(options) {
			return nil, false
		}
		mountValue := domain.TmpfsMount{Target: target}
		if options != "" {
			mountValue.Options = strings.Split(options, ",")
		}
		result = append(result, mountValue)
	}
	slices.SortFunc(result, func(left, right domain.TmpfsMount) int {
		return strings.Compare(left.Target, right.Target)
	})

	return result, true
}

func dockerObservedUlimits(values []*containertypes.Ulimit) ([]domain.Ulimit, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]domain.Ulimit, len(values))
	for index, value := range values {
		if value == nil || value.Name == "" || !validDockerText(value.Name) {
			return nil, false
		}
		result[index] = domain.Ulimit{Name: value.Name, Soft: value.Soft, Hard: value.Hard}
	}
	slices.SortFunc(result, func(left, right domain.Ulimit) int {
		return strings.Compare(left.Name, right.Name)
	})

	return result, true
}

//nolint:cyclop // Inspect exposes anonymous volumes and bind mounts in separate API fields.
func dockerObservedMounts(
	volumes map[string]struct{},
	mounts []mount.Mount,
) ([]domain.Mount, bool) {
	result := make([]domain.Mount, 0, len(volumes)+len(mounts))
	for target := range volumes {
		if target == "" || !validDockerText(target) {
			return nil, false
		}
		result = append(result, domain.Mount{Kind: domain.MountVolume, Target: target})
	}
	for _, value := range mounts {
		if value.Type != mount.TypeBind || value.Source == "" || value.Target == "" ||
			!validDockerText(value.Source) || !validDockerText(value.Target) || value.Consistency != "" ||
			value.BindOptions != nil || value.VolumeOptions != nil || value.ImageOptions != nil ||
			value.TmpfsOptions != nil || value.ClusterOptions != nil {
			return nil, false
		}
		result = append(result, domain.Mount{
			Kind: domain.MountBind, Source: value.Source, Target: value.Target, ReadOnly: value.ReadOnly,
		})
	}
	slices.SortFunc(result, func(left, right domain.Mount) int {
		return strings.Compare(left.Target+"\x00"+left.Source, right.Target+"\x00"+right.Source)
	})

	return result, true
}

// dockerRuntimeMounts binds each desired persistent target to the exact source
// identity reported by container inspect. Unsupported or additional mounts
// fail closed so backup code never acts on an ambiguous host path or volume.
//
//nolint:cyclop // Bind, volume, and tmpfs metadata have disjoint native invariants.
func dockerRuntimeMounts(
	values []containertypes.MountPoint,
	spec domain.WorkloadSpec,
) ([]domain.RuntimeMount, bool) {
	desired := make(map[string]domain.Mount, len(spec.Mounts))
	for _, value := range spec.Mounts {
		if _, duplicate := desired[value.Target]; duplicate {
			return nil, false
		}
		desired[value.Target] = value
	}
	tmpfs := make(map[string]struct{}, len(spec.Tmpfs))
	for _, value := range spec.Tmpfs {
		tmpfs[value.Target] = struct{}{}
	}
	result := make([]domain.RuntimeMount, 0, len(desired))
	for _, value := range values {
		if _, found := tmpfs[value.Destination]; found && value.Type == mount.TypeTmpfs {
			continue
		}
		expected, found := desired[value.Destination]
		if !found || value.Source == "" || !validDockerText(value.Source) {
			return nil, false
		}
		delete(desired, value.Destination)
		switch expected.Kind {
		case domain.MountBind:
			if value.Type != mount.TypeBind || value.Name != "" || value.Driver != "" ||
				value.Source != expected.Source || value.RW == expected.ReadOnly || value.Mode != "" ||
				value.Propagation != mount.PropagationRPrivate {
				return nil, false
			}
			result = append(result, domain.RuntimeMount{
				Kind: domain.MountBind, Source: value.Source, Target: value.Destination, ReadOnly: !value.RW,
			})
		case domain.MountVolume:
			if value.Type != mount.TypeVolume || value.Name == "" || !validDockerText(value.Name) ||
				value.Driver != dockerVolumeDriverLocal || !value.RW || value.Mode != "" || value.Propagation != "" ||
				!path.IsAbs(value.Source) || path.Clean(value.Source) != value.Source {
				return nil, false
			}
			result = append(result, domain.RuntimeMount{
				Kind: domain.MountVolume, Name: value.Name, Source: value.Source, Target: value.Destination,
			})
		default:
			return nil, false
		}
	}
	if len(desired) != 0 {
		return nil, false
	}
	if len(result) == 0 {
		return nil, true
	}
	slices.SortFunc(result, func(left, right domain.RuntimeMount) int {
		return strings.Compare(left.Target, right.Target)
	})

	return result, true
}

//nolint:cyclop // Disabled and active healthchecks have disjoint wire invariants.
func dockerObservedHealthcheck(value *containertypes.HealthConfig) (*domain.Healthcheck, bool) {
	if value == nil {
		return nil, true
	}
	if slices.Equal(value.Test, []string{dockerHealthcheckNone}) {
		if value.Interval != 0 || value.Timeout != 0 || value.Retries != 0 ||
			value.StartPeriod != 0 || value.StartInterval != 0 {
			return nil, false
		}

		return &domain.Healthcheck{Disabled: true}, true
	}
	if !validProcessArguments(value.Test) || value.Interval < 0 || value.Timeout < 0 || value.Retries < 0 ||
		value.StartPeriod < 0 || value.StartInterval < 0 {
		return nil, false
	}
	result := &domain.Healthcheck{
		Test: slices.Clone(value.Test), Interval: dockerDurationString(value.Interval),
		Timeout: dockerDurationString(value.Timeout), StartPeriod: dockerDurationString(value.StartPeriod),
		StartInterval: dockerDurationString(value.StartInterval),
	}
	if value.Retries != 0 {
		retries := value.Retries
		result.Retries = &retries
	}

	return result, true
}

func dockerDurationString(value time.Duration) string {
	if value == 0 {
		return ""
	}

	return value.String()
}

func dockerObservedRestart(value containertypes.RestartPolicy) (string, bool) {
	value = normalizeRestartPolicy(value)
	if containertypes.ValidateRestartPolicy(value) != nil {
		return "", false
	}
	result := string(value.Name)
	if value.MaximumRetryCount != 0 {
		result += ":" + strconv.Itoa(value.MaximumRetryCount)
	}

	return result, true
}

func dockerObservedSecurity(values []string) (bool, bool) {
	if len(values) == 0 {
		return false, true
	}
	if len(values) == 1 && (values[0] == "no-new-privileges" ||
		values[0] == "no-new-privileges:true" || values[0] == "no-new-privileges=true") {
		return true, true
	}

	return false, false
}

func dockerCPUString(value int64) string {
	if value == 0 {
		return ""
	}
	integer := value / dockerNanoCPUsPerCPU
	fraction := strings.TrimRight(
		strconv.FormatInt(value%dockerNanoCPUsPerCPU+dockerNanoCPUsPerCPU, 10)[1:],
		"0",
	)
	if fraction == "" {
		return strconv.FormatInt(integer, 10)
	}

	return strconv.FormatInt(integer, 10) + "." + fraction
}

func trueDockerPointer(value bool) *bool {
	if !value {
		return nil
	}
	result := true

	return &result
}
