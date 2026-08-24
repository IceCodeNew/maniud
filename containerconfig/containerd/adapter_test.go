//nolint:goconst // Conversion matrices keep source values beside expected OCI and control fields.
package containerd

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"

	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/containerconfig"
)

func minimalSpec() containerconfig.Spec {
	return containerconfig.Spec{
		ServiceName: "api", ContainerName: "api",
		Platform:   containerconfig.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint: []string{"/bin/true"}, NetworkMode: "bridge",
	}
}

func completeSpec() containerconfig.Spec {
	return containerconfig.Spec{
		ServiceName: "api", ContainerName: "api-1",
		Platform:   containerconfig.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"},
		Entrypoint: []string{"/bin/app"}, Command: []string{"serve"}, NetworkMode: "bridge",
		BlkioWeight: new(500), CgroupParent: "/maniud", Cgroup: "private", CPUs: "1.5",
		Hostname: "host", MemoryBytes: 1024, OOMScoreAdj: new(100), PidsLimit: new(int64(-1)),
		Restart: "unless-stopped", SharedMemoryBytes: 2048, StopSignal: "SIGTERM",
		StopTimeout: new(int64(10)), User: "1000:1001", WorkingDirectory: "/work",
		CapAdd: []string{"SYS_ADMIN", "SYS_TIME"}, CapDrop: []string{"NET_RAW"},
		DNS: []string{"2001:db8::1", "1.1.1.1"}, DNSOptions: []string{"rotate", "ndots:1"},
		DNSSearch: []string{"example.test"},
		Devices: []containerconfig.DeviceMapping{
			{Source: "/dev/zero", Target: "/dev/a", Permissions: "r"},
			{Source: "/dev/null", Target: "/dev/example", Permissions: "rw"},
		},
		ExtraHosts: []string{"database=192.0.2.10"}, GroupAdd: []string{"1002", "1003"},
		Sysctls: map[string]string{"net.ipv4.ip_unprivileged_port_start": "0"},
		Tmpfs: []containerconfig.TmpfsMount{
			{Target: "/cache", Options: []string{"mode=1770"}}, {Target: "/scratch", Options: []string{"rw"}},
		},
		Ulimits: []containerconfig.Ulimit{
			{Name: "core", Soft: -1, Hard: -1}, {Name: "nofile", Soft: 1024, Hard: 2048},
		},
		Environment: []string{"A=1", "B=2"}, Labels: []string{"a=1", "b=2"},
		ExposedPorts: []containerconfig.ExposedPort{
			{TargetPort: 443, Protocol: "tcp"}, {TargetPort: 53, Protocol: "udp"},
		},
		Ports: []containerconfig.PortBinding{
			{HostIP: "127.0.0.1", PublishedPort: 8080, TargetPort: 80, Protocol: "tcp"},
			{PublishedPort: 5353, TargetPort: 5353, Protocol: "udp"},
		},
		NoNewPrivileges: true,
		Mounts: []containerconfig.Mount{
			{Kind: containerconfig.MountBind, Source: "/source", Target: "/data", ReadOnly: true},
			{Kind: containerconfig.MountVolume, Target: "/state"},
		},
		Init: new(false), StdinOpen: new(true), OOMKillDisable: new(true),
		ReadOnly: new(true), TTY: new(true),
		Healthcheck: &containerconfig.Healthcheck{Disabled: true},
	}
}

