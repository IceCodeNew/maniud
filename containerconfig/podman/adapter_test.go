//nolint:goconst,lll // Native wire fixtures keep protocol values adjacent to their fields.
package podman

import (
	"bytes"
	"encoding/json"
	"errors"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	testContainerID      = "dddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddddd"
	testImageID          = "sha256:aaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa"
	testImageDigest      = "sha256:bbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbbb"
	testImage            = "registry.example.test/example/app@sha256:cccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccccc"
	testPodmanAPIVersion = "6.1.0"
	testStartedAt        = "2026-09-02T11:00:00.123456789+01:00"
)

func minimalSpec() containerconfig.Spec {
	return containerconfig.Spec{
		ServiceName: "api", ContainerName: "example-api",
		Platform:   containerconfig.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint: []string{"/bin/true"}, NetworkMode: networkBridge,
	}
}

func richSpec() containerconfig.Spec {
	return containerconfig.Spec{
		ServiceName: "api", ContainerName: "example-api",
		Platform:   containerconfig.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint: []string{"/usr/local/bin/app"}, Command: []string{"serve"},
		NetworkMode: networkBridge, BlkioWeight: new(500),
		CgroupParent: "/maniud.slice", Cgroup: cgroupHost, CPUs: "1.5", Hostname: "api.internal",
		MemoryBytes: 256 << 20, OOMScoreAdj: new(100), PidsLimit: new(int64(-1)),
		Restart: "on-failure:3", SharedMemoryBytes: 32 << 20,
		StopSignal: "SIGINT", StopTimeout: new(int64(20)), User: "1000:1000", WorkingDirectory: "/app",
		CapAdd: []string{"NET_BIND_SERVICE"}, CapDrop: []string{"MKNOD"},
		DNS: []string{"1.1.1.1", "2001:4860:4860::8888"}, DNSOptions: []string{"ndots:1"},
		DNSSearch: []string{"example.test"}, ExtraHosts: []string{"db=192.0.2.10"},
		GroupAdd: []string{"2000"}, Tmpfs: []containerconfig.TmpfsMount{{
			Target: "/tmp", Options: []string{"size=64m", "mode=1777"},
		}},
		Ulimits:     []containerconfig.Ulimit{{Name: ulimitNoFile, Soft: 1024, Hard: 2048}},
		Environment: []string{"B=two", "A=one"}, Labels: []string{"com.example.role=api"},
		ExposedPorts:    []containerconfig.ExposedPort{{TargetPort: 9090, Protocol: protocolSCTP}},
		Ports:           []containerconfig.PortBinding{{PublishedPort: 18080, TargetPort: 8080, Protocol: protocolTCP}},
		NoNewPrivileges: true,
		Mounts: []containerconfig.Mount{{
			Kind: containerconfig.MountBind, Source: "/srv/data", Target: "/data", ReadOnly: true,
		}, {Kind: containerconfig.MountVolume, Target: "/image-data"}},
		Init: new(true), StdinOpen: new(true), OOMKillDisable: new(true), ReadOnly: new(true), TTY: new(true),
		Healthcheck: &containerconfig.Healthcheck{
			Test: []string{"CMD", "check"}, Interval: "30s", Timeout: "5s", Retries: new(3),
			StartPeriod: "10s", StartInterval: "2s",
		},
	}
}

