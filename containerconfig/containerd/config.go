// Package containerd translates portable container configuration to the OCI
// process contract and the control-plane settings containerd leaves to its
// client, such as CNI, restart, health, and anonymous-volume policy.
package containerd

import (
	"slices"

	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	allCapabilities        = "ALL"
	capabilityNetRaw       = "CAP_NET_RAW"
	cgroupPrivate          = "private"
	healthCommandExec      = "CMD"
	mountOptionNoExec      = "noexec"
	mountOptionNoDev       = "nodev"
	mountOptionNoSUID      = "nosuid"
	mountTypeTmpfs         = "tmpfs"
	networkBridge          = "bridge"
	operatingSystemLinux   = "linux"
	protocolSCTP           = "sctp"
	protocolTCP            = "tcp"
	protocolUDP            = "udp"
	sharedMemoryMountPoint = "/dev/shm"

	pathCapabilities = "/capabilities"
	pathDevices      = "/devices"
	pathDNS          = "/dns"
	pathEnvironment  = "/environment"
	pathHealthcheck  = "/healthcheck"
	pathLabels       = "/labels"
	pathMounts       = "/mounts"
	pathPlatform     = "/platform"
	pathPorts        = "/ports"
	pathRestart      = "/restart"
	pathServiceName  = "/service_name"
	pathTmpfs        = "/tmpfs"
	pathUlimits      = "/ulimits"
	pathUser         = "/user"
)

// Configuration is the complete phase-one containerd projection. OCI is sent
// to the runtime. Control contains settings implemented by the containerd
// client around the task and is kept separate so callers cannot mistake them
// for OCI runtime features.
type Configuration struct {
	OCI     specs.Spec
	Control Control
}

// Control contains portable fields that containerd's OCI task contract does
// not represent or cannot reconstruct without host and image state.
type Control struct {
	ServiceName       string
	ContainerName     string
	Platform          containerconfig.Platform
	EntrypointLength  int
	EntrypointDefined bool
	CommandDefined    bool
	NetworkMode       string
	CgroupParent      string
	CPUs              string
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
	Devices           []containerconfig.DeviceMapping
	ExtraHosts        []string
	GroupAdd          []string
	Labels            []string
	ExposedPorts      []containerconfig.ExposedPort
	Ports             []containerconfig.PortBinding
	Tmpfs             []containerconfig.TmpfsMount
	Mounts            []containerconfig.Mount
	Init              *bool
	StdinOpen         *bool
	OOMKillDisable    *bool
	ReadOnly          *bool
	TTY               *bool
	Healthcheck       *containerconfig.Healthcheck
}

func cloneHealthcheck(value *containerconfig.Healthcheck) *containerconfig.Healthcheck {
	if value == nil {
		return nil
	}
	clone := *value
	clone.Test = slices.Clone(value.Test)
	clone.Retries = cloneValue(value.Retries)

	return &clone
}

func cloneTmpfs(values []containerconfig.TmpfsMount) []containerconfig.TmpfsMount {
	clone := slices.Clone(values)
	for index := range clone {
		clone[index].Options = slices.Clone(clone[index].Options)
	}

	return clone
}

func cloneValue[T any](value *T) *T {
	if value == nil {
		return nil
	}
	clone := *value

	return &clone
}
