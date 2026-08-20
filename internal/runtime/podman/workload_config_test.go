package podman

import (
	"bytes"
	"encoding/json"
	"fmt"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func testCreateOptions() application.WorkloadCreateOptions {
	return application.WorkloadCreateOptions{CopyImageVolumes: true}
}

const (
	podmanTestImageDataTarget = "/image-data"
	podmanTestMountTarget     = "/target"
	podmanTestMountSource     = "/source"
	podmanTestVolumeSource    = "/volume"
	podmanTestVolumeName      = "test-volume"
)

func podmanRichWorkload(t *testing.T) domain.DesiredWorkload {
	t.Helper()
	workload := podmanTestWorkload(t)
	workload.WorkloadSpec = domain.WorkloadSpec{
		ServiceName: podmanTestService, ContainerName: podmanTestContainer,
		Platform:   domain.Platform{OS: podmanOSLinux, Architecture: podmanArchAMD64},
		Entrypoint: []string{podmanTestEntrypoint}, Command: []string{podmanTestCommand},
		NetworkMode: podmanNetworkBridge, BlkioWeight: new(500),
		CgroupParent: "/maniud.slice", Cgroup: podmanCgroupHost, CPUs: "1.5", Hostname: "api.internal",
		MemoryBytes: 256 << 20, OOMScoreAdj: new(100), PidsLimit: new(int64(-1)),
		Restart: podmanRestartOnFailure + ":3", SharedMemoryBytes: 32 << 20,
		StopSignal:  podmanSignalInterruptName,
		StopTimeout: new(int64(20)), User: "1000:1000", WorkingDirectory: "/app",
		CapAdd: []string{"NET_BIND_SERVICE"}, CapDrop: []string{"MKNOD"},
		DNS: []string{"1.1.1.1", "2001:4860:4860::8888"}, DNSOptions: []string{"ndots:1"},
		DNSSearch: []string{"example.test"}, ExtraHosts: []string{"db=192.0.2.10"},
		GroupAdd: []string{"2000"}, Tmpfs: []domain.TmpfsMount{{
			Target: podmanTestTmpfs, Options: []string{"size=64m", "mode=1777"},
		}},
		Ulimits:     []domain.Ulimit{{Name: podmanTestNoFile, Soft: 1024, Hard: 2048}},
		Environment: []string{"B=two", "A=one"}, Labels: []string{"com.example.role=api"},
		ExposedPorts:    []domain.ExposedPort{{TargetPort: 9090, Protocol: podmanProtocolSCTP}},
		Ports:           []domain.PortBinding{{PublishedPort: 18080, TargetPort: 8080, Protocol: podmanProtocolTCP}},
		NoNewPrivileges: true,
		Mounts: []domain.Mount{{
			Kind: domain.MountBind, Source: "/srv/data", Target: "/data", ReadOnly: true,
		}, {Kind: domain.MountVolume, Target: podmanTestImageDataTarget}},
		Init: new(true), StdinOpen: new(true),
		OOMKillDisable: new(true), ReadOnly: new(true), TTY: new(true),
		Healthcheck: &domain.Healthcheck{
			Test: []string{podmanTestHealthCMD, "check"}, Interval: "30s", Timeout: "5s", Retries: new(3),
			StartPeriod: "10s", StartInterval: "2s",
		},
	}
	workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)

	return workload
}

