package domain

import (
	"slices"

	"github.com/IceCodeNew/maniud/containerconfig"
)

// WorkloadSpec is the runtime-neutral desired container configuration shared
// by runtime-command ingestion, Compose serialization, Compose loading, and
// runtime adapters. It intentionally contains no Compose or runtime wire types.
type WorkloadSpec = containerconfig.Spec

// DeviceMapping exposes one ordinary Linux device at a container path.
type DeviceMapping = containerconfig.DeviceMapping

// MountKind identifies one supported persistent container mount.
type MountKind = containerconfig.MountKind

const (
	// MountBind uses one absolute host path.
	MountBind = containerconfig.MountBind
	// MountVolume declares one anonymous volume destination.
	MountVolume = containerconfig.MountVolume
)

// Mount is one bind or anonymous-volume destination.
type Mount = containerconfig.Mount

// RuntimeMount is the exact persistent source identity observed from a
// container runtime. Anonymous volumes gain both Name and Source at creation;
// bind mounts keep Name empty and retain their requested host Source.
type RuntimeMount struct {
	Kind     MountKind
	Name     string
	Source   string
	Target   string
	ReadOnly bool
}

// TmpfsMount is one portable in-memory filesystem destination.
type TmpfsMount = containerconfig.TmpfsMount

// Ulimit is one portable POSIX resource limit.
type Ulimit = containerconfig.Ulimit

// PortBinding maps one host socket to a container port.
type PortBinding = containerconfig.PortBinding

// ExposedPort identifies one image-declared or service-declared container port.
type ExposedPort = containerconfig.ExposedPort

// Healthcheck is one native container health command and timing policy.
type Healthcheck = containerconfig.Healthcheck

func cloneHealthcheck(source *Healthcheck) *Healthcheck {
	if source == nil {
		return nil
	}
	clone := *source
	clone.Test = slices.Clone(source.Test)
	clone.Retries = cloneValue(source.Retries)

	return &clone
}

func cloneValue[T any](source *T) *T {
	if source == nil {
		return nil
	}
	clone := *source

	return &clone
}
