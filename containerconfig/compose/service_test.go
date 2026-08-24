//nolint:goconst // Conversion matrices keep source values beside their expected portable fields.
package compose

import (
	"errors"
	"math"
	"reflect"
	"testing"
	"time"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	testDataPath = "/data"
	testRunPath  = "/run"
)

//nolint:cyclop // Contract matrix for every supported field and rejection boundary.
func TestFromServiceMapsPortableFields(t *testing.T) {
	t.Parallel()

	text := "value"
	truth := true
	weight := uint16(10)
	stop := composetypes.Duration(3 * time.Second)
	retries := uint64(2)
	service := composetypes.ServiceConfig{
		Name: "api", ContainerName: "container", NetworkMode: bridgeNetwork,
		CgroupParent: "parent", Cgroup: "private", Command: []string{"command"}, Entrypoint: []string{"entrypoint"},
		CPUS: 1.5, Hostname: "host", MemLimit: 1024, OomScoreAdj: 2, PidsLimit: 3, Restart: "always",
		ShmSize: 2048, StopSignal: "SIGTERM", StopGracePeriod: &stop, User: "1000", WorkingDir: "/work",
		CapAdd: []string{"NET_ADMIN"}, CapDrop: []string{"MKNOD"}, DNS: []string{"1.1.1.1"},
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
		Ports:       []composetypes.ServicePortConfig{{Published: "8080", Target: 80, Protocol: protocolTCP}},
		SecurityOpt: []string{"no-new-privileges"},
		Volumes:     []composetypes.ServiceVolumeConfig{{Type: "volume", Target: "/cache"}},
		HealthCheck: &composetypes.HealthCheckConfig{Test: []string{"CMD", "true"}, Retries: &retries},
	}
	spec, err := FromService(service, containerconfig.Platform{
		OS: "linux", Architecture: "amd64",
	}, PathMapping{}, ServiceOptions{})
	if err != nil || spec.CPUs != "1.5" || spec.OOMScoreAdj == nil || spec.PidsLimit == nil ||
		spec.BlkioWeight == nil || spec.StopTimeout == nil || spec.Init == nil || spec.Healthcheck == nil ||
		!reflect.DeepEqual(spec.Entrypoint, []string{"entrypoint"}) ||
		!reflect.DeepEqual(spec.Command, []string{"command"}) ||
		!reflect.DeepEqual(spec.Environment, []string{"EMPTY", "KEY=value"}) ||
		!reflect.DeepEqual(spec.ExposedPorts, []containerconfig.ExposedPort{{TargetPort: 53, Protocol: protocolUDP}}) {
		t.Fatalf("FromService() = %#v, %v", spec, err)
	}
	if cloneMapping(composetypes.Mapping{"a": "b"})["a"] != "b" || *clonePointer(&truth) != truth ||
		truePointer(true) == nil || !reflect.DeepEqual(hostsList(service.ExtraHosts), []string{"host=127.0.0.1"}) ||
		!reflect.DeepEqual(labelsList(service.Labels), []string{"team=platform"}) {
		t.Fatal("non-nil helper semantics were not preserved")
	}
}