func podmanRichInspect(t *testing.T, workload domain.DesiredWorkload) podmanInspectData {
	t.Helper()
	labels := workloadOwnershipLabels(workload, podmanTestTransaction)
	labels["com.example.role"] = "api"

	return podmanInspectData{
		ID: podmanTestContainerID, Name: workload.ContainerName, Image: podmanImageConfig,
		ImageName: workload.Image.Reference, ImageDigest: podmanManifestDigest,
		State: &podmanInspectState{Status: podmanStateRunning, Running: true},
		Mounts: []podmanInspectMount{{
			Type: podmanMountBind, Source: "/srv/data", Destination: "/data",
			Options: []string{podmanRecursiveBind}, ReadWrite: false, Propagation: podmanPropagationPrivate,
		}, {
			Type: podmanMountVolume, Name: "anonymous-volume", Source: "/var/lib/containers/storage/volumes/anonymous/_data",
			Destination: podmanTestImageDataTarget, Driver: podmanVolumeDriverLocal, ReadWrite: true,
		}},
		Config: &podmanInspectConfig{
			Image: workload.Image.Reference, Command: []string{podmanTestCommand},
			Entrypoint: []string{podmanTestEntrypoint}, Labels: labels,
			Environment: []string{"A=one", "B=two"}, Hostname: "api.internal", User: "1000:1000",
			WorkingDir: "/app", OpenStdin: true, TTY: true,
			StopSignal: podmanSignalInterruptName, StopTimeout: 20,
			Healthcheck: &podmanHealthConfig{
				Test: []string{podmanTestHealthCMD, "check"}, Interval: 30 * time.Second, Timeout: 5 * time.Second,
				Retries: 3, StartPeriod: 10 * time.Second, StartInterval: 2 * time.Second,
			},
			ExposedPorts: map[string]any{"8080/tcp": nil, "9090/sctp": nil},
		},
		HostConfig: &podmanInspectHost{
			NetworkMode: podmanNetworkBridge, CgroupMode: podmanCgroupHost, Cgroups: podmanCgroupsEnabled,
			CgroupParent: "/maniud.slice", NanoCPUs: 1_500_000_000,
			CPUPeriod: podmanCPUPeriod, CPUQuota: 150_000, Memory: 256 << 20,
			OOMKillDisable: true, OOMScoreAdj: 100, PidsLimit: -1, BlkioWeight: 500, ShmSize: 32 << 20,
			RestartPolicy: &podmanInspectRestart{Name: podmanRestartOnFailure, MaximumRetryCount: 3},
			CapAdd:        []string{"NET_BIND_SERVICE"}, CapDrop: []string{"MKNOD"},
			DNS: []string{"1.1.1.1", "2001:4860:4860::8888"}, DNSSearch: []string{"example.test"},
			DNSOptions: []string{"ndots:1"}, ExtraHosts: []string{"db:192.0.2.10"}, GroupAdd: []string{"2000"},
			Binds: []string{"/srv/data:/data:rbind,ro,rprivate"},
			Tmpfs: map[string]string{
				podmanTestTmpfs: "size=64m,mode=1777," + podmanPropagationPrivate + ",nosuid,nodev,tmpcopyup",
			},
			Ulimits: []podmanInspectUlimit{{Name: "RLIMIT_NOFILE", Soft: 1024, Hard: 2048}},
			PortBindings: map[string][]podmanInspectPortBinding{
				"8080/tcp": {{HostIP: podmanHostAnyIPv4, HostPort: "18080"}},
			},
			Init: true, ReadonlyRootfs: true, SecurityOpt: []string{"no-new-privileges"},
		},
	}
}

func TestPodmanCreateConfigurationOmitsImageCopyForReplacementVolumes(t *testing.T) {
	t.Parallel()

	volumeWorkload := podmanTestWorkload(t)
	volumeWorkload.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: podmanTestImageDataTarget}}
	noCopy, valid := podmanCreateConfiguration(
		volumeWorkload, podmanTestTransaction, application.WorkloadCreateOptions{},
	)
	if !valid || len(noCopy.Volumes) != 1 || !slices.Equal(noCopy.Volumes[0].Options, []string{"nocopy"}) {
		t.Fatalf("podmanCreateConfiguration(no-copy) = %#v, %t", noCopy, valid)
	}
}

//nolint:cyclop // The assertion checks every resource group populated by the rich fixture.
func TestPodmanRichConfigurationRoundTripsThroughInspect(t *testing.T) {
	t.Parallel()

	workload := podmanRichWorkload(t)
	configuration, valid := podmanCreateConfiguration(workload, podmanTestTransaction, testCreateOptions())
	if !valid || configuration.ResourceLimits.Pids == nil || configuration.ResourceLimits.CPU == nil ||
		configuration.ResourceLimits.Memory == nil || configuration.ResourceLimits.BlockIO == nil ||
		configuration.CgroupNamespace.Mode != podmanCgroupHost || len(configuration.Mounts) != 2 ||
		len(configuration.Volumes) != 1 || !configuration.Volumes[0].IsAnonymous ||
		configuration.Volumes[0].Dest != podmanTestImageDataTarget ||
		len(configuration.Volumes[0].Options) != 0 {
		t.Fatalf("podmanCreateConfiguration(rich) = %#v, %t", configuration, valid)
	}
	encoded, valid := encodePodmanCreateConfiguration(configuration)
	wantVolume := []byte(`"volumes":[{"Name":"","Dest":"/image-data","Options":null,"IsAnonymous":true}]`)
	if !valid || !bytes.Contains(encoded, wantVolume) {
		t.Fatalf("encodePodmanCreateConfiguration() missing native anonymous volume: %s", encoded)
	}
	payload := podmanRichInspect(t, workload)
	container, valid := podmanContainerFromInspect(podmanTestContainerID, payload)
	if !valid {
		t.Fatal("podmanContainerFromInspect(rich) = false")
	}
	if !containerConfigurationMatches(container, workload) {
		t.Fatalf("containerConfigurationMatches(rich) = false\nobserved=%#v\nexpected=%#v",
			canonicalPodmanSpec(container.WorkloadSpec), canonicalPodmanSpec(workload.WorkloadSpec))
	}
	if container.State != ContainerRunning || container.Ownership.Status != domain.OwnershipManaged ||
		containerConfigurationDigest(container) == (domain.Digest{}) || len(container.RuntimeMounts) != 2 ||
		container.RuntimeMounts[1].Name != "anonymous-volume" {
		t.Fatalf("decoded rich container = %#v", container)
	}
}

