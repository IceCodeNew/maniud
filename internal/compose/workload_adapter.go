package compose

import (
	"maps"
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/internal/domain"
)

//nolint:cyclop // The adapter maps each supported Compose field into one shared specification.
func workloadSpecFromService(
	service composetypes.ServiceConfig,
	platform domain.Platform,
	pathFrom string,
	pathTo string,
) (domain.WorkloadSpec, error) {
	spec := domain.WorkloadSpec{
		ServiceName: service.Name, ContainerName: service.ContainerName, Platform: platform,
		NetworkMode: service.NetworkMode, CgroupParent: service.CgroupParent, Cgroup: service.Cgroup,
		Hostname: service.Hostname, MemoryBytes: int64(service.MemLimit), Restart: service.Restart,
		SharedMemoryBytes: int64(service.ShmSize), StopSignal: service.StopSignal, User: service.User,
		WorkingDirectory: service.WorkingDir, CapAdd: slices.Clone(service.CapAdd),
		CapDrop: slices.Clone(service.CapDrop), DNS: slices.Clone(service.DNS),
		DNSOptions: slices.Clone(service.DNSOpts), DNSSearch: slices.Clone(service.DNSSearch),
		ExtraHosts: hostsList(service.ExtraHosts), GroupAdd: slices.Clone(service.GroupAdd),
		Sysctls: cloneMapping(service.Sysctls), Environment: mappingWithEqualsList(service.Environment),
		Labels: labelsList(service.Labels), Init: clonePointer(service.Init),
	}
	if service.CPUS != 0 {
		spec.CPUs = strconv.FormatFloat(float64(service.CPUS), 'f', -1, 32)
	}
	if service.OomScoreAdj != 0 {
		value := int(service.OomScoreAdj)
		spec.OOMScoreAdj = &value
	}
	if service.PidsLimit != 0 {
		value := service.PidsLimit
		spec.PidsLimit = &value
	}
	spec.StdinOpen = truePointer(service.StdinOpen)
	spec.OOMKillDisable = truePointer(service.OomKillDisable)
	spec.ReadOnly = truePointer(service.ReadOnly)
	spec.TTY = truePointer(service.Tty)

	if !addBlkio(&spec, service.BlkioConfig) || !addStopTimeout(&spec, service.StopGracePeriod) ||
		!addDevices(&spec, service.Devices) || !addTmpfs(&spec, service.Tmpfs) ||
		!addUlimits(&spec, service.Ulimits) || !addExposedPorts(&spec, service.Expose) ||
		!addPorts(&spec, service.Ports) ||
		!addSecurityOptions(&spec, service.SecurityOpt) || !addMounts(&spec, service.Volumes, pathFrom, pathTo) ||
		!addHealthcheck(&spec, service.HealthCheck) {
		return domain.WorkloadSpec{}, ErrInvalidSource
	}

	slices.Sort(spec.ExtraHosts)
	slices.Sort(spec.Environment)
	slices.Sort(spec.Labels)

	return spec, nil
}

func hostsList(values composetypes.HostsList) []string {
	if values == nil {
		return nil
	}

	return values.AsList("=")
}

func labelsList(values composetypes.Labels) []string {
	if values == nil {
		return nil
	}

	return values.AsList()
}

func cloneMapping(values composetypes.Mapping) map[string]string {
	if values == nil {
		return nil
	}
	result := make(map[string]string, len(values))
	maps.Copy(result, values)

	return result
}

func mappingWithEqualsList(values composetypes.MappingWithEquals) []string {
	if values == nil {
		return nil
	}
	result := make([]string, 0, len(values))
	for key, value := range values {
		if value == nil {
			result = append(result, key)
		} else {
			result = append(result, key+"="+*value)
		}
	}

	return result
}

func clonePointer[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value

	return &clone
}

func truePointer(value bool) *bool {
	if !value {
		return nil
	}
	result := true

	return &result
}

func addBlkio(spec *domain.WorkloadSpec, config *composetypes.BlkioConfig) bool {
	if config == nil {
		return true
	}
	if config.Weight < 10 || config.Weight > 1000 || len(config.WeightDevice) != 0 ||
		len(config.DeviceReadBps) != 0 || len(config.DeviceReadIOps) != 0 ||
		len(config.DeviceWriteBps) != 0 || len(config.DeviceWriteIOps) != 0 || len(config.Extensions) != 0 {
		return false
	}
	weight := int(config.Weight)
	spec.BlkioWeight = &weight

	return true
}

