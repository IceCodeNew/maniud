package application

import (
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const backupRootName = "backups"

// backedStorageSource is one writable runtime mount that upgrade must copy.
type backedStorageSource struct {
	Mount domain.RuntimeMount
}

func backupRootPath(statePath string) string {
	return filepath.Join(filepath.Dir(statePath), backupRootName)
}

func backedStorageSources(observation WorkloadObservation, workload domain.DesiredWorkload) []backedStorageSource {
	if observation.State != WorkloadObservationPresent {
		return nil
	}

	sources := make([]backedStorageSource, 0, len(observation.RuntimeMounts))
	for _, mount := range observation.RuntimeMounts {
		if !isBackedStorageSource(mount, workload) {
			continue
		}

		sources = append(sources, backedStorageSource{Mount: mount})
	}

	if len(sources) == 0 {
		return nil
	}

	return sources
}

func isBackedStorageSource(mount domain.RuntimeMount, workload domain.DesiredWorkload) bool {
	if mount.Target == "" || mount.Source == "" || mount.ReadOnly {
		return false
	}

	switch mount.Kind {
	case domain.MountVolume:
		return mount.Name != ""
	case domain.MountBind:
		return writableBindNeedsCopy(mount, workload)
	default:
		return false
	}
}

func writableBindNeedsCopy(mount domain.RuntimeMount, workload domain.DesiredWorkload) bool {
	for _, desired := range workload.Mounts {
		if desired.Target != mount.Target {
			continue
		}

		return desired.Kind == domain.MountBind && !desired.ReadOnly && desired.Source != mount.Source
	}

	return false
}

func upgradeCreateOptions(sources []backedStorageSource) WorkloadCreateOptions {
	options := defaultWorkloadCreateOptions()
	for _, source := range sources {
		if source.Mount.Kind == domain.MountVolume {
			options.CopyImageVolumes = false

			return options
		}
	}

	return options
}

func replacementBindSources(sources []backedStorageSource, workload domain.DesiredWorkload) []backedStorageSource {
	replacements := make([]backedStorageSource, 0, len(sources))
	for _, source := range sources {
		if source.Mount.Kind != domain.MountBind {
			continue
		}

		desired, found := desiredWritableBind(workload, source.Mount.Target)
		if !found || desired.Source == "" || desired.Source == source.Mount.Source {
			continue
		}

		replacements = append(replacements, backedStorageSource{Mount: source.Mount})
	}

	if len(replacements) == 0 {
		return nil
	}

	return replacements
}

func desiredWritableBind(workload domain.DesiredWorkload, target string) (domain.Mount, bool) {
	for _, desired := range workload.Mounts {
		if desired.Target != target || desired.Kind != domain.MountBind || desired.ReadOnly {
			continue
		}

		return desired, true
	}

	return domain.Mount{}, false
}
