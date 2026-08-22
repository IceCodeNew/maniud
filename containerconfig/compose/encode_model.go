package compose

import (
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/containerconfig"
)

//nolint:tagliatelle // Compose defines these YAML field names.
type encodedService struct {
	Image           string                   `yaml:"image"`
	ContainerName   string                   `yaml:"container_name"`
	Platform        string                   `yaml:"platform"`
	Command         []string                 `yaml:"command,omitempty"`
	Entrypoint      []string                 `yaml:"entrypoint,omitempty"`
	NetworkMode     string                   `yaml:"network_mode"`
	BlkioConfig     *encodedBlkioConfig      `yaml:"blkio_config,omitempty"`
	CgroupParent    string                   `yaml:"cgroup_parent,omitempty"`
	Cgroup          string                   `yaml:"cgroup,omitempty"`
	CPUs            string                   `yaml:"cpus,omitempty"`
	Hostname        string                   `yaml:"hostname,omitempty"`
	MemLimit        int64                    `yaml:"mem_limit,omitempty"`
	OOMScoreAdj     *int                     `yaml:"oom_score_adj,omitempty"`
	PidsLimit       *int64                   `yaml:"pids_limit,omitempty"`
	Restart         string                   `yaml:"restart,omitempty"`
	ShmSize         int64                    `yaml:"shm_size,omitempty"`
	StopSignal      string                   `yaml:"stop_signal,omitempty"`
	StopGracePeriod string                   `yaml:"stop_grace_period,omitempty"`
	User            string                   `yaml:"user,omitempty"`
	WorkingDir      string                   `yaml:"working_dir,omitempty"`
	CapAdd          []string                 `yaml:"cap_add,omitempty"`
	CapDrop         []string                 `yaml:"cap_drop,omitempty"`
	DNS             []string                 `yaml:"dns,omitempty"`
	DNSOpt          []string                 `yaml:"dns_opt,omitempty"`
	DNSSearch       []string                 `yaml:"dns_search,omitempty"`
	Devices         []encodedDeviceMapping   `yaml:"devices,omitempty"`
	ExtraHosts      []string                 `yaml:"extra_hosts,omitempty"`
	GroupAdd        []string                 `yaml:"group_add,omitempty"`
	Sysctls         map[string]string        `yaml:"sysctls,omitempty"`
	Tmpfs           []string                 `yaml:"tmpfs,omitempty"`
	Ulimits         map[string]encodedUlimit `yaml:"ulimits,omitempty"`
	Environment     []string                 `yaml:"environment,omitempty"`
	EnvFile         []string                 `yaml:"env_file,omitempty"`
	Expose          []string                 `yaml:"expose,omitempty"`
	Labels          []string                 `yaml:"labels,omitempty"`
	Ports           []string                 `yaml:"ports,omitempty"`
	PullPolicy      string                   `yaml:"pull_policy,omitempty"`
	SecurityOpt     []string                 `yaml:"security_opt,omitempty"`
	Volumes         []encodedMount           `yaml:"volumes,omitempty"`
	Init            *bool                    `yaml:"init,omitempty"`
	StdinOpen       *bool                    `yaml:"stdin_open,omitempty"`
	OOMKillDisable  *bool                    `yaml:"oom_kill_disable,omitempty"`
	ReadOnly        *bool                    `yaml:"read_only,omitempty"`
	TTY             *bool                    `yaml:"tty,omitempty"`
	Healthcheck     *encodedHealthcheck      `yaml:"healthcheck,omitempty"`
}

type encodedBlkioConfig struct {
	Weight int `yaml:"weight"`
}

type encodedDeviceMapping struct {
	Source      string `yaml:"source"`
	Target      string `yaml:"target"`
	Permissions string `yaml:"permissions"`
}

type encodedMount struct {
	short string
	bind  *encodedBindMount
}

func (value encodedMount) MarshalYAML() (any, error) {
	if value.bind != nil {
		return *value.bind, nil
	}

	return value.short, nil
}

type encodedBindMount struct {
	Type     string `yaml:"type"`
	Source   string `yaml:"source"`
	Target   string `yaml:"target"`
	ReadOnly bool   `yaml:"read_only,omitempty"` //nolint:tagliatelle // Compose defines this key.
}

type encodedUlimit struct {
	Soft int64
	Hard int64
}

func (value encodedUlimit) MarshalYAML() (any, error) {
	if value.Soft == value.Hard {
		return value.Soft, nil
	}

	return struct {
		Soft int64 `yaml:"soft"`
		Hard int64 `yaml:"hard"`
	}{Soft: value.Soft, Hard: value.Hard}, nil
}

//nolint:tagliatelle // Compose defines these YAML field names.
type encodedHealthcheck struct {
	Test          []string `yaml:"test,omitempty"`
	Interval      string   `yaml:"interval,omitempty"`
	Timeout       string   `yaml:"timeout,omitempty"`
	Retries       *int     `yaml:"retries,omitempty"`
	StartPeriod   string   `yaml:"start_period,omitempty"`
	StartInterval string   `yaml:"start_interval,omitempty"`
	Disable       bool     `yaml:"disable,omitempty"`
}

type encodedDocument struct {
	Name       string                    `yaml:"name"`
	Services   map[string]encodedService `yaml:"services"`
	Extensions map[string]any            `yaml:",inline"`
}

