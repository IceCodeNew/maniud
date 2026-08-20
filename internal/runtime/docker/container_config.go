package docker

import (
	"maps"
	"math"
	"net/netip"
	"path"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	dockerDefaultSharedMemoryBytes = 64 << 20
	maximumDockerTextBytes         = 4096
	dockerNanoCPUsPerCPU           = int64(1_000_000_000)
	dockerCPUFractionDigits        = 9
	dockerProtocolTCP              = "tcp"
	dockerProtocolUDP              = "udp"
	dockerProtocolSCTP             = "sctp"
	dockerHealthcheckNone          = "NONE"
	dockerCgroupPrivate            = "private"
	dockerVolumeDriverLocal        = "local"
	minimumDockerBlkioWeight       = 10
	maximumDockerBlkioWeight       = 1000
	minimumDockerOOMScore          = -1000
	maximumDockerOOMScore          = 1000
)

func dockerCreateConfiguration(
	workload domain.DesiredWorkload,
	transaction string,
	options application.WorkloadCreateOptions,
) (containertypes.CreateRequest, bool) {
	var empty containertypes.CreateRequest
	labels, valid := dockerLabels(workload.Labels, workloadOwnershipLabels(workload, transaction))
	if !valid {
		return empty, false
	}
	config, host, valid := dockerConfiguration(workload.WorkloadSpec, workload.Image.Reference, labels)
	if !valid {
		return empty, false
	}
	if !options.CopyImageVolumes {
		host.Mounts = appendNoCopyVolumes(host.Mounts, config.Volumes)
		config.Volumes = nil
	}

	return containertypes.CreateRequest{Config: config, HostConfig: host}, true
}

//nolint:cyclop // This function is the single WorkloadSpec-to-API mapping table.
func dockerConfiguration(
	spec domain.WorkloadSpec,
	image string,
	labels map[string]string,
) (*containertypes.Config, *containertypes.HostConfig, bool) {
	nanoCPUs, valid := dockerNanoCPUs(spec.CPUs)
	blkioWeight, blkioValid := dockerBlkioWeight(spec.BlkioWeight)
	restart, restartValid := dockerRestartPolicy(spec.Restart)
	ports, exposed, portsValid := dockerPorts(spec.ExposedPorts, spec.Ports)
	dns, dnsValid := dockerDNS(spec.DNS)
	tmpfs, tmpfsValid := dockerTmpfs(spec.Tmpfs)
	devices, devicesValid := dockerDevices(spec.Devices)
	ulimits, ulimitsValid := dockerUlimits(spec.Ulimits)
	volumes, mounts, mountsValid := dockerMounts(spec.Mounts)
	healthcheck, healthcheckValid := dockerHealthcheck(spec.Healthcheck)
	stopTimeout, stopTimeoutValid := dockerStopTimeout(spec.StopTimeout)
	if !valid || !blkioValid || !restartValid || !portsValid || !dnsValid || !tmpfsValid || !devicesValid ||
		!ulimitsValid || !mountsValid || !healthcheckValid || !stopTimeoutValid ||
		!validDockerWorkloadSpec(spec) {
		return nil, nil, false
	}
	securityOptions := []string(nil)
	if spec.NoNewPrivileges {
		securityOptions = []string{"no-new-privileges:true"}
	}

	return &containertypes.Config{ //nolint:exhaustruct // Unsupported API fields intentionally keep zero values.
			Hostname: spec.Hostname, User: spec.User, ExposedPorts: exposed,
			Tty: valueOrZero(spec.TTY), OpenStdin: valueOrZero(spec.StdinOpen),
			Env: slices.Clone(spec.Environment), Cmd: slices.Clone(spec.Command), Healthcheck: healthcheck,
			Image: image, Volumes: volumes, WorkingDir: spec.WorkingDirectory,
			Entrypoint: slices.Clone(spec.Entrypoint), Labels: labels, StopSignal: spec.StopSignal,
			StopTimeout: stopTimeout,
		}, &containertypes.HostConfig{ //nolint:exhaustruct // Unsupported API fields intentionally keep zero values.
			NetworkMode: containertypes.NetworkMode(spec.NetworkMode), PortBindings: ports, RestartPolicy: restart,
			CapAdd: slices.Clone(spec.CapAdd), CapDrop: slices.Clone(spec.CapDrop),
			CgroupnsMode: containertypes.CgroupnsMode(spec.Cgroup), DNS: dns,
			DNSOptions: slices.Clone(spec.DNSOptions), DNSSearch: slices.Clone(spec.DNSSearch),
			ExtraHosts: slices.Clone(spec.ExtraHosts), GroupAdd: slices.Clone(spec.GroupAdd),
			OomScoreAdj: optionalInt(spec.OOMScoreAdj), ReadonlyRootfs: valueOrZero(spec.ReadOnly),
			SecurityOpt: securityOptions, Tmpfs: tmpfs, ShmSize: spec.SharedMemoryBytes,
			Sysctls: maps.Clone(spec.Sysctls),
			Resources: containertypes.Resources{ //nolint:exhaustruct // Only supported resources are populated.
				Memory: spec.MemoryBytes, NanoCPUs: nanoCPUs, CgroupParent: spec.CgroupParent,
				BlkioWeight: blkioWeight, Devices: devices,
				OomKillDisable: cloneDockerPointer(spec.OOMKillDisable), PidsLimit: cloneDockerPointer(spec.PidsLimit),
				Ulimits: ulimits,
			},
			Mounts: mounts, Init: cloneDockerPointer(spec.Init),
		}, true
}

