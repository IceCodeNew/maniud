package compose

import (
	"net/netip"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/containerconfig"
)

func addExposedPorts(spec *containerconfig.Spec, values composetypes.StringOrNumberList) bool {
	if values == nil {
		return true
	}
	spec.ExposedPorts = make([]containerconfig.ExposedPort, len(values))
	for index, value := range values {
		port, protocol, found := strings.Cut(value, "/")
		if !found {
			protocol = protocolTCP
		}
		parsed, err := strconv.ParseUint(port, 10, 16)
		if err != nil || parsed == 0 || protocol != protocolTCP && protocol != protocolUDP {
			return false
		}
		spec.ExposedPorts[index] = containerconfig.ExposedPort{TargetPort: uint16(parsed), Protocol: protocol}
	}

	return true
}

//nolint:cyclop // Published-port parsing keeps every unsupported Compose option adjacent.
func addPorts(spec *containerconfig.Spec, values []composetypes.ServicePortConfig) bool {
	if values == nil {
		return true
	}
	spec.Ports = make([]containerconfig.PortBinding, len(values))
	for index, value := range values {
		published, err := strconv.ParseUint(value.Published, 10, 16)
		if err != nil || published == 0 || value.Target == 0 || value.Target > 65535 ||
			value.Protocol != protocolTCP && value.Protocol != protocolUDP ||
			value.Mode != "" && value.Mode != "ingress" || value.Name != "" || value.AppProtocol != "" ||
			len(value.Extensions) != 0 || !validHostIP(value.HostIP) {
			return false
		}
		spec.Ports[index] = containerconfig.PortBinding{
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

func addSecurityOptions(spec *containerconfig.Spec, values []string) bool {
	if len(values) == 0 {
		return true
	}
	if len(values) != 1 || values[0] != "no-new-privileges" && values[0] != "no-new-privileges:true" &&
		values[0] != "no-new-privileges=true" {
		return false
	}
	spec.NoNewPrivileges = true

	return true
}

//nolint:cyclop // Bind and anonymous-volume variants reject different Compose options.
func addMounts(
	spec *containerconfig.Spec,
	values []composetypes.ServiceVolumeConfig,
	paths PathMapping,
) bool {
	if values == nil {
		return true
	}
	spec.Mounts = make([]containerconfig.Mount, len(values))
	for index, value := range values {
		if !absoluteCleanPath(value.Target) || value.Consistency != "" || len(value.Extensions) != 0 {
			return false
		}
		switch value.Type {
		case bindMount:
			if !absoluteCleanPath(value.Source) || !emptyBindOptions(value.Bind) || value.Volume != nil ||
				value.Tmpfs != nil || value.Image != nil {
				return false
			}
			source, valid := rebasePath(value.Source, paths)
			if !valid {
				return false
			}
			spec.Mounts[index] = containerconfig.Mount{
				Kind: containerconfig.MountBind, Source: source, Target: value.Target, ReadOnly: value.ReadOnly,
			}
		case volumeMount:
			if value.Source != "" || value.Bind != nil || value.Tmpfs != nil || value.Image != nil ||
				!emptyVolumeOptions(value.Volume) || value.ReadOnly {
				return false
			}
			spec.Mounts[index] = containerconfig.Mount{Kind: containerconfig.MountVolume, Target: value.Target}
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

func rebasePath(value string, paths PathMapping) (string, bool) {
	if paths.From == "" {
		return value, true
	}
	relative, err := filepath.Rel(paths.From, value)
	if err != nil || relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
		return value, err == nil
	}
	rebased := filepath.Join(paths.To, relative)

	return rebased, absoluteCleanPath(rebased)
}

func emptyVolumeOptions(value *composetypes.ServiceVolumeVolume) bool {
	return value == nil || len(value.Labels) == 0 && !value.NoCopy && value.Subpath == "" && len(value.Extensions) == 0
}

//nolint:cyclop // Healthcheck disable and timing semantics form one fail-closed boundary.
func addHealthcheck(spec *containerconfig.Spec, value *composetypes.HealthCheckConfig) bool {
	if value == nil {
		return true
	}
	if len(value.Extensions) != 0 || value.Disable && (len(value.Test) != 0 || value.Timeout != nil ||
		value.Interval != nil || value.Retries != nil || value.StartPeriod != nil || value.StartInterval != nil) {
		return false
	}
	healthcheck := containerconfig.Healthcheck{Test: slices.Clone(value.Test), Disabled: value.Disable}
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
