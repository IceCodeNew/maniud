package compose

import (
	"path/filepath"
	"strings"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/containerconfig"
)

func addBlkio(spec *containerconfig.Spec, config *composetypes.BlkioConfig) bool {
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

func addStopTimeout(spec *containerconfig.Spec, value *composetypes.Duration) bool {
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

func addDevices(spec *containerconfig.Spec, values []composetypes.DeviceMapping) bool {
	if values == nil {
		return true
	}
	spec.Devices = make([]containerconfig.DeviceMapping, len(values))
	for index, value := range values {
		if len(value.Extensions) != 0 || !absoluteCleanPath(value.Source) || !absoluteCleanPath(value.Target) ||
			!validDevicePermissions(value.Permissions) {
			return false
		}
		spec.Devices[index] = containerconfig.DeviceMapping{
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

func addTmpfs(spec *containerconfig.Spec, values composetypes.StringList) bool {
	if values == nil {
		return true
	}
	spec.Tmpfs = make([]containerconfig.TmpfsMount, len(values))
	for index, value := range values {
		target, options, found := strings.Cut(value, ":")
		if !absoluteCleanPath(target) || found && options == "" {
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
func addUlimits(spec *containerconfig.Spec, values map[string]*composetypes.UlimitsConfig) bool {
	if values == nil {
		return true
	}
	spec.Ulimits = make([]containerconfig.Ulimit, 0, len(values))
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
		if soft < -1 || hard < -1 || soft == -1 && hard != -1 || hard != -1 && soft > hard {
			return false
		}
		spec.Ulimits = append(spec.Ulimits, containerconfig.Ulimit{Name: name, Soft: soft, Hard: hard})
	}

	return true
}