func richInspectDocument() inspectDocument {
	return inspectDocument{
		ID: testContainerID, Name: "example-api", Image: testImageID,
		ImageName: testImage, ImageDigest: testImageDigest,
		State: &inspectState{Status: "running", Running: true, StartedAt: testStartedAt},
		Mounts: []inspectMount{{
			Type: mountBind, Source: "/srv/data", Destination: "/data",
			Options: []string{recursiveBind}, ReadWrite: false, Propagation: propagationPrivate,
		}, {
			Type: mountVolume, Name: "anonymous-volume",
			Source:      "/var/lib/containers/storage/volumes/anonymous/_data",
			Destination: "/image-data", Driver: volumeDriverLocal, ReadWrite: true,
		}},
		Config: &inspectConfig{
			Image: testImage, Command: []string{"serve"},
			Entrypoint:  json.RawMessage(`["/usr/local/bin/app"]`),
			Labels:      map[string]string{"com.example.role": "api"},
			Environment: []string{"A=one", "B=two", podmanEnvironmentKey + "=" + podmanEnvironmentValue},
			Hostname:    "api.internal", User: "1000:1000", WorkingDir: "/app", OpenStdin: true, TTY: true,
			StopSignal: json.RawMessage(`"SIGINT"`), StopTimeout: 20,
			Healthcheck: &healthConfig{
				Test: []string{"CMD", "check"}, Interval: 30 * time.Second, Timeout: 5 * time.Second,
				Retries: 3, StartPeriod: 10 * time.Second, StartInterval: 2 * time.Second,
			},
			ExposedPorts: map[string]any{"8080/tcp": nil, "9090/sctp": nil},
		},
		HostConfig: &inspectHost{
			NetworkMode: networkBridge, IPCMode: namespacePrivate,
			PIDMode: namespacePrivate, UTSMode: namespacePrivate,
			CgroupMode: cgroupHost, Cgroups: cgroupsDefault,
			CgroupParent: "/maniud.slice", NanoCPUs: 1_500_000_000,
			CPUPeriod: cpuPeriod, CPUQuota: 150_000, Memory: 256 << 20,
			OOMKillDisable: true, OOMScoreAdj: 100, PidsLimit: -1, BlkioWeight: 500, ShmSize: 32 << 20,
			RestartPolicy: &inspectRestart{Name: "on-failure", MaximumRetryCount: 3},
			CapAdd:        []string{"NET_BIND_SERVICE"}, CapDrop: []string{"MKNOD"},
			DNS: []string{"1.1.1.1", "2001:4860:4860::8888"}, DNSSearch: []string{"example.test"},
			DNSOptions: []string{"ndots:1"}, ExtraHosts: []string{"db:192.0.2.10"}, GroupAdd: []string{"2000"},
			Binds:   []string{"/srv/data:/data:rbind,ro,rprivate"},
			Tmpfs:   map[string]string{"/tmp": "size=64m,mode=1777,rprivate,nosuid,nodev,tmpcopyup"},
			Ulimits: []inspectUlimit{{Name: "RLIMIT_NOFILE", Soft: 1024, Hard: 2048}},
			PortBindings: map[string][]inspectPortBinding{
				"8080/tcp": {{HostIP: hostAnyIPv4, HostPort: "18080"}},
			},
			Init: true, ReadonlyRootfs: true, SecurityOpt: []string{"no-new-privileges"},
		},
	}
}

//nolint:cyclop // The assertion verifies every independently encoded rich field.
func TestEncodeRichConfiguration(t *testing.T) {
	t.Parallel()

	spec := richSpec()
	encoded, err := Encode(spec, CreateOptions{ImageReference: testImage, CopyImageVolumes: true})
	if err != nil {
		t.Fatalf("Encode() error = %v", err)
	}
	var create createDocument
	if json.Unmarshal(encoded, &create) != nil || create.Image != testImage || create.RawImageName != testImage ||
		create.ResourceLimits.CPU == nil || create.ResourceLimits.Memory == nil ||
		create.ResourceLimits.Pids == nil || create.ResourceLimits.BlockIO == nil ||
		create.IPCNamespace.Mode != namespacePrivate || create.PIDNamespace.Mode != namespacePrivate ||
		create.UTSNamespace.Mode != namespacePrivate ||
		create.CgroupNamespace.Mode != cgroupHost || len(create.Mounts) != 2 || len(create.Volumes) != 1 ||
		!create.Volumes[0].IsAnonymous || create.Volumes[0].Dest != "/image-data" ||
		len(create.Volumes[0].Options) != 0 {
		t.Fatalf("Encode() = %s", encoded)
	}
	if err := Validate(spec, CreateOptions{ImageReference: testImage}); err != nil {
		t.Fatalf("Validate() error = %v", err)
	}
}