//nolint:cyclop,gocyclo // One complete value proves that every adapter field round-trips together.
func TestEncodeDecodeRoundTripCompleteConfiguration(t *testing.T) {
	t.Parallel()

	spec := completeSpec()
	want := containerconfig.Canonical(spec)
	configuration, err := Encode(spec)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	if err := Validate(spec); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}

	process := configuration.OCI.Process
	resources := configuration.OCI.Linux.Resources
	if configuration.OCI.Version != specs.Version || process == nil || resources == nil ||
		configuration.OCI.Root == nil || !configuration.OCI.Root.Readonly || !process.Terminal ||
		process.Cwd != "/work" || process.User.UID != 1000 || process.User.GID != 1001 ||
		!slices.Equal(process.User.AdditionalGids, []uint32{1002, 1003}) ||
		resources.CPU == nil || resources.CPU.Period == nil || *resources.CPU.Period != cpuPeriod ||
		resources.CPU.Quota == nil || *resources.CPU.Quota != 150000 ||
		resources.Memory == nil || resources.Memory.Limit == nil || *resources.Memory.Limit != 1024 ||
		resources.Memory.DisableOOMKiller == nil || !*resources.Memory.DisableOOMKiller ||
		resources.Pids == nil || resources.Pids.Limit == nil || *resources.Pids.Limit != -1 ||
		resources.BlockIO == nil || resources.BlockIO.Weight == nil || *resources.BlockIO.Weight != 500 {
		t.Fatalf("Encode() projection = %#v", configuration)
	}
	if !slices.Contains(configuration.OCI.Linux.Namespaces, specs.LinuxNamespace{Type: specs.CgroupNamespace}) ||
		!hasOCIMount(configuration.OCI.Mounts, "/dev/shm", "size=2048") ||
		!hasOCIMount(configuration.OCI.Mounts, "/data", "ro") ||
		hasOCIMount(configuration.OCI.Mounts, "/state", "") {
		t.Fatalf("Encode() mounts = %#v", configuration.OCI.Mounts)
	}

	got, err := Decode(configuration)
	if err != nil || !reflect.DeepEqual(got, want) {
		t.Fatalf("Decode(Encode()) = %#v, %v; want %#v", got, err, want)
	}
}

func hasOCIMount(values []specs.Mount, target, option string) bool {
	return slices.ContainsFunc(values, func(value specs.Mount) bool {
		return value.Destination == target && (option == "" || slices.Contains(value.Options, option))
	})
}

func TestEncodeOwnsInputAndDecodeOwnsOutput(t *testing.T) {
	t.Parallel()

	spec := completeSpec()
	want := containerconfig.Canonical(spec)
	configuration, err := Encode(spec)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	*spec.BlkioWeight = 10
	spec.Entrypoint[0] = "changed"
	spec.GroupAdd[0] = "9"
	spec.Sysctls["net.ipv4.ip_unprivileged_port_start"] = "1"
	spec.Tmpfs[0].Options[0] = "mode=0000"
	spec.Healthcheck.Disabled = false

	first, err := Decode(configuration)
	if err != nil || !reflect.DeepEqual(first, want) {
		t.Fatalf("Decode() after input mutation = %#v, %v", first, err)
	}
	first.Command[0] = "changed"
	first.Mounts[0].Target = "/changed"
	first.Healthcheck.Disabled = false
	second, err := Decode(configuration)
	if err != nil || !reflect.DeepEqual(second, want) {
		t.Fatalf("Decode() after output mutation = %#v, %v", second, err)
	}
}

