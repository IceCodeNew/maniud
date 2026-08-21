package compose

import (
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

//nolint:cyclop // Contract matrix for every supported field and rejection boundary.
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
