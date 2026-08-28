package docker

import (
	"path"
	"slices"
	"strings"

	containerdocker "github.com/IceCodeNew/maniud/containerconfig/docker"
	containertypes "github.com/moby/moby/api/types/container"
	"github.com/moby/moby/api/types/mount"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const dockerVolumeDriverLocal = "local"

func dockerWorkloadFromInspect(
	name string,
	config *containertypes.Config,
	host *containertypes.HostConfig,
) (domain.WorkloadSpec, bool) {
	spec, err := containerdocker.Decode(name, config, host)
	if err != nil {
		return domain.WorkloadSpec{}, false
	}
	spec.Labels = slices.DeleteFunc(spec.Labels, func(value string) bool {
		key, _, _ := strings.Cut(value, "=")

		return domain.IsOwnershipLabel(key)
	})
	if len(spec.Labels) == 0 {
		spec.Labels = nil
	}

	return spec, true
}

// dockerRuntimeMounts binds each desired persistent target to the exact source
// identity reported by container inspect. Unsupported or additional mounts
// fail closed so backup code never acts on an ambiguous host path or volume.
//
//nolint:cyclop // Bind, volume, and tmpfs metadata have disjoint native invariants.
func dockerRuntimeMounts(
	values []containertypes.MountPoint,
	spec domain.WorkloadSpec,
) ([]domain.RuntimeMount, bool) {
	desired := make(map[string]domain.Mount, len(spec.Mounts))
	for _, value := range spec.Mounts {
		if _, duplicate := desired[value.Target]; duplicate {
			return nil, false
		}
		desired[value.Target] = value
	}
	tmpfs := make(map[string]struct{}, len(spec.Tmpfs))
	for _, value := range spec.Tmpfs {
		tmpfs[value.Target] = struct{}{}
	}
	result := make([]domain.RuntimeMount, 0, len(desired))
	for _, value := range values {
		if _, found := tmpfs[value.Destination]; found && value.Type == mount.TypeTmpfs {
			continue
		}
		expected, found := desired[value.Destination]
		if !found || value.Source == "" || !validDockerConfigurationText(value.Source) {
			return nil, false
		}
		delete(desired, value.Destination)
		switch expected.Kind {
		case domain.MountBind:
			if value.Type != mount.TypeBind || value.Name != "" || value.Driver != "" ||
				value.Source != expected.Source || value.RW == expected.ReadOnly || value.Mode != "" ||
				value.Propagation != mount.PropagationRPrivate {
				return nil, false
			}
			result = append(result, domain.RuntimeMount{
				Kind: domain.MountBind, Source: value.Source, Target: value.Destination, ReadOnly: !value.RW,
			})
		case domain.MountVolume:
			if value.Type != mount.TypeVolume || value.Name == "" || !validDockerConfigurationText(value.Name) ||
				value.Driver != dockerVolumeDriverLocal || !value.RW || value.Mode != "" || value.Propagation != "" ||
				!path.IsAbs(value.Source) || path.Clean(value.Source) != value.Source {
				return nil, false
			}
			result = append(result, domain.RuntimeMount{
				Kind: domain.MountVolume, Name: value.Name, Source: value.Source, Target: value.Destination,
			})
		default:
			return nil, false
		}
	}
	if len(desired) != 0 {
		return nil, false
	}
	if len(result) == 0 {
		return nil, true
	}
	slices.SortFunc(result, func(left, right domain.RuntimeMount) int {
		return strings.Compare(left.Target, right.Target)
	})

	return result, true
}
