package containerd

import (
	"maps"
	"math"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"
	"unicode/utf8"

	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	maximumTextBytes = 4096
	minimumBlkio     = 10
	maximumBlkio     = 1000
	minimumOOMScore  = -1000
	maximumOOMScore  = 1000
	cpuPrecision     = 9
	cpuScale         = 1_000_000_000
)

func canonicalSpec(spec containerconfig.Spec) (containerconfig.Spec, error) {
	canonical := spec.Clone()
	checks := []struct {
		path  string
		valid bool
	}{
		{pathServiceName, validName(canonical.ServiceName)},
		{"/container_name", validName(canonical.ContainerName)},
		{pathPlatform, validPlatform(canonical.Platform)},
		{"/network_mode", canonical.NetworkMode == networkBridge},
		{"/entrypoint", validArguments(canonical.Entrypoint)},
		{"/command", validArguments(canonical.Command)},
		{"/process", len(canonical.Entrypoint)+len(canonical.Command) != 0},
		{"/hostname", validText(canonical.Hostname)},
		{"/working_directory", validWorkingDirectory(canonical.WorkingDirectory)},
		{"/stop_signal", validText(canonical.StopSignal)},
		{pathUser, validUser(canonical.User)},
		{"/memory_bytes", canonical.MemoryBytes >= 0},
		{"/shared_memory_bytes", canonical.SharedMemoryBytes >= 0},
		{"/blkio_weight", validOptionalRange(canonical.BlkioWeight, minimumBlkio, maximumBlkio)},
		{"/oom_score_adj", validOptionalRange(canonical.OOMScoreAdj, minimumOOMScore, maximumOOMScore)},
		{"/pids_limit", validPids(canonical.PidsLimit)},
		{"/cgroup", canonical.Cgroup == "" || canonical.Cgroup == cgroupPrivate},
		{"/cgroup_parent", validText(canonical.CgroupParent)},
		{pathRestart, validRestart(canonical.Restart)},
		{"/stop_timeout", validStopTimeout(canonical.StopTimeout)},
		{pathCapabilities, validCapabilities(canonical.CapAdd, canonical.CapDrop)},
		{"/dns_options", validTextValues(canonical.DNSOptions)},
		{"/dns_search", validTextValues(canonical.DNSSearch)},
		{"/extra_hosts", validTextValues(canonical.ExtraHosts)},
		{"/group_add", validNumericGroups(canonical.GroupAdd)},
		{pathEnvironment, validEnvironment(canonical.Environment)},
		{pathLabels, validLabels(canonical.Labels)},
		{"/sysctls", validStringMap(canonical.Sysctls)},
		{pathDNS, validDNS(canonical.DNS)},
		{pathDevices, validDevices(canonical.Devices)},
		{pathTmpfs, validTmpfs(canonical.Tmpfs)},
		{pathUlimits, validUlimits(canonical.Ulimits)},
		{pathPorts, validPorts(canonical.ExposedPorts, canonical.Ports)},
		{pathMounts, validMounts(canonical.Mounts)},
		{pathMounts, validTargets(canonical)},
		{pathHealthcheck, validHealthcheck(canonical.Healthcheck)},
	}
	for _, check := range checks {
		if !check.valid {
			return containerconfig.Spec{}, validationError(containerconfig.ValidationInvalidValue, check.path)
		}
	}
	if canonical.Init != nil && *canonical.Init {
		return containerconfig.Spec{}, validationError(containerconfig.ValidationUnsupportedCapability, "/init")
	}
	if canonical.Healthcheck != nil && !canonical.Healthcheck.Disabled {
		return containerconfig.Spec{}, validationError(containerconfig.ValidationUnsupportedCapability, pathHealthcheck)
	}
	if _, valid := cpuQuota(canonical.CPUs); !valid {
		return containerconfig.Spec{}, validationError(containerconfig.ValidationInvalidValue, "/cpus")
	}
	canonical = containerconfig.Canonical(canonical)

	return canonical, nil
}

func validPlatform(value containerconfig.Platform) bool {
	return value.OS == operatingSystemLinux &&
		(value.Architecture == "amd64" && value.Variant == "" ||
			value.Architecture == "arm64" && value.Variant == "v8")
}

