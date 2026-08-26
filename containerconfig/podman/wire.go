package podman

import (
	"encoding/json"
	"time"
)

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectDocument struct {
	ID          string         `json:"Id"` //nolint:tagliatelle // Native Libpod wire field.
	Name        string         `json:"Name"`
	Image       string         `json:"Image"`
	ImageName   string         `json:"ImageName"`
	ImageDigest string         `json:"ImageDigest"`
	State       *inspectState  `json:"State"`
	Mounts      []inspectMount `json:"Mounts"`
	Config      *inspectConfig `json:"Config"`
	HostConfig  *inspectHost   `json:"HostConfig"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectState struct {
	Status     string `json:"Status"`
	Running    bool   `json:"Running"`
	Paused     bool   `json:"Paused"`
	Restarting bool   `json:"Restarting"`
	Dead       bool   `json:"Dead"`
}

//nolint:tagliatelle // Native Libpod health configuration owns these established wire names.
type healthConfig struct {
	Test          []string      `json:"Test"`
	Interval      time.Duration `json:"Interval"`
	Timeout       time.Duration `json:"Timeout"`
	Retries       int           `json:"Retries"`
	StartPeriod   time.Duration `json:"StartPeriod"`
	StartInterval time.Duration `json:"StartInterval"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectConfig struct {
	Image        string            `json:"Image"`
	Command      []string          `json:"Cmd"`
	Entrypoint   json.RawMessage   `json:"Entrypoint"`
	Labels       map[string]string `json:"Labels"`
	Environment  []string          `json:"Env"`
	Hostname     string            `json:"Hostname"`
	User         string            `json:"User"`
	WorkingDir   string            `json:"WorkingDir"`
	OpenStdin    bool              `json:"OpenStdin"`
	TTY          bool              `json:"Tty"`
	StopSignal   json.RawMessage   `json:"StopSignal"`
	StopTimeout  uint              `json:"StopTimeout"`
	Healthcheck  *healthConfig     `json:"Healthcheck"`
	ExposedPorts map[string]any    `json:"ExposedPorts"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectRestart struct {
	Name              string `json:"Name"`
	MaximumRetryCount uint   `json:"MaximumRetryCount"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectDevice struct {
	PathOnHost        string `json:"PathOnHost"`
	PathInContainer   string `json:"PathInContainer"`
	CgroupPermissions string `json:"CgroupPermissions"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectUlimit struct {
	Name string `json:"Name"`
	Soft int64  `json:"Soft"`
	Hard int64  `json:"Hard"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectPortBinding struct {
	HostIP   string `json:"HostIp"` //nolint:tagliatelle // Native Libpod wire field.
	HostPort string `json:"HostPort"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectHost struct {
	NetworkMode    string                          `json:"NetworkMode"`
	IPCMode        string                          `json:"IpcMode"`
	PIDMode        string                          `json:"PidMode"`
	UTSMode        string                          `json:"UTSMode"`
	CgroupMode     string                          `json:"CgroupMode"`
	Cgroups        string                          `json:"Cgroups"`
	CgroupParent   string                          `json:"CgroupParent"`
	NanoCPUs       int64                           `json:"NanoCpus"`
	CPUPeriod      uint64                          `json:"CpuPeriod"`
	CPUQuota       int64                           `json:"CpuQuota"`
	Memory         int64                           `json:"Memory"`
	OOMKillDisable bool                            `json:"OomKillDisable"`
	OOMScoreAdj    int                             `json:"OomScoreAdj"`
	PidsLimit      int64                           `json:"PidsLimit"`
	BlkioWeight    uint16                          `json:"BlkioWeight"`
	ShmSize        int64                           `json:"ShmSize"`
	RestartPolicy  *inspectRestart                 `json:"RestartPolicy"`
	CapAdd         []string                        `json:"CapAdd"`
	CapDrop        []string                        `json:"CapDrop"`
	DNS            []string                        `json:"Dns"`
	DNSSearch      []string                        `json:"DnsSearch"`
	DNSOptions     []string                        `json:"DnsOptions"`
	ExtraHosts     []string                        `json:"ExtraHosts"`
	GroupAdd       []string                        `json:"GroupAdd"`
	Devices        []inspectDevice                 `json:"Devices"`
	Binds          []string                        `json:"Binds"`
	Tmpfs          map[string]string               `json:"Tmpfs"`
	Ulimits        []inspectUlimit                 `json:"Ulimits"`
	PortBindings   map[string][]inspectPortBinding `json:"PortBindings"`
	Init           bool                            `json:"Init"`
	ReadonlyRootfs bool                            `json:"ReadonlyRootfs"`
	SecurityOpt    []string                        `json:"SecurityOpt"`
}

//nolint:tagliatelle // Native Libpod inspect owns these established wire names.
type inspectMount struct {
	Type        string   `json:"Type"`
	Name        string   `json:"Name"`
	Source      string   `json:"Source"`
	Destination string   `json:"Destination"`
	Driver      string   `json:"Driver"`
	Mode        string   `json:"Mode"`
	Options     []string `json:"Options"`
	ReadWrite   bool     `json:"RW"`
	Propagation string   `json:"Propagation"`
	SubPath     string   `json:"SubPath"`
}

type namespace struct {
	Mode  string `json:"nsmode,omitempty"`
	Value string `json:"value,omitempty"`
}

type createCPU struct {
	Quota  *int64  `json:"quota,omitempty"`
	Period *uint64 `json:"period,omitempty"`
}

type createMemory struct {
	Limit            *int64 `json:"limit,omitempty"`
	DisableOOMKiller *bool  `json:"disableOOMKiller,omitempty"` //nolint:tagliatelle // OCI runtime-spec wire field.
}

type createPids struct {
	Limit *int64 `json:"limit,omitempty"`
}

type createBlockIO struct {
	Weight *uint16 `json:"weight,omitempty"`
}

type createResources struct {
	CPU     *createCPU     `json:"cpu,omitempty"`
	Memory  *createMemory  `json:"memory,omitempty"`
	Pids    *createPids    `json:"pids,omitempty"`
	BlockIO *createBlockIO `json:"blockIO,omitempty"` //nolint:tagliatelle // OCI wire field.
}

type createPort struct {
	HostIP        string `json:"host_ip,omitempty"`
	ContainerPort uint16 `json:"container_port"`
	HostPort      uint16 `json:"host_port"`
	Range         uint16 `json:"range"`
	Protocol      string `json:"protocol"`
}

type createMount struct {
	Destination string   `json:"destination"`
	Type        string   `json:"type"`
	Source      string   `json:"source"`
	Options     []string `json:"options,omitempty"`
}

//nolint:tagliatelle // Native Libpod wire fields intentionally use Go member casing.
type createVolume struct {
	Name        string   `json:"Name"`
	Dest        string   `json:"Dest"`
	Options     []string `json:"Options"`
	IsAnonymous bool     `json:"IsAnonymous"`
}

type createUlimit struct {
	Type string `json:"type"`
	Soft int64  `json:"soft"`
	Hard int64  `json:"hard"`
}

type createDocument struct {
	Image               string            `json:"image"`
	RawImageName        string            `json:"raw_image_name"`
	Command             []string          `json:"command"`
	Entrypoint          []string          `json:"entrypoint"`
	Name                string            `json:"name"`
	ImageOS             string            `json:"image_os"`
	ImageArchitecture   string            `json:"image_arch"`
	ImageVariant        string            `json:"image_variant,omitempty"`
	Labels              map[string]string `json:"labels"`
	Environment         map[string]string `json:"env"`
	WorkingDirectory    string            `json:"work_dir,omitempty"`
	Hostname            string            `json:"hostname,omitempty"`
	User                string            `json:"user,omitempty"`
	Stdin               *bool             `json:"stdin,omitempty"`
	Terminal            *bool             `json:"terminal,omitempty"`
	Init                *bool             `json:"init,omitempty"`
	ReadOnlyFilesystem  *bool             `json:"read_only_filesystem,omitempty"`
	StopSignal          *int              `json:"stop_signal,omitempty"`
	StopTimeout         *uint             `json:"stop_timeout"`
	RestartPolicy       string            `json:"restart_policy"`
	RestartTries        *uint             `json:"restart_tries,omitempty"`
	NetworkNamespace    namespace         `json:"netns"`
	IPCNamespace        namespace         `json:"ipcns"`
	PIDNamespace        namespace         `json:"pidns"`
	UTSNamespace        namespace         `json:"utsns"`
	CgroupNamespace     namespace         `json:"cgroupns"`
	CgroupParent        string            `json:"cgroup_parent,omitempty"`
	DNS                 []string          `json:"dns_server,omitempty"`
	DNSSearch           []string          `json:"dns_search,omitempty"`
	DNSOptions          []string          `json:"dns_option,omitempty"`
	ExtraHosts          []string          `json:"hostadd,omitempty"`
	Groups              []string          `json:"groups,omitempty"`
	CapAdd              []string          `json:"cap_add,omitempty"`
	CapDrop             []string          `json:"cap_drop,omitempty"`
	NoNewPrivileges     *bool             `json:"no_new_privileges,omitempty"`
	OOMScoreAdj         *int              `json:"oom_score_adj,omitempty"`
	SharedMemoryBytes   *int64            `json:"shm_size"`
	ResourceLimits      createResources   `json:"resource_limits"`
	PortMappings        []createPort      `json:"portmappings,omitempty"`
	PublishExposedPorts *bool             `json:"publish_image_ports"`
	Expose              map[uint16]string `json:"expose,omitempty"`
	Mounts              []createMount     `json:"mounts,omitempty"`
	Volumes             []createVolume    `json:"volumes,omitempty"`
	Ulimits             []createUlimit    `json:"r_limits,omitempty"`
	Healthcheck         *healthConfig     `json:"healthconfig,omitempty"`
}
