package podman

import (
	"encoding/json"
	"io"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	cgroupsDefault     = "default"
	cgroupsDisabled    = "disabled"
	propagationPrivate = "rprivate"
	volumeDriverLocal  = "local"
)

// DecodeInspect validates and decodes one bounded native Libpod inspect JSON
// document. Unknown fields are allowed because Libpod adds observational
// fields independently of this configuration contract; duplicate keys and
// malformed required fields fail closed.
func DecodeInspect(reader io.Reader, maximumBytes int64) (Inspection, error) {
	document, valid := readJSON(reader, maximumBytes)
	if !valid {
		return Inspection{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}
	var fields map[string]json.RawMessage
	var payload inspectDocument
	if json.Unmarshal(document, &fields) != nil || json.Unmarshal(document, &payload) != nil {
		return Inspection{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}
	for _, name := range []string{
		"Id", "Name", "Image", "ImageName", "ImageDigest", "State", "Mounts", "Config", "HostConfig",
	} {
		if _, found := fields[name]; !found {
			return Inspection{}, validationError(containerconfig.ValidationInvalidDocument, "")
		}
	}

	return inspectionFromDocument(payload)
}

//nolint:cyclop // Native inspect core fields form one fail-closed document boundary.
func inspectionFromDocument(payload inspectDocument) (Inspection, error) {
	if !validID(payload.ID) || !validName(payload.Name, containerNameBytes) ||
		payload.Config == nil || payload.HostConfig == nil ||
		payload.State == nil || payload.Image == "" || !validText(payload.Image) ||
		payload.Config.Image == "" || !validText(payload.Config.Image) ||
		payload.ImageName != payload.Config.Image || !validText(payload.ImageDigest) {
		return Inspection{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}
	state, valid := observedState(payload.State)
	if !valid {
		return Inspection{}, validationError(containerconfig.ValidationInvalidValue, "/State")
	}
	spec, runtimeMounts, err := observedSpec(payload.ID, payload.Name, payload)
	if err != nil {
		return Inspection{}, err
	}

	return Inspection{
		ID: payload.ID, Name: payload.Name, ImageID: payload.Image,
		ImageReference: payload.ImageName, ImageDigest: payload.ImageDigest,
		State: state, Spec: spec, RuntimeMounts: runtimeMounts,
		RawLabels: cloneStringMap(payload.Config.Labels),
	}, nil
}

func validID(value string) bool {
	if len(value) != containerIDBytes {
		return false
	}
	for index := range value {
		if value[index] < '0' || value[index] > '9' {
			if value[index] < 'a' || value[index] > 'f' {
				return false
			}
		}
	}

	return true
}

//nolint:cyclop // Native lifecycle flags must agree with each accepted status.
func observedState(state *inspectState) (State, bool) {
	if state == nil || state.Restarting || state.Dead {
		return StateUnknown, false
	}
	switch state.Status {
	case stateCreated, "initialized":
		return StateCreated, !state.Running && !state.Paused
	case stateRunning:
		return StateRunning, state.Running && !state.Paused
	case statePaused:
		return StatePaused, state.Paused
	case stateStopped, "exited":
		return StateExited, !state.Running && !state.Paused
	case stateRemoving, "stopping":
		return StateRemoving, !state.Running && !state.Paused
	case stateUnknown:
		return StateUnknown, !state.Running && !state.Paused
	default:
		return StateUnknown, false
	}
}

//nolint:cyclop,funlen // This function is the native inspect-to-Spec mapping table.
func observedSpec(identifier, name string, payload inspectDocument) (
	containerconfig.Spec,
	[]RuntimeMount,
	error,
) {
	config := payload.Config
	host := payload.HostConfig
	if config == nil || host == nil || host.RestartPolicy == nil || !validInspectScalars(identifier, config, host) {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidDocument, "")
	}
	labels, valid := observedLabels(config.Labels)
	environment, environmentValid := observedEnvironment(config.Environment)
	if !valid || !environmentValid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/Config")
	}
	restart, valid := observedRestart(*host.RestartPolicy)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(
			containerconfig.ValidationInvalidValue, "/HostConfig/RestartPolicy",
		)
	}
	stopSignal, valid := observedStopSignal(config.StopSignal)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/Config/StopSignal")
	}
	cpus, valid := observedCPUs(host.NanoCPUs, host.CPUPeriod, host.CPUQuota)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/HostConfig/CpuQuota")
	}
	blkio, valid := observedBlkio(host.BlkioWeight)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/HostConfig/BlkioWeight")
	}
	pids, valid := observedPids(host.PidsLimit)
	if !valid || !validDNS(host.DNS) {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/HostConfig")
	}
	extraHosts, valid := observedExtraHosts(host.ExtraHosts)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/HostConfig/ExtraHosts")
	}
	tmpfs, valid := observedTmpfs(host.Tmpfs)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/HostConfig/Tmpfs")
	}
	ulimits, valid := observedUlimits(host.Ulimits)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/HostConfig/Ulimits")
	}
	exposed, ports, valid := observedPorts(config.ExposedPorts, host.PortBindings)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(
			containerconfig.ValidationInvalidValue, "/HostConfig/PortBindings",
		)
	}
	mounts, runtimeMounts, valid := observedMounts(payload.Mounts, host.Binds)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/Mounts")
	}
	healthcheck, valid := observedHealthcheck(config.Healthcheck)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/Config/Healthcheck")
	}
	security, valid := observedSecurity(host.SecurityOpt)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(
			containerconfig.ValidationUnsupportedCapability, "/HostConfig/SecurityOpt",
		)
	}
	stopTimeout, valid := observedStopTimeout(config.StopTimeout)
	if !valid {
		return containerconfig.Spec{}, nil, validationError(containerconfig.ValidationInvalidValue, "/Config/StopTimeout")
	}
	spec := containerconfig.Spec{
		ContainerName: name, Entrypoint: slices.Clone(config.Entrypoint), Command: slices.Clone(config.Command),
		NetworkMode: host.NetworkMode, BlkioWeight: blkio, CgroupParent: host.CgroupParent,
		Cgroup: host.CgroupMode, CPUs: cpus, Hostname: normalizedHostname(identifier, config.Hostname),
		MemoryBytes: host.Memory, OOMScoreAdj: optionalInt(host.OOMScoreAdj), PidsLimit: pids,
		Restart: restart, SharedMemoryBytes: host.ShmSize, StopSignal: stopSignal,
		StopTimeout: stopTimeout, User: config.User, WorkingDirectory: config.WorkingDir,
		CapAdd: slices.Clone(host.CapAdd), CapDrop: slices.Clone(host.CapDrop), DNS: slices.Clone(host.DNS),
		DNSOptions: slices.Clone(host.DNSOptions), DNSSearch: slices.Clone(host.DNSSearch),
		ExtraHosts: extraHosts, GroupAdd: slices.Clone(host.GroupAdd), Tmpfs: tmpfs, Ulimits: ulimits,
		Environment: environment, Labels: labels, ExposedPorts: exposed, Ports: ports,
		NoNewPrivileges: security, Mounts: mounts, Healthcheck: healthcheck,
		Init: truePointer(host.Init), StdinOpen: truePointer(config.OpenStdin),
		OOMKillDisable: truePointer(host.OOMKillDisable), ReadOnly: truePointer(host.ReadonlyRootfs),
		TTY: truePointer(config.TTY),
	}
	canonicalDefaults(&spec)
	spec = containerconfig.Canonical(spec)
	canonicalPodmanOrderedCollections(&spec)

	return spec, runtimeMounts, nil
}