func documentFromSpec(spec containerconfig.Spec, image, projectName string) encodedDocument {
	service := encodedService{
		Image: image, ContainerName: spec.ContainerName, Platform: FormatPlatform(spec.Platform),
		Command: spec.Command, Entrypoint: spec.Entrypoint, NetworkMode: spec.NetworkMode,
		CgroupParent: spec.CgroupParent, Cgroup: spec.Cgroup, CPUs: spec.CPUs,
		Hostname: spec.Hostname, MemLimit: spec.MemoryBytes, OOMScoreAdj: spec.OOMScoreAdj,
		PidsLimit: spec.PidsLimit, Restart: spec.Restart, ShmSize: spec.SharedMemoryBytes,
		StopSignal: spec.StopSignal, User: spec.User, WorkingDir: spec.WorkingDirectory,
		CapAdd: spec.CapAdd, CapDrop: spec.CapDrop, DNS: spec.DNS,
		DNSOpt: spec.DNSOptions, DNSSearch: spec.DNSSearch,
		ExtraHosts: spec.ExtraHosts, GroupAdd: spec.GroupAdd, Sysctls: spec.Sysctls,
		Environment: spec.Environment, Expose: encodedExposedPorts(spec.ExposedPorts), Labels: spec.Labels,
		Init: spec.Init, StdinOpen: spec.StdinOpen, OOMKillDisable: spec.OOMKillDisable,
		ReadOnly: spec.ReadOnly, TTY: spec.TTY,
	}
	if spec.BlkioWeight != nil {
		service.BlkioConfig = &encodedBlkioConfig{Weight: *spec.BlkioWeight}
	}
	if spec.StopTimeout != nil {
		service.StopGracePeriod = strconv.FormatInt(*spec.StopTimeout, 10) + "s"
	}
	service.Devices = encodedDevices(spec.Devices)
	service.Tmpfs = encodedTmpfs(spec.Tmpfs)
	service.Ulimits = encodedUlimits(spec.Ulimits)
	service.Ports = encodedPorts(spec.Ports)
	service.Volumes = encodedMounts(spec.Mounts)
	if spec.NoNewPrivileges {
		service.SecurityOpt = []string{"no-new-privileges:true"}
	}
	if spec.Healthcheck != nil {
		service.Healthcheck = &encodedHealthcheck{
			Test: spec.Healthcheck.Test, Interval: spec.Healthcheck.Interval,
			Timeout: spec.Healthcheck.Timeout, Retries: spec.Healthcheck.Retries,
			StartPeriod: spec.Healthcheck.StartPeriod, StartInterval: spec.Healthcheck.StartInterval,
			Disable: spec.Healthcheck.Disabled,
		}
	}

	return encodedDocument{Name: projectName, Services: map[string]encodedService{spec.ServiceName: service}}
}

// FormatPlatform returns the canonical Compose platform string.
func FormatPlatform(platform containerconfig.Platform) string {
	value := platform.OS + "/" + platform.Architecture
	if platform.Variant != "" {
		value += "/" + platform.Variant
	}

	return value
}

func encodedDevices(values []containerconfig.DeviceMapping) []encodedDeviceMapping {
	result := make([]encodedDeviceMapping, len(values))
	for index, value := range values {
		result[index] = encodedDeviceMapping(value)
	}

	return result
}

func encodedTmpfs(values []containerconfig.TmpfsMount) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = value.Target
		if len(value.Options) != 0 {
			result[index] += ":" + strings.Join(value.Options, ",")
		}
	}

	return result
}

func encodedUlimits(values []containerconfig.Ulimit) map[string]encodedUlimit {
	if values == nil {
		return nil
	}
	result := make(map[string]encodedUlimit, len(values))
	for _, value := range values {
		result[value.Name] = encodedUlimit{Soft: value.Soft, Hard: value.Hard}
	}

	return result
}

func encodedPorts(values []containerconfig.PortBinding) []string {
	result := make([]string, len(values))
	for index, value := range values {
		host := value.HostIP
		if strings.ContainsRune(host, ':') {
			host = "[" + host + "]"
		}
		if host != "" {
			host += ":"
		}
		result[index] = host + strconv.FormatUint(uint64(value.PublishedPort), 10) + ":" +
			strconv.FormatUint(uint64(value.TargetPort), 10)
		if value.Protocol != protocolTCP {
			result[index] += "/" + value.Protocol
		}
	}

	return result
}

func encodedExposedPorts(values []containerconfig.ExposedPort) []string {
	result := make([]string, len(values))
	for index, value := range values {
		result[index] = strconv.FormatUint(uint64(value.TargetPort), 10)
		if value.Protocol != protocolTCP {
			result[index] += "/" + value.Protocol
		}
	}

	return result
}

func encodedMounts(values []containerconfig.Mount) []encodedMount {
	result := make([]encodedMount, len(values))
	for index, value := range values {
		if value.Kind == containerconfig.MountBind {
			result[index].bind = &encodedBindMount{
				Type: bindMount, Source: value.Source, Target: value.Target, ReadOnly: value.ReadOnly,
			}
		} else {
			result[index].short = value.Target
		}
	}

	return result
}
