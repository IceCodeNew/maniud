package podman

const (
	podmanNetworkBridge       = "bridge"
	podmanIPCPrivate          = "private"
	podmanCgroupPrivate       = "private"
	podmanCgroupHost          = "host"
	podmanStateRunning        = "running"
	podmanStatePaused         = "paused"
	podmanStateRemoving       = "removing"
	podmanStateUnknown        = "unknown"
	podmanCgroupsDefault      = "default"
	podmanDefaultSharedMemory = int64(65536000)
)

type podmanNamespace struct {
	Mode string `json:"nsmode,omitempty"`
}

type podmanCreateSpec struct {
	Image            string            `json:"image"`
	Name             string            `json:"name"`
	Labels           map[string]string `json:"labels"`
	RestartPolicy    string            `json:"restart_policy"`
	NetworkNamespace podmanNamespace   `json:"netns"`
	IPCNamespace     podmanNamespace   `json:"ipcns"`
	PIDNamespace     podmanNamespace   `json:"pidns"`
	UTSNamespace     podmanNamespace   `json:"utsns"`
	CgroupNamespace  podmanNamespace   `json:"cgroupns"`
}

//nolint:tagliatelle // Test fixture mirrors native Libpod wire fields.
type podmanInspectData struct {
	ID          string               `json:"Id"`
	Name        string               `json:"Name"`
	Image       string               `json:"Image"`
	ImageName   string               `json:"ImageName"`
	ImageDigest string               `json:"ImageDigest"`
	State       *podmanInspectState  `json:"State"`
	Mounts      []podmanInspectMount `json:"Mounts"`
	Config      *podmanInspectConfig `json:"Config"`
	HostConfig  *podmanInspectHost   `json:"HostConfig"`
}

//nolint:tagliatelle // Test fixture mirrors native Libpod wire fields.
type podmanInspectState struct {
	Status    string `json:"Status"`
	Running   bool   `json:"Running"`
	Paused    bool   `json:"Paused"`
	StartedAt string `json:"StartedAt,omitempty"`
}

//nolint:tagliatelle // Test fixture mirrors native Libpod wire fields.
type podmanInspectMount struct {
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

//nolint:tagliatelle // Test fixture mirrors native Libpod wire fields.
type podmanInspectConfig struct {
	Image       string            `json:"Image"`
	Command     []string          `json:"Cmd"`
	Entrypoint  []string          `json:"Entrypoint"`
	Labels      map[string]string `json:"Labels"`
	Environment []string          `json:"Env"`
	Hostname    string            `json:"Hostname"`
	User        string            `json:"User"`
	WorkingDir  string            `json:"WorkingDir"`
	OpenStdin   bool              `json:"OpenStdin"`
	TTY         bool              `json:"Tty"`
	StopSignal  string            `json:"StopSignal"`
	StopTimeout uint              `json:"StopTimeout"`
}

//nolint:tagliatelle // Test fixture mirrors native Libpod wire fields.
type podmanInspectRestart struct {
	Name              string `json:"Name"`
	MaximumRetryCount uint   `json:"MaximumRetryCount"`
}

//nolint:tagliatelle // Test fixture mirrors native Libpod wire fields.
type podmanInspectHost struct {
	NetworkMode    string                `json:"NetworkMode"`
	IPCMode        string                `json:"IpcMode"`
	PIDMode        string                `json:"PidMode"`
	UTSMode        string                `json:"UTSMode"`
	CgroupMode     string                `json:"CgroupMode"`
	Cgroups        string                `json:"Cgroups"`
	CgroupParent   string                `json:"CgroupParent"`
	NanoCPUs       int64                 `json:"NanoCpus"`
	CPUPeriod      uint64                `json:"CpuPeriod"`
	CPUQuota       int64                 `json:"CpuQuota"`
	Memory         int64                 `json:"Memory"`
	OOMKillDisable bool                  `json:"OomKillDisable"`
	OOMScoreAdj    int                   `json:"OomScoreAdj"`
	PidsLimit      int64                 `json:"PidsLimit"`
	BlkioWeight    uint16                `json:"BlkioWeight"`
	ShmSize        int64                 `json:"ShmSize"`
	RestartPolicy  *podmanInspectRestart `json:"RestartPolicy"`
	CapAdd         []string              `json:"CapAdd"`
	CapDrop        []string              `json:"CapDrop"`
	DNS            []string              `json:"Dns"`
	DNSSearch      []string              `json:"DnsSearch"`
	DNSOptions     []string              `json:"DnsOptions"`
	ExtraHosts     []string              `json:"ExtraHosts"`
	GroupAdd       []string              `json:"GroupAdd"`
	Binds          []string              `json:"Binds"`
	Tmpfs          map[string]string     `json:"Tmpfs"`
	Init           bool                  `json:"Init"`
	ReadonlyRootfs bool                  `json:"ReadonlyRootfs"`
	SecurityOpt    []string              `json:"SecurityOpt"`
}
