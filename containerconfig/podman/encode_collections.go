package podman

import (
	"net/netip"
	"slices"
	"strconv"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
)

//nolint:cyclop // Exposed and published port sets must remain unambiguous.
func createPorts(exposedValues []containerconfig.ExposedPort, values []containerconfig.PortBinding) (
	[]createPort,
	map[uint16]string,
	bool,
) {
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
	ports := make([]createPort, len(values))
	for index, value := range values {
		if value.TargetPort == 0 || value.PublishedPort == 0 ||
			value.Protocol != protocolTCP && value.Protocol != protocolUDP {
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
		ports[index] = createPort{
			HostIP: value.HostIP, ContainerPort: value.TargetPort,
			HostPort: value.PublishedPort, Range: 1, Protocol: value.Protocol,
		}
	}
	if len(exposed) == 0 {
		exposed = nil
	}

	return ports, exposed, true
}

func validExposedProtocol(value string) bool {
	return value == protocolTCP || value == protocolUDP || value == protocolSCTP
}

//nolint:cyclop // Bind and tmpfs destinations share one collision domain.
func createMounts(values []containerconfig.Mount, tmpfs []containerconfig.TmpfsMount, copyImageVolumes bool) (
	[]createMount,
	[]createVolume,
	bool,
) {
	result := make([]createMount, 0, len(values)+len(tmpfs))
	volumes := make([]createVolume, 0, len(values))
	targets := make(map[string]struct{}, len(values)+len(tmpfs))
	for _, value := range values {
		if value.Target == "" || !validText(value.Target) {
			return nil, nil, false
		}
		if _, exists := targets[value.Target]; exists {
			return nil, nil, false
		}
		targets[value.Target] = struct{}{}
		switch value.Kind {
		case containerconfig.MountBind:
			if value.Source == "" || !validText(value.Source) {
				return nil, nil, false
			}
			options := []string{recursiveBind, "rw"}
			if value.ReadOnly {
				options[1] = "ro"
			}
			result = append(result, createMount{
				Destination: value.Target, Type: mountBind, Source: value.Source, Options: options,
			})
		case containerconfig.MountVolume:
			if value.Source != "" || value.ReadOnly {
				return nil, nil, false
			}
			volume := createVolume{Dest: value.Target, IsAnonymous: true}
			if !copyImageVolumes {
				volume.Options = []string{"nocopy"}
			}
			volumes = append(volumes, volume)
		default:
			return nil, nil, false
		}
	}
	for _, value := range tmpfs {
		if value.Target == "" || !validText(value.Target) || !validStrings(value.Options) {
			return nil, nil, false
		}
		if _, exists := targets[value.Target]; exists {
			return nil, nil, false
		}
		targets[value.Target] = struct{}{}
		result = append(result, createMount{
			Destination: value.Target, Type: "tmpfs", Source: "tmpfs", Options: slices.Clone(value.Options),
		})
	}

	return result, volumes, true
}

//nolint:cyclop // POSIX soft and hard limit invariants are checked together.
func createUlimits(values []containerconfig.Ulimit) ([]createUlimit, bool) {
	result := make([]createUlimit, len(values))
	seen := make(map[string]struct{}, len(values))
	for index, value := range values {
		name := strings.ToUpper(value.Name)
		if name == "" || !validText(name) || value.Soft < -1 || value.Hard < -1 ||
			(value.Soft == -1 && value.Hard != -1) || value.Hard != -1 && value.Soft > value.Hard {
			return nil, false
		}
		if _, exists := seen[name]; exists {
			return nil, false
		}
		seen[name] = struct{}{}
		result[index] = createUlimit{Type: "RLIMIT_" + name, Soft: value.Soft, Hard: value.Hard}
	}

	return result, true
}

//nolint:cyclop // Disabled and command healthchecks have disjoint field invariants.
func createHealthcheck(value *containerconfig.Healthcheck) (*healthConfig, bool) {
	if value == nil {
		return nil, true
	}
	if value.Disabled {
		if len(value.Test) != 0 || value.Interval != "" || value.Timeout != "" || value.Retries != nil ||
			value.StartPeriod != "" || value.StartInterval != "" {
			return nil, false
		}

		return &healthConfig{Test: []string{healthcheckNone}}, true
	}
	interval, intervalValid := parseDuration(value.Interval)
	timeout, timeoutValid := parseDuration(value.Timeout)
	startPeriod, startPeriodValid := parseDuration(value.StartPeriod)
	startInterval, startIntervalValid := parseDuration(value.StartInterval)
	if !intervalValid || !timeoutValid || !startPeriodValid || !startIntervalValid ||
		value.Retries != nil && *value.Retries <= 0 || !validStrings(value.Test) {
		return nil, false
	}
	retries := 0
	if value.Retries != nil {
		retries = *value.Retries
	}

	return &healthConfig{
		Test: slices.Clone(value.Test), Interval: interval, Timeout: timeout, Retries: retries,
		StartPeriod: startPeriod, StartInterval: startInterval,
	}, true
}

func parseDuration(value string) (time.Duration, bool) {
	if value == "" {
		return 0, true
	}
	duration, err := time.ParseDuration(value)

	return duration, err == nil && duration > 0
}

func nonzeroInt64(value int64) *int64 {
	if value == 0 {
		return nil
	}
	result := value

	return &result
}

func boolPointer(value bool) *bool {
	if !value {
		return nil
	}
	result := true

	return &result
}