//nolint:cyclop // The assertion covers each independent runtime default normalization.
func TestPodmanConfigurationCanonicalizesRuntimeDefaults(t *testing.T) {
	t.Parallel()

	spec := domain.WorkloadSpec{
		Cgroup: podmanCgroupPrivate, SharedMemoryBytes: podmanDefaultSharedMemory,
		StopSignal: "15", StopTimeout: new(podmanDefaultStopTimeout),
		OOMScoreAdj: new(0), Init: new(false), StdinOpen: new(false),
		OOMKillDisable: new(false), ReadOnly: new(false), TTY: new(false),
		Tmpfs: []domain.TmpfsMount{{Target: podmanTestTmpfs, Options: []string{
			"size=1m", podmanPropagationPrivate, "nosuid", "nodev", "tmpcopyup",
		}}},
		ExposedPorts: []domain.ExposedPort{{TargetPort: 80, Protocol: podmanProtocolTCP}},
		Ports: []domain.PortBinding{{
			HostIP: podmanHostAnyIPv4, PublishedPort: 8080, TargetPort: 80, Protocol: podmanProtocolTCP,
		}},
	}
	canonical := canonicalPodmanSpec(spec)
	if canonical.Cgroup != "" || canonical.SharedMemoryBytes != 0 || canonical.StopSignal != "" ||
		canonical.StopTimeout != nil || canonical.OOMScoreAdj != nil || canonical.Init != nil ||
		canonical.StdinOpen != nil || canonical.OOMKillDisable != nil || canonical.ReadOnly != nil ||
		canonical.TTY != nil || len(canonical.ExposedPorts) != 0 || canonical.Ports[0].HostIP != "" ||
		!slices.Equal(canonical.Tmpfs[0].Options, []string{"size=1m"}) {
		t.Fatalf("canonicalPodmanSpec() = %#v", canonical)
	}
}

//nolint:cyclop // The table covers independent scalar wire grammars.
func TestPodmanScalarMappingsCoverAcceptedForms(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"": "no", "no": "no", podmanRestartAlways: podmanRestartAlways,
		podmanRestartUnlessStopped: podmanRestartUnlessStopped, podmanRestartOnFailure: podmanRestartOnFailure,
		podmanRestartOnFailure + ":2": podmanRestartOnFailure,
	} {
		policy, _, valid := podmanRestart(input)
		if !valid || policy != want {
			t.Fatalf("podmanRestart(%q) = %q, %t", input, policy, valid)
		}
	}
	for _, input := range []string{podmanSignalTerminateName, "term", "15", "64"} {
		if _, valid := podmanSignal(input); !valid {
			t.Fatalf("podmanSignal(%q) = false", input)
		}
	}
	for _, input := range []string{"1", "1.5", "0.00001", "8.000000000"} {
		value, valid := podmanNanoCPUs(input)
		if !valid || value <= 0 || podmanCPUString(value) == "" {
			t.Fatalf("podmanNanoCPUs(%q) = %d, %t", input, value, valid)
		}
	}
	if timeout, valid := podmanObservedStopTimeout(^uint(0)); strconv.IntSize == 32 || valid || timeout != nil {
		if strconv.IntSize != 32 {
			t.Fatalf("podmanObservedStopTimeout(max) = %#v, %t", timeout, valid)
		}
	}
	if pointer, valid := podmanCreateStopTimeout(new(int64(math.MaxInt64))); !valid || pointer == nil {
		t.Fatalf("podmanCreateStopTimeout(max) = %#v, %t", pointer, valid)
	}
}