//nolint:cyclop // Validation mirrors every field accepted by dockerConfiguration.
func validDockerWorkloadSpec(spec domain.WorkloadSpec) bool {
	return validOwnershipName(spec.ServiceName) && validContainerName(spec.ContainerName) &&
		spec.NetworkMode == dockerNetworkMode && validProcessArguments(spec.Entrypoint) &&
		validProcessArguments(spec.Command) && validDockerPlatform(spec.Platform) &&
		validDockerText(spec.Hostname) && validDockerText(spec.User) && validDockerText(spec.WorkingDirectory) &&
		validDockerText(spec.StopSignal) && spec.MemoryBytes >= 0 && spec.SharedMemoryBytes >= 0 &&
		validOptionalRange(spec.BlkioWeight, minimumDockerBlkioWeight, maximumDockerBlkioWeight) &&
		validOptionalRange(spec.OOMScoreAdj, minimumDockerOOMScore, maximumDockerOOMScore) &&
		validPidsLimit(spec.PidsLimit) && validDockerStringSlice(spec.CapAdd) &&
		validDockerStringSlice(spec.CapDrop) && validDockerStringSlice(spec.DNSOptions) &&
		validDockerStringSlice(spec.DNSSearch) && validDockerStringSlice(spec.ExtraHosts) &&
		validDockerStringSlice(spec.GroupAdd) && validDockerStringMap(spec.Sysctls) &&
		validDockerEnvironment(spec.Environment) && validDockerStringSlice(spec.Labels)
}

func validDockerPlatform(platform domain.Platform) bool {
	return platform.OS == dockerOperatingSystem &&
		(platform.Architecture == dockerArchitectureAMD64 && platform.Variant == "" ||
			platform.Architecture == dockerArchitectureARM64 && platform.Variant == dockerARM64Variant)
}

func validDockerText(value string) bool {
	return len(value) <= maximumDockerTextBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validDockerStringSlice(values []string) bool {
	return !slices.ContainsFunc(values, func(value string) bool { return !validDockerText(value) })
}

func validDockerStringMap(values map[string]string) bool {
	for key, value := range values {
		if key == "" || !validDockerText(key) || !validDockerText(value) {
			return false
		}
	}

	return true
}

func validDockerEnvironment(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key, _, found := strings.Cut(value, "=")
		if !found || key == "" || !validDockerText(value) {
			return false
		}
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}

	return true
}

func validOptionalRange[T ~int](value *T, minimum, maximum T) bool {
	return value == nil || *value >= minimum && *value <= maximum
}

func validPidsLimit(value *int64) bool {
	return value == nil || *value == -1 || *value > 0
}

func dockerLabels(values []string, ownership map[string]string) (map[string]string, bool) {
	labels := make(map[string]string, len(values)+len(ownership))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found {
			selected = ""
		}
		if key == "" || !validDockerText(key) || !validDockerText(selected) || reservedWorkloadLabel(key) {
			return nil, false
		}
		if _, exists := labels[key]; exists {
			return nil, false
		}
		labels[key] = selected
	}
	maps.Copy(labels, ownership)

	return labels, true
}

func reservedWorkloadLabel(value string) bool {
	return domain.IsOwnershipLabel(value)
}