func validWorkingDirectory(value string) bool {
	return validText(value) && (value == "" || validAbsolutePath(value))
}

func validationError(code containerconfig.ValidationCode, field string) error {
	return containerconfig.ValidationError{Code: code, Path: field}
}

func validName(value string) bool {
	if value == "" || !validText(value) || !asciiAlphaNumeric(value[0]) || !asciiAlphaNumeric(value[len(value)-1]) {
		return false
	}
	for index := 1; index+1 < len(value); index++ {
		current := value[index]
		if !asciiAlphaNumeric(current) && current != '.' && current != '-' && current != '_' {
			return false
		}
	}

	return true
}

func asciiAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= 'A' && value <= 'Z' || value >= '0' && value <= '9'
}

func validText(value string) bool {
	return len(value) <= maximumTextBytes && utf8.ValidString(value) && !strings.ContainsRune(value, 0)
}

func validArguments(values []string) bool {
	return !slices.ContainsFunc(values, func(value string) bool { return !validText(value) })
}

func validTextValues(values []string) bool {
	return !slices.ContainsFunc(values, func(value string) bool { return value == "" || !validText(value) })
}

func validAbsolutePath(value string) bool {
	return path.IsAbs(value) && path.Clean(value) == value
}

func validUser(value string) bool {
	_, valid := parsedUser(value)

	return valid
}

func parsedUser(value string) (specs.User, bool) {
	if value == "" {
		return specs.User{}, true
	}
	uidValue, gidValue, found := strings.Cut(value, ":")
	uid, err := strconv.ParseUint(uidValue, 10, 32)
	if err != nil {
		return specs.User{}, false
	}
	gid := uid
	if found {
		gid, err = strconv.ParseUint(gidValue, 10, 32)
		if err != nil {
			return specs.User{}, false
		}
	}

	return specs.User{UID: uint32(uid), GID: uint32(gid)}, true
}

func validOptionalRange[T ~int](value *T, minimum, maximum T) bool {
	return value == nil || *value >= minimum && *value <= maximum
}

func validPids(value *int64) bool {
	return value == nil || *value == -1 || *value > 0
}

func validRestart(value string) bool {
	if value == "" || value == "no" || value == "always" || value == "unless-stopped" || value == "on-failure" {
		return true
	}
	retries, found := strings.CutPrefix(value, "on-failure:")
	parsed, err := strconv.ParseUint(retries, 10, 31)

	return found && err == nil && parsed > 0
}

func validStopTimeout(value *int64) bool {
	return value == nil || *value > 0
}

func validCapabilities(add, drop []string) bool {
	allowlist := supportedCapabilities()
	for index, values := range [][]string{add, drop} {
		seen := make(map[string]struct{}, len(values))
		for _, value := range values {
			name := capabilityName(value)
			_, supported := allowlist[name]
			if index == 1 && name == allCapabilities {
				supported = true
			}
			if value == "" || !validText(value) || !supported {
				return false
			}
			if _, duplicate := seen[name]; duplicate {
				return false
			}
			seen[name] = struct{}{}
		}
	}

	return true
}

