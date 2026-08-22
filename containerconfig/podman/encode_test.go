//nolint:goconst,lll // White-box boundary matrices keep invalid values next to each field mutation.
package podman

import (
	"errors"
	"math"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
)

//nolint:cyclop // The assertion covers portable unordered fields and ordered DNS fields.
func TestCanonicalUsesPortableOrderAndPreservesDNSPriority(t *testing.T) {
	t.Parallel()

	spec := minimalSpec()
	spec.CapAdd = []string{"Z", "A"}
	spec.CapDrop = []string{"Z", "A"}
	spec.DNS = []string{"2001:db8::1", "1.1.1.1"}
	spec.DNSOptions = []string{"z", "a"}
	spec.DNSSearch = []string{"z.test", "a.test"}
	spec.ExtraHosts = []string{"z=192.0.2.2", "a=192.0.2.1"}
	spec.GroupAdd = []string{"z", "a"}
	spec.Environment = []string{"Z=1", "A=1"}
	spec.Labels = []string{"z=1", "a=1"}
	spec.Tmpfs = []containerconfig.TmpfsMount{{Target: "/z"}, {Target: "/a"}}
	spec.Ulimits = []containerconfig.Ulimit{{Name: "z"}, {Name: "a"}}
	spec.ExposedPorts = []containerconfig.ExposedPort{
		{TargetPort: 81, Protocol: protocolTCP},
		{TargetPort: 80, Protocol: protocolUDP},
		{TargetPort: 80, Protocol: protocolTCP},
	}
	spec.Ports = []containerconfig.PortBinding{
		{HostIP: "127.0.0.2", PublishedPort: 82, TargetPort: 83, Protocol: protocolTCP},
		{HostIP: "127.0.0.1", PublishedPort: 81, TargetPort: 82, Protocol: protocolTCP},
	}
	spec.Mounts = []containerconfig.Mount{{Source: "/z", Target: "/z"}, {Source: "/a", Target: "/a"}}
	canonical, err := Canonical(spec)
	if err != nil {
		t.Fatalf("Canonical() error = %v", err)
	}
	if canonical.CapAdd[0] != "A" || canonical.CapDrop[0] != "A" || canonical.ExtraHosts[0][0] != 'a' ||
		canonical.GroupAdd[0] != "a" || canonical.Environment[0][0] != 'A' || canonical.Labels[0][0] != 'a' ||
		canonical.Tmpfs[0].Target != "/a" || canonical.Ulimits[0].Name != "a" ||
		canonical.ExposedPorts[0].Protocol != protocolTCP || canonical.ExposedPorts[2].TargetPort != 81 ||
		canonical.Ports[0].TargetPort != 82 || canonical.Mounts[0].Target != "/a" {
		t.Fatalf("Canonical() = %#v", canonical)
	}
	if !slices.Equal(canonical.DNS, spec.DNS) || !slices.Equal(canonical.DNSOptions, spec.DNSOptions) ||
		!slices.Equal(canonical.DNSSearch, spec.DNSSearch) {
		t.Fatalf("Canonical() reordered DNS priority: %#v", canonical)
	}
	reorderedDNS := canonical.Clone()
	slices.Reverse(reorderedDNS.DNS)
	if containerconfig.Equivalent(canonical, reorderedDNS) {
		t.Fatal("DNS resolver priority compared equal after reordering")
	}
}