//nolint:cyclop // Decimal CPU syntax is validated field by field before conversion.
func dockerNanoCPUs(value string) (int64, bool) {
	if value == "" {
		return 0, true
	}
	integer, fraction, found := strings.Cut(value, ".")
	if integer == "" || len(fraction) > dockerCPUFractionDigits ||
		strings.HasPrefix(integer, "+") || strings.HasPrefix(integer, "-") {
		return 0, false
	}
	whole, err := strconv.ParseUint(integer, 10, 63)
	if err != nil || whole > uint64(math.MaxInt64/dockerNanoCPUsPerCPU) {
		return 0, false
	}
	if !found {
		fraction = ""
	}
	padded := fraction + strings.Repeat("0", dockerCPUFractionDigits-len(fraction))
	partial, err := strconv.ParseUint(padded, 10, 32)
	if err != nil {
		return 0, false
	}
	result := whole*uint64(dockerNanoCPUsPerCPU) + partial
	if result == 0 || result > uint64(math.MaxInt64) {
		return 0, false
	}

	return int64(result), true
}

func dockerBlkioWeight(value *int) (uint16, bool) {
	if value == nil {
		return 0, true
	}
	if *value < minimumDockerBlkioWeight || *value > maximumDockerBlkioWeight {
		return 0, false
	}

	return uint16(*value), true
}

func dockerRestartPolicy(value string) (containertypes.RestartPolicy, bool) {
	policy := containertypes.RestartPolicy{Name: containertypes.RestartPolicyDisabled}
	if value == "" || value == string(containertypes.RestartPolicyDisabled) {
		return policy, true
	}
	name, retries, found := strings.Cut(value, ":")
	policy.Name = containertypes.RestartPolicyMode(name)
	if found {
		parsed, err := strconv.ParseUint(retries, 10, 31)
		if err != nil || parsed == 0 {
			return containertypes.RestartPolicy{}, false
		}
		policy.MaximumRetryCount = int(parsed)
	}

	return policy, containertypes.ValidateRestartPolicy(policy) == nil
}

//nolint:cyclop // Port validation keeps protocol, address, and uniqueness checks adjacent.
func dockerPorts(
	exposedValues []domain.ExposedPort,
	values []domain.PortBinding,
) (network.PortMap, network.PortSet, bool) {
	if exposedValues == nil && values == nil {
		return nil, nil, true
	}
	bindings := make(network.PortMap, len(values))
	exposed := make(network.PortSet, len(exposedValues)+len(values))
	for _, value := range exposedValues {
		port, ok := network.PortFrom(value.TargetPort, network.IPProtocol(value.Protocol))
		if !ok || value.TargetPort == 0 || !validExposedProtocol(value.Protocol) {
			return nil, nil, false
		}
		exposed[port] = struct{}{}
	}
	for _, value := range values {
		port, ok := network.PortFrom(value.TargetPort, network.IPProtocol(value.Protocol))
		if !ok || value.PublishedPort == 0 || value.TargetPort == 0 ||
			(value.Protocol != dockerProtocolTCP && value.Protocol != dockerProtocolUDP) {
			return nil, nil, false
		}
		var host netip.Addr
		if value.HostIP != "" {
			parsed, err := netip.ParseAddr(value.HostIP)
			if err != nil || parsed.String() != value.HostIP {
				return nil, nil, false
			}
			host = parsed
		}
		if _, found := bindings[port]; found {
			return nil, nil, false
		}
		bindings[port] = []network.PortBinding{{
			HostIP: host, HostPort: strconv.FormatUint(uint64(value.PublishedPort), 10),
		}}
		exposed[port] = struct{}{}
	}

	return bindings, exposed, true
}

func dockerDNS(values []string) ([]netip.Addr, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]netip.Addr, len(values))
	for index, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil || address.String() != value {
			return nil, false
		}
		result[index] = address
	}

	return result, true
}

func dockerTmpfs(values []domain.TmpfsMount) (map[string]string, bool) {
	if values == nil {
		return nil, true
	}
	result := make(map[string]string, len(values))
	for _, value := range values {
		if value.Target == "" || !validDockerText(value.Target) || !validDockerStringSlice(value.Options) {
			return nil, false
		}
		if _, exists := result[value.Target]; exists {
			return nil, false
		}
		result[value.Target] = strings.Join(value.Options, ",")
	}

	return result, true
}

func dockerDevices(values []domain.DeviceMapping) ([]containertypes.DeviceMapping, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]containertypes.DeviceMapping, len(values))
	for index, value := range values {
		if !validDockerText(value.Source) || !validDockerText(value.Target) || value.Permissions == "" ||
			strings.Trim(value.Permissions, "rwm") != "" {
			return nil, false
		}
		result[index] = containertypes.DeviceMapping{
			PathOnHost: value.Source, PathInContainer: value.Target, CgroupPermissions: value.Permissions,
		}
	}

	return result, true
}