//nolint:cyclop // The assertion verifies every independently decoded rich field.
func TestDecodeInspectRichConfiguration(t *testing.T) {
	t.Parallel()

	document, err := json.Marshal(richInspectDocument())
	if err != nil {
		t.Fatal(err)
	}
	inspection, err := DecodeInspect(bytes.NewReader(document), int64(len(document)), testPodmanAPIVersion)
	if err != nil {
		t.Fatalf("DecodeInspect() error = %v", err)
	}
	want := richSpec()
	want.ServiceName = ""
	want.Platform = containerconfig.Platform{}
	want.ExposedPorts = []containerconfig.ExposedPort{{TargetPort: 9090, Protocol: protocolSCTP}}
	want.Ports[0].HostIP = ""
	want.Tmpfs[0].Options = []string{
		"size=64m", "mode=1777", propagationPrivate, tmpfsNoSUID, tmpfsNoDevice, tmpfsCopyUp,
	}
	slices.Sort(want.Environment)
	if inspection.ID != testContainerID || inspection.ImageID != testImageID ||
		inspection.ImageReference != testImage || inspection.ImageDigest != testImageDigest ||
		inspection.StartedAt.Format(time.RFC3339Nano) != "2026-09-02T10:00:00.123456789Z" ||
		inspection.State != StateRunning || !reflect.DeepEqual(inspection.Spec, want) ||
		len(inspection.RuntimeMounts) != 2 || inspection.RuntimeMounts[1].Name != "anonymous-volume" ||
		inspection.RawLabels["com.example.role"] != "api" {
		t.Fatalf("DecodeInspect() = %#v\nwant spec = %#v", inspection, want)
	}

	clone := inspection.Clone()
	clone.Spec.Command[0] = "changed"
	clone.RuntimeMounts[0].Target = "/changed"
	clone.RawLabels["com.example.role"] = "changed"
	if inspection.Spec.Command[0] == "changed" || inspection.RuntimeMounts[0].Target == "/changed" ||
		inspection.RawLabels["com.example.role"] == "changed" {
		t.Fatal("Inspection.Clone() shared state")
	}
}

func TestEncodeUsesLibpodDefaultsAndVolumeCopyPolicy(t *testing.T) {
	t.Parallel()

	spec := minimalSpec()
	spec.Mounts = []containerconfig.Mount{{Kind: containerconfig.MountVolume, Target: "/data"}}
	encoded, err := Encode(spec, CreateOptions{ImageReference: testImage})
	if err != nil {
		t.Fatal(err)
	}
	var document createDocument
	if json.Unmarshal(encoded, &document) != nil || document.CgroupNamespace.Mode != namespacePrivate ||
		document.SharedMemoryBytes == nil || *document.SharedMemoryBytes != defaultSharedMemoryBytes ||
		document.StopTimeout == nil || *document.StopTimeout != uint(defaultStopTimeout) ||
		len(document.Volumes) != 1 || !slices.Equal(document.Volumes[0].Options, []string{"nocopy"}) {
		t.Fatalf("Encode(defaults) = %s", encoded)
	}
}