func TestCanonicalRejectsInvalidPortableScalars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		edit func(*containerconfig.Spec)
		code containerconfig.ValidationCode
	}{
		{"service", func(spec *containerconfig.Spec) { spec.ServiceName = "" }, containerconfig.ValidationInvalidValue},
		{"container", func(spec *containerconfig.Spec) { spec.ContainerName = "-bad" }, containerconfig.ValidationInvalidValue},
		{"platform", func(spec *containerconfig.Spec) { spec.Platform.OS = "darwin" }, containerconfig.ValidationInvalidValue},
		{"network", func(spec *containerconfig.Spec) { spec.NetworkMode = "host" }, containerconfig.ValidationInvalidValue},
		{"entrypoint", func(spec *containerconfig.Spec) { spec.Entrypoint = []string{"bad\x00"} }, containerconfig.ValidationInvalidValue},
		{"command", func(spec *containerconfig.Spec) { spec.Command = []string{"bad\x00"} }, containerconfig.ValidationInvalidValue},
		{"process", func(spec *containerconfig.Spec) { spec.Entrypoint = nil }, containerconfig.ValidationInvalidValue},
		{"hostname", func(spec *containerconfig.Spec) { spec.Hostname = "bad\x00" }, containerconfig.ValidationInvalidValue},
		{"user", func(spec *containerconfig.Spec) { spec.User = "bad\x00" }, containerconfig.ValidationInvalidValue},
		{"workdir", func(spec *containerconfig.Spec) { spec.WorkingDirectory = "bad\x00" }, containerconfig.ValidationInvalidValue},
		{"memory", func(spec *containerconfig.Spec) { spec.MemoryBytes = -1 }, containerconfig.ValidationInvalidValue},
		{"shm", func(spec *containerconfig.Spec) { spec.SharedMemoryBytes = -1 }, containerconfig.ValidationInvalidValue},
		{"oom", func(spec *containerconfig.Spec) { spec.OOMScoreAdj = new(1001) }, containerconfig.ValidationInvalidValue},
		{"cgroup", func(spec *containerconfig.Spec) { spec.Cgroup = "bad" }, containerconfig.ValidationInvalidValue},
		{"cgroup parent", func(spec *containerconfig.Spec) { spec.CgroupParent = "bad\x00" }, containerconfig.ValidationInvalidValue},
		{"cap add", func(spec *containerconfig.Spec) { spec.CapAdd = []string{"bad\x00"} }, containerconfig.ValidationInvalidValue},
		{"cap drop", func(spec *containerconfig.Spec) { spec.CapDrop = []string{"bad\x00"} }, containerconfig.ValidationInvalidValue},
		{"dns option", func(spec *containerconfig.Spec) { spec.DNSOptions = []string{"bad\x00"} }, containerconfig.ValidationInvalidValue},
		{"dns search", func(spec *containerconfig.Spec) { spec.DNSSearch = []string{"bad\x00"} }, containerconfig.ValidationInvalidValue},
		{"group", func(spec *containerconfig.Spec) { spec.GroupAdd = []string{"bad\x00"} }, containerconfig.ValidationInvalidValue},
		{"dns", func(spec *containerconfig.Spec) { spec.DNS = []string{"01.1.1.1"} }, containerconfig.ValidationInvalidValue},
		{"host", func(spec *containerconfig.Spec) { spec.ExtraHosts = []string{"bad"} }, containerconfig.ValidationInvalidValue},
		{"devices", func(spec *containerconfig.Spec) {
			spec.Devices = []containerconfig.DeviceMapping{{Source: "/dev/null"}}
		}, containerconfig.ValidationUnsupportedCapability},
		{"sysctls", func(spec *containerconfig.Spec) { spec.Sysctls = map[string]string{"a": "b"} }, containerconfig.ValidationUnsupportedCapability},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			spec := minimalSpec()
			test.edit(&spec)
			_, err := Canonical(spec)
			var validation containerconfig.ValidationError
			if !errorsAs(err, &validation) || validation.Code != test.code {
				t.Fatalf("Canonical() error = %#v", err)
			}
		})
	}
}

func errorsAs(err error, target *containerconfig.ValidationError) bool {
	return errors.As(err, target)
}