//nolint:cyclop,funlen,gocyclo // Defaults and explicit overrides form one round-trip contract.
func TestEncodeDefaultsAndCapabilityOperations(t *testing.T) {
	t.Parallel()

	spec := minimalSpec()
	spec.CapDrop = []string{"ALL"}
	spec.CapAdd = []string{"SYS_ADMIN"}
	configuration, err := Encode(spec)
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	process := configuration.OCI.Process
	if process == nil || process.Cwd != "/" || !reflect.DeepEqual(process.User, specs.User{}) ||
		process.Capabilities == nil || !slices.Equal(process.Capabilities.Bounding, []string{"CAP_SYS_ADMIN"}) ||
		configuration.OCI.Root == nil || configuration.OCI.Root.Readonly ||
		configuration.OCI.Linux == nil || configuration.OCI.Linux.Resources == nil ||
		configuration.OCI.Linux.Resources.Memory != nil || configuration.OCI.Linux.Resources.CPU != nil ||
		configuration.OCI.Linux.Resources.Pids != nil || configuration.OCI.Linux.Resources.BlockIO != nil ||
		slices.Contains(configuration.OCI.Linux.Namespaces, specs.LinuxNamespace{Type: specs.CgroupNamespace}) ||
		!hasOCIMount(configuration.OCI.Mounts, "/dev/shm", "size=67108864") {
		t.Fatalf("Encode(defaults) = %#v", configuration)
	}
	if decoded, decodeErr := Decode(configuration); decodeErr != nil || !reflect.DeepEqual(decoded, spec) {
		t.Fatalf("Decode(defaults) = %#v, %v", decoded, decodeErr)
	}

	oom := minimalSpec()
	oom.OOMKillDisable = new(false)
	oomConfiguration, err := Encode(oom)
	if err != nil {
		t.Fatalf("Encode(OOM) error = %v", err)
	}
	if decoded, decodeErr := Decode(oomConfiguration); decodeErr != nil || !reflect.DeepEqual(decoded, oom) {
		t.Fatalf("Decode(OOM) = %#v, %v", decoded, decodeErr)
	}
	commandOnly := minimalSpec()
	commandOnly.Entrypoint = nil
	commandOnly.Command = []string{"true"}
	commandConfiguration, err := Encode(commandOnly)
	if err != nil {
		t.Fatalf("Encode(command only) error = %v", err)
	}
	if decoded, decodeErr := Decode(commandConfiguration); decodeErr != nil ||
		!reflect.DeepEqual(decoded, commandOnly) {
		t.Fatalf("Decode(command only) = %#v, %v", decoded, decodeErr)
	}

	uidOnly := minimalSpec()
	uidOnly.User = "1000"
	uidConfiguration, err := Encode(uidOnly)
	if err != nil {
		t.Fatalf("Encode(UID only) error = %v", err)
	}
	if decoded, decodeErr := Decode(uidConfiguration); decodeErr != nil ||
		!reflect.DeepEqual(decoded, uidOnly) {
		t.Fatalf("Decode(UID only) = %#v, %v", decoded, decodeErr)
	}

	custom := minimalSpec()
	custom.Mounts = []containerconfig.Mount{{
		Kind: containerconfig.MountBind, Source: "/shm", Target: "/dev/shm",
	}}
	customConfiguration, err := Encode(custom)
	if err != nil || !hasOCIMount(customConfiguration.OCI.Mounts, "/dev/shm", "rbind") ||
		hasOCIMount(customConfiguration.OCI.Mounts, "/dev/shm", "size=67108864") {
		t.Fatalf("Encode(custom shm) = %#v, %v", customConfiguration, err)
	}
}

func TestDecodeRejectsIncompleteAndTamperedProjection(t *testing.T) {
	t.Parallel()

	valid, err := Encode(completeSpec())
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	tests := []struct {
		name   string
		mutate func(*Configuration)
	}{
		{"missing process", func(value *Configuration) { value.OCI.Process = nil }},
		{"missing root", func(value *Configuration) { value.OCI.Root = nil }},
		{"missing linux", func(value *Configuration) { value.OCI.Linux = nil }},
		{"negative entrypoint", func(value *Configuration) { value.Control.EntrypointLength = -1 }},
		{"long entrypoint", func(value *Configuration) { value.Control.EntrypointLength = 99 }},
		{"missing resources", func(value *Configuration) { value.OCI.Linux.Resources = nil }},
		{"invalid control", func(value *Configuration) { value.Control.ServiceName = "" }},
		{"version drift", func(value *Configuration) { value.OCI.Version = "changed" }},
		{"empty block io", func(value *Configuration) { value.OCI.Linux.Resources.BlockIO = &specs.LinuxBlockIO{} }},
		{"empty pids", func(value *Configuration) { value.OCI.Linux.Resources.Pids = &specs.LinuxPids{} }},
		{"ulimit overflow", func(value *Configuration) {
			value.OCI.Process.Rlimits[0].Soft = math.MaxInt64 + 1
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			configuration, encodeErr := Encode(completeSpec())
			if encodeErr != nil {
				t.Fatalf("Encode() error = %v", encodeErr)
			}
			test.mutate(&configuration)
			if _, decodeErr := Decode(configuration); decodeErr == nil {
				t.Fatal("Decode() accepted tampered projection")
			}
		})
	}
	if decoded, decodeErr := Decode(valid); decodeErr != nil || decoded.ServiceName != "api" {
		t.Fatalf("Decode(valid) = %#v, %v", decoded, decodeErr)
	}
}