//nolint:cyclop // Every accepted native scalar and collection is checked independently.
func validInspectScalars(identifier string, config *inspectConfig, host *inspectHost) bool {
	return host.NetworkMode == networkBridge && host.IPCMode == namespacePrivate &&
		host.PIDMode == namespacePrivate && host.UTSMode == namespacePrivate &&
		(host.CgroupMode == namespacePrivate || host.CgroupMode == cgroupHost) &&
		validCgroupsMode(host) && len(host.Devices) == 0 &&
		validStrings(config.Command) && validStrings(config.Entrypoint) && validStrings(config.Environment) &&
		validText(config.Hostname) && validText(config.User) && validText(config.WorkingDir) &&
		validStrings(host.CapAdd) && validStrings(host.CapDrop) && validStrings(host.DNSOptions) &&
		validStrings(host.DNSSearch) && validStrings(host.GroupAdd) && validText(host.CgroupParent) &&
		host.Memory >= 0 && host.ShmSize >= 0 && host.OOMScoreAdj >= minimumOOMScore &&
		host.OOMScoreAdj <= maximumOOMScore && identifier != ""
}

func validCgroupsMode(host *inspectHost) bool {
	if host.Cgroups == cgroupsDefault {
		return true
	}

	return host.Cgroups == cgroupsDisabled && host.CgroupParent == "" &&
		host.NanoCPUs == 0 && host.CPUPeriod == 0 && host.CPUQuota == 0 &&
		host.Memory == 0 && !host.OOMKillDisable && host.PidsLimit == 0 && host.BlkioWeight == 0
}

