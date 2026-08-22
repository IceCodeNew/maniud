package containerconfig_test

import (
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const (
	changedValue      = "changed"
	testDeviceA       = "/dev/a"
	testDeviceB       = "/dev/b"
	testEnvironmentA  = "A=1"
	testNoFileUlimit  = "nofile"
	testNodevOption   = "nodev"
	testNosuidOption  = "nosuid"
	testOrderedFirst  = "first"
	testOrderedSecond = "second"
	testTCPProtocol   = "tcp"
	testTmpfsTarget   = "/tmp"
	testUDPProtocol   = "udp"
)

func TestSpecCloneOwnsMutableValues(t *testing.T) {
	t.Parallel()

	integer := 1
	integer64 := int64(1)
	truth := true
	original := containerconfig.Spec{
		ServiceName: "api", ContainerName: "api", Platform: containerconfig.Platform{OS: "linux", Architecture: "amd64"},
		Entrypoint: []string{"entrypoint"}, Command: []string{"command"}, BlkioWeight: &integer,
		OOMScoreAdj: &integer, PidsLimit: &integer64, StopTimeout: &integer64,
		CapAdd: []string{"NET_ADMIN"}, CapDrop: []string{"MKNOD"}, DNS: []string{"192.0.2.1"},
		DNSOptions: []string{"rotate"}, DNSSearch: []string{"example.test"},
		Devices:    []containerconfig.DeviceMapping{{Source: testDeviceA, Target: testDeviceB, Permissions: "rw"}},
		ExtraHosts: []string{"host=192.0.2.2"}, GroupAdd: []string{"1000"}, Sysctls: map[string]string{"key": "value"},
		Tmpfs:       []containerconfig.TmpfsMount{{Target: testTmpfsTarget, Options: []string{"rw"}}},
		Ulimits:     []containerconfig.Ulimit{{Name: testNoFileUlimit, Soft: 1, Hard: 2}},
		Environment: []string{testEnvironmentA}, Labels: []string{"key=value"},
		ExposedPorts: []containerconfig.ExposedPort{{TargetPort: 80, Protocol: testTCPProtocol}},
		Ports:        []containerconfig.PortBinding{{PublishedPort: 8080, TargetPort: 80, Protocol: testTCPProtocol}},
		Mounts:       []containerconfig.Mount{{Kind: containerconfig.MountBind, Source: "/data", Target: "/data"}},
		Init:         &truth, StdinOpen: &truth, OOMKillDisable: &truth, ReadOnly: &truth, TTY: &truth,
		Healthcheck: &containerconfig.Healthcheck{Test: []string{"CMD", "true"}, Retries: &integer},
	}
	want := original.Clone()

	clone := original.Clone()
	mutateSpec(&clone)

	if !reflect.DeepEqual(original, want) {
		t.Fatalf("Clone() aliases original: got %#v, want %#v", original, want)
	}
}

func TestZeroSpecClonePreservesNilCollections(t *testing.T) {
	t.Parallel()

	if clone := (containerconfig.Spec{}).Clone(); !reflect.DeepEqual(clone, containerconfig.Spec{}) {
		t.Fatalf("Spec{}.Clone() = %#v", clone)
	}
}

func mutateSpec(spec *containerconfig.Spec) {
	*spec.BlkioWeight = 2
	*spec.OOMScoreAdj = 2
	*spec.PidsLimit = 2
	*spec.StopTimeout = 2
	*spec.Init = false
	*spec.StdinOpen = false
	*spec.OOMKillDisable = false
	*spec.ReadOnly = false
	*spec.TTY = false
	spec.Entrypoint[0] = changedValue
	spec.Command[0] = changedValue
	spec.CapAdd[0] = changedValue
	spec.CapDrop[0] = changedValue
	spec.DNS[0] = changedValue
	spec.DNSOptions[0] = changedValue
	spec.DNSSearch[0] = changedValue
	spec.Devices[0].Source = changedValue
	spec.ExtraHosts[0] = changedValue
	spec.GroupAdd[0] = changedValue
	spec.Sysctls["key"] = changedValue
	spec.Tmpfs[0].Options[0] = changedValue
	spec.Ulimits[0].Name = changedValue
	spec.Environment[0] = changedValue
	spec.Labels[0] = changedValue
	spec.ExposedPorts[0].Protocol = testUDPProtocol
	spec.Ports[0].Protocol = testUDPProtocol
	spec.Mounts[0].Source = changedValue
	spec.Healthcheck.Test[0] = changedValue
	*spec.Healthcheck.Retries = 2
}