//nolint:cyclop,funlen,gocognit,gocyclo // The table exhausts independent scalar acceptance and rejection paths.
func TestScalarMappersCoverAcceptedAndRejectedForms(t *testing.T) {
	t.Parallel()

	for input, want := range map[string]string{
		"": "no", "no": "no", "always": "always", "unless-stopped": "unless-stopped",
		"on-failure": "on-failure", "on-failure:2": "on-failure",
	} {
		policy, _, valid := createRestart(input)
		if !valid || policy != want {
			t.Fatalf("createRestart(%q) = %q, %t", input, policy, valid)
		}
	}
	for _, input := range []string{"never", "always:2", "on-failure:0", "on-failure:x", "unknown"} {
		if _, _, valid := createRestart(input); valid {
			t.Fatalf("createRestart(%q) = true", input)
		}
	}
	for _, input := range []string{"SIGTERM", "term", "15", "64"} {
		if _, valid := parseSignal(input); !valid {
			t.Fatalf("parseSignal(%q) = false", input)
		}
	}
	for _, input := range []string{"0", "65", "SIGNOTREAL"} {
		if _, valid := parseSignal(input); valid {
			t.Fatalf("parseSignal(%q) = true", input)
		}
	}
	if signalName(-1) != "-1" || signalName(64) != "64" || signalName(15) != "SIGTERM" {
		t.Fatal("signalName() returned an invalid value")
	}
	for _, input := range []string{"", "1", "1.5", "0.00001", "8.000000000"} {
		value, valid := nanoCPUs(input)
		if !valid || input != "" && (value <= 0 || cpuString(value) == "") {
			t.Fatalf("nanoCPUs(%q) = %d, %t", input, value, valid)
		}
	}
	for _, input := range []string{"0", ".5", "1.0000000000", "+1", "-1", "9223372037", "1.000000001", "1.bad"} {
		if _, valid := nanoCPUs(input); valid {
			t.Fatalf("nanoCPUs(%q) = true", input)
		}
	}
	for _, value := range []string{"", "1ns", "5s"} {
		if _, valid := parseDuration(value); !valid {
			t.Fatalf("parseDuration(%q) = false", value)
		}
	}
	for _, value := range []string{"0s", "-1s", "bad"} {
		if _, valid := parseDuration(value); valid {
			t.Fatalf("parseDuration(%q) = true", value)
		}
	}
	if create, valid := createStopTimeout(new(int64(math.MaxInt64))); !valid || create == nil {
		t.Fatalf("createStopTimeout(max) = %#v, %t", create, valid)
	}
	for _, timeout := range []int64{0, -1} {
		if _, valid := createStopTimeout(&timeout); valid {
			t.Fatalf("createStopTimeout(%d) = true", timeout)
		}
	}
	if nonzeroInt64(0) != nil || nonzeroInt64(1) == nil || boolPointer(false) != nil || boolPointer(true) == nil {
		t.Fatal("optional create helpers returned invalid pointers")
	}
}

//nolint:cyclop // The assertion covers disjoint collection grammars.
func TestCreateCollectionsRejectAmbiguity(t *testing.T) {
	t.Parallel()

	if labels, valid := createLabels([]string{"key-only"}); !valid || labels["key-only"] != "" {
		t.Fatalf("createLabels(key-only) = %#v, %t", labels, valid)
	}
	for _, values := range [][]string{{"=value"}, {"a=1", "a=2"}, {"bad\x00=1"}} {
		if _, valid := createLabels(values); valid {
			t.Fatalf("createLabels(%#v) = true", values)
		}
	}
	for _, values := range [][]string{
		{"bad"}, {"A=1", "A=2"}, {"A=bad\x00"}, {podmanEnvironmentKey + "=custom"},
	} {
		if _, valid := createEnvironment(values); valid {
			t.Fatalf("createEnvironment(%#v) = true", values)
		}
	}
	invalidPorts := []struct {
		exposed []containerconfig.ExposedPort
		ports   []containerconfig.PortBinding
	}{
		{exposed: []containerconfig.ExposedPort{{Protocol: protocolTCP}}},
		{exposed: []containerconfig.ExposedPort{{TargetPort: 80, Protocol: "bad"}}},
		{exposed: []containerconfig.ExposedPort{{TargetPort: 80, Protocol: protocolTCP}, {TargetPort: 80, Protocol: protocolUDP}}},
		{ports: []containerconfig.PortBinding{{TargetPort: 80, Protocol: protocolTCP}}},
		{ports: []containerconfig.PortBinding{{PublishedPort: 80, TargetPort: 80, Protocol: protocolSCTP}}},
		{ports: []containerconfig.PortBinding{{HostIP: "bad", PublishedPort: 80, TargetPort: 80, Protocol: protocolTCP}}},
		{exposed: []containerconfig.ExposedPort{{TargetPort: 80, Protocol: protocolUDP}}, ports: []containerconfig.PortBinding{{PublishedPort: 80, TargetPort: 80, Protocol: protocolTCP}}},
		{ports: []containerconfig.PortBinding{{PublishedPort: 80, TargetPort: 80, Protocol: protocolTCP}, {PublishedPort: 81, TargetPort: 80, Protocol: protocolTCP}}},
	}
	for _, test := range invalidPorts {
		if _, _, valid := createPorts(test.exposed, test.ports); valid {
			t.Fatalf("createPorts(%#v) = true", test)
		}
	}
	if _, ports, valid := createPorts(nil, []containerconfig.PortBinding{{
		HostIP: "127.0.0.1", PublishedPort: 8080, TargetPort: 80, Protocol: protocolTCP,
	}}); !valid || len(ports) != 1 {
		t.Fatalf("createPorts(valid) = %#v, %t", ports, valid)
	}
}

