// Package docker translates portable container configuration to and from the
// Docker Engine API container create and inspect contracts.
package docker

import (
	"maps"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"
	"github.com/moby/moby/api/types/network"

	"github.com/IceCodeNew/maniud/containerconfig"
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
	dockerNetworkMode              = "bridge"
	dockerOperatingSystem          = "linux"
	dockerArchitectureAMD64        = "amd64"
	dockerArchitectureARM64        = "arm64"
	dockerARM64Variant             = "v8"
	maximumDockerContainerName     = 63
	maximumDockerServiceName       = 128
	minimumDockerBlkioWeight       = 10
	maximumDockerBlkioWeight       = 1000
	minimumDockerOOMScore          = -1000
	maximumDockerOOMScore          = 1000
)

// CreateOptions contains Docker create inputs that are intentionally absent
// from the runtime-neutral Spec.
type CreateOptions struct {
	ImageReference   string
	CopyImageVolumes bool
}

// Encode validates spec and returns one native Docker Engine create request.
func Encode(spec containerconfig.Spec, options CreateOptions) (containertypes.CreateRequest, error) {
	var empty containertypes.CreateRequest
	labels, valid := dockerLabels(spec.Labels)
	if !valid {
		return empty, validationError("/labels")
	}
	if options.ImageReference == "" || !validDockerText(options.ImageReference) {
		return empty, validationError("/image_reference")
	}
	config, host, valid := dockerConfiguration(spec, options.ImageReference, labels)
	if !valid {
		return empty, validationError("")
	}
	if !options.CopyImageVolumes {
		host.Mounts = appendNoCopyVolumes(host.Mounts, config.Volumes)
		config.Volumes = nil
	}

	return containertypes.CreateRequest{Config: config, HostConfig: host}, nil
}

// Validate reports whether spec and options can be represented without a
// lossy Docker Engine fallback.
func Validate(spec containerconfig.Spec, options CreateOptions) error {
	_, err := Encode(spec, options)

	return err
}

//nolint:cyclop // This function is the single WorkloadSpec-to-API mapping table.
func dockerConfiguration(
	spec containerconfig.Spec,
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
		//nolint:exhaustruct // Only supported resources are populated.
		Memory: spec.MemoryBytes, NanoCPUs: nanoCPUs, CgroupParent: spec.CgroupParent,
		BlkioWeight: blkioWeight, Devices: devices,
		OomKillDisable: cloneDockerPointer(spec.OOMKillDisable), PidsLimit: cloneDockerPointer(spec.PidsLimit),
		Ulimits: ulimits,
		Mounts:  mounts, Init: cloneDockerPointer(spec.Init),
	}, true
}

//nolint:cyclop // Validation mirrors every field accepted by dockerConfiguration.
func validDockerWorkloadSpec(spec containerconfig.Spec) bool {
	return validServiceName(spec.ServiceName) && validDockerName(spec.ContainerName, maximumDockerContainerName) &&
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

func validDockerPlatform(platform containerconfig.Platform) bool {
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

func dockerLabels(values []string) (map[string]string, bool) {
	labels := make(map[string]string, len(values))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found {
			selected = ""
		}
		if key == "" || !validDockerText(key) || !validDockerText(selected) {
			return nil, false
		}
		if _, exists := labels[key]; exists {
			return nil, false
		}
		labels[key] = selected
	}

	return labels, true
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
	exposedValues []containerconfig.ExposedPort,
	values []containerconfig.PortBinding,
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

func dockerTmpfs(values []containerconfig.TmpfsMount) (map[string]string, bool) {
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

func dockerDevices(values []containerconfig.DeviceMapping) ([]containertypes.DeviceMapping, bool) {
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
func dockerUlimits(values []containerconfig.Ulimit) ([]*containertypes.Ulimit, bool) {
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
func dockerMounts(values []containerconfig.Mount) (map[string]struct{}, []mount.Mount, bool) {
	if values == nil {
		return nil, nil, true
	}
	volumes := make(map[string]struct{})
	mounts := make([]mount.Mount, 0, len(values))
	targets := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.Target == "" || !validDockerText(value.Target) {
			return nil, nil, false
		}
		if _, exists := targets[value.Target]; exists {
			return nil, nil, false
		}
		targets[value.Target] = struct{}{}
		switch value.Kind {
		case containerconfig.MountBind:
			if value.Source == "" || !validDockerText(value.Source) {
				return nil, nil, false
			}
			mounts = append(mounts, mount.Mount{
				Type: mount.TypeBind, Source: value.Source, Target: value.Target, ReadOnly: value.ReadOnly,
			})
		case containerconfig.MountVolume:
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
func dockerHealthcheck(value *containerconfig.Healthcheck) (*containertypes.HealthConfig, bool) {
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

func validationError(path string) error {
	return containerconfig.ValidationError{Code: containerconfig.ValidationInvalidValue, Path: path}
}

func validDockerName(value string, maximum int) bool {
	if len(value) == 0 || len(value) > maximum || !lowerAlphaNumeric(value[0]) ||
		!lowerAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for index := 1; index < len(value)-1; index++ {
		if value[index] != '-' && !lowerAlphaNumeric(value[index]) {
			return false
		}
	}

	return true
}

func lowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
}

func validServiceName(value string) bool {
	if len(value) == 0 || len(value) > maximumDockerServiceName || !alphaNumeric(value[0]) {
		return false
	}
	for index := range value {
		if !alphaNumeric(value[index]) && value[index] != '.' && value[index] != '_' && value[index] != '-' {
			return false
		}
	}

	return true
}

func alphaNumeric(value byte) bool {
	return lowerAlphaNumeric(value) || value >= 'A' && value <= 'Z'
}

func validProcessArguments(arguments []string) bool {
	for _, argument := range arguments {
		if !utf8.ValidString(argument) || strings.IndexByte(argument, 0) >= 0 {
			return false
		}
	}

	return true
}
