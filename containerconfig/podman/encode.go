package podman

import (
	"encoding/json"
	"math"
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	cpuPeriod         = uint64(100_000)
	nanoCPUsPerCPU    = int64(1_000_000_000)
	cpuFractionDigits = 9
	protocolTCP       = "tcp"
	protocolUDP       = "udp"
	protocolSCTP      = "sctp"
	healthcheckNone   = "NONE"
	minimumBlkio      = 10
	maximumBlkio      = 1000
	minimumOOMScore   = -1000
	maximumOOMScore   = 1000
	signalTerminate   = 15
	mountBind         = "bind"
	mountVolume       = "volume"
	recursiveBind     = "rbind"
)

// Encode validates spec and returns one native Libpod create document.
func Encode(spec containerconfig.Spec, options CreateOptions) ([]byte, error) {
	canonical, err := canonicalSpec(spec)
	if err != nil {
		return nil, err
	}
	if options.ImageReference == "" || !validText(options.ImageReference) {
		return nil, validationError(containerconfig.ValidationInvalidValue, "/image_reference")
	}
	document, err := createConfiguration(canonical, options)
	if err != nil {
		return nil, err
	}
	encoded, _ := json.Marshal(document) //nolint:errchkjson // The private DTO has no unsupported JSON values.

	return encoded, nil
}

// Validate reports whether spec and options can be represented without a
// lossy Libpod fallback.
func Validate(spec containerconfig.Spec, options CreateOptions) error {
	_, err := Encode(spec, options)

	return err
}

// Canonical validates spec and returns the form produced by a create and
// inspect round trip through Libpod.
func Canonical(spec containerconfig.Spec) (containerconfig.Spec, error) {
	return canonicalSpec(spec)
}

//nolint:cyclop,funlen // Every supported portable field has one native mapping.
func createConfiguration(spec containerconfig.Spec, options CreateOptions) (createDocument, error) {
	labels, valid := createLabels(spec.Labels)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/labels")
	}
	environment, valid := createEnvironment(spec.Environment)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/environment")
	}
	restart, tries, valid := createRestart(spec.Restart)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/restart")
	}
	stopSignal, valid := createStopSignal(spec.StopSignal)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/stop_signal")
	}
	stopTimeout, valid := createStopTimeout(spec.StopTimeout)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/stop_timeout")
	}
	cpus, valid := createCPUs(spec.CPUs)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/cpus")
	}
	blkio, valid := createBlkio(spec.BlkioWeight)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/blkio_weight")
	}
	pids, valid := createPidsLimit(spec.PidsLimit)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/pids_limit")
	}
	ports, exposed, valid := createPorts(spec.ExposedPorts, spec.Ports)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/ports")
	}
	mounts, volumes, valid := createMounts(spec.Mounts, spec.Tmpfs, options.CopyImageVolumes)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/mounts")
	}
	ulimits, valid := createUlimits(spec.Ulimits)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/ulimits")
	}
	healthcheck, valid := createHealthcheck(spec.Healthcheck)
	if !valid {
		return createDocument{}, validationError(containerconfig.ValidationInvalidValue, "/healthcheck")
	}
	sharedMemory := spec.SharedMemoryBytes
	if sharedMemory == 0 {
		sharedMemory = defaultSharedMemoryBytes
	}
	cgroupMode := spec.Cgroup
	if cgroupMode == "" {
		cgroupMode = namespacePrivate
	}
	extraHosts := make([]string, len(spec.ExtraHosts))
	for index, value := range spec.ExtraHosts {
		extraHosts[index] = strings.Replace(value, "=", ":", 1)
	}
	resources := createResources{CPU: cpus, BlockIO: blkio}
	if pids != nil {
		resources.Pids = &createPids{Limit: pids}
	}
	if spec.MemoryBytes != 0 || spec.OOMKillDisable != nil {
		resources.Memory = &createMemory{
			Limit: nonzeroInt64(spec.MemoryBytes), DisableOOMKiller: cloneValue(spec.OOMKillDisable),
		}
	}
	publishExposed := false

	return createDocument{
		Image: options.ImageReference, RawImageName: options.ImageReference,
		Command: slices.Clone(spec.Command), Entrypoint: slices.Clone(spec.Entrypoint),
		Name: spec.ContainerName, ImageOS: spec.Platform.OS,
		ImageArchitecture: spec.Platform.Architecture, ImageVariant: spec.Platform.Variant,
		Labels: labels, Environment: environment, WorkingDirectory: spec.WorkingDirectory,
		Hostname: spec.Hostname, User: spec.User, Stdin: cloneValue(spec.StdinOpen),
		Terminal: cloneValue(spec.TTY), Init: cloneValue(spec.Init),
		ReadOnlyFilesystem: cloneValue(spec.ReadOnly), StopSignal: stopSignal, StopTimeout: stopTimeout,
		RestartPolicy: restart, RestartTries: tries,
		NetworkNamespace: namespace{Mode: networkBridge},
		IPCNamespace:     namespace{Mode: namespacePrivate},
		PIDNamespace:     namespace{Mode: namespacePrivate},
		UTSNamespace:     namespace{Mode: namespacePrivate},
		CgroupNamespace:  namespace{Mode: cgroupMode}, CgroupParent: spec.CgroupParent,
		DNS: slices.Clone(spec.DNS), DNSSearch: slices.Clone(spec.DNSSearch),
		DNSOptions: slices.Clone(spec.DNSOptions), ExtraHosts: extraHosts,
		Groups: slices.Clone(spec.GroupAdd), CapAdd: slices.Clone(spec.CapAdd),
		CapDrop: slices.Clone(spec.CapDrop), NoNewPrivileges: boolPointer(spec.NoNewPrivileges),
		OOMScoreAdj: cloneValue(spec.OOMScoreAdj), SharedMemoryBytes: &sharedMemory,
		ResourceLimits: resources, PortMappings: ports, PublishExposedPorts: &publishExposed,
		Expose: exposed, Mounts: mounts, Volumes: volumes, Ulimits: ulimits, Healthcheck: healthcheck,
	}, nil
}