//nolint:funlen // The matrix keeps every validation code and field path auditable together.
func TestValidationRejectsInvalidValuesWithStablePaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		mutate func(*containerconfig.Spec)
	}{
		{"service empty", "/service_name", func(value *containerconfig.Spec) { value.ServiceName = "" }},
		{"service prefix", "/service_name", func(value *containerconfig.Spec) { value.ServiceName = "-api" }},
		{"service suffix", "/service_name", func(value *containerconfig.Spec) { value.ServiceName = "api-" }},
		{"service separator", "/service_name", func(value *containerconfig.Spec) { value.ServiceName = "a/b" }},
		{"service nul", "/service_name", func(value *containerconfig.Spec) { value.ServiceName = "a\x00b" }},
		{"service utf8", "/service_name", func(value *containerconfig.Spec) { value.ServiceName = string([]byte{0xff}) }},
		{"container", "/container_name", func(value *containerconfig.Spec) { value.ContainerName = "" }},
		{"platform os", "/platform", func(value *containerconfig.Spec) { value.Platform.OS = "windows" }},
		{"platform architecture", "/platform", func(value *containerconfig.Spec) { value.Platform.Architecture = "386" }},
		{"platform variant", "/platform", func(value *containerconfig.Spec) { value.Platform.Variant = "v8" }},
		{"network", "/network_mode", func(value *containerconfig.Spec) { value.NetworkMode = "host" }},
		{"entrypoint", "/entrypoint", func(value *containerconfig.Spec) { value.Entrypoint[0] = "a\x00b" }},
		{"command", "/command", func(value *containerconfig.Spec) { value.Command = []string{"a\x00b"} }},
		{"process", "/process", func(value *containerconfig.Spec) { value.Entrypoint = nil }},
		{"hostname", "/hostname", func(value *containerconfig.Spec) {
			value.Hostname = strings.Repeat("a", maximumTextBytes+1)
		}},
		{"working directory", "/working_directory", func(value *containerconfig.Spec) {
			value.WorkingDirectory = "relative"
		}},
		{"stop signal", "/stop_signal", func(value *containerconfig.Spec) { value.StopSignal = "SIG\x00TERM" }},
		{"user", "/user", func(value *containerconfig.Spec) { value.User = "user" }},
		{"user group", "/user", func(value *containerconfig.Spec) { value.User = "1:group" }},
		{"memory", "/memory_bytes", func(value *containerconfig.Spec) { value.MemoryBytes = -1 }},
		{"shared memory", "/shared_memory_bytes", func(value *containerconfig.Spec) { value.SharedMemoryBytes = -1 }},
		{"blkio", "/blkio_weight", func(value *containerconfig.Spec) { value.BlkioWeight = new(9) }},
		{"oom score", "/oom_score_adj", func(value *containerconfig.Spec) { value.OOMScoreAdj = new(1001) }},
		{"pids", "/pids_limit", func(value *containerconfig.Spec) { value.PidsLimit = new(int64(0)) }},
		{"cgroup", "/cgroup", func(value *containerconfig.Spec) { value.Cgroup = "host" }},
		{"cgroup parent", "/cgroup_parent", func(value *containerconfig.Spec) { value.CgroupParent = "a\x00b" }},
		{"restart", "/restart", func(value *containerconfig.Spec) { value.Restart = "sometimes" }},
		{"restart retries", "/restart", func(value *containerconfig.Spec) { value.Restart = "on-failure:0" }},
		{"stop timeout", "/stop_timeout", func(value *containerconfig.Spec) { value.StopTimeout = new(int64(0)) }},
		{"cap add all", "/capabilities", func(value *containerconfig.Spec) { value.CapAdd = []string{"ALL"} }},
		{"cap unknown", "/capabilities", func(value *containerconfig.Spec) { value.CapAdd = []string{"UNKNOWN"} }},
		{"cap duplicate", "/capabilities", func(value *containerconfig.Spec) {
			value.CapDrop = []string{"NET_RAW", "CAP_NET_RAW"}
		}},
		{"dns option", "/dns_options", func(value *containerconfig.Spec) { value.DNSOptions = []string{""} }},
		{"dns search", "/dns_search", func(value *containerconfig.Spec) { value.DNSSearch = []string{""} }},
		{"extra host", "/extra_hosts", func(value *containerconfig.Spec) { value.ExtraHosts = []string{""} }},
		{"group", "/group_add", func(value *containerconfig.Spec) { value.GroupAdd = []string{"group"} }},
		{"environment syntax", "/environment", func(value *containerconfig.Spec) { value.Environment = []string{"A"} }},
		{"environment name", "/environment", func(value *containerconfig.Spec) { value.Environment = []string{"=1"} }},
		{"environment duplicate", "/environment", func(value *containerconfig.Spec) {
			value.Environment = []string{"A=1", "A=2"}
		}},
		{"label name", "/labels", func(value *containerconfig.Spec) { value.Labels = []string{"=1"} }},
		{"label duplicate", "/labels", func(value *containerconfig.Spec) { value.Labels = []string{"a=1", "a=2"} }},
		{"sysctl key", "/sysctls", func(value *containerconfig.Spec) { value.Sysctls = map[string]string{"": "1"} }},
		{"dns", "/dns", func(value *containerconfig.Spec) { value.DNS = []string{"localhost"} }},
		{"dns canonical", "/dns", func(value *containerconfig.Spec) { value.DNS = []string{"2001:0db8::1"} }},
		{"device source", "/devices", func(value *containerconfig.Spec) {
			value.Devices = []containerconfig.DeviceMapping{{Source: "dev", Target: "/dev/x", Permissions: "r"}}
		}},
		{"device target", "/devices", func(value *containerconfig.Spec) {
			value.Devices = []containerconfig.DeviceMapping{{Source: "/dev/x", Target: "dev", Permissions: "r"}}
		}},
		{"device permission", "/devices", func(value *containerconfig.Spec) {
			value.Devices = []containerconfig.DeviceMapping{{Source: "/dev/x", Target: "/dev/x", Permissions: "rx"}}
		}},
		{"device permission empty", "/devices", func(value *containerconfig.Spec) {
			value.Devices = []containerconfig.DeviceMapping{{Source: "/dev/x", Target: "/dev/x"}}
		}},
		{"device permission duplicate", "/devices", func(value *containerconfig.Spec) {
			value.Devices = []containerconfig.DeviceMapping{{Source: "/dev/x", Target: "/dev/x", Permissions: "rr"}}
		}},
		{"device duplicate", "/devices", func(value *containerconfig.Spec) {
			value.Devices = []containerconfig.DeviceMapping{
				{Source: "/dev/x", Target: "/dev/x", Permissions: "r"},
				{Source: "/dev/y", Target: "/dev/x", Permissions: "w"},
			}
		}},
		{"tmpfs target", "/tmpfs", func(value *containerconfig.Spec) {
			value.Tmpfs = []containerconfig.TmpfsMount{{Target: "tmp"}}
		}},
		{"tmpfs option", "/tmpfs", func(value *containerconfig.Spec) {
			value.Tmpfs = []containerconfig.TmpfsMount{{Target: "/tmp", Options: []string{""}}}
		}},
		{"tmpfs duplicate", "/tmpfs", func(value *containerconfig.Spec) {
			value.Tmpfs = []containerconfig.TmpfsMount{{Target: "/tmp"}, {Target: "/tmp"}}
		}},
		{"ulimit name", "/ulimits", func(value *containerconfig.Spec) {
			value.Ulimits = []containerconfig.Ulimit{{Soft: 1, Hard: 1}}
		}},
		{"ulimit soft", "/ulimits", func(value *containerconfig.Spec) {
			value.Ulimits = []containerconfig.Ulimit{{Name: "core", Soft: -2, Hard: 1}}
		}},
		{"ulimit hard", "/ulimits", func(value *containerconfig.Spec) {
			value.Ulimits = []containerconfig.Ulimit{{Name: "core", Soft: 1, Hard: -2}}
		}},
		{"ulimit infinity", "/ulimits", func(value *containerconfig.Spec) {
			value.Ulimits = []containerconfig.Ulimit{{Name: "core", Soft: -1, Hard: 1}}
		}},
		{"ulimit order", "/ulimits", func(value *containerconfig.Spec) {
			value.Ulimits = []containerconfig.Ulimit{{Name: "core", Soft: 2, Hard: 1}}
		}},
		{"ulimit duplicate", "/ulimits", func(value *containerconfig.Spec) {
			value.Ulimits = []containerconfig.Ulimit{{Name: "core", Soft: 1, Hard: 1}, {Name: "core", Soft: 1, Hard: 1}}
		}},
		{"exposed port", "/ports", func(value *containerconfig.Spec) {
			value.ExposedPorts = []containerconfig.ExposedPort{{Protocol: "tcp"}}
		}},
		{"exposed protocol", "/ports", func(value *containerconfig.Spec) {
			value.ExposedPorts = []containerconfig.ExposedPort{{TargetPort: 1, Protocol: "bad"}}
		}},
		{"published port", "/ports", func(value *containerconfig.Spec) {
			value.Ports = []containerconfig.PortBinding{{TargetPort: 1, Protocol: "tcp"}}
		}},
		{"port target", "/ports", func(value *containerconfig.Spec) {
			value.Ports = []containerconfig.PortBinding{{PublishedPort: 1, Protocol: "tcp"}}
		}},
		{"port protocol", "/ports", func(value *containerconfig.Spec) {
			value.Ports = []containerconfig.PortBinding{{PublishedPort: 1, TargetPort: 1, Protocol: "sctp"}}
		}},
		{"port host", "/ports", func(value *containerconfig.Spec) {
			value.Ports = []containerconfig.PortBinding{{HostIP: "host", PublishedPort: 1, TargetPort: 1, Protocol: "tcp"}}
		}},
		{"port duplicate", "/ports", func(value *containerconfig.Spec) {
			value.ExposedPorts = []containerconfig.ExposedPort{{TargetPort: 1, Protocol: "tcp"}}
			value.Ports = []containerconfig.PortBinding{{PublishedPort: 1, TargetPort: 1, Protocol: "tcp"}}
		}},
		{"mount target", "/mounts", func(value *containerconfig.Spec) {
			value.Mounts = []containerconfig.Mount{{Kind: containerconfig.MountVolume, Target: "data"}}
		}},
		{"mount duplicate", "/mounts", func(value *containerconfig.Spec) {
			value.Mounts = []containerconfig.Mount{
				{Kind: containerconfig.MountVolume, Target: "/data"},
				{Kind: containerconfig.MountVolume, Target: "/data"},
			}
		}},
		{"bind source", "/mounts", func(value *containerconfig.Spec) {
			value.Mounts = []containerconfig.Mount{{Kind: containerconfig.MountBind, Source: "data", Target: "/data"}}
		}},
		{"volume source", "/mounts", func(value *containerconfig.Spec) {
			value.Mounts = []containerconfig.Mount{{Kind: containerconfig.MountVolume, Source: "data", Target: "/data"}}
		}},
		{"volume readonly", "/mounts", func(value *containerconfig.Spec) {
			value.Mounts = []containerconfig.Mount{{Kind: containerconfig.MountVolume, Target: "/data", ReadOnly: true}}
		}},
		{"mount kind", "/mounts", func(value *containerconfig.Spec) {
			value.Mounts = []containerconfig.Mount{{Kind: 99, Target: "/data"}}
		}},
		{"target conflict", "/mounts", func(value *containerconfig.Spec) {
			value.Mounts = []containerconfig.Mount{{Kind: containerconfig.MountVolume, Target: "/data"}}
			value.Tmpfs = []containerconfig.TmpfsMount{{Target: "/data"}}
		}},
		{"shm conflict", "/mounts", func(value *containerconfig.Spec) {
			value.SharedMemoryBytes = 1
			value.Tmpfs = []containerconfig.TmpfsMount{{Target: "/dev/shm"}}
		}},
		{"health disabled fields", "/healthcheck", func(value *containerconfig.Spec) {
			value.Healthcheck = &containerconfig.Healthcheck{Disabled: true, Test: []string{"CMD", "true"}}
		}},
		{"health command", "/healthcheck", func(value *containerconfig.Spec) {
			value.Healthcheck = &containerconfig.Healthcheck{Test: []string{"BAD"}}
		}},
		{"health duration", "/healthcheck", func(value *containerconfig.Spec) {
			value.Healthcheck = &containerconfig.Healthcheck{Interval: "bad"}
		}},
		{"health retries", "/healthcheck", func(value *containerconfig.Spec) {
			value.Healthcheck = &containerconfig.Healthcheck{Retries: new(0)}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			spec := minimalSpec()
			test.mutate(&spec)
			assertValidation(t, Validate(spec), containerconfig.ValidationInvalidValue, test.path)
		})
	}
}