func supportedCapabilities() map[string]struct{} {
	return map[string]struct{}{
		"CAP_AUDIT_CONTROL": {}, "CAP_AUDIT_READ": {}, "CAP_AUDIT_WRITE": {},
		"CAP_BLOCK_SUSPEND": {}, "CAP_BPF": {}, "CAP_CHECKPOINT_RESTORE": {},
		"CAP_CHOWN": {}, "CAP_DAC_OVERRIDE": {}, "CAP_DAC_READ_SEARCH": {},
		"CAP_FOWNER": {}, "CAP_FSETID": {}, "CAP_IPC_LOCK": {}, "CAP_IPC_OWNER": {},
		"CAP_KILL": {}, "CAP_LEASE": {}, "CAP_LINUX_IMMUTABLE": {}, "CAP_MAC_ADMIN": {},
		"CAP_MAC_OVERRIDE": {}, "CAP_MKNOD": {}, "CAP_NET_ADMIN": {},
		"CAP_NET_BIND_SERVICE": {}, "CAP_NET_BROADCAST": {}, capabilityNetRaw: {},
		"CAP_PERFMON": {}, "CAP_SETFCAP": {}, "CAP_SETGID": {}, "CAP_SETPCAP": {},
		"CAP_SETUID": {}, "CAP_SYS_ADMIN": {}, "CAP_SYS_BOOT": {}, "CAP_SYS_CHROOT": {},
		"CAP_SYS_MODULE": {}, "CAP_SYS_NICE": {}, "CAP_SYS_PACCT": {},
		"CAP_SYS_PTRACE": {}, "CAP_SYS_RAWIO": {}, "CAP_SYS_RESOURCE": {},
		"CAP_SYS_TIME": {}, "CAP_SYS_TTY_CONFIG": {}, "CAP_SYSLOG": {}, "CAP_WAKE_ALARM": {},
	}
}

func validNumericGroups(values []string) bool {
	for _, value := range values {
		if _, err := strconv.ParseUint(value, 10, 32); err != nil {
			return false
		}
	}

	return true
}

func validEnvironment(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name, _, found := strings.Cut(value, "=")
		if !found || name == "" || !validText(value) {
			return false
		}
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
	}

	return true
}

func validLabels(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		name, _, _ := strings.Cut(value, "=")
		if name == "" || !validText(value) {
			return false
		}
		if _, duplicate := seen[name]; duplicate {
			return false
		}
		seen[name] = struct{}{}
	}

	return true
}

func validStringMap(values map[string]string) bool {
	for key, value := range values {
		if key == "" || !validText(key) || !validText(value) {
			return false
		}
	}

	return true
}

func validDNS(values []string) bool {
	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil || address.String() != value {
			return false
		}
	}

	return true
}

func validDevices(values []containerconfig.DeviceMapping) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validAbsolutePath(value.Source) || !validAbsolutePath(value.Target) ||
			!validDevicePermissions(value.Permissions) {
			return false
		}
		if _, duplicate := seen[value.Target]; duplicate {
			return false
		}
		seen[value.Target] = struct{}{}
	}

	return true
}

func validDevicePermissions(value string) bool {
	if value == "" {
		return false
	}
	seen := make(map[rune]struct{}, len(value))
	for _, permission := range value {
		if !strings.ContainsRune("rwm", permission) {
			return false
		}
		if _, duplicate := seen[permission]; duplicate {
			return false
		}
		seen[permission] = struct{}{}
	}

	return true
}

func validTmpfs(values []containerconfig.TmpfsMount) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validAbsolutePath(value.Target) || !validTextValues(value.Options) {
			return false
		}
		if _, duplicate := seen[value.Target]; duplicate {
			return false
		}
		seen[value.Target] = struct{}{}
	}

	return true
}

//nolint:cyclop // Each branch is one independent ulimit relation or uniqueness boundary.
func validUlimits(values []containerconfig.Ulimit) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if value.Name == "" || !validText(value.Name) || value.Soft < -1 || value.Hard < -1 ||
			value.Soft == -1 && value.Hard != -1 || value.Hard != -1 && value.Soft > value.Hard {
			return false
		}
		if _, duplicate := seen[value.Name]; duplicate {
			return false
		}
		seen[value.Name] = struct{}{}
	}

	return true
}

//nolint:cyclop // Exposed and published ports have distinct protocol and address constraints.
func validPorts(exposed []containerconfig.ExposedPort, ports []containerconfig.PortBinding) bool {
	seen := make(map[string]struct{}, len(exposed)+len(ports))
	for _, value := range exposed {
		if value.TargetPort == 0 || !validProtocol(value.Protocol, true) ||
			!uniquePort(seen, value.TargetPort, value.Protocol) {
			return false
		}
	}
	for _, value := range ports {
		if value.PublishedPort == 0 || value.TargetPort == 0 || !validProtocol(value.Protocol, false) ||
			!validHostIP(value.HostIP) || !uniquePort(seen, value.TargetPort, value.Protocol) {
			return false
		}
	}

	return true
}