//nolint:cyclop // Each ulimit relation is part of the fail-closed wire validation.
func dockerUlimits(values []domain.Ulimit) ([]*containertypes.Ulimit, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]*containertypes.Ulimit, len(values))
	for index, value := range values {
		if value.Name == "" || !validDockerText(value.Name) || value.Soft < -1 || value.Hard < -1 ||
			(value.Soft == -1 && value.Hard != -1) || value.Hard != -1 && value.Soft > value.Hard {
			return nil, false
		}
		result[index] = &containertypes.Ulimit{Name: value.Name, Soft: value.Soft, Hard: value.Hard}
	}

	return result, true
}

//nolint:cyclop // Bind and anonymous-volume variants have distinct rejected fields.
func dockerMounts(values []domain.Mount) (map[string]struct{}, []mount.Mount, bool) {
	if values == nil {
		return nil, nil, true
	}
	volumes := make(map[string]struct{})
	mounts := make([]mount.Mount, 0, len(values))
	for _, value := range values {
		if value.Target == "" || !validDockerText(value.Target) {
			return nil, nil, false
		}
		switch value.Kind {
		case domain.MountBind:
			if value.Source == "" || !validDockerText(value.Source) {
				return nil, nil, false
			}
			mounts = append(mounts, mount.Mount{
				Type: mount.TypeBind, Source: value.Source, Target: value.Target, ReadOnly: value.ReadOnly,
			})
		case domain.MountVolume:
			if value.Source != "" || value.ReadOnly {
				return nil, nil, false
			}
			volumes[value.Target] = struct{}{}
		default:
			return nil, nil, false
		}
	}
	if len(volumes) == 0 {
		volumes = nil
	}

	return volumes, mounts, true
}

func appendNoCopyVolumes(mounts []mount.Mount, volumes map[string]struct{}) []mount.Mount {
	if len(volumes) == 0 {
		return mounts
	}

	targets := make([]string, 0, len(volumes))
	for target := range volumes {
		targets = append(targets, target)
	}
	slices.Sort(targets)
	for _, target := range targets {
		mounts = append(mounts, mount.Mount{
			Type:   mount.TypeVolume,
			Target: target,
			VolumeOptions: &mount.VolumeOptions{
				NoCopy: true,
			},
		})
	}

	return mounts
}

//nolint:cyclop // Healthcheck timing and disable semantics are one wire contract.
func dockerHealthcheck(value *domain.Healthcheck) (*containertypes.HealthConfig, bool) {
	if value == nil {
		return nil, true
	}
	if value.Disabled {
		if len(value.Test) != 0 || value.Interval != "" || value.Timeout != "" || value.Retries != nil ||
			value.StartPeriod != "" || value.StartInterval != "" {
			return nil, false
		}

		return &containertypes.HealthConfig{Test: []string{dockerHealthcheckNone}}, true
	}
	interval, intervalValid := dockerDuration(value.Interval)
	timeout, timeoutValid := dockerDuration(value.Timeout)
	startPeriod, startPeriodValid := dockerDuration(value.StartPeriod)
	startInterval, startIntervalValid := dockerDuration(value.StartInterval)
	if !intervalValid || !timeoutValid || !startPeriodValid || !startIntervalValid ||
		value.Retries != nil && *value.Retries <= 0 || !validProcessArguments(value.Test) {
		return nil, false
	}
	retries := 0
	if value.Retries != nil {
		retries = *value.Retries
	}

	return &containertypes.HealthConfig{
		Test: slices.Clone(value.Test), Interval: interval, Timeout: timeout, Retries: retries,
		StartPeriod: startPeriod, StartInterval: startInterval,
	}, true
}

func dockerDuration(value string) (time.Duration, bool) {
	if value == "" {
		return 0, true
	}
	duration, err := time.ParseDuration(value)

	return duration, err == nil && duration > 0
}

func dockerStopTimeout(value *int64) (*int, bool) {
	if value == nil {
		return nil, true
	}
	if *value <= 0 || *value > int64(math.MaxInt) {
		return nil, false
	}
	result := int(*value)

	return &result, true
}

func optionalInt(value *int) int {
	if value == nil {
		return 0
	}

	return *value
}

func valueOrZero(value *bool) bool {
	return value != nil && *value
}

func cloneDockerPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value

	return &clone
}

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
