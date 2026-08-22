package containerconfig_test

import (
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
)

func TestCanonicalOrdersPortableSetsAndOwnsInput(t *testing.T) {
	t.Parallel()

	original := unorderedSpec()
	wantOriginal := original.Clone()
	want := containerconfig.Spec{
		CapAdd: []string{"A", "B"}, CapDrop: []string{"C", "D"},
		Devices: []containerconfig.DeviceMapping{
			{Source: testDeviceA, Target: testDeviceA, Permissions: "r"},
			{Source: testDeviceA, Target: testDeviceA, Permissions: "rw"},
			{Source: testDeviceB, Target: testDeviceB, Permissions: "r"},
		},
		ExtraHosts: []string{"a=192.0.2.1", "b=192.0.2.2"}, GroupAdd: []string{"1", "2"},
		Sysctls: map[string]string{"net.example": "1"},
		Tmpfs: []containerconfig.TmpfsMount{
			{Target: "/run", Options: []string{testNodevOption}},
			{Target: testTmpfsTarget, Options: []string{testNodevOption}},
			{Target: testTmpfsTarget, Options: []string{testNosuidOption}},
		},
		Ulimits: []containerconfig.Ulimit{
			{Name: testNoFileUlimit, Soft: 1, Hard: 2},
			{Name: testNoFileUlimit, Soft: 2, Hard: 2},
			{Name: testNoFileUlimit, Soft: 2, Hard: 3},
		},
		Environment: []string{testEnvironmentA, "B=2"}, Labels: []string{"a=1", "b=2"},
		ExposedPorts: []containerconfig.ExposedPort{
			{TargetPort: 53, Protocol: testTCPProtocol},
			{TargetPort: 53, Protocol: testUDPProtocol},
			{TargetPort: 80, Protocol: testTCPProtocol},
		},
		Ports: []containerconfig.PortBinding{
			{HostIP: "", PublishedPort: 8053, TargetPort: 53, Protocol: testTCPProtocol},
			{HostIP: "", PublishedPort: 8054, TargetPort: 53, Protocol: testTCPProtocol},
			{HostIP: "127.0.0.1", PublishedPort: 8053, TargetPort: 53, Protocol: testTCPProtocol},
			{HostIP: "", PublishedPort: 8053, TargetPort: 53, Protocol: testUDPProtocol},
			{HostIP: "", PublishedPort: 8080, TargetPort: 80, Protocol: testTCPProtocol},
		},
		Mounts: []containerconfig.Mount{
			{Kind: containerconfig.MountBind, Source: "/a", Target: "/a"},
			{Kind: containerconfig.MountVolume, Source: "/a", Target: "/a"},
			{Kind: containerconfig.MountVolume, Source: "/a", Target: "/a", ReadOnly: true},
			{Kind: containerconfig.MountBind, Source: "/b", Target: "/b"},
		},
	}

	got := containerconfig.Canonical(original)

	if !reflect.DeepEqual(got, want) {
		t.Fatalf("Canonical() = %#v, want %#v", got, want)
	}
	if !reflect.DeepEqual(original, wantOriginal) {
		t.Fatalf("Canonical() mutated input: %#v", original)
	}
}

func TestEquivalentNormalizesEmptySetsWithoutRemovingDuplicates(t *testing.T) {
	t.Parallel()

	left := containerconfig.Spec{CapAdd: []string{"B", "A", "A"}, Labels: []string{}, Sysctls: map[string]string{}}
	right := containerconfig.Spec{CapAdd: []string{"A", "A", "B"}}
	withoutDuplicate := containerconfig.Spec{CapAdd: []string{"A", "B"}}

	if !containerconfig.Equivalent(left, right) {
		t.Fatal("Equivalent() rejected reordered sets and equivalent empty values")
	}
	if containerconfig.Equivalent(left, withoutDuplicate) {
		t.Fatal("Equivalent() removed a duplicate value")
	}
}