//nolint:cyclop // The table exhausts independent artifact mapping paths.
func TestCreateMountUlimitAndHealthBranches(t *testing.T) {
	t.Parallel()

	invalidMounts := []struct {
		mounts []containerconfig.Mount
		tmpfs  []containerconfig.TmpfsMount
	}{
		{mounts: []containerconfig.Mount{{Kind: containerconfig.MountBind, Source: "/a"}}},
		{mounts: []containerconfig.Mount{{Kind: containerconfig.MountBind, Target: "/b"}}},
		{mounts: []containerconfig.Mount{{Kind: containerconfig.MountBind, Source: "/a", Target: "/same"}, {Kind: containerconfig.MountBind, Source: "/b", Target: "/same"}}},
		{mounts: []containerconfig.Mount{{Kind: containerconfig.MountVolume, Source: "/a", Target: "/b"}}},
		{mounts: []containerconfig.Mount{{Kind: containerconfig.MountVolume, Target: "/b", ReadOnly: true}}},
		{mounts: []containerconfig.Mount{{Kind: containerconfig.MountKind(255), Target: "/b"}}},
		{mounts: []containerconfig.Mount{{Kind: containerconfig.MountBind, Source: "/a", Target: "/same"}}, tmpfs: []containerconfig.TmpfsMount{{Target: "/same"}}},
		{tmpfs: []containerconfig.TmpfsMount{{Target: ""}}},
		{tmpfs: []containerconfig.TmpfsMount{{Target: "/tmp", Options: []string{"bad\x00"}}}},
	}
	for _, test := range invalidMounts {
		if _, _, valid := createMounts(test.mounts, test.tmpfs, false); valid {
			t.Fatalf("createMounts(%#v) = true", test)
		}
	}
	validMounts := []containerconfig.Mount{{Kind: containerconfig.MountBind, Source: "/a", Target: "/a", ReadOnly: true}, {Kind: containerconfig.MountVolume, Target: "/data"}}
	if mounts, volumes, valid := createMounts(validMounts, []containerconfig.TmpfsMount{{Target: "/tmp"}}, true); !valid || len(mounts) != 2 || len(volumes) != 1 || volumes[0].Options != nil {
		t.Fatalf("createMounts(valid) = %#v, %#v, %t", mounts, volumes, valid)
	}
	invalidUlimits := [][]containerconfig.Ulimit{
		{{Name: "", Soft: 1, Hard: 2}}, {{Name: ulimitNoFile, Soft: -2, Hard: 2}},
		{{Name: ulimitNoFile, Soft: -1, Hard: 2}}, {{Name: ulimitNoFile, Soft: 3, Hard: 2}},
		{{Name: ulimitNoFile, Soft: 1, Hard: 2}, {Name: "NOFILE", Soft: 1, Hard: 2}},
	}
	for _, values := range invalidUlimits {
		if _, valid := createUlimits(values); valid {
			t.Fatalf("createUlimits(%#v) = true", values)
		}
	}
	if values, valid := createUlimits([]containerconfig.Ulimit{{Name: ulimitNoFile, Soft: 1, Hard: -1}}); !valid || values[0].Type != "RLIMIT_NOFILE" {
		t.Fatalf("createUlimits(valid) = %#v, %t", values, valid)
	}
	if health, valid := createHealthcheck(&containerconfig.Healthcheck{Disabled: true}); !valid || health == nil || !slices.Equal(health.Test, []string{healthcheckNone}) {
		t.Fatalf("createHealthcheck(disabled) = %#v, %t", health, valid)
	}
	for _, health := range []*containerconfig.Healthcheck{
		{Disabled: true, Test: []string{"CMD"}}, {Interval: "bad"}, {Retries: new(0)}, {Test: []string{"bad\x00"}},
	} {
		if _, valid := createHealthcheck(health); valid {
			t.Fatalf("createHealthcheck(%#v) = true", health)
		}
	}
	if health, valid := createHealthcheck(&containerconfig.Healthcheck{Test: []string{"CMD", "true"}}); !valid || health == nil || health.Retries != 0 {
		t.Fatalf("createHealthcheck(command) = %#v, %t", health, valid)
	}
}