func validProtocol(value string, exposed bool) bool {
	return value == protocolTCP || value == protocolUDP || exposed && value == protocolSCTP
}

func validHostIP(value string) bool {
	if value == "" {
		return true
	}
	address, err := netip.ParseAddr(value)

	return err == nil && address.String() == value
}

func uniquePort(seen map[string]struct{}, target uint16, protocol string) bool {
	key := protocol + "/" + strconv.FormatUint(uint64(target), 10)
	if _, duplicate := seen[key]; duplicate {
		return false
	}
	seen[key] = struct{}{}

	return true
}

func validMounts(values []containerconfig.Mount) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		if !validAbsolutePath(value.Target) {
			return false
		}
		if _, duplicate := seen[value.Target]; duplicate {
			return false
		}
		seen[value.Target] = struct{}{}
		switch value.Kind {
		case containerconfig.MountBind:
			if !validAbsolutePath(value.Source) {
				return false
			}
		case containerconfig.MountVolume:
			if value.Source != "" || value.ReadOnly {
				return false
			}
		default:
			return false
		}
	}

	return true
}

func validTargets(spec containerconfig.Spec) bool {
	seen := make(map[string]struct{}, len(spec.Devices)+len(spec.Tmpfs)+len(spec.Mounts))
	for _, target := range targetPaths(spec) {
		if _, duplicate := seen[target]; duplicate {
			return false
		}
		seen[target] = struct{}{}
	}
	_, sharedMemoryOverridden := seen[sharedMemoryMountPoint]

	return spec.SharedMemoryBytes == 0 || !sharedMemoryOverridden
}

func targetPaths(spec containerconfig.Spec) []string {
	result := make([]string, 0, len(spec.Devices)+len(spec.Tmpfs)+len(spec.Mounts))
	for _, value := range spec.Devices {
		result = append(result, value.Target)
	}
	for _, value := range spec.Tmpfs {
		result = append(result, value.Target)
	}
	for _, value := range spec.Mounts {
		result = append(result, value.Target)
	}

	return result
}

//nolint:cyclop // Disabled and active healthchecks have disjoint portable contracts.
func validHealthcheck(value *containerconfig.Healthcheck) bool {
	if value == nil {
		return true
	}
	if value.Disabled {
		return len(value.Test) == 0 && value.Interval == "" && value.Timeout == "" && value.Retries == nil &&
			value.StartPeriod == "" && value.StartInterval == ""
	}

	return validArguments(value.Test) &&
		(len(value.Test) == 0 || value.Test[0] == healthCommandExec || value.Test[0] == "CMD-SHELL") &&
		validDuration(value.Interval) && validDuration(value.Timeout) && validDuration(value.StartPeriod) &&
		validDuration(value.StartInterval) && (value.Retries == nil || *value.Retries > 0)
}

func validDuration(value string) bool {
	if value == "" {
		return true
	}
	parsed, err := time.ParseDuration(value)

	return err == nil && parsed > 0
}

//nolint:cyclop // Decimal CPU syntax is canonicalized without a binary float.
func cpuQuota(value string) (int64, bool) {
	if value == "" {
		return 0, true
	}
	integer, fraction, found := strings.Cut(value, ".")
	if integer == "" || strings.HasPrefix(integer, "+") || strings.HasPrefix(integer, "-") ||
		len(fraction) > cpuPrecision {
		return 0, false
	}
	whole, err := strconv.ParseUint(integer, 10, 63)
	if err != nil || whole > math.MaxInt64/cpuPeriod {
		return 0, false
	}
	if !found {
		fraction = ""
	}
	padded := fraction + strings.Repeat("0", cpuPrecision-len(fraction))
	partial, err := strconv.ParseUint(padded, 10, 32)
	if err != nil {
		return 0, false
	}
	quota := whole*cpuPeriod + partial*cpuPeriod/cpuScale
	if quota == 0 || quota > math.MaxInt64 {
		return 0, false
	}

	return int64(quota), true
}

func cloneMap(values map[string]string) map[string]string {
	return maps.Clone(values)
}
