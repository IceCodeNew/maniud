// Package compose translates Compose service configuration to and from the
// portable containerconfig contract.
package compose

import (
	"maps"
	"reflect"
	"slices"
	"strconv"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	bridgeNetwork = "bridge"
	bindMount     = "bind"
	volumeMount   = "volume"
	protocolTCP   = "tcp"
	protocolUDP   = "udp"
)

// PathMapping rebases bind sources resolved beneath From to the corresponding
// location beneath To. Sources outside From remain unchanged.
type PathMapping struct {
	From string
	To   string
}

// ServiceOptions declares syntax that a caller handles outside the portable
// container configuration contract.
type ServiceOptions struct {
	AllowPullPolicy bool
}

// ValidateService checks whether a normalized Compose service can be
// represented without loss by Spec.
func ValidateService(service composetypes.ServiceConfig, options ServiceOptions) error {
	_, err := FromService(service, containerconfig.Platform{}, PathMapping{}, options)

	return err
}

func validateServiceShape(service composetypes.ServiceConfig, options ServiceOptions) error {
	value := reflect.ValueOf(service)
	valueType := value.Type()
	for index := range valueType.NumField() {
		field := valueType.Field(index)
		if supportedServiceField(field.Name, options.AllowPullPolicy) || value.Field(index).IsZero() {
			continue
		}

		return validationError(containerconfig.ValidationUnsupportedField, servicePath(service.Name, yamlField(field)))
	}
	if !validContainerName(service.ContainerName) {
		return validationError(containerconfig.ValidationInvalidValue, servicePath(service.Name, "container_name"))
	}
	if service.NetworkMode != bridgeNetwork {
		return validationError(containerconfig.ValidationUnsupportedCapability, servicePath(service.Name, "network_mode"))
	}

	return nil
}

// FromService converts one normalized and validated Compose service into a
// portable Spec. Image defaults remain the caller's responsibility.
//
//nolint:funlen // The supported field projection stays adjacent to its fail-closed conversion checks.
func FromService(
	service composetypes.ServiceConfig,
	platform containerconfig.Platform,
	paths PathMapping,
	options ServiceOptions,
) (containerconfig.Spec, error) {
	if err := validateServiceShape(service, options); err != nil {
		return containerconfig.Spec{}, err
	}

	spec := containerconfig.Spec{
		ServiceName: service.Name, ContainerName: service.ContainerName, Platform: platform,
		Entrypoint: slices.Clone(service.Entrypoint), Command: slices.Clone(service.Command),
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

	checks := []struct {
		field string
		valid bool
	}{
		{"blkio_config", addBlkio(&spec, service.BlkioConfig)},
		{"stop_grace_period", addStopTimeout(&spec, service.StopGracePeriod)},
		{"devices", addDevices(&spec, service.Devices)},
		{"tmpfs", addTmpfs(&spec, service.Tmpfs)},
		{"ulimits", addUlimits(&spec, service.Ulimits)},
		{"expose", addExposedPorts(&spec, service.Expose)},
		{"ports", addPorts(&spec, service.Ports)},
		{"security_opt", addSecurityOptions(&spec, service.SecurityOpt)},
		{"volumes", addMounts(&spec, service.Volumes, paths)},
		{"healthcheck", addHealthcheck(&spec, service.HealthCheck)},
	}
	for _, check := range checks {
		if !check.valid {
			return containerconfig.Spec{}, validationError(
				containerconfig.ValidationInvalidValue,
				servicePath(service.Name, check.field),
			)
		}
	}

	return containerconfig.Canonical(spec), nil
}

func supportedServiceField(name string, allowPullPolicy bool) bool {
	switch name {
	case "BlkioConfig", "CapAdd", "CapDrop", "CgroupParent", "Cgroup", "CPUS", "Command",
		"ContainerName", "Devices", "DNS", "DNSOpts", "DNSSearch", "Entrypoint", "Environment",
		"Expose", "ExtraHosts", "GroupAdd", "HealthCheck", "Hostname", "Image", "Init", "Labels", "MemLimit",
		"Name", "NetworkMode", "OomKillDisable", "OomScoreAdj", "PidsLimit", "Platform", "Ports",
		"Profiles", "ReadOnly", "Restart", "SecurityOpt", "ShmSize", "StdinOpen", "StopGracePeriod",
		"StopSignal", "Sysctls", "Tmpfs", "Tty", "Ulimits", "User", "Volumes", "WorkingDir":
		return true
	case "PullPolicy":
		return allowPullPolicy
	default:
		return false
	}
}

func yamlField(field reflect.StructField) string {
	name, _, _ := strings.Cut(field.Tag.Get("yaml"), ",")
	if name == "" || name == "-" || strings.HasPrefix(name, "#") {
		return strings.ToLower(field.Name)
	}

	return name
}

func servicePath(name, field string) string {
	return "/services/" + pointerToken(name) + "/" + pointerToken(field)
}

func pointerToken(value string) string {
	return strings.ReplaceAll(strings.ReplaceAll(value, "~", "~0"), "/", "~1")
}

func validationError(code containerconfig.ValidationCode, path string) error {
	return containerconfig.ValidationError{Code: code, Path: path}
}

func validContainerName(name string) bool {
	if len(name) == 0 || len(name) > 63 || !lowerAlphaNumeric(name[0]) || !lowerAlphaNumeric(name[len(name)-1]) {
		return false
	}
	for index := 1; index < len(name)-1; index++ {
		if name[index] != '-' && !lowerAlphaNumeric(name[index]) {
			return false
		}
	}

	return true
}

func lowerAlphaNumeric(value byte) bool {
	return value >= 'a' && value <= 'z' || value >= '0' && value <= '9'
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
