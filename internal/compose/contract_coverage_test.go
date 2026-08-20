package compose

import (
	"math"
	"reflect"
	"testing"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	contractDevX     = "/dev/x"
	contractHealth   = "CMD"
	contractRelative = "relative"
	contractTrue     = "true"
	contractValue    = "value"
	contractVolume   = "volume"
)

//nolint:cyclop,funlen // Contract matrix for every supported field and rejection boundary.
func TestWorkloadSpecFullFieldMappingAndRejections(t *testing.T) {
	t.Parallel()

	text := contractValue
	truth := true
	weight := uint16(10)
	stop := composetypes.Duration(3 * time.Second)
	retries := uint64(2)
	service := composetypes.ServiceConfig{
		Name: "api", ContainerName: "container", NetworkMode: composeBridgeNetwork,
		CgroupParent: "parent", Cgroup: "private",
		CPUS: 1.5, Hostname: "host", MemLimit: 1024, OomScoreAdj: 2, PidsLimit: 3, Restart: "always",
		ShmSize: 2048, StopSignal: "SIGTERM", StopGracePeriod: &stop, User: "1000",
		WorkingDir: composeTestWorkingDirectory,
		CapAdd:     []string{"NET_ADMIN"}, CapDrop: []string{"MKNOD"}, DNS: []string{"1.1.1.1"},
		DNSOpts: []string{"rotate"}, DNSSearch: []string{"example.test"}, GroupAdd: []string{"100"},
		Sysctls:     composetypes.Mapping{"net.ipv4.ip_forward": "1"},
		Environment: composetypes.MappingWithEquals{"EMPTY": nil, "KEY": &text},
		Expose:      composetypes.StringOrNumberList{"53/udp"},
		Labels:      composetypes.Labels{"team": "platform"},
		ExtraHosts:  composetypes.HostsList{"host": []string{"127.0.0.1"}},
		Init:        &truth, StdinOpen: true, OomKillDisable: true, ReadOnly: true, Tty: true,
		BlkioConfig: &composetypes.BlkioConfig{Weight: weight},
		Devices:     []composetypes.DeviceMapping{{Source: "/dev/a", Target: "/dev/b", Permissions: "rw"}},
		Tmpfs:       composetypes.StringList{"/tmp:ro,size=1m"},
		Ulimits:     map[string]*composetypes.UlimitsConfig{"core": {Single: 4}},
		Ports:       []composetypes.ServicePortConfig{{Published: "8080", Target: 80, Protocol: composeProtocolTCP}},
		SecurityOpt: []string{"no-new-privileges"},
		Volumes:     []composetypes.ServiceVolumeConfig{{Type: contractVolume, Target: "/cache"}},
		HealthCheck: &composetypes.HealthCheckConfig{Test: []string{contractHealth, contractTrue}, Retries: &retries},
	}
	spec, err := workloadSpecFromService(service, domain.Platform{
		OS: archiveLinuxOS, Architecture: archiveAMD64,
	}, "", "")
	if err != nil || spec.CPUs != "1.5" || spec.OOMScoreAdj == nil || spec.PidsLimit == nil ||
		spec.BlkioWeight == nil || spec.StopTimeout == nil || spec.Init == nil || spec.Healthcheck == nil ||
		!reflect.DeepEqual(spec.Environment, []string{"EMPTY", "KEY=value"}) ||
		!reflect.DeepEqual(spec.ExposedPorts, []domain.ExposedPort{{TargetPort: 53, Protocol: composeProtocolUDP}}) {
		t.Fatalf("full mapping = %#v, %v", spec, err)
	}
	if cloneMapping(composetypes.Mapping{"a": "b"})["a"] != "b" || *clonePointer(&truth) != truth ||
		truePointer(true) == nil || !reflect.DeepEqual(hostsList(service.ExtraHosts), []string{"host=127.0.0.1"}) {
		t.Fatal("non-nil helper semantics were not preserved")
	}

	var target domain.WorkloadSpec
	badUlimits := []map[string]*composetypes.UlimitsConfig{
		{"x": nil}, {"x": {Single: 1, Soft: 1}}, {"x": {Soft: -2}},
		{"x": {Soft: -1, Hard: 1}}, {"x": {Soft: 2, Hard: 1}},
	}
	for _, value := range badUlimits {
		if addUlimits(&target, value) {
			t.Fatalf("ulimit accepted %#v", value)
		}
	}
	badDevices := []composetypes.DeviceMapping{
		{Source: contractRelative, Target: contractDevX, Permissions: "r"},
		{Source: contractDevX, Target: contractRelative, Permissions: "r"},
		{Source: contractDevX, Target: "/dev/y", Permissions: "x"},
	}
	for _, value := range badDevices {
		if addDevices(&target, []composetypes.DeviceMapping{value}) {
			t.Fatalf("device accepted %#v", value)
		}
	}
	for _, value := range []string{"0", "65536", "80/sctp", "80/tcp/extra"} {
		if addExposedPorts(&target, composetypes.StringOrNumberList{value}) {
			t.Fatalf("exposed port accepted %q", value)
		}
	}
	badMounts := []composetypes.ServiceVolumeConfig{
		{Type: composeBindMountType, Source: contractRelative, Target: "/x"},
		{Type: composeBindMountType, Source: "/x", Target: contractRelative},
		{Type: contractVolume, Source: "named", Target: "/x"},
		{Type: contractVolume, Target: "/x", ReadOnly: true},
	}
	for _, value := range badMounts {
		if addMounts(&target, []composetypes.ServiceVolumeConfig{value}, "", "") {
			t.Fatalf("mount accepted %#v", value)
		}
	}
	tooManyRetries := uint64(math.MaxInt) + 1
	if addHealthcheck(&target, &composetypes.HealthCheckConfig{Retries: &tooManyRetries}) ||
		addHealthcheck(&target, &composetypes.HealthCheckConfig{Disable: true, Timeout: &stop}) {
		t.Fatal("invalid healthcheck accepted")
	}
}

func TestRepositoryDocumentCollectorContractMatrix(t *testing.T) {
	t.Parallel()

	for _, content := range [][]byte{
		[]byte("services: [bad]\n"),
		[]byte("services:\n  api: bad\n"),
		[]byte("services:\n  api:\n    develop: {}\n"),
		[]byte("configs:\n  x:\n    file: /absolute\nservices: {}\n"),
	} {
		if _, _, valid := repositoryDocumentReferences(content, "."); valid {
			t.Fatalf("repository document accepted %q", content)
		}
	}
	var documents []repositoryDocument
	pathList := []any{map[string]any{repositoryPathKey: "a.env", "required": true, "format": "raw"}}
	if !collectResourceFiles(map[string]any{"external": "ignored"}, ".", &documents) ||
		!collectExtends(map[string]any{"service": "base"}, ".", &documents) ||
		!collectPathList(pathList, ".", &documents) {
		t.Fatal("supported secondary-reference form rejected")
	}
	paths, valid := repositoryPaths([]any{"a.yaml", "b.yaml"}, "sub")
	if !valid || !reflect.DeepEqual(paths, []string{"sub/a.yaml", "sub/b.yaml"}) {
		t.Fatalf("secondary path bases = %q, %v", paths, valid)
	}
	var mounts []string
	if !collectBindMounts([]any{"named:/data", map[string]any{"type": contractVolume}}, ".", &mounts) || mounts != nil {
		t.Fatalf("non-bind mounts = %q", mounts)
	}
}