//nolint:cyclop // The assertion verifies each independent native default.
func TestCanonicalOwnsInputAndNormalizesRuntimeDefaults(t *testing.T) {
	t.Parallel()

	spec := minimalSpec()
	spec.Cgroup = namespacePrivate
	spec.WorkingDirectory = "/"
	spec.Restart = "no"
	spec.SharedMemoryBytes = defaultSharedMemoryBytes
	spec.StopSignal = "15"
	spec.StopTimeout = new(defaultStopTimeout)
	spec.OOMScoreAdj = new(0)
	spec.Init, spec.StdinOpen, spec.OOMKillDisable, spec.ReadOnly, spec.TTY = new(false), new(false), new(false), new(false), new(false)
	spec.Tmpfs = []containerconfig.TmpfsMount{{
		Target: "/tmp", Options: []string{
			"size=1m", propagationPrivate, tmpfsNoSUID, tmpfsNoDevice, tmpfsCopyUp,
		},
	}}
	spec.ExposedPorts = []containerconfig.ExposedPort{{TargetPort: 80, Protocol: protocolTCP}}
	spec.Ports = []containerconfig.PortBinding{{
		HostIP: hostAnyIPv4, PublishedPort: 8080, TargetPort: 80, Protocol: protocolTCP,
	}}
	canonical, err := Canonical(spec)
	if err != nil {
		t.Fatal(err)
	}
	if canonical.Cgroup != "" || canonical.WorkingDirectory != "" || canonical.Restart != "" ||
		canonical.SharedMemoryBytes != 0 || canonical.StopSignal != "" ||
		canonical.StopTimeout != nil || canonical.OOMScoreAdj != nil || canonical.Init != nil ||
		canonical.StdinOpen != nil || canonical.OOMKillDisable != nil || canonical.ReadOnly != nil ||
		canonical.TTY != nil || len(canonical.ExposedPorts) != 0 || canonical.Ports[0].HostIP != "" ||
		!slices.Equal(
			canonical.Tmpfs[0].Options,
			[]string{"size=1m", propagationPrivate, tmpfsNoSUID, tmpfsNoDevice, tmpfsCopyUp},
		) {
		t.Fatalf("Canonical() = %#v", canonical)
	}
	spec.Command = []string{"changed"}
	spec.Tmpfs[0].Options[0] = "changed"
	if len(canonical.Command) != 0 || canonical.Tmpfs[0].Options[0] == "changed" {
		t.Fatal("Canonical() shared input state")
	}
}

func podmanRuntimeDefaultSpecs() (containerconfig.Spec, containerconfig.Spec) {
	expected := minimalSpec()
	expected.Environment = []string{"PATH=/usr/bin"}
	observed := expected.Clone()
	observed.ServiceName = ""
	observed.Platform = containerconfig.Platform{}
	observed.Environment = append(observed.Environment, "HOME=/root", "HOSTNAME=container-id", "TERM=xterm")
	observed.CapDrop = []string{"CAP_AUDIT_WRITE", "CAP_MKNOD", "CAP_NET_RAW"}
	observed.Ulimits = []containerconfig.Ulimit{
		{Name: ulimitNoFile, Soft: 1_048_576, Hard: 1_048_576},
		{Name: ulimitNProc, Soft: 1_048_576, Hard: 1_048_576},
	}
	observed.PidsLimit = new(defaultPidsLimit)

	return observed, expected
}

func TestEquivalentHandlesLibpodRuntimeDefaults(t *testing.T) {
	t.Parallel()

	observed, expected := podmanRuntimeDefaultSpecs()
	if !Equivalent(observed, expected, podmanAPI431) {
		t.Fatal("Equivalent() rejected Libpod runtime defaults")
	}
	if Equivalent(observed, expected, "4.7.0") || Equivalent(observed, expected, testPodmanAPIVersion) {
		t.Fatal("Equivalent() ignored version-specific Libpod defaults")
	}
	modern := observed.Clone()
	modern.Environment = slices.DeleteFunc(modern.Environment, func(value string) bool { return value == "TERM=xterm" })
	modern.CapDrop = nil
	if !Equivalent(modern, expected, testPodmanAPIVersion) {
		t.Fatal("Equivalent() rejected modern Libpod defaults")
	}
	if Equivalent(modern, expected, "6.1.1") {
		t.Fatal("Equivalent() accepted an unsupported API version")
	}
}

func TestEquivalentRejectsLibpodResourceDifferences(t *testing.T) {
	t.Parallel()

	observed, expected := podmanRuntimeDefaultSpecs()
	explicitPids := expected.Clone()
	explicitPids.PidsLimit = new(int64(-1))
	if Equivalent(observed, explicitPids, podmanAPI431) {
		t.Fatal("Equivalent() ignored an explicit pids limit")
	}
	unexpectedPids := observed.Clone()
	unexpectedPidsLimit := defaultPidsLimit - 1
	unexpectedPids.PidsLimit = &unexpectedPidsLimit
	if Equivalent(unexpectedPids, expected, podmanAPI431) {
		t.Fatal("Equivalent() ignored a non-default pids limit")
	}
	observed.Ulimits = append(observed.Ulimits, containerconfig.Ulimit{Name: "memlock", Soft: 1, Hard: 1})
	if Equivalent(observed, expected, podmanAPI431) {
		t.Fatal("Equivalent() ignored an unexpected ulimit")
	}
	expected = minimalSpec()
	expected.Ulimits = []containerconfig.Ulimit{{Name: ulimitNoFile, Soft: 1024, Hard: 2048}}
	observed = expected.Clone()
	observed.Ulimits[0].Soft++
	if Equivalent(observed, expected, testPodmanAPIVersion) {
		t.Fatal("Equivalent() ignored an explicit ulimit")
	}
}

