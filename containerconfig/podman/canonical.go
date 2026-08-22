package podman

import (
	"reflect"
	"slices"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/containerconfig"
)

// Equivalent reports whether observed represents expected after native Libpod
// defaulting and inspect normalization.
func Equivalent(observed containerconfig.Spec, expected containerconfig.Spec) bool {
	observed = observed.Clone()
	expected = expected.Clone()
	observed.ServiceName = expected.ServiceName
	observed.Platform = expected.Platform
	observed.Environment = podmanComparableEnvironment(observed.Environment, expected.Environment)
	observed.Ulimits = podmanComparableUlimits(observed.Ulimits, expected.Ulimits)
	observed, observedErr := Canonical(observed)
	expected, expectedErr := Canonical(expected)

	return observedErr == nil && expectedErr == nil && reflect.DeepEqual(observed, expected)
}

func podmanComparableEnvironment(observed, expected []string) []string {
	expectedKeys := make(map[string]struct{}, len(expected))
	for _, value := range expected {
		key, _, _ := strings.Cut(value, "=")
		expectedKeys[key] = struct{}{}
	}

	return slices.DeleteFunc(observed, func(value string) bool {
		key, _, _ := strings.Cut(value, "=")
		_, explicit := expectedKeys[key]

		return !explicit && (key == "HOME" || key == "HOSTNAME")
	})
}

func podmanComparableUlimits(observed, expected []containerconfig.Ulimit) []containerconfig.Ulimit {
	expectedNames := make(map[string]struct{}, len(expected))
	for _, value := range expected {
		expectedNames[value.Name] = struct{}{}
	}

	return slices.DeleteFunc(observed, func(value containerconfig.Ulimit) bool {
		_, explicit := expectedNames[value.Name]

		return !explicit
	})
}

//nolint:cyclop // Libpod independently supplies defaults for optional fields.
func canonicalDefaults(spec *containerconfig.Spec) {
	if spec.Cgroup == namespacePrivate {
		spec.Cgroup = ""
	}
	if spec.Restart == "no" {
		spec.Restart = ""
	}
	if spec.SharedMemoryBytes == defaultSharedMemoryBytes {
		spec.SharedMemoryBytes = 0
	}
	if spec.StopSignal == signalTerminateName || spec.StopSignal == "15" {
		spec.StopSignal = ""
	}
	if spec.StopTimeout != nil && *spec.StopTimeout == defaultStopTimeout {
		spec.StopTimeout = nil
	}
	if spec.OOMScoreAdj != nil && *spec.OOMScoreAdj == 0 {
		spec.OOMScoreAdj = nil
	}
	if spec.Init != nil && !*spec.Init {
		spec.Init = nil
	}
	if spec.StdinOpen != nil && !*spec.StdinOpen {
		spec.StdinOpen = nil
	}
	if spec.OOMKillDisable != nil && !*spec.OOMKillDisable {
		spec.OOMKillDisable = nil
	}
	if spec.ReadOnly != nil && !*spec.ReadOnly {
		spec.ReadOnly = nil
	}
	if spec.TTY != nil && !*spec.TTY {
		spec.TTY = nil
	}
	canonicalPorts(spec)
	canonicalTmpfs(spec)
}

func canonicalPorts(spec *containerconfig.Spec) {
	bound := make(map[string]struct{}, len(spec.Ports))
	for index := range spec.Ports {
		if spec.Ports[index].HostIP == hostAnyIPv4 {
			spec.Ports[index].HostIP = ""
		}
		key := strconv.FormatUint(uint64(spec.Ports[index].TargetPort), 10) + "/" + spec.Ports[index].Protocol
		bound[key] = struct{}{}
	}
	spec.ExposedPorts = slices.DeleteFunc(spec.ExposedPorts, func(value containerconfig.ExposedPort) bool {
		key := strconv.FormatUint(uint64(value.TargetPort), 10) + "/" + value.Protocol
		_, published := bound[key]

		return published
	})
}

func canonicalTmpfs(spec *containerconfig.Spec) {
	for index := range spec.Tmpfs {
		spec.Tmpfs[index].Options = slices.DeleteFunc(spec.Tmpfs[index].Options, func(value string) bool {
			return value == "rprivate" || value == "nosuid" || value == "nodev" || value == "tmpcopyup"
		})
	}
}

func canonicalPodmanOrderedCollections(spec *containerconfig.Spec) {
	if len(spec.DNS) == 0 {
		spec.DNS = nil
	}
	if len(spec.DNSOptions) == 0 {
		spec.DNSOptions = nil
	}
	if len(spec.DNSSearch) == 0 {
		spec.DNSSearch = nil
	}
}
