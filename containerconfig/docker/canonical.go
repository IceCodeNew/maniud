package docker

import (
	"reflect"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/containerconfig"
)

// Equivalent reports whether observed represents expected after Docker Engine
// defaulting and inspect normalization.
func Equivalent(observed containerconfig.Spec, expected containerconfig.Spec) bool {
	observed = observed.Clone()
	expected = expected.Clone()
	observed.ServiceName = expected.ServiceName
	observed.Platform = expected.Platform
	if expected.SharedMemoryBytes == 0 && observed.SharedMemoryBytes == dockerDefaultSharedMemoryBytes {
		observed.SharedMemoryBytes = 0
	}
	if expected.Cgroup == "" && observed.Cgroup == string(containertypes.CgroupnsModePrivate) {
		observed.Cgroup = ""
	}
	if expected.Restart == "" && observed.Restart == string(containertypes.RestartPolicyDisabled) {
		observed.Restart = ""
	}
	observed = canonicalDockerSpec(observed)
	expected = canonicalDockerSpec(expected)

	return reflect.DeepEqual(observed, expected)
}

// Canonical validates spec and returns its stable Docker comparison form.
func Canonical(spec containerconfig.Spec) (containerconfig.Spec, error) {
	if err := Validate(spec, CreateOptions{ImageReference: "scratch", CopyImageVolumes: true}); err != nil {
		return containerconfig.Spec{}, err
	}

	return canonicalDockerSpec(spec), nil
}

func canonicalDockerSpec(spec containerconfig.Spec) containerconfig.Spec {
	spec = canonicalDockerPointers(spec)
	spec = containerconfig.Canonical(spec)
	canonicalDockerOrderedCollections(&spec)

	return spec
}

//nolint:cyclop // Docker inspect collapses each explicit false or zero pointer independently.
func canonicalDockerPointers(spec containerconfig.Spec) containerconfig.Spec {
	if spec.StdinOpen != nil && !*spec.StdinOpen {
		spec.StdinOpen = nil
	}
	if spec.ReadOnly != nil && !*spec.ReadOnly {
		spec.ReadOnly = nil
	}
	if spec.TTY != nil && !*spec.TTY {
		spec.TTY = nil
	}
	if spec.OOMScoreAdj != nil && *spec.OOMScoreAdj == 0 {
		spec.OOMScoreAdj = nil
	}
	if spec.OOMKillDisable != nil && !*spec.OOMKillDisable {
		spec.OOMKillDisable = nil
	}

	return spec
}

func canonicalDockerOrderedCollections(spec *containerconfig.Spec) {
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