func normalizedHostname(identifier, hostname string) string {
	if hostname == identifier || len(identifier) >= 12 && hostname == identifier[:12] {
		return ""
	}

	return hostname
}

func observedLabels(values map[string]string) ([]string, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		if key == "" || !validText(key) || !validText(value) {
			return nil, false
		}
		result = append(result, key+"="+value)
	}

	return result, true
}

func observedEnvironment(values []string) ([]string, bool) {
	seen := make(map[string]struct{}, len(values))
	result := make([]string, 0, len(values))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found || key == "" || !validText(value) {
			return nil, false
		}
		if _, exists := seen[key]; exists {
			return nil, false
		}
		seen[key] = struct{}{}
		if key == podmanEnvironmentKey {
			if selected != podmanEnvironmentValue {
				return nil, false
			}

			continue
		}
		result = append(result, value)
	}

	return result, true
}

func observedRestart(value inspectRestart) (string, bool) {
	if value.MaximumRetryCount > math.MaxInt32 {
		return "", false
	}
	switch value.Name {
	case "", "no":
		return "", value.MaximumRetryCount == 0
	case restartAlways, restartUnlessStopped:
		return value.Name, value.MaximumRetryCount == 0
	case restartOnFailure:
		if value.MaximumRetryCount == 0 {
			return value.Name, true
		}

		return value.Name + ":" + strconv.FormatUint(uint64(value.MaximumRetryCount), 10), true
	default:
		return "", false
	}
}

func observedStopSignal(value string) (string, bool) {
	signal, valid := parseSignal(value)
	if !valid {
		return "", false
	}
	if signal == signalTerminate {
		return "", true
	}

	return signalName(signal), true
}

func observedStopTimeout(value uint) (*int64, bool) {
	if uint64(value) > math.MaxInt64 {
		return nil, false
	}
	result := int64(value)

	return &result, true
}

func observedCPUs(nanoValue int64, period uint64, quota int64) (string, bool) {
	if nanoValue == 0 && period == 0 && quota == 0 {
		return "", true
	}
	if nanoValue <= 0 || period != cpuPeriod || quota <= 0 ||
		quota > math.MaxInt64/(nanoCPUsPerCPU/int64(cpuPeriod)) ||
		nanoValue != quota*(nanoCPUsPerCPU/int64(cpuPeriod)) {
		return "", false
	}

	return cpuString(nanoValue), true
}

