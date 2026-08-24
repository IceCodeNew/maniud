package podman

import (
	"maps"
	"path"
	"slices"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
)

//nolint:cyclop,funlen // Native metadata proves each supported persistent mount variant.
func observedMounts(values []inspectMount, binds []string) ([]containerconfig.Mount, []RuntimeMount, bool) {
	result := make([]containerconfig.Mount, 0, len(values))
	runtimeMounts := make([]RuntimeMount, 0, len(values))
	observedBinds := make(map[string]struct{}, len(binds))
	targets := make(map[string]struct{}, len(values))
	for _, value := range binds {
		if !validText(value) {
			return nil, nil, false
		}
		if _, exists := observedBinds[value]; exists {
			return nil, nil, false
		}
		observedBinds[value] = struct{}{}
	}
	for _, value := range values {
		if value.Source == "" || value.Destination == "" || !validText(value.Source) ||
			!validText(value.Destination) || value.Mode != "" || value.SubPath != "" {
			return nil, nil, false
		}
		if _, duplicate := targets[value.Destination]; duplicate {
			return nil, nil, false
		}
		targets[value.Destination] = struct{}{}
		switch value.Type {
		case mountBind:
			if value.Name != "" || value.Driver != "" || !slices.Equal(value.Options, []string{recursiveBind}) ||
				value.Propagation != propagationPrivate {
				return nil, nil, false
			}
			mode := "rw"
			if !value.ReadWrite {
				mode = "ro"
			}
			expectedBind := value.Source + ":" + value.Destination + ":" + recursiveBind + "," + mode +
				"," + propagationPrivate
			if _, found := observedBinds[expectedBind]; !found {
				return nil, nil, false
			}
			delete(observedBinds, expectedBind)
			result = append(result, containerconfig.Mount{
				Kind: containerconfig.MountBind, Source: value.Source,
				Target: value.Destination, ReadOnly: !value.ReadWrite,
			})
			runtimeMounts = append(runtimeMounts, RuntimeMount{
				Kind: containerconfig.MountBind, Source: value.Source,
				Target: value.Destination, ReadOnly: !value.ReadWrite,
			})
		case mountVolume:
			if value.Name == "" || !validText(value.Name) || value.Driver != volumeDriverLocal ||
				!path.IsAbs(value.Source) || path.Clean(value.Source) != value.Source ||
				!value.ReadWrite || len(value.Options) != 0 || value.Propagation != "" {
				return nil, nil, false
			}
			result = append(result, containerconfig.Mount{
				Kind: containerconfig.MountVolume, Target: value.Destination,
			})
			runtimeMounts = append(runtimeMounts, RuntimeMount{
				Kind: containerconfig.MountVolume, Name: value.Name,
				Source: value.Source, Target: value.Destination,
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
	slices.SortFunc(runtimeMounts, func(left, right RuntimeMount) int {
		return strings.Compare(left.Target, right.Target)
	})

	return result, runtimeMounts, true
}

//nolint:cyclop // Disabled and command healthchecks have disjoint field invariants.
func observedHealthcheck(value *healthConfig) (*containerconfig.Healthcheck, bool) {
	if value == nil {
		return nil, true
	}
	if slices.Equal(value.Test, []string{healthcheckNone}) {
		if value.Interval != 0 || value.Timeout != 0 || value.Retries != 0 ||
			value.StartPeriod != 0 || value.StartInterval != 0 {
			return nil, false
		}

		return &containerconfig.Healthcheck{Disabled: true}, true
	}
	if !validStrings(value.Test) || value.Interval < 0 || value.Timeout < 0 || value.Retries < 0 ||
		value.StartPeriod < 0 || value.StartInterval < 0 {
		return nil, false
	}
	result := &containerconfig.Healthcheck{
		Test: slices.Clone(value.Test), Interval: durationString(value.Interval),
		Timeout: durationString(value.Timeout), StartPeriod: durationString(value.StartPeriod),
		StartInterval: durationString(value.StartInterval),
	}
	if value.Retries != 0 {
		retries := value.Retries
		result.Retries = &retries
	}

	return result, true
}

func durationString(value time.Duration) string {
	if value == 0 {
		return ""
	}

	return value.String()
}

func observedSecurity(values []string) (bool, bool) {
	if len(values) == 0 {
		return false, true
	}
	if len(values) == 1 && (values[0] == noNewPrivileges ||
		values[0] == noNewPrivileges+"=true" || values[0] == noNewPrivileges+":true") {
		return true, true
	}

	return false, false
}

func optionalInt(value int) *int {
	if value == 0 {
		return nil
	}
	result := value

	return &result
}

func truePointer(value bool) *bool {
	if !value {
		return nil
	}
	result := true

	return &result
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	maps.Copy(clone, source)

	return clone
}