func TestValidationRejectsUnsupportedCapabilities(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		path   string
		mutate func(*containerconfig.Spec)
	}{
		{"init", "/init", func(value *containerconfig.Spec) { value.Init = new(true) }},
		{"healthcheck", "/healthcheck", func(value *containerconfig.Spec) {
			value.Healthcheck = &containerconfig.Healthcheck{Test: []string{"CMD", "true"}, Interval: "1s"}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			spec := minimalSpec()
			test.mutate(&spec)
			assertValidation(t, Validate(spec), containerconfig.ValidationUnsupportedCapability, test.path)
		})
	}
}

func TestCPUValidationBoundaries(t *testing.T) {
	t.Parallel()

	valid := []string{"", "1", "0.5", "1.000000001"}
	for _, cpus := range valid {
		t.Run("valid "+cpus, func(t *testing.T) {
			t.Parallel()

			spec := minimalSpec()
			spec.CPUs = cpus
			if err := Validate(spec); err != nil {
				t.Fatalf("Validate(CPUs=%q) error = %v", cpus, err)
			}
		})
	}
	invalid := []string{".5", "+1", "-1", "1.0000000001", "1.bad", "0", "0.000001", "92233720368548"}
	for _, cpus := range invalid {
		t.Run("invalid "+cpus, func(t *testing.T) {
			t.Parallel()

			spec := minimalSpec()
			spec.CPUs = cpus
			assertValidation(t, Validate(spec), containerconfig.ValidationInvalidValue, "/cpus")
		})
	}
}

func assertValidation(t *testing.T, err error, code containerconfig.ValidationCode, path string) {
	t.Helper()
	var validation containerconfig.ValidationError
	if !errors.As(err, &validation) || validation.Code != code || validation.Path != path {
		t.Fatalf("validation = %#v, %v; want %s at %s", validation, err, code, path)
	}
}