//nolint:cyclop,gocyclo // Independent adapter validation branches form one focused matrix.
func TestServiceConversionBoundaries(t *testing.T) {
	t.Parallel()

	var spec containerconfig.Spec
	weight := uint16(10)
	if !addBlkio(&spec, &composetypes.BlkioConfig{Weight: weight}) || spec.BlkioWeight == nil ||
		addBlkio(&spec, &composetypes.BlkioConfig{Weight: 9}) {
		t.Fatal("blkio boundary")
	}
	duration := composetypes.Duration(2 * time.Second)
	badDuration := composetypes.Duration(time.Millisecond)
	if !addStopTimeout(&spec, &duration) || spec.StopTimeout == nil || addStopTimeout(&spec, &badDuration) {
		t.Fatal("stop timeout boundary")
	}
	if !addDevices(&spec, []composetypes.DeviceMapping{{Source: "/dev/a", Target: "/dev/b", Permissions: "rwm"}}) {
		t.Fatal("valid device rejected")
	}
	for _, permission := range []string{"", "rr", "x"} {
		if validDevicePermissions(permission) {
			t.Fatalf("permission %q accepted", permission)
		}
	}
	if !addTmpfs(&spec, composetypes.StringList{testRunPath, "/tmp:ro,size=1m"}) ||
		addTmpfs(&spec, composetypes.StringList{"relative"}) {
		t.Fatal("tmpfs boundary")
	}
	if !addUlimits(&spec, map[string]*composetypes.UlimitsConfig{"nofile": {Soft: 1, Hard: 2}}) ||
		addUlimits(&spec, map[string]*composetypes.UlimitsConfig{"": {}}) {
		t.Fatal("ulimit boundary")
	}
	validPorts := []composetypes.ServicePortConfig{{Published: "80", Target: 81, Protocol: protocolTCP, HostIP: "::1"}}
	invalidPorts := []composetypes.ServicePortConfig{{Published: "0", Target: 1, Protocol: protocolTCP}}
	if !addPorts(&spec, validPorts) || addPorts(&spec, invalidPorts) || !validHostIP("") || validHostIP("01.2.3.4") {
		t.Fatal("port boundary")
	}
	if !addSecurityOptions(&spec, []string{"no-new-privileges=true"}) || addSecurityOptions(&spec, []string{"label=x"}) {
		t.Fatal("security boundary")
	}
	volumes := []composetypes.ServiceVolumeConfig{
		{Type: "volume", Target: "/cache"},
		{Type: "bind", Source: "/repo/data", Target: testDataPath, ReadOnly: true},
	}
	if !addMounts(&spec, volumes, PathMapping{From: "/repo", To: "/runtime"}) ||
		spec.Mounts[1].Source != "/runtime/data" ||
		addMounts(&spec, []composetypes.ServiceVolumeConfig{{Type: "tmpfs", Target: "/x"}}, PathMapping{}) {
		t.Fatalf("mount boundary: %#v", spec.Mounts)
	}
	if !emptyVolumeOptions(nil) || emptyVolumeOptions(&composetypes.ServiceVolumeVolume{NoCopy: true}) {
		t.Fatal("volume options boundary")
	}
	zero := uint64(0)
	health := &composetypes.HealthCheckConfig{Test: []string{"CMD", "true"}, Retries: &zero}
	if !addHealthcheck(&spec, health) || spec.Healthcheck == nil || spec.Healthcheck.Retries != nil ||
		addHealthcheck(&spec, &composetypes.HealthCheckConfig{Disable: true, Test: []string{"NONE"}}) {
		t.Fatal("healthcheck boundary")
	}
	if durationString(nil) != "" || durationString(&duration) != "2s" || cloneMapping(nil) != nil ||
		clonePointer[int](nil) != nil || truePointer(false) != nil || hostsList(nil) != nil || labelsList(nil) != nil {
		t.Fatal("nil helper boundary")
	}
}