func TestPodmanConfigurationRejectsAmbiguousValues(t *testing.T) {
	t.Parallel()

	invalidCPUs := []string{"0", ".5", "1.0000000000", "+1", "-1", "9223372037", "1.000000001"}
	for _, value := range invalidCPUs {
		if _, valid := podmanNanoCPUs(value); valid {
			t.Fatalf("podmanNanoCPUs(%q) = true", value)
		}
	}
	for _, value := range []string{
		"never", podmanRestartAlways + ":2", podmanRestartOnFailure + ":0", podmanRestartOnFailure + ":x",
	} {
		if _, _, valid := podmanRestart(value); valid {
			t.Fatalf("podmanRestart(%q) = true", value)
		}
	}
	if _, _, valid := podmanCreatePorts(nil, []domain.PortBinding{
		{PublishedPort: 8080, TargetPort: 80, Protocol: podmanProtocolTCP},
		{PublishedPort: 8081, TargetPort: 80, Protocol: podmanProtocolTCP},
	}); valid {
		t.Fatal("podmanCreatePorts(ambiguous) = true")
	}
	if _, _, valid := podmanObservedMounts([]podmanInspectMount{{
		Type: podmanMountBind, Source: podmanTestMountSource,
		Destination: podmanTestMountTarget, Options: []string{podmanMountBind},
		Propagation: podmanPropagationPrivate, ReadWrite: true,
	}}, []string{"/source:/target:bind,rw,rprivate"}); valid {
		t.Fatal("podmanObservedMounts(unsupported options) = true")
	}
	if _, valid := podmanCreateHealthcheck(&domain.Healthcheck{
		Disabled: true, Test: []string{podmanTestHealthCMD},
	}); valid {
		t.Fatal("podmanCreateHealthcheck(conflicting disabled) = true")
	}
}

func TestPodmanCollectionMappersRejectDuplicateAndMalformedEntries(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		valid bool
	}{
		{name: "labels", valid: func() bool {
			_, ok := podmanLabels([]string{"a=1", "a=2"}, map[string]string{})

			return ok
		}()},
		{name: "environment", valid: func() bool {
			_, ok := podmanEnvironment([]string{"A=1", "A=2"})

			return ok
		}()},
		{name: "ulimits", valid: func() bool {
			_, ok := podmanCreateUlimits([]domain.Ulimit{{Name: podmanTestNoFile, Soft: 3, Hard: 2}})

			return ok
		}()},
		{name: "extra hosts", valid: validPodmanExtraHosts([]string{podmanTestInvalid})},
		{name: "dns", valid: validPodmanDNS([]string{"01.1.1.1"})},
	}
	for _, test := range tests {
		if test.valid {
			t.Fatalf("%s mapper accepted malformed input", test.name)
		}
	}
	labels, valid := podmanObservedLabels(nil)
	if !valid || !reflect.DeepEqual(labels, []string(nil)) {
		t.Fatalf("podmanObservedLabels(nil) = %#v", labels)
	}
}

//nolint:cyclop // The assertion checks every tiny optional-value helper together.
func TestPodmanDurationAndOptionalHelpers(t *testing.T) {
	t.Parallel()

	for _, value := range []string{"", "1ns", "5s"} {
		if _, valid := podmanDuration(value); !valid {
			t.Fatalf("podmanDuration(%q) = false", value)
		}
	}
	for _, value := range []string{"0s", "-1s", podmanTestBad} {
		if _, valid := podmanDuration(value); valid {
			t.Fatalf("podmanDuration(%q) = true", value)
		}
	}
	nonzero := podmanNonzeroInt64(1)
	trueValue := podmanBoolPointer(true)
	clone := clonePodmanPointer(new(1))
	if podmanDurationString(0) != "" || podmanDurationString(time.Second) != "1s" ||
		podmanNonzeroInt64(0) != nil || nonzero == nil || *nonzero != 1 || podmanBoolPointer(false) != nil ||
		trueValue == nil || !*trueValue || clonePodmanPointer[int](nil) != nil || clone == nil || *clone != 1 {
		t.Fatal("optional Podman helpers returned invalid values")
	}
	if strings.Contains(podmanPortSortKey(domain.PortBinding{TargetPort: 80, Protocol: "tcp"}), "80") == false {
		t.Fatal("podmanPortSortKey() omitted target port")
	}
}

