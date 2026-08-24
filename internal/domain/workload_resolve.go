package domain

import (
	"maps"
	"slices"
	"strings"

	"github.com/IceCodeNew/maniud/containerconfig"
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

	return containerconfig.Canonical(spec)
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

	return slices.Collect(maps.Values(values))
}

func mergeExposedPorts(image, service []ExposedPort, published []PortBinding) []ExposedPort {
	values := make(map[ExposedPort]struct{}, len(image)+len(service)+len(published))
	for _, value := range image {
		values[value] = struct{}{}
	}
	for _, value := range service {
		values[value] = struct{}{}
	}
	for _, value := range published {
		exposed := ExposedPort{TargetPort: value.TargetPort, Protocol: value.Protocol}
		values[exposed] = struct{}{}
	}

	return slices.Collect(maps.Keys(values))
}

func mergeImageVolumes(image []string, service []Mount) []Mount {
	values := make(map[string]Mount, len(image)+len(service))
	for _, target := range image {
		values[target] = Mount{Kind: MountVolume, Target: target}
	}
	for _, value := range service {
		values[value.Target] = value
	}

	return slices.Collect(maps.Values(values))
}