//nolint:cyclop // Rejection matrix keeps each portable contract boundary explicit.
func TestServiceConversionRejectsLossyValues(t *testing.T) {
	t.Parallel()

	var target containerconfig.Spec
	for _, value := range []map[string]*composetypes.UlimitsConfig{
		{"x": nil}, {"x": {Single: 1, Soft: 1}}, {"x": {Soft: -2}},
		{"x": {Soft: -1, Hard: 1}}, {"x": {Soft: 2, Hard: 1}},
	} {
		if addUlimits(&target, value) {
			t.Fatalf("ulimit accepted %#v", value)
		}
	}
	for _, value := range []composetypes.DeviceMapping{
		{Source: "relative", Target: "/dev/x", Permissions: "r"},
		{Source: "/dev/x", Target: "relative", Permissions: "r"},
		{Source: "/dev/x", Target: "/dev/y", Permissions: "x"},
	} {
		if addDevices(&target, []composetypes.DeviceMapping{value}) {
			t.Fatalf("device accepted %#v", value)
		}
	}
	for _, value := range []string{"0", "65536", "80/sctp", "80/tcp/extra"} {
		if addExposedPorts(&target, composetypes.StringOrNumberList{value}) {
			t.Fatalf("exposed port accepted %q", value)
		}
	}
	for _, value := range []composetypes.ServiceVolumeConfig{
		{Type: "bind", Source: "relative", Target: "/x"},
		{Type: "bind", Source: "/x", Target: "relative"},
		{Type: "volume", Source: "named", Target: "/x"},
		{Type: "volume", Target: "/x", ReadOnly: true},
	} {
		if addMounts(&target, []composetypes.ServiceVolumeConfig{value}, PathMapping{}) {
			t.Fatalf("mount accepted %#v", value)
		}
	}
	tooManyRetries := uint64(math.MaxInt) + 1
	stop := composetypes.Duration(time.Second)
	if addHealthcheck(&target, &composetypes.HealthCheckConfig{Retries: &tooManyRetries}) ||
		addHealthcheck(&target, &composetypes.HealthCheckConfig{Disable: true, Timeout: &stop}) {
		t.Fatal("invalid healthcheck accepted")
	}
	if path, valid := rebasePath("/outside", PathMapping{From: "/repo", To: "/runtime"}); !valid || path != "/outside" {
		t.Fatalf("outside path = %q, %v", path, valid)
	}
	if addMounts(&target, []composetypes.ServiceVolumeConfig{{
		Type: "bind", Source: "/repo/data", Target: "/data",
	}}, PathMapping{From: "/repo", To: "relative"}) {
		t.Fatal("relative rebase accepted")
	}
}

//nolint:cyclop,funlen // The validation matrix exercises independent error-code and field-path boundaries.
func TestValidateServiceReportsStableFieldPaths(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		service composetypes.ServiceConfig
		options ServiceOptions
		code    containerconfig.ValidationCode
		path    string
	}{
		{
			name: "unsupported field", service: composetypes.ServiceConfig{Name: "api/a", Build: &composetypes.BuildConfig{}},
			code: containerconfig.ValidationUnsupportedField, path: "/services/api~1a/build",
		},
		{
			name: "container name", service: composetypes.ServiceConfig{Name: "api", ContainerName: "BAD"},
			code: containerconfig.ValidationInvalidValue, path: "/services/api/container_name",
		},
		{
			name: "network", service: composetypes.ServiceConfig{Name: "api", ContainerName: "api", NetworkMode: "host"},
			code: containerconfig.ValidationUnsupportedCapability, path: "/services/api/network_mode",
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := ValidateService(test.service, test.options)
			var validation containerconfig.ValidationError
			if !errors.As(err, &validation) || validation.Code != test.code || validation.Path != test.path {
				t.Fatalf("ValidateService() error = %#v", err)
			}
		})
	}
	valid := composetypes.ServiceConfig{
		Name: "api", ContainerName: "api", NetworkMode: bridgeNetwork, PullPolicy: "never",
	}
	if ValidateService(valid, ServiceOptions{}) == nil ||
		ValidateService(valid, ServiceOptions{AllowPullPolicy: true}) != nil {
		t.Fatal("pull policy capability was not enforced")
	}
	if _, err := FromService(composetypes.ServiceConfig{Name: "api", Build: &composetypes.BuildConfig{}},
		containerconfig.Platform{}, PathMapping{}, ServiceOptions{}); err == nil {
		t.Fatal("FromService() accepted an unsupported field")
	}
	weight := uint16(9)
	invalidWeight := composetypes.ServiceConfig{
		Name: "api", ContainerName: "api", NetworkMode: bridgeNetwork,
		BlkioConfig: &composetypes.BlkioConfig{Weight: weight},
	}
	if ValidateService(invalidWeight, ServiceOptions{}) == nil {
		t.Fatal("ValidateService() accepted an invalid portable conversion")
	}
	field, _ := reflect.TypeFor[struct{ Value string }]().FieldByName("Value")
	if yamlField(field) != "value" || validContainerName("a_b") {
		t.Fatal("field and container-name fallback validation failed")
	}
	var spec containerconfig.Spec
	if !addHealthcheck(&spec, &composetypes.HealthCheckConfig{Test: []string{"CMD", "true"}}) ||
		spec.Healthcheck == nil || spec.Healthcheck.Retries != nil {
		t.Fatal("healthcheck without retries was not preserved")
	}
}