func TestEquivalentRejectsLibpodEnvironmentAndCapabilityDifferences(t *testing.T) {
	t.Parallel()

	observed, expected := podmanRuntimeDefaultSpecs()
	expected.Environment = append(expected.Environment, "HOME=/home/app")
	if Equivalent(observed, expected, podmanAPI431) {
		t.Fatal("Equivalent() ignored an explicit HOME value")
	}
	expected = minimalSpec()
	expected.Environment = []string{"TERM=screen"}
	observed = minimalSpec()
	observed.Environment = []string{"TERM=xterm"}
	if Equivalent(observed, expected, podmanAPI431) {
		t.Fatal("Equivalent() ignored an explicit TERM value")
	}
	expected = minimalSpec()
	expected.CapDrop = []string{"CAP_MKNOD"}
	observed = expected.Clone()
	observed.CapDrop = []string{"CAP_AUDIT_WRITE", "CAP_MKNOD", "CAP_NET_RAW"}
	if Equivalent(observed, expected, podmanAPI431) {
		t.Fatal("Equivalent() accepted an ambiguous Podman 4.3.1 capability drop")
	}
	expected.CapDrop = []string{"CAP_SYS_ADMIN"}
	observed.CapDrop = append(observed.CapDrop, "CAP_SYS_ADMIN")
	if !Equivalent(observed, expected, podmanAPI431) {
		t.Fatal("Equivalent() rejected an unambiguous explicit capability drop")
	}
	observed.CapDrop = append(observed.CapDrop, "CAP_SYS_TIME")
	if Equivalent(observed, expected, podmanAPI431) {
		t.Fatal("Equivalent() ignored an unexpected capability drop")
	}
	expected = minimalSpec()
	observed = expected.Clone()
	observed.Environment = []string{"EXTRA=value"}
	if Equivalent(observed, expected, testPodmanAPIVersion) {
		t.Fatal("Equivalent() ignored a non-Libpod environment value")
	}
}

func TestEquivalentPreservesExplicitTmpfsDefaults(t *testing.T) {
	t.Parallel()

	defaults := []string{propagationPrivate, tmpfsNoSUID, tmpfsNoDevice, tmpfsCopyUp}
	expected := minimalSpec()
	expected.Tmpfs = []containerconfig.TmpfsMount{{Target: "/tmp"}}
	observed := expected.Clone()
	observed.Tmpfs[0].Options = slices.Clone(defaults)
	if !Equivalent(observed, expected, testPodmanAPIVersion) {
		t.Fatal("Equivalent() rejected implicit Libpod tmpfs defaults")
	}

	for _, option := range defaults {
		explicit := expected.Clone()
		explicit.Tmpfs[0].Options = []string{option}
		missing := expected.Clone()
		if Equivalent(missing, explicit, testPodmanAPIVersion) {
			t.Fatalf("Equivalent() ignored explicit tmpfs option %q", option)
		}
		matching := explicit.Clone()
		if !Equivalent(matching, explicit, testPodmanAPIVersion) {
			t.Fatalf("Equivalent() rejected explicit tmpfs option %q", option)
		}
	}
}

func TestPublicErrorsUseStableValueFreeContract(t *testing.T) {
	t.Parallel()

	_, err := Encode(minimalSpec(), CreateOptions{})
	var validation containerconfig.ValidationError
	if !errors.As(err, &validation) || validation.Code != containerconfig.ValidationInvalidValue ||
		validation.Path != "/image_reference" || strings.Contains(err.Error(), testImage) {
		t.Fatalf("Encode() error = %#v", err)
	}
}