func TestPodmanInspectMappingRejectsMalformedWireValues(t *testing.T) {
	t.Parallel()

	if _, valid := decodePodmanContainer(strings.NewReader(`{`)); valid {
		t.Fatal("decodePodmanContainer(malformed) = true")
	}
	if _, valid := decodePodmanContainer(strings.NewReader(`{}`)); valid {
		t.Fatal("decodePodmanContainer(missing fields) = true")
	}
	workload := podmanRichWorkload(t)
	base := podmanRichInspect(t, workload)
	invalidCore := base
	invalidCore.ID = "short"
	if _, valid := podmanContainerFromInspect(podmanTestContainerID, invalidCore); valid {
		t.Fatal("podmanContainerFromInspect(invalid core) = true")
	}
	invalidImage := base
	invalidImage.Image = podmanTestInvalid
	if _, valid := podmanContainerFromInspect(podmanTestContainerID, invalidImage); valid {
		t.Fatal("podmanContainerFromInspect(invalid image) = true")
	}
	if _, _, valid := podmanWorkloadFromInspect("id", "name", podmanInspectData{}); valid {
		t.Fatal("podmanWorkloadFromInspect(empty) = true")
	}
	invalidWorkload := base
	invalidWorkload.HostConfig = clonePodmanInspectHost(base.HostConfig)
	invalidWorkload.HostConfig.NetworkMode = "host"
	if _, _, valid := podmanWorkloadFromInspect(
		invalidWorkload.ID, invalidWorkload.Name, invalidWorkload,
	); valid {
		t.Fatal("podmanWorkloadFromInspect(invalid mapping) = true")
	}

	states := []struct {
		state *podmanInspectState
		want  ContainerState
		valid bool
	}{
		{state: nil, want: ContainerStateUnknown, valid: false},
		{state: &podmanInspectState{Status: "initialized"}, want: ContainerCreated, valid: true},
		{state: &podmanInspectState{Status: podmanStatePaused, Paused: true}, want: ContainerPaused, valid: true},
		{state: &podmanInspectState{Status: "stopped"}, want: ContainerExited, valid: true},
		{state: &podmanInspectState{Status: podmanStateRemoving}, want: ContainerRemoving, valid: true},
		{state: &podmanInspectState{Status: podmanStateUnknown}, want: ContainerStateUnknown, valid: true},
		{state: &podmanInspectState{Status: podmanTestInvalid}, want: ContainerStateUnknown, valid: false},
		{state: &podmanInspectState{Status: podmanStateRunning, Restarting: true}, valid: false},
		{state: &podmanInspectState{Status: podmanStateRunning, Dead: true}, valid: false},
	}
	for _, test := range states {
		got, valid := podmanContainerState(test.state)
		if got != test.want || valid != test.valid {
			t.Fatalf("podmanContainerState(%#v) = %d, %t", test.state, got, valid)
		}
	}
	podmanAssertMalformedInspectScalars(t)
	podmanAssertMalformedInspectCollections(t)
}

func podmanAssertMalformedInspectScalars(t *testing.T) {
	t.Helper()
	invalidLabels := map[string]string{"": "value"}
	if _, valid := podmanObservedLabels(invalidLabels); valid {
		t.Fatal("podmanObservedLabels(invalid) = true")
	}
	for _, environment := range [][]string{{podmanTestInvalid}, {"A=1", "A=2"}} {
		if validPodmanEnvironment(environment) {
			t.Fatalf("validPodmanEnvironment(%#v) = true", environment)
		}
	}
	for _, restart := range []podmanInspectRestart{
		{Name: "no", MaximumRetryCount: 1},
		{Name: podmanRestartAlways, MaximumRetryCount: 1},
		{Name: podmanRestartOnFailure},
		{Name: podmanRestartOnFailure, MaximumRetryCount: 2},
		{Name: podmanTestInvalid},
		{Name: podmanRestartOnFailure, MaximumRetryCount: math.MaxUint32},
	} {
		_, _ = podmanObservedRestart(restart)
	}
	if _, valid := podmanObservedStopSignal(podmanTestInvalid); valid {
		t.Fatal("podmanObservedStopSignal(invalid) = true")
	}
	podmanAssertMalformedInspectResources(t)
}

func podmanAssertMalformedInspectResources(t *testing.T) {
	t.Helper()
	if _, valid := podmanObservedCPUs(-1, podmanCPUPeriod, 1); valid {
		t.Fatal("podmanObservedCPUs(invalid) = true")
	}
	for _, weight := range []uint16{1, 1001} {
		if _, valid := podmanObservedBlkio(weight); valid {
			t.Fatalf("podmanObservedBlkio(%d) = true", weight)
		}
	}
	if _, valid := podmanObservedPids(-2); valid {
		t.Fatal("podmanObservedPids(-2) = true")
	}
	if _, valid := podmanObservedExtraHosts([]string{podmanTestInvalid}); valid {
		t.Fatal("podmanObservedExtraHosts(invalid) = true")
	}
	if _, valid := podmanObservedTmpfs(map[string]string{"": "rw"}); valid {
		t.Fatal("podmanObservedTmpfs(invalid) = true")
	}
	if _, valid := podmanObservedUlimits([]podmanInspectUlimit{{Name: "NOFILE", Soft: 1, Hard: 2}}); valid {
		t.Fatal("podmanObservedUlimits(invalid) = true")
	}
}

func podmanAssertMalformedInspectCollections(t *testing.T) {
	t.Helper()
	invalidPorts := []struct {
		exposed  map[string]any
		bindings map[string][]podmanInspectPortBinding
	}{
		{exposed: map[string]any{podmanTestBad: nil}},
		{bindings: map[string][]podmanInspectPortBinding{podmanTestPort80TCP: {}}},
		{bindings: map[string][]podmanInspectPortBinding{podmanTestPort80TCP: {{HostPort: "0"}}}},
		{bindings: map[string][]podmanInspectPortBinding{podmanTestPort80TCP: {{
			HostIP: podmanTestInvalid, HostPort: "80",
		}}}},
	}
	for _, test := range invalidPorts {
		if _, _, valid := podmanObservedPorts(test.exposed, test.bindings); valid {
			t.Fatalf("podmanObservedPorts(%#v) = true", test)
		}
	}
	podmanAssertMalformedInspectMounts(t)
	podmanAssertMalformedInspectHealth(t)
}

