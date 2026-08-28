package docker

import (
	"maps"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/IceCodeNew/maniud/containerconfig"
)

// Decode validates and translates one Docker Engine Config and HostConfig.
// The Engine values do not carry the portable service name or target platform,
// so those Spec fields retain their zero values.
func Decode(
	name string,
	config *containertypes.Config,
	host *containertypes.HostConfig,
) (containerconfig.Spec, error) {
	spec, valid := dockerWorkloadFromInspect(name, config, host)
	if !valid {
		return containerconfig.Spec{}, validationError("")
	}

	return spec, nil
}

//nolint:cyclop,funlen,gocyclo // This function is the inverse API-to-WorkloadSpec mapping table.
func dockerWorkloadFromInspect(
	name string,
	config *containertypes.Config,
	host *containertypes.HostConfig,
) (containerconfig.Spec, bool) {
	var empty containerconfig.Spec
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
		!validDockerEnvironment(config.Env) || !validProcessArguments(config.Entrypoint) ||
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
	spec := containerconfig.Spec{
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
		!config.StdinOnce && !config.NetworkDisabled && len(config.OnBuild) == 0
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
		result = append(result, key+"="+value)
	}

	return result, true
}

//nolint:cyclop // Port and exposure sets are decoded as one consistency boundary.
func dockerObservedPorts(
	exposed network.PortSet,
	bindings network.PortMap,
) ([]containerconfig.ExposedPort, []containerconfig.PortBinding, bool) {
	exposedResult := make([]containerconfig.ExposedPort, 0, len(exposed))
	for port := range exposed {
		if !port.IsValid() || port.Num() == 0 || !validExposedProtocol(string(port.Proto())) {
			return nil, nil, false
		}
		exposedResult = append(exposedResult, containerconfig.ExposedPort{
			TargetPort: port.Num(), Protocol: string(port.Proto()),
		})
	}
	ports := make([]containerconfig.PortBinding, 0, len(bindings))
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
		ports = append(ports, containerconfig.PortBinding{
			HostIP: host, PublishedPort: uint16(published), TargetPort: port.Num(), Protocol: string(port.Proto()),
		})
	}

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

func dockerObservedDevices(values []containertypes.DeviceMapping) ([]containerconfig.DeviceMapping, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]containerconfig.DeviceMapping, len(values))
	for index, value := range values {
		if !validDockerText(value.PathOnHost) || !validDockerText(value.PathInContainer) ||
			value.CgroupPermissions == "" || strings.Trim(value.CgroupPermissions, "rwm") != "" {
			return nil, false
		}
		result[index] = containerconfig.DeviceMapping{
			Source: value.PathOnHost, Target: value.PathInContainer, Permissions: value.CgroupPermissions,
		}
	}

	return result, true
}

func dockerObservedTmpfs(values map[string]string) ([]containerconfig.TmpfsMount, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]containerconfig.TmpfsMount, 0, len(values))
	for target, options := range values {
		if target == "" || !validDockerText(target) || !validDockerText(options) {
			return nil, false
		}
		mountValue := containerconfig.TmpfsMount{Target: target}
		if options != "" {
			mountValue.Options = strings.Split(options, ",")
		}
		result = append(result, mountValue)
	}

	return result, true
}

func dockerObservedUlimits(values []*containertypes.Ulimit) ([]containerconfig.Ulimit, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]containerconfig.Ulimit, len(values))
	for index, value := range values {
		if value == nil || value.Name == "" || !validDockerText(value.Name) {
			return nil, false
		}
		result[index] = containerconfig.Ulimit{Name: value.Name, Soft: value.Soft, Hard: value.Hard}
	}

	return result, true
}

//nolint:cyclop,gocyclo // Inspect exposes anonymous volumes and bind mounts in separate API fields.
func dockerObservedMounts(
	volumes map[string]struct{},
	mounts []mount.Mount,
) ([]containerconfig.Mount, bool) {
	initialCapacity := max(len(volumes), len(mounts))
	result := make([]containerconfig.Mount, 0, initialCapacity)
	targets := make(map[string]struct{}, initialCapacity)
	for target := range volumes {
		if target == "" || !validDockerText(target) {
			return nil, false
		}
		targets[target] = struct{}{}
		result = append(result, containerconfig.Mount{Kind: containerconfig.MountVolume, Target: target})
	}
	for _, value := range mounts {
		if value.Target == "" || !validDockerText(value.Target) {
			return nil, false
		}
		if _, exists := targets[value.Target]; exists {
			return nil, false
		}
		targets[value.Target] = struct{}{}
		if value.Type == mount.TypeVolume {
			if value.Source != "" || value.ReadOnly || value.Consistency != "" || value.BindOptions != nil ||
				value.ImageOptions != nil || value.TmpfsOptions != nil || value.ClusterOptions != nil ||
				value.VolumeOptions == nil || !value.VolumeOptions.NoCopy ||
				len(value.VolumeOptions.Labels) != 0 || value.VolumeOptions.Subpath != "" ||
				value.VolumeOptions.DriverConfig != nil {
				return nil, false
			}
			result = append(result, containerconfig.Mount{Kind: containerconfig.MountVolume, Target: value.Target})

			continue
		}
		if value.Type != mount.TypeBind || value.Source == "" || value.Target == "" ||
			!validDockerText(value.Source) || value.Consistency != "" ||
			value.BindOptions != nil || value.VolumeOptions != nil || value.ImageOptions != nil ||
			value.TmpfsOptions != nil || value.ClusterOptions != nil {
			return nil, false
		}
		result = append(result, containerconfig.Mount{
			Kind: containerconfig.MountBind, Source: value.Source, Target: value.Target, ReadOnly: value.ReadOnly,
		})
	}

	return result, true
}

//nolint:cyclop // Disabled and active healthchecks have disjoint wire invariants.
func dockerObservedHealthcheck(value *containertypes.HealthConfig) (*containerconfig.Healthcheck, bool) {
	if value == nil {
		return nil, true
	}
	if slices.Equal(value.Test, []string{dockerHealthcheckNone}) {
		if value.Interval != 0 || value.Timeout != 0 || value.Retries != 0 ||
			value.StartPeriod != 0 || value.StartInterval != 0 {
			return nil, false
		}

		return &containerconfig.Healthcheck{Disabled: true}, true
	}
	if !validProcessArguments(value.Test) || value.Interval < 0 || value.Timeout < 0 || value.Retries < 0 ||
		value.StartPeriod < 0 || value.StartInterval < 0 {
		return nil, false
	}
	result := &containerconfig.Healthcheck{
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

func normalizeRestartPolicy(policy containertypes.RestartPolicy) containertypes.RestartPolicy {
	if policy.Name == "" {
		return containertypes.RestartPolicy{
			Name:              containertypes.RestartPolicyDisabled,
			MaximumRetryCount: 0,
		}
	}

	return policy
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
