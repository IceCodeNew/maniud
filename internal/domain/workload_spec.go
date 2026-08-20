package domain

import (
	"maps"
	"slices"
)

// WorkloadSpec is the runtime-neutral desired container configuration shared
// by runtime-command ingestion, Compose serialization, Compose loading, and
// runtime adapters. It intentionally contains no Compose or runtime wire types.
type WorkloadSpec struct {
	ServiceName       string
	ContainerName     string
	Platform          Platform
	Entrypoint        []string
	Command           []string
	NetworkMode       string
	BlkioWeight       *int
	CgroupParent      string
	Cgroup            string
	CPUs              string
	Hostname          string
	MemoryBytes       int64
	OOMScoreAdj       *int
	PidsLimit         *int64
	Restart           string
	SharedMemoryBytes int64
	StopSignal        string
	StopTimeout       *int64
	User              string
	WorkingDirectory  string
	CapAdd            []string
	CapDrop           []string
	DNS               []string
	DNSOptions        []string
	DNSSearch         []string
	Devices           []DeviceMapping
	ExtraHosts        []string
	GroupAdd          []string
	Sysctls           map[string]string
	Tmpfs             []TmpfsMount
	Ulimits           []Ulimit
	Environment       []string
	Labels            []string
	ExposedPorts      []ExposedPort
	Ports             []PortBinding
	NoNewPrivileges   bool
	Mounts            []Mount
	Init              *bool
	StdinOpen         *bool
	OOMKillDisable    *bool
	ReadOnly          *bool
	TTY               *bool
	Healthcheck       *Healthcheck
}

// DeviceMapping exposes one ordinary Linux device at a container path.
type DeviceMapping struct {
	Source      string
	Target      string
	Permissions string
}

// MountKind identifies one supported persistent container mount.
type MountKind uint8

const (
	// MountBind uses one absolute host path.
	MountBind MountKind = iota + 1
	// MountVolume declares one anonymous volume destination.
	MountVolume
)

// Mount is one bind or anonymous-volume destination.
type Mount struct {
	Kind     MountKind
	Source   string
	Target   string
	ReadOnly bool
}

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
type TmpfsMount struct {
	Target  string
	Options []string
}

// Ulimit is one portable POSIX resource limit.
type Ulimit struct {
	Name string
	Soft int64
	Hard int64
}

// PortBinding maps one host socket to a container port.
type PortBinding struct {
	HostIP        string
	PublishedPort uint16
	TargetPort    uint16
	Protocol      string
}

// ExposedPort identifies one image-declared or service-declared container port.
type ExposedPort struct {
	TargetPort uint16
	Protocol   string
}

// Healthcheck is one native container health command and timing policy.
type Healthcheck struct {
	Test          []string
	Interval      string
	Timeout       string
	Retries       *int
	StartPeriod   string
	StartInterval string
	Disabled      bool
}

// Clone returns a deep copy that callers may mutate without sharing state.
func (spec WorkloadSpec) Clone() WorkloadSpec {
	clone := spec
	clone.BlkioWeight = cloneValue(spec.BlkioWeight)
	clone.OOMScoreAdj = cloneValue(spec.OOMScoreAdj)
	clone.PidsLimit = cloneValue(spec.PidsLimit)
	clone.StopTimeout = cloneValue(spec.StopTimeout)
	clone.Init = cloneValue(spec.Init)
	clone.StdinOpen = cloneValue(spec.StdinOpen)
	clone.OOMKillDisable = cloneValue(spec.OOMKillDisable)
	clone.ReadOnly = cloneValue(spec.ReadOnly)
	clone.TTY = cloneValue(spec.TTY)
	clone.Entrypoint = slices.Clone(spec.Entrypoint)
	clone.Command = slices.Clone(spec.Command)
	clone.CapAdd = slices.Clone(spec.CapAdd)
	clone.CapDrop = slices.Clone(spec.CapDrop)
	clone.DNS = slices.Clone(spec.DNS)
	clone.DNSOptions = slices.Clone(spec.DNSOptions)
	clone.DNSSearch = slices.Clone(spec.DNSSearch)
	clone.Devices = slices.Clone(spec.Devices)
	clone.ExtraHosts = slices.Clone(spec.ExtraHosts)
	clone.GroupAdd = slices.Clone(spec.GroupAdd)
	clone.Sysctls = cloneStringMap(spec.Sysctls)
	clone.Tmpfs = cloneTmpfs(spec.Tmpfs)
	clone.Ulimits = slices.Clone(spec.Ulimits)
	clone.Environment = slices.Clone(spec.Environment)
	clone.Labels = slices.Clone(spec.Labels)
	clone.ExposedPorts = slices.Clone(spec.ExposedPorts)
	clone.Ports = slices.Clone(spec.Ports)
	clone.Mounts = slices.Clone(spec.Mounts)
	clone.Healthcheck = cloneHealthcheck(spec.Healthcheck)

	return clone
}

func cloneStringMap(source map[string]string) map[string]string {
	if source == nil {
		return nil
	}
	clone := make(map[string]string, len(source))
	maps.Copy(clone, source)

	return clone
}

func cloneTmpfs(source []TmpfsMount) []TmpfsMount {
	clone := slices.Clone(source)
	for index := range clone {
		clone[index].Options = slices.Clone(clone[index].Options)
	}

	return clone
}

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