func podmanAssertMalformedInspectMounts(t *testing.T) {
	t.Helper()
	if _, _, valid := podmanObservedMounts(nil, []string{podmanTestDuplicate, podmanTestDuplicate}); valid {
		t.Fatal("podmanObservedMounts(duplicate binds) = true")
	}
	if _, _, valid := podmanObservedMounts([]podmanInspectMount{{
		Type: podmanMountBind, Source: podmanTestMountSource, Destination: podmanTestMountTarget,
		Options: []string{podmanRecursiveBind}, Propagation: podmanPropagationPrivate, ReadWrite: true,
	}}, nil); valid {
		t.Fatal("podmanObservedMounts(missing bind) = true")
	}
	if _, _, valid := podmanObservedMounts([]podmanInspectMount{{
		Type: podmanMountBind, Source: podmanTestMountSource, Destination: podmanTestBadNUL,
	}}, nil); valid {
		t.Fatal("podmanObservedMounts(invalid destination) = true")
	}
	if _, _, valid := podmanObservedMounts([]podmanInspectMount{{
		Type: podmanMountVolume, Name: podmanTestVolumeName, Source: podmanTestVolumeSource,
		Destination: podmanTestMountTarget, Driver: podmanVolumeDriverLocal, ReadWrite: true, SubPath: "nested",
	}}, nil); valid {
		t.Fatal("podmanObservedMounts(volume subpath) = true")
	}
	if _, _, valid := podmanObservedMounts([]podmanInspectMount{
		{
			Type: podmanMountVolume, Name: podmanTestVolumeName, Source: "/volume/one",
			Destination: podmanTestMountTarget, Driver: podmanVolumeDriverLocal, ReadWrite: true,
		}, {
			Type: podmanMountVolume, Name: "other", Source: "/volume/two",
			Destination: podmanTestMountTarget, Driver: podmanVolumeDriverLocal, ReadWrite: true,
		},
	}, nil); valid {
		t.Fatal("podmanObservedMounts(duplicate target) = true")
	}
	if _, _, valid := podmanObservedMounts([]podmanInspectMount{{
		Type: "unknown", Source: podmanTestMountSource, Destination: podmanTestMountTarget,
	}}, nil); valid {
		t.Fatal("podmanObservedMounts(unknown type) = true")
	}
	for _, volume := range []podmanInspectMount{
		{
			Type: podmanMountVolume, Source: podmanTestVolumeSource, Destination: podmanTestMountTarget,
			Driver: podmanVolumeDriverLocal, ReadWrite: true,
		},
		{
			Type: podmanMountVolume, Name: podmanTestVolumeName, Source: "relative", Destination: podmanTestMountTarget,
			Driver: podmanVolumeDriverLocal, ReadWrite: true,
		},
		{
			Type: podmanMountVolume, Name: podmanTestVolumeName, Source: "/volume/../other", Destination: podmanTestMountTarget,
			Driver: podmanVolumeDriverLocal, ReadWrite: true,
		},
		{
			Type: podmanMountVolume, Name: podmanTestVolumeName, Source: podmanTestVolumeSource,
			Destination: podmanTestMountTarget,
			Driver:      "plugin", ReadWrite: true,
		},
	} {
		if _, _, valid := podmanObservedMounts([]podmanInspectMount{volume}, nil); valid {
			t.Fatalf("podmanObservedMounts(%#v) = true", volume)
		}
	}
}

func podmanAssertMalformedInspectHealth(t *testing.T) {
	t.Helper()
	for _, health := range []*podmanHealthConfig{
		{Test: []string{podmanHealthcheckNone}, Interval: time.Second},
		{Test: []string{podmanTestBadNUL}},
	} {
		if _, valid := podmanObservedHealthcheck(health); valid {
			t.Fatalf("podmanObservedHealthcheck(%#v) = true", health)
		}
	}
	disabled, valid := podmanObservedHealthcheck(&podmanHealthConfig{Test: []string{podmanHealthcheckNone}})
	if !valid || disabled == nil || !disabled.Disabled {
		t.Fatalf("podmanObservedHealthcheck(disabled) = %#v, %t", disabled, valid)
	}
	if _, valid := podmanObservedSecurity([]string{"seccomp=unconfined"}); valid {
		t.Fatal("podmanObservedSecurity(invalid) = true")
	}
}