func TestEncodeMapsValidationFailures(t *testing.T) {
	t.Parallel()

	invalidPortable := minimalSpec()
	invalidPortable.ServiceName = ""
	if _, err := Encode(invalidPortable, CreateOptions{ImageReference: testImage}); err == nil {
		t.Fatal("Encode(invalid portable spec) error = nil")
	}
	tests := []func(*containerconfig.Spec){
		func(spec *containerconfig.Spec) { spec.Labels = []string{"a=1", "a=2"} },
		func(spec *containerconfig.Spec) { spec.Environment = []string{"bad"} },
		func(spec *containerconfig.Spec) { spec.Restart = "bad" },
		func(spec *containerconfig.Spec) { spec.StopSignal = "bad" },
		func(spec *containerconfig.Spec) { spec.StopTimeout = new(int64(0)) },
		func(spec *containerconfig.Spec) { spec.CPUs = "bad" },
		func(spec *containerconfig.Spec) { spec.BlkioWeight = new(1) },
		func(spec *containerconfig.Spec) { spec.PidsLimit = new(int64(-2)) },
		func(spec *containerconfig.Spec) { spec.Ports = []containerconfig.PortBinding{{}} },
		func(spec *containerconfig.Spec) { spec.Mounts = []containerconfig.Mount{{}} },
		func(spec *containerconfig.Spec) { spec.Ulimits = []containerconfig.Ulimit{{}} },
		func(spec *containerconfig.Spec) { spec.Healthcheck = &containerconfig.Healthcheck{Interval: "bad"} },
	}
	for index, edit := range tests {
		spec := minimalSpec()
		edit(&spec)
		if _, err := Encode(spec, CreateOptions{ImageReference: testImage}); err == nil {
			t.Fatalf("Encode(invalid %d) error = nil", index)
		}
	}
	if _, err := Encode(minimalSpec(), CreateOptions{ImageReference: "bad\x00"}); err == nil {
		t.Fatal("Encode(invalid image) error = nil")
	}
}

//nolint:cyclop // The assertion covers independent pure helper outcomes.
func TestTinyValidationHelpers(t *testing.T) {
	t.Parallel()

	if !validPlatform(containerconfig.Platform{OS: "linux", Architecture: "arm64", Variant: "v8"}) ||
		validPlatform(containerconfig.Platform{OS: "linux", Architecture: "arm64"}) ||
		!asciiAlphaNumeric('a') || !asciiAlphaNumeric('Z') || !asciiAlphaNumeric('0') || asciiAlphaNumeric('-') ||
		validName("a bad", 63) || validName(strings.Repeat("a", 64), 63) || validText("bad\x00") ||
		!validOptionalRange(nil, 0, 1) || validOptionalRange(new(2), 0, 1) ||
		!reflect.DeepEqual(cloneValue[int](nil), (*int)(nil)) {
		t.Fatal("validation helper returned an invalid result")
	}
	if strconv.IntSize == 64 {
		value := int64(math.MaxInt64)
		if _, valid := createStopTimeout(&value); !valid {
			t.Fatal("64-bit maximum timeout was rejected")
		}
	}
	if time.Second.String() != "1s" {
		t.Fatal("unreachable time representation")
	}
}