func TestEquivalentPreservesOrderedFields(t *testing.T) {
	t.Parallel()

	left := containerconfig.Spec{
		Entrypoint: []string{testOrderedFirst, testOrderedSecond},
		Command:    []string{testOrderedFirst, testOrderedSecond},
		DNS:        []string{"192.0.2.1", "192.0.2.2"}, DNSOptions: []string{"timeout:1", "rotate"},
		DNSSearch:   []string{"first.example", "second.example"},
		Tmpfs:       []containerconfig.TmpfsMount{{Target: testTmpfsTarget, Options: []string{"mode=700", testNosuidOption}}},
		Healthcheck: &containerconfig.Healthcheck{Test: []string{"CMD", testOrderedFirst, testOrderedSecond}},
	}
	tests := []struct {
		name   string
		mutate func(*containerconfig.Spec)
	}{
		{"entrypoint", func(spec *containerconfig.Spec) { reverse(spec.Entrypoint) }},
		{"command", func(spec *containerconfig.Spec) { reverse(spec.Command) }},
		{"dns", func(spec *containerconfig.Spec) { reverse(spec.DNS) }},
		{"dns options", func(spec *containerconfig.Spec) { reverse(spec.DNSOptions) }},
		{"dns search", func(spec *containerconfig.Spec) { reverse(spec.DNSSearch) }},
		{"tmpfs options", func(spec *containerconfig.Spec) { reverse(spec.Tmpfs[0].Options) }},
		{"health command", func(spec *containerconfig.Spec) { reverse(spec.Healthcheck.Test[1:]) }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			right := left.Clone()
			test.mutate(&right)
			if containerconfig.Equivalent(left, right) {
				t.Fatal("Equivalent() ignored ordered input")
			}
		})
	}
}

func TestCanonicalPreservesOrderedEmptyCollections(t *testing.T) {
	t.Parallel()

	spec := containerconfig.Spec{
		Entrypoint: []string{}, Command: []string{}, DNS: []string{},
		DNSOptions: []string{}, DNSSearch: []string{},
	}

	if got := containerconfig.Canonical(spec); !reflect.DeepEqual(got, spec) {
		t.Fatalf("Canonical() = %#v, want %#v", got, spec)
	}
}

func unorderedSpec() containerconfig.Spec {
	return containerconfig.Spec{
		CapAdd: []string{"B", "A"}, CapDrop: []string{"D", "C"},
		Devices: []containerconfig.DeviceMapping{
			{Source: testDeviceB, Target: testDeviceB, Permissions: "r"},
			{Source: testDeviceA, Target: testDeviceA, Permissions: "rw"},
			{Source: testDeviceA, Target: testDeviceA, Permissions: "r"},
		},
		ExtraHosts: []string{"b=192.0.2.2", "a=192.0.2.1"}, GroupAdd: []string{"2", "1"},
		Sysctls: map[string]string{"net.example": "1"},
		Tmpfs: []containerconfig.TmpfsMount{
			{Target: testTmpfsTarget, Options: []string{testNosuidOption}},
			{Target: testTmpfsTarget, Options: []string{testNodevOption}},
			{Target: "/run", Options: []string{testNodevOption}},
		},
		Ulimits: []containerconfig.Ulimit{
			{Name: testNoFileUlimit, Soft: 2, Hard: 3},
			{Name: testNoFileUlimit, Soft: 2, Hard: 2},
			{Name: testNoFileUlimit, Soft: 1, Hard: 2},
		},
		Environment: []string{"B=2", testEnvironmentA}, Labels: []string{"b=2", "a=1"},
		ExposedPorts: []containerconfig.ExposedPort{
			{TargetPort: 80, Protocol: testTCPProtocol},
			{TargetPort: 53, Protocol: testUDPProtocol},
			{TargetPort: 53, Protocol: testTCPProtocol},
		},
		Ports: []containerconfig.PortBinding{
			{PublishedPort: 8080, TargetPort: 80, Protocol: testTCPProtocol},
			{PublishedPort: 8053, TargetPort: 53, Protocol: testUDPProtocol},
			{HostIP: "127.0.0.1", PublishedPort: 8053, TargetPort: 53, Protocol: testTCPProtocol},
			{PublishedPort: 8054, TargetPort: 53, Protocol: testTCPProtocol},
			{PublishedPort: 8053, TargetPort: 53, Protocol: testTCPProtocol},
		},
		Mounts: []containerconfig.Mount{
			{Kind: containerconfig.MountBind, Source: "/b", Target: "/b"},
			{Kind: containerconfig.MountVolume, Source: "/a", Target: "/a", ReadOnly: true},
			{Kind: containerconfig.MountVolume, Source: "/a", Target: "/a"},
			{Kind: containerconfig.MountBind, Source: "/a", Target: "/a"},
		},
	}
}

func reverse(values []string) {
	for left, right := 0, len(values)-1; left < right; left, right = left+1, right-1 {
		values[left], values[right] = values[right], values[left]
	}
}