func TestPodmanConfigurationMappingCoversRemainingValidationBranches(t *testing.T) {
	t.Parallel()

	workload := podmanRichWorkload(t)
	payload := podmanRichInspect(t, workload)
	payload.Config.Healthcheck = &podmanHealthConfig{Test: []string{podmanTestBadNUL}}
	if _, _, valid := podmanWorkloadFromInspect(payload.ID, payload.Name, payload); valid {
		t.Fatal("podmanWorkloadFromInspect(invalid healthcheck) = true")
	}
	if _, _, valid := podmanObservedMounts(nil, []string{podmanTestBadNUL}); valid {
		t.Fatal("podmanObservedMounts(invalid bind text) = true")
	}
	if _, _, valid := podmanObservedMounts(nil, []string{"/source:/target:rbind,rw,rprivate"}); valid {
		t.Fatal("podmanObservedMounts(unmatched bind) = true")
	}
	labels, valid := podmanLabels([]string{"key-only"}, nil)
	if !valid || labels["key-only"] != "" {
		t.Fatalf("podmanLabels(key only) = %#v, %t", labels, valid)
	}
	if _, valid := podmanNanoCPUs("1.bad"); valid {
		t.Fatal("podmanNanoCPUs(invalid fraction) = true")
	}
	if _, _, valid := podmanCreateMounts([]domain.Mount{
		{Kind: domain.MountBind, Source: "/a", Target: podmanTestSamePath},
		{Kind: domain.MountBind, Source: "/b", Target: podmanTestSamePath},
	}, nil, testCreateOptions()); valid {
		t.Fatal("podmanCreateMounts(duplicate bind target) = true")
	}
	invalid := podmanTestWorkload(t)
	invalid.Devices = []domain.DeviceMapping{{Source: "/dev/null", Target: "/dev/null", Permissions: "r"}}
	if _, valid := podmanCreateConfiguration(
		invalid, podmanTestTransaction, testCreateOptions(),
	); valid {
		t.Fatal("podmanCreateConfiguration(unsupported device) = true")
	}
}

func clonePodmanInspectHost(source *podmanInspectHost) *podmanInspectHost {
	clone := *source

	return &clone
}

func TestPodmanCreateMappingRejectsMalformedDesiredValues(t *testing.T) {
	t.Parallel()

	if _, valid := podmanLabels([]string{"=value"}, map[string]string{}); valid {
		t.Fatal("podmanLabels(invalid key) = true")
	}
	if _, valid := podmanLabels(
		[]string{domain.LabelService + "=foreign"}, map[string]string{},
	); valid {
		t.Fatal("podmanLabels(reserved) = true")
	}
	if _, valid := podmanEnvironment([]string{podmanTestInvalid}); valid {
		t.Fatal("podmanEnvironment(invalid) = true")
	}
	podmanAssertMalformedCreateScalars(t)
	podmanAssertMalformedCreateCollections(t)
}

func podmanAssertMalformedCreateScalars(t *testing.T) {
	t.Helper()
	if _, valid := podmanCreateStopSignal(podmanTestInvalid); valid {
		t.Fatal("podmanCreateStopSignal(invalid) = true")
	}
	if _, valid := podmanSignal("0"); valid {
		t.Fatal("podmanSignal(0) = true")
	}
	if _, valid := podmanSignal("SIGNOTREAL"); valid {
		t.Fatal("podmanSignal(name) = true")
	}
	if podmanSignalName(-1) != "-1" || podmanSignalName(64) != "64" {
		t.Fatal("podmanSignalName(out of range) changed")
	}
	for _, timeout := range []int64{0, -1} {
		if _, valid := podmanCreateStopTimeout(&timeout); valid {
			t.Fatalf("podmanCreateStopTimeout(%d) = true", timeout)
		}
	}
	podmanAssertMalformedCreateResources(t)
}

func podmanAssertMalformedCreateResources(t *testing.T) {
	t.Helper()
	if _, valid := podmanCreateCPUs(podmanTestInvalid); valid {
		t.Fatal("podmanCreateCPUs(invalid) = true")
	}
	for _, weight := range []int{1, 1001} {
		if _, valid := podmanCreateBlkio(&weight); valid {
			t.Fatalf("podmanCreateBlkio(%d) = true", weight)
		}
	}
	for _, limit := range []int64{0, -2} {
		if _, valid := podmanCreatePidsLimit(&limit); valid {
			t.Fatalf("podmanCreatePidsLimit(%d) = true", limit)
		}
	}
}