func addStopTimeout(spec *domain.WorkloadSpec, value *composetypes.Duration) bool {
	if value == nil {
		return true
	}
	duration := time.Duration(*value)
	if duration <= 0 || duration%time.Second != 0 {
		return false
	}
	seconds := int64(duration / time.Second)
	spec.StopTimeout = &seconds

	return true
}

func addDevices(spec *domain.WorkloadSpec, values []composetypes.DeviceMapping) bool {
	if values == nil {
		return true
	}
	spec.Devices = make([]domain.DeviceMapping, len(values))
	for index, value := range values {
		if len(value.Extensions) != 0 || !absoluteCleanPath(value.Source) || !absoluteCleanPath(value.Target) ||
			!validDevicePermissions(value.Permissions) {
			return false
		}
		spec.Devices[index] = domain.DeviceMapping{
			Source: value.Source, Target: value.Target, Permissions: value.Permissions,
		}
	}

	return true
}

func absoluteCleanPath(value string) bool {
	return filepath.IsAbs(value) && filepath.Clean(value) == value && !strings.HasPrefix(value, "//")
}

func validDevicePermissions(value string) bool {
	if value == "" {
		return false
	}
	for _, permission := range "rwm" {
		if strings.Count(value, string(permission)) > 1 {
			return false
		}
	}
	for _, permission := range value {
		if !strings.ContainsRune("rwm", permission) {
			return false
		}
	}

	return true
}

func addTmpfs(spec *domain.WorkloadSpec, values composetypes.StringList) bool {
	if values == nil {
		return true
	}
	spec.Tmpfs = make([]domain.TmpfsMount, len(values))
	for index, value := range values {
		target, options, found := strings.Cut(value, ":")
		if !absoluteCleanPath(target) || (found && options == "") {
			return false
		}
		spec.Tmpfs[index].Target = target
		if found {
			spec.Tmpfs[index].Options = strings.Split(options, ",")
		}
	}

	return true
}

//nolint:cyclop // Ulimit short and long forms share one relational validation boundary.
func addUlimits(spec *domain.WorkloadSpec, values map[string]*composetypes.UlimitsConfig) bool {
	if values == nil {
		return true
	}
	spec.Ulimits = make([]domain.Ulimit, 0, len(values))
	for name, value := range values {
		if name == "" || value == nil || len(value.Extensions) != 0 {
			return false
		}
		soft, hard := int64(value.Soft), int64(value.Hard)
		if value.Single != 0 {
			if value.Soft != 0 || value.Hard != 0 {
				return false
			}
			soft, hard = int64(value.Single), int64(value.Single)
		}
		if soft < -1 || hard < -1 || (soft == -1 && hard != -1) || (hard != -1 && soft > hard) {
			return false
		}
		spec.Ulimits = append(spec.Ulimits, domain.Ulimit{Name: name, Soft: soft, Hard: hard})
	}
	slices.SortFunc(spec.Ulimits, func(first, second domain.Ulimit) int {
		return strings.Compare(first.Name, second.Name)
	})

	return true
}

func addExposedPorts(spec *domain.WorkloadSpec, values composetypes.StringOrNumberList) bool {
	if values == nil {
		return true
	}
	spec.ExposedPorts = make([]domain.ExposedPort, len(values))
	for index, value := range values {
		port, protocol, found := strings.Cut(value, "/")
		if !found {
			protocol = composeProtocolTCP
		}
		parsed, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsed == 0 ||
			(protocol != composeProtocolTCP && protocol != composeProtocolUDP) {
			return false
		}
		spec.ExposedPorts[index] = domain.ExposedPort{TargetPort: uint16(parsed), Protocol: protocol}
	}

	return true
}

//nolint:cyclop // Published-port parsing keeps every unsupported Compose option adjacent.
func addPorts(spec *domain.WorkloadSpec, values []composetypes.ServicePortConfig) bool {
	if values == nil {
		return true
	}
	spec.Ports = make([]domain.PortBinding, len(values))
	for index, value := range values {
		published, err := strconv.ParseUint(value.Published, 10, 16)
		if err != nil || published == 0 || value.Target == 0 || value.Target > 65535 ||
			(value.Protocol != composeProtocolTCP && value.Protocol != composeProtocolUDP) ||
			(value.Mode != "" && value.Mode != "ingress") || value.Name != "" || value.AppProtocol != "" ||
			len(value.Extensions) != 0 || !validHostIP(value.HostIP) {
			return false
		}
		spec.Ports[index] = domain.PortBinding{
			HostIP: value.HostIP, PublishedPort: uint16(published), TargetPort: uint16(value.Target),
			Protocol: value.Protocol,
		}
	}

	return true
}