func canonicalSpec(spec containerconfig.Spec) (containerconfig.Spec, error) {
	canonical := spec.Clone()
	checks := []struct {
		path  string
		valid bool
	}{
		{"/service_name", validName(canonical.ServiceName, serviceNameBytes)},
		{"/container_name", validName(canonical.ContainerName, containerNameBytes)},
		{"/platform", validPlatform(canonical.Platform)},
		{"/network_mode", canonical.NetworkMode == networkBridge},
		{"/entrypoint", validStrings(canonical.Entrypoint)},
		{"/command", validStrings(canonical.Command)},
		{"/process", len(canonical.Entrypoint)+len(canonical.Command) != 0},
		{"/hostname", validText(canonical.Hostname)},
		{"/user", validText(canonical.User)},
		{"/working_directory", validText(canonical.WorkingDirectory)},
		{"/memory_bytes", canonical.MemoryBytes >= 0},
		{"/shared_memory_bytes", canonical.SharedMemoryBytes >= 0},
		{"/oom_score_adj", validOptionalRange(canonical.OOMScoreAdj, minimumOOMScore, maximumOOMScore)},
		{"/cgroup", canonical.Cgroup == "" || canonical.Cgroup == namespacePrivate || canonical.Cgroup == cgroupHost},
		{"/cgroup_parent", validText(canonical.CgroupParent)},
		{"/cap_add", validStrings(canonical.CapAdd)},
		{"/cap_drop", validStrings(canonical.CapDrop)},
		{"/dns_options", validStrings(canonical.DNSOptions)},
		{"/dns_search", validStrings(canonical.DNSSearch)},
		{"/group_add", validStrings(canonical.GroupAdd)},
		{"/dns", validDNS(canonical.DNS)},
		{"/extra_hosts", validExtraHosts(canonical.ExtraHosts)},
		{"/devices", len(canonical.Devices) == 0},
		{"/sysctls", len(canonical.Sysctls) == 0},
	}
	for _, check := range checks {
		if !check.valid {
			code := containerconfig.ValidationInvalidValue
			if check.path == "/devices" || check.path == "/sysctls" {
				code = containerconfig.ValidationUnsupportedCapability
			}

			return containerconfig.Spec{}, validationError(code, check.path)
		}
	}
	canonicalDefaults(&canonical)
	canonical = containerconfig.Canonical(canonical)
	canonicalPodmanOrderedCollections(&canonical)

	return canonical, nil
}

func validPlatform(value containerconfig.Platform) bool {
	return value.OS == osLinux &&
		(value.Architecture == architectureAMD64 && value.Variant == "" ||
			value.Architecture == architectureARM64 && value.Variant == variantV8)
}

func validName(value string, maximum int) bool {
	if value == "" || len(value) > maximum || !asciiAlphaNumeric(value[0]) || !validText(value) {
		return false
	}
	for index := 1; index < len(value); index++ {
		if !asciiAlphaNumeric(value[index]) && value[index] != '.' && value[index] != '_' && value[index] != '-' {
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

func validStrings(values []string) bool {
	return !slices.ContainsFunc(values, func(value string) bool { return !validText(value) })
}

func validOptionalRange(value *int, minimum, maximum int) bool {
	return value == nil || *value >= minimum && *value <= maximum
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

func validExtraHosts(values []string) bool {
	for _, value := range values {
		name, address, found := strings.Cut(value, "=")
		if !found || name == "" || address == "" || !validText(value) {
			return false
		}
	}

	return true
}

func createLabels(values []string) (map[string]string, bool) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found {
			selected = ""
		}
		if key == "" || !validText(key) || !validText(selected) {
			return nil, false
		}
		if _, exists := result[key]; exists {
			return nil, false
		}
		result[key] = selected
	}

	return result, true
}

func createEnvironment(values []string) (map[string]string, bool) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found || key == "" || key == podmanEnvironmentKey || !validText(value) {
			return nil, false
		}
		if _, exists := result[key]; exists {
			return nil, false
		}
		result[key] = selected
	}

	return result, true
}