func podmanAssertMalformedCreateCollections(t *testing.T) {
	t.Helper()
	invalidPorts := []struct {
		exposed []domain.ExposedPort
		ports   []domain.PortBinding
	}{
		{exposed: []domain.ExposedPort{{Protocol: podmanProtocolTCP}}},
		{exposed: []domain.ExposedPort{{TargetPort: 80, Protocol: podmanProtocolTCP}, {
			TargetPort: 80, Protocol: podmanProtocolUDP,
		}}},
		{ports: []domain.PortBinding{{TargetPort: 80, Protocol: podmanProtocolTCP}}},
		{ports: []domain.PortBinding{{
			HostIP: podmanTestInvalid, PublishedPort: 80, TargetPort: 80, Protocol: podmanProtocolTCP,
		}}},
		{exposed: []domain.ExposedPort{{TargetPort: 80, Protocol: podmanProtocolUDP}}, ports: []domain.PortBinding{{
			PublishedPort: 80, TargetPort: 80, Protocol: podmanProtocolTCP,
		}}},
	}
	for _, test := range invalidPorts {
		if _, _, valid := podmanCreatePorts(test.exposed, test.ports); valid {
			t.Fatalf("podmanCreatePorts(%#v) = true", test)
		}
	}
	invalidMounts := []struct {
		mounts []domain.Mount
		tmpfs  []domain.TmpfsMount
	}{
		{mounts: []domain.Mount{{Kind: domain.MountBind, Source: "/a", Target: ""}}},
		{mounts: []domain.Mount{{Kind: domain.MountBind, Source: "", Target: "/b"}}},
		{mounts: []domain.Mount{{Kind: domain.MountVolume, Source: "/a", Target: "/b"}}},
		{mounts: []domain.Mount{{Kind: domain.MountKind(255), Target: "/b"}}},
		{mounts: []domain.Mount{{Kind: domain.MountBind, Source: "/a", Target: podmanTestSamePath}},
			tmpfs: []domain.TmpfsMount{{Target: podmanTestSamePath}}},
		{tmpfs: []domain.TmpfsMount{{Target: ""}}},
	}
	for _, test := range invalidMounts {
		if _, _, valid := podmanCreateMounts(test.mounts, test.tmpfs, testCreateOptions()); valid {
			t.Fatalf("podmanCreateMounts(%#v) = true", test)
		}
	}
	podmanAssertMalformedCreateArtifacts(t)
}

func podmanAssertMalformedCreateArtifacts(t *testing.T) {
	t.Helper()
	if _, valid := podmanCreateUlimits([]domain.Ulimit{
		{Name: podmanTestNoFile, Soft: 1, Hard: 2}, {Name: "NOFILE", Soft: 1, Hard: 2},
	}); valid {
		t.Fatal("podmanCreateUlimits(duplicate) = true")
	}
	health, valid := podmanCreateHealthcheck(&domain.Healthcheck{Disabled: true})
	if !valid || health == nil || !slices.Equal(health.Test, []string{podmanHealthcheckNone}) {
		t.Fatalf("podmanCreateHealthcheck(disabled) = %#v, %t", health, valid)
	}
	if _, valid := podmanCreateHealthcheck(&domain.Healthcheck{Interval: podmanTestBad}); valid {
		t.Fatal("podmanCreateHealthcheck(invalid) = true")
	}
	podmanAssertMalformedCreateEncoding(t)
}

func podmanAssertMalformedCreateEncoding(t *testing.T) {
	t.Helper()
	invalidImage := podmanTestWorkload(t).Image
	invalidImage.Reference = podmanTestInvalid
	if _, err := parseExpectedImageReference(invalidImage); err == nil {
		t.Fatal("parseExpectedImageReference(invalid) = nil")
	}
	oversized := podmanTestWorkload(t)
	oversized.Labels = make([]string, 5000)
	for index := range oversized.Labels {
		oversized.Labels[index] = fmt.Sprintf("label-%d=%s", index, strings.Repeat("x", maximumTextBytes-20))
	}
	oversized.EffectiveDigest = domain.ComputeEffectiveDigest(oversized)
	configuration, valid := podmanCreateConfiguration(
		oversized, podmanTestTransaction, testCreateOptions(),
	)
	if !valid {
		t.Fatal("podmanCreateConfiguration(oversized valid values) = false")
	}
	if _, valid := encodePodmanCreateConfiguration(configuration); valid {
		t.Fatal("encodePodmanCreateConfiguration(oversized) = true")
	}
	encoded, err := json.Marshal(podmanCreateSpec{})
	wantEmpty := []byte(
		`{"image":"","raw_image_name":"","command":null,"entrypoint":null,"name":"",` +
			`"image_os":"","image_arch":"","labels":null,"env":null,"stop_timeout":null,` +
			`"restart_policy":"","netns":{},"cgroupns":{},"shm_size":null,` +
			`"resource_limits":{},"publish_image_ports":null}`,
	)
	if err != nil || !bytes.Equal(encoded, wantEmpty) {
		// The exact empty shape guards accidental omitempty changes in the narrow DTO.
		t.Fatalf("json.Marshal(empty create) = %s, %v", encoded, err)
	}
}
