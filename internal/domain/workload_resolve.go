package domain

import (
	"maps"
	"slices"
	"strings"
)

// ResolveWorkloadSpec applies proven image defaults to one runtime-neutral
// service configuration. Explicit service values keep precedence.
func ResolveWorkloadSpec(spec WorkloadSpec, image ImageIdentity) WorkloadSpec {
	spec = spec.Clone()
	if spec.Entrypoint == nil {
		spec.Entrypoint = slices.Clone(image.Entrypoint)
	}
	if spec.Command == nil {
		spec.Command = slices.Clone(image.Command)
	}
	if spec.User == "" {
		spec.User = image.User
	}
	if spec.WorkingDirectory == "" {
		spec.WorkingDirectory = image.WorkingDirectory
	}
	if spec.StopSignal == "" {
		spec.StopSignal = image.StopSignal
	}
	if spec.Healthcheck == nil {
		spec.Healthcheck = cloneHealthcheck(image.Healthcheck)
	}
	spec.Environment = mergeImageKeyValues(image.Environment, spec.Environment, true)
	spec.Labels = mergeImageKeyValues(image.Labels, spec.Labels, false)
	spec.ExposedPorts = mergeExposedPorts(image.ExposedPorts, spec.ExposedPorts, spec.Ports)
	spec.Mounts = mergeImageVolumes(image.Volumes, spec.Mounts)

	return spec
}

func mergeImageKeyValues(image, service []string, unsetWithoutValue bool) []string {
	values := make(map[string]string, len(image)+len(service))
	for _, value := range image {
		key, _, _ := strings.Cut(value, "=")
		if !IsOwnershipLabel(key) {
			values[key] = value
		}
	}
	for _, value := range service {
		key, selected, found := strings.Cut(value, "=")
		if IsOwnershipLabel(key) {
			continue
		}
		if !found && unsetWithoutValue {
			delete(values, key)

			continue
		}
		values[key] = key + "=" + selected
	}

	return slices.Sorted(maps.Values(values))
}

func mergeExposedPorts(image, service []ExposedPort, published []PortBinding) []ExposedPort {
	values := make(map[string]ExposedPort, len(image)+len(service)+len(published))
	for _, value := range image {
		values[exposedPortKey(value)] = value
	}
	for _, value := range service {
		values[exposedPortKey(value)] = value
	}
	for _, value := range published {
		exposed := ExposedPort{TargetPort: value.TargetPort, Protocol: value.Protocol}
		values[exposedPortKey(exposed)] = exposed
	}
	result := slices.Collect(maps.Values(values))
	slices.SortFunc(result, func(left, right ExposedPort) int {
		return strings.Compare(exposedPortKey(left), exposedPortKey(right))
	})

	return result
}

func exposedPortKey(value ExposedPort) string {
	return value.Protocol + "\x00" + string(rune(value.TargetPort))
}

func mergeImageVolumes(image []string, service []Mount) []Mount {
	values := make(map[string]Mount, len(image)+len(service))
	for _, target := range image {
		values[target] = Mount{Kind: MountVolume, Target: target}
	}
	for _, value := range service {
		values[value.Target] = value
	}
	result := slices.Collect(maps.Values(values))
	slices.SortFunc(result, func(left, right Mount) int {
		return strings.Compare(left.Target, right.Target)
	})

	return result
}