func createRestart(value string) (string, *uint, bool) {
	if value == "" || value == "no" {
		return "no", nil, true
	}
	name, retries, found := strings.Cut(value, ":")
	switch name {
	case restartAlways, restartUnlessStopped:
		return name, nil, !found
	case restartOnFailure:
		if !found {
			return name, nil, true
		}
		parsed, err := strconv.ParseUint(retries, 10, 32)
		if err != nil || parsed == 0 {
			return "", nil, false
		}
		result := uint(parsed)

		return name, &result, true
	default:
		return "", nil, false
	}
}

func createStopSignal(value string) (*int, bool) {
	if value == "" {
		return nil, true
	}
	signal, valid := parseSignal(value)
	if !valid {
		return nil, false
	}

	return &signal, true
}

func parseSignal(value string) (int, bool) {
	if parsed, err := strconv.ParseUint(value, 10, 8); err == nil {
		return int(parsed), parsed > 0 && parsed <= 64
	}
	upper := strings.ToUpper(value)
	if !strings.HasPrefix(upper, "SIG") {
		upper = "SIG" + upper
	}
	for signal := 1; signal <= 31; signal++ {
		if signalName(signal) == upper {
			return signal, true
		}
	}

	return 0, false
}

func signalName(signal int) string {
	names := [...]string{
		"", "SIGHUP", signalInterruptName, "SIGQUIT", "SIGILL", "SIGTRAP", "SIGABRT", "SIGBUS", "SIGFPE",
		"SIGKILL", "SIGUSR1", "SIGSEGV", "SIGUSR2", "SIGPIPE", "SIGALRM", signalTerminateName, "SIGSTKFLT",
		"SIGCHLD", "SIGCONT", "SIGSTOP", "SIGTSTP", "SIGTTIN", "SIGTTOU", "SIGURG", "SIGXCPU",
		"SIGXFSZ", "SIGVTALRM", "SIGPROF", "SIGWINCH", "SIGIO", "SIGPWR", "SIGSYS",
	}
	if signal <= 0 || signal >= len(names) {
		return strconv.Itoa(signal)
	}

	return names[signal] //nolint:gosec // The preceding bounds check proves the fixed array index.
}

func createStopTimeout(value *int64) (*uint, bool) {
	selected := defaultStopTimeout
	if value != nil {
		selected = *value
	}
	if selected <= 0 || uint64(selected) > uint64(^uint(0)) {
		return nil, false
	}
	result := uint(selected)

	return &result, true
}

func createCPUs(value string) (*createCPU, bool) {
	nanoCPUs, valid := nanoCPUs(value)
	if !valid {
		return nil, false
	}
	if nanoCPUs == 0 {
		return nil, true
	}
	quota := nanoCPUs / (nanoCPUsPerCPU / int64(cpuPeriod))
	period := cpuPeriod

	return &createCPU{Quota: &quota, Period: &period}, true
}

//nolint:cyclop // Decimal parsing rejects every lossy or out-of-range CPU value.
func nanoCPUs(value string) (int64, bool) {
	if value == "" {
		return 0, true
	}
	integer, fraction, found := strings.Cut(value, ".")
	if integer == "" || len(fraction) > cpuFractionDigits ||
		strings.HasPrefix(integer, "+") || strings.HasPrefix(integer, "-") {
		return 0, false
	}
	whole, err := strconv.ParseUint(integer, 10, 63)
	if err != nil || whole > uint64(math.MaxInt64/nanoCPUsPerCPU) {
		return 0, false
	}
	if !found {
		fraction = ""
	}
	padded := fraction + strings.Repeat("0", cpuFractionDigits-len(fraction))
	partial, err := strconv.ParseUint(padded, 10, 32)
	if err != nil {
		return 0, false
	}
	result := whole*uint64(nanoCPUsPerCPU) + partial
	if result == 0 || result > uint64(math.MaxInt64) ||
		result%uint64(nanoCPUsPerCPU/int64(cpuPeriod)) != 0 {
		return 0, false
	}

	return int64(result), true
}

func createBlkio(value *int) (*createBlockIO, bool) {
	if value == nil {
		return nil, true
	}
	if *value < minimumBlkio || *value > maximumBlkio {
		return nil, false
	}
	weight := uint16(*value)

	return &createBlockIO{Weight: &weight}, true
}

func createPidsLimit(value *int64) (*int64, bool) {
	if value == nil {
		return nil, true
	}
	if *value == 0 || *value < -1 {
		return nil, false
	}

	return cloneValue(value), true
}