func validHostIP(value string) bool {
	if value == "" {
		return true
	}
	address, err := netip.ParseAddr(value)

	return err == nil && address.String() == value
}

func addSecurityOptions(spec *domain.WorkloadSpec, values []string) bool {
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || (values[0] != "no-new-privileges" && values[0] != "no-new-privileges:true" &&
		values[0] != "no-new-privileges=true") {
		return false
	}
	spec.NoNewPrivileges = true

	return true
}

//nolint:cyclop // Bind and anonymous-volume variants reject different Compose options.
func addMounts(
	spec *domain.WorkloadSpec,
	values []composetypes.ServiceVolumeConfig,
	pathFrom string,
	pathTo string,
) bool {
	if values == nil {
		return true
	}
	spec.Mounts = make([]domain.Mount, len(values))
	for index, value := range values {
		if !absoluteCleanPath(value.Target) || value.Consistency != "" || len(value.Extensions) != 0 {
			return false
		}
		switch value.Type {
		case composeBindMountType:
			if !absoluteCleanPath(value.Source) || !emptyBindOptions(value.Bind) || value.Volume != nil ||
				value.Tmpfs != nil || value.Image != nil {
				return false
			}
			source, valid := rebaseRepositoryPath(value.Source, pathFrom, pathTo)
			if !valid {
				return false
			}
			spec.Mounts[index] = domain.Mount{
				Kind: domain.MountBind, Source: source, Target: value.Target, ReadOnly: value.ReadOnly,
			}
		case "volume":
			if value.Source != "" || value.Bind != nil || value.Tmpfs != nil || value.Image != nil ||
				!emptyVolumeOptions(value.Volume) || value.ReadOnly {
				return false
			}
			spec.Mounts[index] = domain.Mount{Kind: domain.MountVolume, Target: value.Target}
		default:
			return false
		}
	}

	return true
}

func emptyBindOptions(value *composetypes.ServiceVolumeBind) bool {
	return value == nil || value.SELinux == "" && value.Propagation == "" && value.Recursive == "" &&
		len(value.Extensions) == 0
}

func rebaseRepositoryPath(value, from, to string) (string, bool) {
	if from == "" {
		return value, true
	}
	relative, _ := filepath.Rel(from, value)
	if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return value, true
	}
	rebased := filepath.Join(to, relative)

	return rebased, absoluteCleanPath(rebased)
}

func emptyVolumeOptions(value *composetypes.ServiceVolumeVolume) bool {
	return value == nil || len(value.Labels) == 0 && !value.NoCopy && value.Subpath == "" && len(value.Extensions) == 0
}

//nolint:cyclop // Healthcheck disable and timing semantics form one fail-closed boundary.
func addHealthcheck(spec *domain.WorkloadSpec, value *composetypes.HealthCheckConfig) bool {
	if value == nil {
		return true
	}
	if len(value.Extensions) != 0 || value.Disable && (len(value.Test) != 0 || value.Timeout != nil ||
		value.Interval != nil || value.Retries != nil || value.StartPeriod != nil || value.StartInterval != nil) {
		return false
	}
	healthcheck := domain.Healthcheck{Test: slices.Clone(value.Test), Disabled: value.Disable}
	healthcheck.Timeout = durationString(value.Timeout)
	healthcheck.Interval = durationString(value.Interval)
	healthcheck.StartPeriod = durationString(value.StartPeriod)
	healthcheck.StartInterval = durationString(value.StartInterval)
	if value.Retries != nil {
		maximumInt := uint64(^uint(0) >> 1)
		if *value.Retries > maximumInt {
			return false
		}
		if *value.Retries != 0 {
			retries := int(*value.Retries)
			healthcheck.Retries = &retries
		}
	}
	spec.Healthcheck = &healthcheck

	return true
}

func durationString(value *composetypes.Duration) string {
	if value == nil {
		return ""
	}

	return value.String()
}