func cpuString(value int64) string {
	integer := value / nanoCPUsPerCPU
	fraction := strings.TrimRight(strconv.FormatInt(value%nanoCPUsPerCPU+nanoCPUsPerCPU, 10)[1:], "0")
	if fraction == "" {
		return strconv.FormatInt(integer, 10)
	}

	return strconv.FormatInt(integer, 10) + "." + fraction
}

func observedBlkio(value uint16) (*int, bool) {
	if value == 0 {
		return nil, true
	}
	if value < minimumBlkio || value > maximumBlkio {
		return nil, false
	}
	result := int(value)

	return &result, true
}

func observedPids(value int64) (*int64, bool) {
	if value == 0 {
		return nil, true
	}
	if value < -1 {
		return nil, false
	}
	result := value

	return &result, true
}

func observedExtraHosts(values []string) ([]string, bool) {
	result := make([]string, len(values))
	for index, value := range values {
		name, address, found := strings.Cut(value, ":")
		if !found || name == "" || address == "" || !validText(value) {
			return nil, false
		}
		result[index] = name + "=" + address
	}

	return result, true
}

func observedTmpfs(values map[string]string) ([]containerconfig.TmpfsMount, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]containerconfig.TmpfsMount, 0, len(values))
	for target, options := range values {
		if target == "" || !validText(target) || !validText(options) {
			return nil, false
		}
		mount := containerconfig.TmpfsMount{Target: target}
		if options != "" {
			mount.Options = strings.Split(options, ",")
		}
		result = append(result, mount)
	}

	return result, true
}

//nolint:cyclop // POSIX soft and hard limit invariants are checked together.
func observedUlimits(values []inspectUlimit) ([]containerconfig.Ulimit, bool) {
	result := make([]containerconfig.Ulimit, len(values))
	for index, value := range values {
		name, found := strings.CutPrefix(value.Name, "RLIMIT_")
		if !found || name == "" || !validText(name) || value.Soft < -1 || value.Hard < -1 ||
			(value.Soft == -1 && value.Hard != -1) || value.Hard != -1 && value.Soft > value.Hard {
			return nil, false
		}
		result[index] = containerconfig.Ulimit{Name: strings.ToLower(name), Soft: value.Soft, Hard: value.Hard}
	}

	return result, true
}

//nolint:cyclop // Each published and exposed port field participates in the identity.
func observedPorts(exposed map[string]any, bindings map[string][]inspectPortBinding) (
	[]containerconfig.ExposedPort,
	[]containerconfig.PortBinding,
	bool,
) {
	exposedResult := make([]containerconfig.ExposedPort, 0, len(exposed))
	for value := range exposed {
		port, protocol, valid := portKey(value)
		if !valid || !validExposedProtocol(protocol) {
			return nil, nil, false
		}
		exposedResult = append(exposedResult, containerconfig.ExposedPort{TargetPort: port, Protocol: protocol})
	}
	ports := make([]containerconfig.PortBinding, 0, len(bindings))
	for value, entries := range bindings {
		port, protocol, valid := portKey(value)
		if !valid || len(entries) != 1 || protocol != protocolTCP && protocol != protocolUDP {
			return nil, nil, false
		}
		published, err := strconv.ParseUint(entries[0].HostPort, 10, 16)
		if err != nil || published == 0 {
			return nil, nil, false
		}
		if entries[0].HostIP != "" {
			address, addressErr := netip.ParseAddr(entries[0].HostIP)
			if addressErr != nil || address.String() != entries[0].HostIP {
				return nil, nil, false
			}
		}
		ports = append(ports, containerconfig.PortBinding{
			HostIP: entries[0].HostIP, PublishedPort: uint16(published),
			TargetPort: port, Protocol: protocol,
		})
	}

	return exposedResult, ports, true
}

func portKey(value string) (uint16, string, bool) {
	portValue, protocol, found := strings.Cut(value, "/")
	parsed, err := strconv.ParseUint(portValue, 10, 16)

	return uint16(parsed), protocol, found && err == nil && parsed > 0
}
