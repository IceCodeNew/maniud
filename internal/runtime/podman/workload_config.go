package podman

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"maps"
	"math"
	"net/netip"
	"path"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

const (
	podmanCPUPeriod          = uint64(100_000)
	podmanNanoCPUsPerCPU     = int64(1_000_000_000)
	podmanCPUFractionDigits  = 9
	podmanProtocolTCP        = "tcp"
	podmanProtocolUDP        = "udp"
	podmanProtocolSCTP       = "sctp"
	podmanHealthcheckNone    = "NONE"
	podmanCgroupsEnabled     = "enabled"
	minimumPodmanBlkioWeight = 10
	maximumPodmanBlkioWeight = 1000
	minimumPodmanOOMScore    = -1000
	maximumPodmanOOMScore    = 1000
	podmanSignalTerminate    = 15
)

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectData struct {
	ID          string               `json:"Id"` //nolint:tagliatelle // Native Libpod wire field.
	Name        string               `json:"Name"`
	Image       string               `json:"Image"`
	ImageName   string               `json:"ImageName"`
	ImageDigest string               `json:"ImageDigest"`
	State       *podmanInspectState  `json:"State"`
	Mounts      []podmanInspectMount `json:"Mounts"`
	Config      *podmanInspectConfig `json:"Config"`
	HostConfig  *podmanInspectHost   `json:"HostConfig"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	Dead       bool   `json:"Dead"`
}

//nolint:tagliatelle // Native Libpod health configuration owns these established wire names.
type podmanHealthConfig struct {
	Test          []string      `json:"Test"`
	Interval      time.Duration `json:"Interval"`
	Timeout       time.Duration `json:"Timeout"`
	Retries       int           `json:"Retries"`
	StartPeriod   time.Duration `json:"StartPeriod"`
	StartInterval time.Duration `json:"StartInterval"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectConfig struct {
	Image        string              `json:"Image"`
	Command      []string            `json:"Cmd"`
	Entrypoint   []string            `json:"Entrypoint"`
	Labels       map[string]string   `json:"Labels"`
	Environment  []string            `json:"Env"`
	Hostname     string              `json:"Hostname"`
	User         string              `json:"User"`
	WorkingDir   string              `json:"WorkingDir"`
	OpenStdin    bool                `json:"OpenStdin"`
	TTY          bool                `json:"Tty"`
	StopSignal   string              `json:"StopSignal"`
	StopTimeout  uint                `json:"StopTimeout"`
	Healthcheck  *podmanHealthConfig `json:"Healthcheck"`
	ExposedPorts map[string]any      `json:"ExposedPorts"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectRestart struct {
	Name              string `json:"Name"`
	MaximumRetryCount uint   `json:"MaximumRetryCount"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectDevice struct {
	PathOnHost        string `json:"PathOnHost"`
	PathInContainer   string `json:"PathInContainer"`
	CgroupPermissions string `json:"CgroupPermissions"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectUlimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectPortBinding struct {
	HostIP   string `json:"HostIp"` //nolint:tagliatelle // Native Libpod wire field.
	HostPort string `json:"HostPort"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectHost struct {
	NetworkMode    string                                `json:"NetworkMode"`
	CgroupMode     string                                `json:"CgroupMode"`
	Cgroups        string                                `json:"Cgroups"`
	CgroupParent   string                                `json:"CgroupParent"`
	NanoCPUs       int64                                 `json:"NanoCpus"`
	CPUPeriod      uint64                                `json:"CpuPeriod"`
	CPUQuota       int64                                 `json:"CpuQuota"`
	Memory         int64                                 `json:"Memory"`
	OOMKillDisable bool                                  `json:"OomKillDisable"`
	OOMScoreAdj    int                                   `json:"OomScoreAdj"`
	PidsLimit      int64                                 `json:"PidsLimit"`
	BlkioWeight    uint16                                `json:"BlkioWeight"`
	ShmSize        int64                                 `json:"ShmSize"`
	RestartPolicy  *podmanInspectRestart                 `json:"RestartPolicy"`
	CapAdd         []string                              `json:"CapAdd"`
	CapDrop        []string                              `json:"CapDrop"`
	DNS            []string                              `json:"Dns"`
	DNSSearch      []string                              `json:"DnsSearch"`
	DNSOptions     []string                              `json:"DnsOptions"`
	ExtraHosts     []string                              `json:"ExtraHosts"`
	GroupAdd       []string                              `json:"GroupAdd"`
	Devices        []podmanInspectDevice                 `json:"Devices"`
	Binds          []string                              `json:"Binds"`
	Tmpfs          map[string]string                     `json:"Tmpfs"`
	Ulimits        []podmanInspectUlimit                 `json:"Ulimits"`
	PortBindings   map[string][]podmanInspectPortBinding `json:"PortBindings"`
	Init           bool                                  `json:"Init"`
	ReadonlyRootfs bool                                  `json:"ReadonlyRootfs"`
	SecurityOpt    []string                              `json:"SecurityOpt"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type podmanInspectMount struct {
	Type        string   `json:"Type"`
	Name        string   `json:"Name"`
	Source      string   `json:"Source"`
	Destination string   `json:"Destination"`
	Driver      string   `json:"Driver"`
	Mode        string   `json:"Mode"`
	Options     []string `json:"Options"`
	ReadWrite   bool     `json:"RW"`
	Propagation string   `json:"Propagation"`
	SubPath     string   `json:"SubPath"`
}

func decodePodmanContainer(reader io.Reader) (podmanInspectData, bool) {
	var payload podmanInspectData
	document, valid := jsonstrict.Read(reader, maximumControlBytes)
	var fields map[string]json.RawMessage
	if !valid || json.Unmarshal(document, &fields) != nil || json.Unmarshal(document, &payload) != nil {
		return podmanInspectData{}, false
	}
	for _, name := range []string{
		"Id", "Name", "Image", "ImageName", "ImageDigest", "State", "Mounts", "Config", "HostConfig",
	} {
		if _, found := fields[name]; !found {
			return podmanInspectData{}, false
		}
	}

	return payload, true
}

func podmanContainerFromInspect(reference string, payload podmanInspectData) (Container, bool) {
	var empty Container
	if !validPodmanContainerCore(reference, payload) {
		return empty, false
	}
	imageConfig, imageValid := podmanImageID(payload.Image)
	platformManifest, manifestErr := domain.ParseDigest(payload.ImageDigest)
	state, stateValid := podmanContainerState(payload.State)
	workload, runtimeMounts, workloadValid := podmanWorkloadFromInspect(payload.ID, payload.Name, payload)
	if !imageValid || manifestErr != nil || !stateValid || !workloadValid {
		return empty, false
	}

	return Container{
		ID: payload.ID, Name: payload.Name, ImageReference: payload.Config.Image,
		ImageConfig: imageConfig, PlatformManifest: platformManifest,
		WorkloadSpec: workload, RuntimeMounts: runtimeMounts, State: state,
		Ownership: decodeOwnership(payload.Config.Labels, imageConfig, platformManifest),
	}, true
}

//nolint:cyclop // Every required inspect identity field is checked independently.
func validPodmanContainerCore(reference string, payload podmanInspectData) bool {
	return validContainerID(payload.ID) && validContainerName(payload.Name) &&
		(reference == payload.ID || reference == payload.Name) &&
		payload.Config != nil && payload.HostConfig != nil && payload.State != nil &&
		validPodmanText(payload.Config.Image) && payload.Config.Image != "" &&
		payload.ImageName == payload.Config.Image && validPodmanText(payload.ImageName)
}

//nolint:cyclop // Native lifecycle flags must agree with each accepted status.
func podmanContainerState(state *podmanInspectState) (ContainerState, bool) {
	if state == nil || state.Restarting || state.Dead {
		return ContainerStateUnknown, false
	}
	switch state.Status {
	case "created", "initialized":
		return ContainerCreated, !state.Running && !state.Paused
	case podmanStateRunning:
		return ContainerRunning, state.Running && !state.Paused
	case podmanStatePaused:
		return ContainerPaused, state.Paused
	case "stopped", "exited":
		return ContainerExited, !state.Running && !state.Paused
	case podmanStateRemoving, "stopping":
		return ContainerRemoving, !state.Running && !state.Paused
	case podmanStateUnknown:
		return ContainerStateUnknown, !state.Running && !state.Paused
	default:
		return ContainerStateUnknown, false
	}
}

//nolint:cyclop // This function is the native inspect-to-WorkloadSpec mapping table.
func podmanWorkloadFromInspect(
	identifier string,
	name string,
	payload podmanInspectData,
) (domain.WorkloadSpec, []domain.RuntimeMount, bool) {
	var empty domain.WorkloadSpec
	config := payload.Config
	host := payload.HostConfig
	if config == nil || host == nil || host.RestartPolicy == nil ||
		!validPodmanInspectScalars(identifier, config, host) {
		return empty, nil, false
	}
	labels, labelsValid := podmanObservedLabels(config.Labels)
	environmentValid := validPodmanEnvironment(config.Environment)
	restart, restartValid := podmanObservedRestart(*host.RestartPolicy)
	stopSignal, signalValid := podmanObservedStopSignal(config.StopSignal)
	cpus, cpusValid := podmanObservedCPUs(host.NanoCPUs, host.CPUPeriod, host.CPUQuota)
	blkio, blkioValid := podmanObservedBlkio(host.BlkioWeight)
	pids, pidsValid := podmanObservedPids(host.PidsLimit)
	dnsValid := validPodmanDNS(host.DNS)
	extraHosts, extraHostsValid := podmanObservedExtraHosts(host.ExtraHosts)
	tmpfs, tmpfsValid := podmanObservedTmpfs(host.Tmpfs)
	ulimits, ulimitsValid := podmanObservedUlimits(host.Ulimits)
	exposed, ports, portsValid := podmanObservedPorts(config.ExposedPorts, host.PortBindings)
	mounts, runtimeMounts, mountsValid := podmanObservedMounts(payload.Mounts, host.Binds)
	healthcheck, healthcheckValid := podmanObservedHealthcheck(config.Healthcheck)
	security, securityValid := podmanObservedSecurity(host.SecurityOpt)
	stopTimeout, stopTimeoutValid := podmanObservedStopTimeout(config.StopTimeout)
	if !labelsValid || !environmentValid || !restartValid || !signalValid || !cpusValid || !blkioValid ||
		!pidsValid || !dnsValid || !extraHostsValid || !tmpfsValid || !ulimitsValid || !portsValid ||
		!mountsValid || !healthcheckValid || !securityValid || !stopTimeoutValid {
		return empty, nil, false
	}
	spec := domain.WorkloadSpec{
		ContainerName: name, Entrypoint: slices.Clone(config.Entrypoint), Command: slices.Clone(config.Command),
		NetworkMode: host.NetworkMode, BlkioWeight: blkio, CgroupParent: host.CgroupParent,
		Cgroup: host.CgroupMode, CPUs: cpus, Hostname: normalizedPodmanHostname(identifier, config.Hostname),
		MemoryBytes: host.Memory, OOMScoreAdj: optionalPodmanInt(host.OOMScoreAdj), PidsLimit: pids,
		Restart: restart, SharedMemoryBytes: host.ShmSize, StopSignal: stopSignal,
		StopTimeout: stopTimeout, User: config.User, WorkingDirectory: config.WorkingDir,
		CapAdd: slices.Clone(host.CapAdd), CapDrop: slices.Clone(host.CapDrop), DNS: slices.Clone(host.DNS),
		DNSOptions: slices.Clone(host.DNSOptions), DNSSearch: slices.Clone(host.DNSSearch),
		ExtraHosts: extraHosts, GroupAdd: slices.Clone(host.GroupAdd), Tmpfs: tmpfs, Ulimits: ulimits,
		Environment: slices.Clone(config.Environment), Labels: labels, ExposedPorts: exposed, Ports: ports,
		NoNewPrivileges: security, Mounts: mounts, Healthcheck: healthcheck,
		Init: truePodmanPointer(host.Init), StdinOpen: truePodmanPointer(config.OpenStdin),
		OOMKillDisable: truePodmanPointer(host.OOMKillDisable), ReadOnly: truePodmanPointer(host.ReadonlyRootfs),
		TTY: truePodmanPointer(config.TTY),
	}

	return canonicalPodmanSpec(spec), runtimeMounts, true
}

//nolint:cyclop // Every accepted native scalar and collection is checked independently.
func validPodmanInspectScalars(
	identifier string,
	config *podmanInspectConfig,
	host *podmanInspectHost,
) bool {
	return host.NetworkMode == podmanNetworkBridge &&
		(host.CgroupMode == podmanCgroupPrivate || host.CgroupMode == podmanCgroupHost) &&
		(host.Cgroups == "" || host.Cgroups == podmanCgroupsEnabled) && len(host.Devices) == 0 &&
		validPodmanStrings(config.Command) && validPodmanStrings(config.Entrypoint) &&
		validPodmanStrings(config.Environment) && validPodmanOptionalText(config.Hostname) &&
		validPodmanOptionalText(config.User) && validPodmanOptionalText(config.WorkingDir) &&
		validPodmanStrings(host.CapAdd) && validPodmanStrings(host.CapDrop) &&
		validPodmanStrings(host.DNSOptions) && validPodmanStrings(host.DNSSearch) &&
		validPodmanStrings(host.GroupAdd) && validPodmanOptionalText(host.CgroupParent) &&
		host.Memory >= 0 && host.ShmSize >= 0 && host.OOMScoreAdj >= minimumPodmanOOMScore &&
		host.OOMScoreAdj <= maximumPodmanOOMScore && identifier != ""
}

func normalizedPodmanHostname(identifier, hostname string) string {
	if hostname == identifier || len(identifier) >= 12 && hostname == identifier[:12] {
		return ""
	}

	return hostname
}

func podmanObservedLabels(values map[string]string) ([]string, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		if key == "" || !validPodmanText(key) || !validPodmanOptionalText(value) {
			return nil, false
		}
		if !domain.IsOwnershipLabel(key) {
			result = append(result, key+"="+value)
		}
	}
	slices.Sort(result)

	return result, true
}

func validPodmanEnvironment(values []string) bool {
	seen := make(map[string]struct{}, len(values))
	for _, value := range values {
		key, _, found := strings.Cut(value, "=")
		if !found || key == "" || !validPodmanText(value) {
			return false
		}
		if _, exists := seen[key]; exists {
			return false
		}
		seen[key] = struct{}{}
	}

	return true
}

func podmanObservedRestart(value podmanInspectRestart) (string, bool) {
	if value.MaximumRetryCount > math.MaxInt32 {
		return "", false
	}
	switch value.Name {
	case "", "no":
		return "", value.MaximumRetryCount == 0
	case podmanRestartAlways, podmanRestartUnlessStopped:
		return value.Name, value.MaximumRetryCount == 0
	case podmanRestartOnFailure:
		if value.MaximumRetryCount == 0 {
			return value.Name, true
		}

		return value.Name + ":" + strconv.FormatUint(uint64(value.MaximumRetryCount), 10), true
	default:
		return "", false
	}
}

func podmanObservedStopSignal(value string) (string, bool) {
	signal, valid := podmanSignal(value)
	if !valid {
		return "", false
	}
	if signal == podmanSignalTerminate {
		return "", true
	}

	return podmanSignalName(signal), true
}

func podmanObservedStopTimeout(value uint) (*int64, bool) {
	if uint64(value) > math.MaxInt64 {
		return nil, false
	}
	result := int64(value)

	return &result, true
}

func podmanObservedCPUs(nanoCPUs int64, period uint64, quota int64) (string, bool) {
	if nanoCPUs == 0 && period == 0 && quota == 0 {
		return "", true
	}
	if nanoCPUs <= 0 || period != podmanCPUPeriod || quota <= 0 ||
		quota > math.MaxInt64/(podmanNanoCPUsPerCPU/int64(podmanCPUPeriod)) ||
		nanoCPUs != quota*(podmanNanoCPUsPerCPU/int64(podmanCPUPeriod)) {
		return "", false
	}

	return podmanCPUString(nanoCPUs), true
}

func podmanCPUString(value int64) string {
	integer := value / podmanNanoCPUsPerCPU
	fraction := strings.TrimRight(
		strconv.FormatInt(value%podmanNanoCPUsPerCPU+podmanNanoCPUsPerCPU, 10)[1:],
		"0",
	)
	if fraction == "" {
		return strconv.FormatInt(integer, 10)
	}

	return strconv.FormatInt(integer, 10) + "." + fraction
}

func podmanObservedBlkio(value uint16) (*int, bool) {
	if value == 0 {
		return nil, true
	}
	if value < minimumPodmanBlkioWeight || value > maximumPodmanBlkioWeight {
		return nil, false
	}
	result := int(value)

	return &result, true
}

func podmanObservedPids(value int64) (*int64, bool) {
	if value == 0 {
		return nil, true
	}
	if value < -1 {
		return nil, false
	}
	result := value

	return &result, true
}

func validPodmanDNS(values []string) bool {
	for _, value := range values {
		address, err := netip.ParseAddr(value)
		if err != nil || address.String() != value {
			return false
		}
	}

	return true
}

func podmanObservedExtraHosts(values []string) ([]string, bool) {
	result := make([]string, len(values))
	for index, value := range values {
		name, address, found := strings.Cut(value, ":")
		if !found || name == "" || address == "" || !validPodmanText(value) {
			return nil, false
		}
		result[index] = name + "=" + address
	}

	return result, true
}

func podmanObservedTmpfs(values map[string]string) ([]domain.TmpfsMount, bool) {
	if values == nil {
		return nil, true
	}
	result := make([]domain.TmpfsMount, 0, len(values))
	for target, options := range values {
		if target == "" || !validPodmanText(target) || !validPodmanOptionalText(options) {
			return nil, false
		}
		mount := domain.TmpfsMount{Target: target}
		if options != "" {
			mount.Options = strings.Split(options, ",")
		}
		result = append(result, mount)
	}

	return result, true
}

//nolint:cyclop // POSIX soft and hard limit invariants are checked together.
func podmanObservedUlimits(values []podmanInspectUlimit) ([]domain.Ulimit, bool) {
	result := make([]domain.Ulimit, len(values))
	for index, value := range values {
		name, found := strings.CutPrefix(value.Name, "RLIMIT_")
		if !found || name == "" || !validPodmanText(name) || value.Soft < -1 || value.Hard < -1 ||
			(value.Soft == -1 && value.Hard != -1) || value.Hard != -1 && value.Soft > value.Hard {
			return nil, false
		}
		result[index] = domain.Ulimit{Name: strings.ToLower(name), Soft: value.Soft, Hard: value.Hard}
	}

	return result, true
}

//nolint:cyclop // Each published and exposed port field participates in the identity.
func podmanObservedPorts(
	exposed map[string]any,
	bindings map[string][]podmanInspectPortBinding,
) ([]domain.ExposedPort, []domain.PortBinding, bool) {
	exposedResult := make([]domain.ExposedPort, 0, len(exposed))
	for value := range exposed {
		port, protocol, valid := podmanPortKey(value)
		if !valid || !validExposedProtocol(protocol) {
			return nil, nil, false
		}
		exposedResult = append(exposedResult, domain.ExposedPort{TargetPort: port, Protocol: protocol})
	}
	ports := make([]domain.PortBinding, 0, len(bindings))
	for value, entries := range bindings {
		port, protocol, valid := podmanPortKey(value)
		if !valid || len(entries) != 1 || protocol != podmanProtocolTCP && protocol != podmanProtocolUDP {
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
		ports = append(ports, domain.PortBinding{
			HostIP: entries[0].HostIP, PublishedPort: uint16(published),
			TargetPort: port, Protocol: protocol,
		})
	}

	return exposedResult, ports, true
}

func podmanPortKey(value string) (uint16, string, bool) {
	portValue, protocol, found := strings.Cut(value, "/")
	parsed, err := strconv.ParseUint(portValue, 10, 16)

	return uint16(parsed), protocol, found && err == nil && parsed > 0
}

func validExposedProtocol(value string) bool {
	return value == podmanProtocolTCP || value == podmanProtocolUDP || value == podmanProtocolSCTP
}

//nolint:cyclop,funlen // Native metadata proves each supported persistent mount variant.
func podmanObservedMounts(
	values []podmanInspectMount,
	binds []string,
) ([]domain.Mount, []domain.RuntimeMount, bool) {
	result := make([]domain.Mount, 0, len(values))
	runtimeMounts := make([]domain.RuntimeMount, 0, len(values))
	observedBinds := make(map[string]struct{}, len(binds))
	targets := make(map[string]struct{}, len(values))
	for _, value := range binds {
		if !validPodmanText(value) {
			return nil, nil, false
		}
		if _, exists := observedBinds[value]; exists {
			return nil, nil, false
		}
		observedBinds[value] = struct{}{}
	}
	for _, value := range values {
		if value.Source == "" || value.Destination == "" || !validPodmanText(value.Source) ||
			!validPodmanText(value.Destination) || value.Mode != "" || value.SubPath != "" {
			return nil, nil, false
		}
		if _, duplicate := targets[value.Destination]; duplicate {
			return nil, nil, false
		}
		targets[value.Destination] = struct{}{}
		switch value.Type {
		case podmanMountBind:
			if value.Name != "" || value.Driver != "" ||
				!slices.Equal(value.Options, []string{podmanRecursiveBind}) ||
				value.Propagation != podmanPropagationPrivate {
				return nil, nil, false
			}
			mode := "rw"
			if !value.ReadWrite {
				mode = "ro"
			}
			expectedBind := value.Source + ":" + value.Destination + ":" + podmanRecursiveBind + "," + mode +
				"," + podmanPropagationPrivate
			if _, found := observedBinds[expectedBind]; !found {
				return nil, nil, false
			}
			delete(observedBinds, expectedBind)
			result = append(result, domain.Mount{
				Kind: domain.MountBind, Source: value.Source, Target: value.Destination, ReadOnly: !value.ReadWrite,
			})
			runtimeMounts = append(runtimeMounts, domain.RuntimeMount{
				Kind: domain.MountBind, Source: value.Source, Target: value.Destination, ReadOnly: !value.ReadWrite,
			})
		case podmanMountVolume:
			if value.Name == "" || !validPodmanText(value.Name) || value.Driver != podmanVolumeDriverLocal ||
				!path.IsAbs(value.Source) || path.Clean(value.Source) != value.Source ||
				!value.ReadWrite || len(value.Options) != 0 ||
				value.Propagation != "" {
				return nil, nil, false
			}
			result = append(result, domain.Mount{Kind: domain.MountVolume, Target: value.Destination})
			runtimeMounts = append(runtimeMounts, domain.RuntimeMount{
				Kind: domain.MountVolume, Name: value.Name, Source: value.Source, Target: value.Destination,
			})
		default:
			return nil, nil, false
		}
	}
	if len(observedBinds) != 0 {
		return nil, nil, false
	}
	if len(runtimeMounts) == 0 {
		return result, nil, true
	}
	slices.SortFunc(runtimeMounts, func(left, right domain.RuntimeMount) int {
		return strings.Compare(left.Target, right.Target)
	})

	return result, runtimeMounts, true
}

//nolint:cyclop // Disabled and command healthchecks have disjoint field invariants.
func podmanObservedHealthcheck(value *podmanHealthConfig) (*domain.Healthcheck, bool) {
	if value == nil {
		return nil, true
	}
	if slices.Equal(value.Test, []string{podmanHealthcheckNone}) {
		if value.Interval != 0 || value.Timeout != 0 || value.Retries != 0 ||
			value.StartPeriod != 0 || value.StartInterval != 0 {
			return nil, false
		}

		return &domain.Healthcheck{Disabled: true}, true
	}
	if !validPodmanStrings(value.Test) || value.Interval < 0 || value.Timeout < 0 || value.Retries < 0 ||
		value.StartPeriod < 0 || value.StartInterval < 0 {
		return nil, false
	}
	result := &domain.Healthcheck{
		Test: slices.Clone(value.Test), Interval: podmanDurationString(value.Interval),
		Timeout: podmanDurationString(value.Timeout), StartPeriod: podmanDurationString(value.StartPeriod),
		StartInterval: podmanDurationString(value.StartInterval),
	}
	if value.Retries != 0 {
		retries := value.Retries
		result.Retries = &retries
	}

	return result, true
}

func podmanDurationString(value time.Duration) string {
	if value == 0 {
		return ""
	}

	return value.String()
}

func podmanObservedSecurity(values []string) (bool, bool) {
	if len(values) == 0 {
		return false, true
	}
	if len(values) == 1 && (values[0] == "no-new-privileges" ||
		values[0] == "no-new-privileges=true" || values[0] == "no-new-privileges:true") {
		return true, true
	}

	return false, false
}

func optionalPodmanInt(value int) *int {
	if value == 0 {
		return nil
	}
	result := value

	return &result
}

func truePodmanPointer(value bool) *bool {
	if !value {
		return nil
	}
	result := true

	return &result
}

type podmanNamespace struct {
	Mode  string `json:"nsmode,omitempty"`
	Value string `json:"value,omitempty"`
}

type podmanCreateCPU struct {
	Quota  *int64  `json:"quota,omitempty"`
	Period *uint64 `json:"period,omitempty"`
}

type podmanCreateMemory struct {
	Limit            *int64 `json:"limit,omitempty"`
	DisableOOMKiller *bool  `json:"disableOOMKiller,omitempty"` //nolint:tagliatelle // OCI runtime-spec wire field.
}

type podmanCreatePids struct {
	Limit *int64 `json:"limit,omitempty"`
}

type podmanCreateBlockIO struct {
	Weight *uint16 `json:"weight,omitempty"`
}

type podmanCreateResources struct {
	CPU     *podmanCreateCPU     `json:"cpu,omitempty"`
	Memory  *podmanCreateMemory  `json:"memory,omitempty"`
	Pids    *podmanCreatePids    `json:"pids,omitempty"`
	BlockIO *podmanCreateBlockIO `json:"blockIO,omitempty"` //nolint:tagliatelle // OCI wire field.
}

type podmanCreatePort struct {
	HostIP        string `json:"host_ip,omitempty"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	Range         uint16 `json:"range"`
	Protocol      string `json:"protocol"`
}

type podmanCreateMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options,omitempty"`
}

// podmanCreateVolume pins the case-sensitive wire names emitted by the
// untagged upstream specgen.NamedVolume DTO.
//
//nolint:tagliatelle // Native Libpod wire fields intentionally use Go member casing.
type podmanCreateVolume struct {
	Name        string   `json:"Name"`
	Dest        string   `json:"Dest"`
	Options     []string `json:"Options"`
	IsAnonymous bool     `json:"IsAnonymous"`
}

type podmanCreateUlimit struct {
	Type string `json:"type"`
	Soft int64  `json:"soft"`
	Hard int64  `json:"hard"`
}

type podmanCreateSpec struct {
	Image               string                `json:"image"`
	RawImageName        string                `json:"raw_image_name"`
	Command             []string              `json:"command"`
	Entrypoint          []string              `json:"entrypoint"`
	Name                string                `json:"name"`
	ImageOS             string                `json:"image_os"`
	ImageArchitecture   string                `json:"image_arch"`
	ImageVariant        string                `json:"image_variant,omitempty"`
	Labels              map[string]string     `json:"labels"`
	Environment         map[string]string     `json:"env"`
	WorkingDirectory    string                `json:"work_dir,omitempty"`
	Hostname            string                `json:"hostname,omitempty"`
	User                string                `json:"user,omitempty"`
	Stdin               *bool                 `json:"stdin,omitempty"`
	Terminal            *bool                 `json:"terminal,omitempty"`
	Init                *bool                 `json:"init,omitempty"`
	ReadOnlyFilesystem  *bool                 `json:"read_only_filesystem,omitempty"`
	StopSignal          *int                  `json:"stop_signal,omitempty"`
	StopTimeout         *uint                 `json:"stop_timeout"`
	RestartPolicy       string                `json:"restart_policy"`
	RestartTries        *uint                 `json:"restart_tries,omitempty"`
	NetworkNamespace    podmanNamespace       `json:"netns"`
	CgroupNamespace     podmanNamespace       `json:"cgroupns"`
	CgroupParent        string                `json:"cgroup_parent,omitempty"`
	DNS                 []string              `json:"dns_server,omitempty"`
	DNSSearch           []string              `json:"dns_search,omitempty"`
	DNSOptions          []string              `json:"dns_option,omitempty"`
	ExtraHosts          []string              `json:"hostadd,omitempty"`
	Groups              []string              `json:"groups,omitempty"`
	CapAdd              []string              `json:"cap_add,omitempty"`
	CapDrop             []string              `json:"cap_drop,omitempty"`
	NoNewPrivileges     *bool                 `json:"no_new_privileges,omitempty"`
	OOMScoreAdj         *int                  `json:"oom_score_adj,omitempty"`
	SharedMemoryBytes   *int64                `json:"shm_size"`
	ResourceLimits      podmanCreateResources `json:"resource_limits"`
	PortMappings        []podmanCreatePort    `json:"portmappings,omitempty"`
	PublishExposedPorts *bool                 `json:"publish_image_ports"`
	Expose              map[uint16]string     `json:"expose,omitempty"`
	Mounts              []podmanCreateMount   `json:"mounts,omitempty"`
	Volumes             []podmanCreateVolume  `json:"volumes,omitempty"`
	Ulimits             []podmanCreateUlimit  `json:"r_limits,omitempty"`
	Healthcheck         *podmanHealthConfig   `json:"healthconfig,omitempty"`
}

//nolint:cyclop,funlen // Every supported Compose field has an independent native mapping.
func podmanCreateConfiguration(
	workload domain.DesiredWorkload,
	transaction string,
	options application.WorkloadCreateOptions,
) (podmanCreateSpec, bool) {
	var empty podmanCreateSpec
	labels, labelsValid := podmanLabels(workload.Labels, workloadOwnershipLabels(workload, transaction))
	environment, environmentValid := podmanEnvironment(workload.Environment)
	restart, tries, restartValid := podmanRestart(workload.Restart)
	stopSignal, signalValid := podmanCreateStopSignal(workload.StopSignal)
	stopTimeout, timeoutValid := podmanCreateStopTimeout(workload.StopTimeout)
	cpus, cpusValid := podmanCreateCPUs(workload.CPUs)
	blkio, blkioValid := podmanCreateBlkio(workload.BlkioWeight)
	pids, pidsValid := podmanCreatePidsLimit(workload.PidsLimit)
	ports, exposed, portsValid := podmanCreatePorts(workload.ExposedPorts, workload.Ports)
	mounts, volumes, mountsValid := podmanCreateMounts(workload.Mounts, workload.Tmpfs, options)
	ulimits, ulimitsValid := podmanCreateUlimits(workload.Ulimits)
	healthcheck, healthcheckValid := podmanCreateHealthcheck(workload.Healthcheck)
	if !labelsValid || !environmentValid || !restartValid || !signalValid || !timeoutValid || !cpusValid ||
		!blkioValid || !pidsValid || !portsValid || !mountsValid || !ulimitsValid || !healthcheckValid ||
		!validPodmanWorkloadSpec(workload.WorkloadSpec) {
		return empty, false
	}
	readOnly := clonePodmanPointer(workload.ReadOnly)
	publishExposed := false
	sharedMemory := workload.SharedMemoryBytes
	if sharedMemory == 0 {
		sharedMemory = podmanDefaultSharedMemory
	}
	cgroupMode := workload.Cgroup
	if cgroupMode == "" {
		cgroupMode = podmanCgroupPrivate
	}
	extraHosts := make([]string, len(workload.ExtraHosts))
	for index, value := range workload.ExtraHosts {
		extraHosts[index] = strings.Replace(value, "=", ":", 1)
	}
	resources := podmanCreateResources{CPU: cpus, BlockIO: blkio}
	if pids != nil {
		resources.Pids = &podmanCreatePids{Limit: pids}
	}
	if workload.MemoryBytes != 0 || workload.OOMKillDisable != nil {
		resources.Memory = &podmanCreateMemory{
			Limit:            podmanNonzeroInt64(workload.MemoryBytes),
			DisableOOMKiller: clonePodmanPointer(workload.OOMKillDisable),
		}
	}

	return podmanCreateSpec{
		Image: workload.Image.Reference, RawImageName: workload.Image.Reference,
		Command: slices.Clone(workload.Command), Entrypoint: slices.Clone(workload.Entrypoint),
		Name: workload.ContainerName, ImageOS: workload.Platform.OS,
		ImageArchitecture: workload.Platform.Architecture, ImageVariant: workload.Platform.Variant,
		Labels: labels, Environment: environment, WorkingDirectory: workload.WorkingDirectory,
		Hostname: workload.Hostname, User: workload.User, Stdin: clonePodmanPointer(workload.StdinOpen),
		Terminal: clonePodmanPointer(workload.TTY), Init: clonePodmanPointer(workload.Init),
		ReadOnlyFilesystem: readOnly, StopSignal: stopSignal, StopTimeout: stopTimeout,
		RestartPolicy: restart, RestartTries: tries,
		NetworkNamespace: podmanNamespace{Mode: podmanNetworkBridge},
		CgroupNamespace:  podmanNamespace{Mode: cgroupMode}, CgroupParent: workload.CgroupParent,
		DNS: slices.Clone(workload.DNS), DNSSearch: slices.Clone(workload.DNSSearch),
		DNSOptions: slices.Clone(workload.DNSOptions), ExtraHosts: extraHosts,
		Groups: slices.Clone(workload.GroupAdd), CapAdd: slices.Clone(workload.CapAdd),
		CapDrop: slices.Clone(workload.CapDrop), NoNewPrivileges: podmanBoolPointer(workload.NoNewPrivileges),
		OOMScoreAdj: clonePodmanPointer(workload.OOMScoreAdj), SharedMemoryBytes: &sharedMemory,
		ResourceLimits: resources, PortMappings: ports, PublishExposedPorts: &publishExposed,
		Expose: exposed, Mounts: mounts, Volumes: volumes, Ulimits: ulimits, Healthcheck: healthcheck,
	}, true
}

//nolint:cyclop // Validation mirrors every accepted native SpecGenerator field.
func validPodmanWorkloadSpec(spec domain.WorkloadSpec) bool {
	return validOwnershipName(spec.ServiceName) && validContainerName(spec.ContainerName) &&
		spec.NetworkMode == podmanNetworkBridge &&
		(spec.Cgroup == "" || spec.Cgroup == podmanCgroupPrivate || spec.Cgroup == podmanCgroupHost) &&
		validPodmanStrings(spec.Entrypoint) && validPodmanStrings(spec.Command) &&
		validPodmanOptionalText(spec.Hostname) && validPodmanOptionalText(spec.User) &&
		validPodmanOptionalText(spec.WorkingDirectory) &&
		spec.MemoryBytes >= 0 && spec.SharedMemoryBytes >= 0 &&
		validOptionalPodmanRange(spec.OOMScoreAdj, minimumPodmanOOMScore, maximumPodmanOOMScore) &&
		validPodmanStrings(spec.CapAdd) && validPodmanStrings(spec.CapDrop) &&
		validPodmanStrings(spec.DNSOptions) && validPodmanStrings(spec.DNSSearch) &&
		validPodmanStrings(spec.GroupAdd) && validPodmanDNS(spec.DNS) &&
		validPodmanExtraHosts(spec.ExtraHosts) && len(spec.Devices) == 0 && len(spec.Sysctls) == 0
}

func validOptionalPodmanRange(value *int, minimum, maximum int) bool {
	return value == nil || *value >= minimum && *value <= maximum
}

func podmanLabels(values []string, ownership map[string]string) (map[string]string, bool) {
	labels := make(map[string]string, len(values)+len(ownership))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found {
			selected = ""
		}
		if key == "" || !validPodmanText(key) || !validPodmanOptionalText(selected) || domain.IsOwnershipLabel(key) {
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

func podmanEnvironment(values []string) (map[string]string, bool) {
	result := make(map[string]string, len(values))
	for _, value := range values {
		key, selected, found := strings.Cut(value, "=")
		if !found || key == "" || !validPodmanText(value) {
			return nil, false
		}
		if _, exists := result[key]; exists {
			return nil, false
		}
		result[key] = selected
	}

	return result, true
}

func podmanRestart(value string) (string, *uint, bool) {
	if value == "" || value == "no" {
		return "no", nil, true
	}
	name, retries, found := strings.Cut(value, ":")
	switch name {
	case podmanRestartAlways, podmanRestartUnlessStopped:
		return name, nil, !found
	case podmanRestartOnFailure:
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

func podmanCreateStopSignal(value string) (*int, bool) {
	if value == "" {
		return nil, true
	}
	signal, valid := podmanSignal(value)
	if !valid {
		return nil, false
	}

	return &signal, true
}

func podmanSignal(value string) (int, bool) {
	if parsed, err := strconv.ParseUint(value, 10, 8); err == nil {
		return int(parsed), parsed > 0 && parsed <= 64
	}
	upper := strings.ToUpper(value)
	if !strings.HasPrefix(upper, "SIG") {
		upper = "SIG" + upper
	}
	for signal := 1; signal <= 31; signal++ {
		if podmanSignalName(signal) == upper {
			return signal, true
		}
	}

	return 0, false
}

func podmanSignalName(signal int) string {
	names := [...]string{
		"", "SIGHUP", podmanSignalInterruptName, "SIGQUIT", "SIGILL", "SIGTRAP", "SIGABRT", "SIGBUS", "SIGFPE",
		"SIGKILL", "SIGUSR1", "SIGSEGV", "SIGUSR2", "SIGPIPE", "SIGALRM", podmanSignalTerminateName, "SIGSTKFLT",
		"SIGCHLD", "SIGCONT", "SIGSTOP", "SIGTSTP", "SIGTTIN", "SIGTTOU", "SIGURG", "SIGXCPU",
		"SIGXFSZ", "SIGVTALRM", "SIGPROF", "SIGWINCH", "SIGIO", "SIGPWR", "SIGSYS",
	}
	if signal <= 0 || signal >= len(names) {
		return strconv.Itoa(signal)
	}

	return names[signal] //nolint:gosec // The preceding bounds check proves the fixed array index.
}

func podmanCreateStopTimeout(value *int64) (*uint, bool) {
	selected := podmanDefaultStopTimeout
	if value != nil {
		selected = *value
	}
	if selected <= 0 || uint64(selected) > uint64(^uint(0)) {
		return nil, false
	}
	result := uint(selected)

	return &result, true
}

func podmanCreateCPUs(value string) (*podmanCreateCPU, bool) {
	nanoCPUs, valid := podmanNanoCPUs(value)
	if !valid {
		return nil, false
	}
	if nanoCPUs == 0 {
		return nil, true
	}
	quota := nanoCPUs / (podmanNanoCPUsPerCPU / int64(podmanCPUPeriod))
	period := podmanCPUPeriod

	return &podmanCreateCPU{Quota: &quota, Period: &period}, true
}

//nolint:cyclop // Decimal parsing rejects every lossy or out-of-range CPU value.
func podmanNanoCPUs(value string) (int64, bool) {
	if value == "" {
		return 0, true
	}
	integer, fraction, found := strings.Cut(value, ".")
	if integer == "" || len(fraction) > podmanCPUFractionDigits ||
		strings.HasPrefix(integer, "+") || strings.HasPrefix(integer, "-") {
		return 0, false
	}
	whole, err := strconv.ParseUint(integer, 10, 63)
	if err != nil || whole > uint64(math.MaxInt64/podmanNanoCPUsPerCPU) {
		return 0, false
	}
	if !found {
		fraction = ""
	}
	padded := fraction + strings.Repeat("0", podmanCPUFractionDigits-len(fraction))
	partial, err := strconv.ParseUint(padded, 10, 32)
	if err != nil {
		return 0, false
	}
	result := whole*uint64(podmanNanoCPUsPerCPU) + partial
	if result == 0 || result > uint64(math.MaxInt64) ||
		result%uint64(podmanNanoCPUsPerCPU/int64(podmanCPUPeriod)) != 0 {
		return 0, false
	}

	return int64(result), true
}

func podmanCreateBlkio(value *int) (*podmanCreateBlockIO, bool) {
	if value == nil {
		return nil, true
	}
	if *value < minimumPodmanBlkioWeight || *value > maximumPodmanBlkioWeight {
		return nil, false
	}
	weight := uint16(*value)

	return &podmanCreateBlockIO{Weight: &weight}, true
}

func podmanCreatePidsLimit(value *int64) (*int64, bool) {
	if value == nil {
		return nil, true
	}
	if *value == 0 || *value < -1 {
		return nil, false
	}

	return clonePodmanPointer(value), true
}

//nolint:cyclop // Exposed and published port sets must remain unambiguous.
func podmanCreatePorts(
	exposedValues []domain.ExposedPort,
	values []domain.PortBinding,
) ([]podmanCreatePort, map[uint16]string, bool) {
	exposed := make(map[uint16]string, len(exposedValues)+len(values))
	bound := make(map[string]struct{}, len(values))
	for _, value := range exposedValues {
		if value.TargetPort == 0 || !validExposedProtocol(value.Protocol) {
			return nil, nil, false
		}
		if _, exists := exposed[value.TargetPort]; exists {
			return nil, nil, false
		}
		exposed[value.TargetPort] = value.Protocol
	}
	ports := make([]podmanCreatePort, len(values))
	for index, value := range values {
		if value.TargetPort == 0 || value.PublishedPort == 0 ||
			value.Protocol != podmanProtocolTCP && value.Protocol != podmanProtocolUDP {
			return nil, nil, false
		}
		if value.HostIP != "" {
			address, err := netip.ParseAddr(value.HostIP)
			if err != nil || address.String() != value.HostIP {
				return nil, nil, false
			}
		}
		if protocol, exists := exposed[value.TargetPort]; exists && protocol != value.Protocol {
			return nil, nil, false
		}
		key := strconv.FormatUint(uint64(value.TargetPort), 10) + "/" + value.Protocol
		if _, exists := bound[key]; exists {
			return nil, nil, false
		}
		bound[key] = struct{}{}
		exposed[value.TargetPort] = value.Protocol
		ports[index] = podmanCreatePort{
			HostIP: value.HostIP, ContainerPort: value.TargetPort,
			HostPort: value.PublishedPort, Range: 1, Protocol: value.Protocol,
		}
	}
	if len(exposed) == 0 {
		exposed = nil
	}

	return ports, exposed, true
}

//nolint:cyclop // Bind and tmpfs destinations share one collision domain.
func podmanCreateMounts(
	values []domain.Mount,
	tmpfs []domain.TmpfsMount,
	options application.WorkloadCreateOptions,
) ([]podmanCreateMount, []podmanCreateVolume, bool) {
	result := make([]podmanCreateMount, 0, len(values)+len(tmpfs))
	volumes := make([]podmanCreateVolume, 0, len(values))
	targets := make(map[string]struct{}, len(values)+len(tmpfs))
	for _, value := range values {
		if value.Target == "" || !validPodmanText(value.Target) {
			return nil, nil, false
		}
		if _, exists := targets[value.Target]; exists {
			return nil, nil, false
		}
		targets[value.Target] = struct{}{}
		switch value.Kind {
		case domain.MountBind:
			if value.Source == "" || !validPodmanText(value.Source) {
				return nil, nil, false
			}
			options := []string{podmanRecursiveBind, "rw"}
			if value.ReadOnly {
				options[1] = "ro"
			}
			result = append(result, podmanCreateMount{
				Destination: value.Target, Type: podmanMountBind, Source: value.Source, Options: options,
			})
		case domain.MountVolume:
			if value.Source != "" || value.ReadOnly {
				return nil, nil, false
			}
			volume := podmanCreateVolume{Dest: value.Target, IsAnonymous: true}
			if !options.CopyImageVolumes {
				volume.Options = []string{"nocopy"}
			}
			volumes = append(volumes, volume)
		default:
			return nil, nil, false
		}
	}
	for _, value := range tmpfs {
		if value.Target == "" || !validPodmanText(value.Target) || !validPodmanStrings(value.Options) {
			return nil, nil, false
		}
		if _, exists := targets[value.Target]; exists {
			return nil, nil, false
		}
		targets[value.Target] = struct{}{}
		result = append(result, podmanCreateMount{
			Destination: value.Target, Type: "tmpfs", Source: "tmpfs", Options: slices.Clone(value.Options),
		})
	}

	return result, volumes, true
}

//nolint:cyclop // POSIX soft and hard limit invariants are checked together.
func podmanCreateUlimits(values []domain.Ulimit) ([]podmanCreateUlimit, bool) {
	result := make([]podmanCreateUlimit, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		name := strings.ToUpper(value.Name)
		if name == "" || !validPodmanText(name) || value.Soft < -1 || value.Hard < -1 ||
			(value.Soft == -1 && value.Hard != -1) || value.Hard != -1 && value.Soft > value.Hard {
			return nil, false
		}
		if _, exists := seen[name]; exists {
			return nil, false
		}
		seen[name] = struct{}{}
		result[index] = podmanCreateUlimit{Type: "RLIMIT_" + name, Soft: value.Soft, Hard: value.Hard}
	}

	return result, true
}

//nolint:cyclop // Disabled and command healthchecks have disjoint field invariants.
func podmanCreateHealthcheck(value *domain.Healthcheck) (*podmanHealthConfig, bool) {
	if value == nil {
		return nil, true
	}
	if value.Disabled {
		if len(value.Test) != 0 || value.Interval != "" || value.Timeout != "" || value.Retries != nil ||
			value.StartPeriod != "" || value.StartInterval != "" {
			return nil, false
		}

		return &podmanHealthConfig{Test: []string{podmanHealthcheckNone}}, true
	}
	interval, intervalValid := podmanDuration(value.Interval)
	timeout, timeoutValid := podmanDuration(value.Timeout)
	startPeriod, startPeriodValid := podmanDuration(value.StartPeriod)
	startInterval, startIntervalValid := podmanDuration(value.StartInterval)
	if !intervalValid || !timeoutValid || !startPeriodValid || !startIntervalValid ||
		value.Retries != nil && *value.Retries <= 0 || !validPodmanStrings(value.Test) {
		return nil, false
	}
	retries := 0
	if value.Retries != nil {
		retries = *value.Retries
	}

	return &podmanHealthConfig{
		Test: slices.Clone(value.Test), Interval: interval, Timeout: timeout, Retries: retries,
		StartPeriod: startPeriod, StartInterval: startInterval,
	}, true
}

func podmanDuration(value string) (time.Duration, bool) {
	if value == "" {
		return 0, true
	}
	duration, err := time.ParseDuration(value)

	return duration, err == nil && duration > 0
}

func validPodmanExtraHosts(values []string) bool {
	for _, value := range values {
		name, address, found := strings.Cut(value, "=")
		if !found || name == "" || address == "" || !validPodmanText(value) {
			return false
		}
	}

	return true
}

func podmanNonzeroInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	result := value

	return &result
}

func podmanBoolPointer(value bool) *bool {
	if !value {
		return nil
	}
	result := true

	return &result
}

func clonePodmanPointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	result := *value

	return &result
}

func parseExpectedImageReference(image domain.ImageIdentity) (imageref.Reference, error) {
	reference, err := imageref.Parse(image.Reference)
	if err != nil {
		return imageref.Reference{}, fmt.Errorf("parse podman image reference: %w", err)
	}

	return reference, nil
}

func encodePodmanCreateConfiguration(configuration podmanCreateSpec) ([]byte, bool) {
	encoded, err := json.Marshal(configuration)

	return encoded, err == nil && len(encoded) <= int(maximumControlBytes) &&
		jsonstrict.Decode(bytes.NewReader(encoded), maximumControlBytes, &podmanCreateSpec{})
}
