package docker

import (
	"strings"
	"testing"

	containertypes "github.com/moby/moby/api/types/container"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestDecodeContainerRejectsIncompleteIdentity(t *testing.T) {
	t.Parallel()

	base := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
	tests := []struct {
		name   string
		mutate func(*containertypes.InspectResponse)
	}{
		{name: "state", mutate: func(value *containertypes.InspectResponse) { value.State = nil }},
		{name: "config", mutate: func(value *containertypes.InspectResponse) { value.Config = nil }},
		{name: "host config", mutate: func(value *containertypes.InspectResponse) { value.HostConfig = nil }},
		{name: "descriptor absent", mutate: func(value *containertypes.InspectResponse) {
			value.ImageManifestDescriptor = nil
		}},
		{name: "ID", mutate: func(value *containertypes.InspectResponse) { value.ID = "short" }},
		{name: containerNameQueryKey, mutate: func(value *containertypes.InspectResponse) { value.Name = "example-api" }},
		{name: "name grammar", mutate: func(value *containertypes.InspectResponse) { value.Name = "/example_api" }},
		{name: "image reference", mutate: func(value *containertypes.InspectResponse) { value.Config.Image = "bad image" }},
		{name: "image digest", mutate: func(value *containertypes.InspectResponse) { value.Image = "sha256:bad" }},
		{
			name: "manifest media type",
			mutate: func(value *containertypes.InspectResponse) {
				value.ImageManifestDescriptor.MediaType = "application/octet-stream"
			},
		},
		{
			name: "manifest digest",
			mutate: func(value *containertypes.InspectResponse) {
				value.ImageManifestDescriptor.Digest = "sha256:bad"
			},
		},
		{
			name: "manifest size",
			mutate: func(value *containertypes.InspectResponse) {
				value.ImageManifestDescriptor.Size = 0
			},
		},
		{name: "state semantics", mutate: func(value *containertypes.InspectResponse) { value.State.Running = false }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			payload := base
			descriptor := *base.ImageManifestDescriptor
			payload.State = cloneContainerState(base.State)
			payload.Config = cloneContainerConfig(base.Config)
			payload.HostConfig = cloneContainerHostConfig(base.HostConfig)
			payload.ImageManifestDescriptor = &descriptor
			test.mutate(&payload)

			observed, valid := decodeContainer(testContainerName, payload)
			if valid || observed.ID != "" {
				t.Fatalf("decodeContainer(%s) = %#v, %t", test.name, observed, valid)
			}
		})
	}
}

func TestDecodeContainerRejectsInvalidNetworkMode(t *testing.T) {
	t.Parallel()

	for _, networkMode := range []string{"", "bridge\n"} {
		payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
		payload.HostConfig.NetworkMode = containertypes.NetworkMode(networkMode)

		observed, valid := decodeContainer(testContainerName, payload)
		if valid || observed.ID != "" {
			t.Fatalf("decodeContainer(network mode %q) = %#v, %t", networkMode, observed, valid)
		}
	}
}

func TestDecodeContainerRejectsInvalidRestartPolicy(t *testing.T) {
	t.Parallel()

	policies := []containertypes.RestartPolicy{
		{Name: testInvalidLiteral, MaximumRetryCount: 0},
		{Name: containertypes.RestartPolicyAlways, MaximumRetryCount: 1},
	}
	for _, policy := range policies {
		payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
		payload.HostConfig.RestartPolicy = policy

		observed, valid := decodeContainer(testContainerName, payload)
		if valid || observed.ID != "" {
			t.Fatalf("decodeContainer(restart policy %#v) = %#v, %t", policy, observed, valid)
		}
	}
}

func TestDecodeContainerNormalizesSupportedRestartPolicies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		policy containertypes.RestartPolicy
		want   containertypes.RestartPolicy
	}{
		{
			name:   "legacy empty",
			policy: containertypes.RestartPolicy{Name: "", MaximumRetryCount: 3},
			want: containertypes.RestartPolicy{
				Name:              containertypes.RestartPolicyDisabled,
				MaximumRetryCount: 0,
			},
		},
		{
			name:   "always",
			policy: containertypes.RestartPolicy{Name: containertypes.RestartPolicyAlways, MaximumRetryCount: 0},
			want:   containertypes.RestartPolicy{Name: containertypes.RestartPolicyAlways, MaximumRetryCount: 0},
		},
		{
			name:   "unless stopped",
			policy: containertypes.RestartPolicy{Name: containertypes.RestartPolicyUnlessStopped, MaximumRetryCount: 0},
			want: containertypes.RestartPolicy{
				Name:              containertypes.RestartPolicyUnlessStopped,
				MaximumRetryCount: 0,
			},
		},
		{
			name:   "on failure",
			policy: containertypes.RestartPolicy{Name: containertypes.RestartPolicyOnFailure, MaximumRetryCount: 3},
			want:   containertypes.RestartPolicy{Name: containertypes.RestartPolicyOnFailure, MaximumRetryCount: 3},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
			payload.HostConfig.RestartPolicy = test.policy

			observed, valid := decodeContainer(testContainerName, payload)
			want, wantValid := dockerObservedRestart(test.want)
			if !valid || !wantValid || observed.WorkloadSpec.Restart != want {
				t.Fatalf("decodeContainer(%s) = %#v, %t", test.name, observed.WorkloadSpec.Restart, valid)
			}
		})
	}
}

func TestDecodeContainerRejectsConflictingReference(t *testing.T) {
	t.Parallel()

	payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))

	observed, valid := decodeContainer("other-name", payload)
	if valid || observed.ID != "" {
		t.Fatalf("decodeContainer(conflicting request) = %#v, %t", observed, valid)
	}
}

func TestDecodeContainerAcceptsDockerManifest(t *testing.T) {
	t.Parallel()

	payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
	payload.ImageManifestDescriptor.MediaType = dockerManifestMediaType

	container, valid := decodeContainer(testContainerName, payload)
	if !valid || container.ID != testContainerID {
		t.Fatalf("decodeContainer(Docker manifest) = %#v, %t", container, valid)
	}
}

func TestDecodeContainerPreservesNilAndEmptyProcessValues(t *testing.T) {
	t.Parallel()

	payload := inspectPayload(t, validContainerDocument(t, managedContainerLabels(), runningContainerState()))
	payload.Config.Entrypoint = nil
	payload.Config.Cmd = []string{}

	container, valid := decodeContainer(testContainerName, payload)
	if !valid || container.WorkloadSpec.Entrypoint != nil ||
		container.WorkloadSpec.Command == nil || len(container.WorkloadSpec.Command) != 0 {
		t.Fatalf("decodeContainer(process values) = %#v, %t", container, valid)
	}
}

func TestDecodeContainerStateSemantics(t *testing.T) {
	t.Parallel()

	tests := []struct {
		status     ContainerState
		running    bool
		paused     bool
		restarting bool
		dead       bool
		valid      bool
	}{
		{status: ContainerCreated, running: false, paused: false, restarting: false, dead: false, valid: true},
		{status: ContainerRunning, running: true, paused: false, restarting: false, dead: false, valid: true},
		{status: ContainerPaused, running: true, paused: true, restarting: false, dead: false, valid: true},
		{status: ContainerRestarting, running: true, paused: false, restarting: true, dead: false, valid: true},
		{status: ContainerRemoving, running: false, paused: false, restarting: false, dead: false, valid: true},
		{status: ContainerRemoving, running: false, paused: false, restarting: false, dead: true, valid: true},
		{status: ContainerExited, running: false, paused: false, restarting: false, dead: false, valid: true},
		{status: ContainerDead, running: false, paused: false, restarting: false, dead: true, valid: true},
		{status: ContainerRunning, running: false, paused: false, restarting: false, dead: false, valid: false},
		{status: ContainerPaused, running: true, paused: false, restarting: false, dead: false, valid: false},
		{status: ContainerRestarting, running: true, paused: true, restarting: true, dead: false, valid: false},
		{status: ContainerRunning, running: true, paused: false, restarting: false, dead: true, valid: false},
		{status: ContainerCreated, running: false, paused: true, restarting: false, dead: false, valid: false},
		{status: ContainerCreated, running: false, paused: false, restarting: false, dead: true, valid: false},
		{status: ContainerRemoving, running: true, paused: false, restarting: false, dead: false, valid: false},
		{status: ContainerExited, running: false, paused: false, restarting: false, dead: true, valid: false},
		{status: ContainerDead, running: true, paused: false, restarting: false, dead: true, valid: false},
		{status: testUnknownValue, running: false, paused: false, restarting: false, dead: false, valid: false},
	}

	for _, test := range tests {
		state := dockerContainerState(test.status, test.running, test.paused, test.restarting, test.dead)

		got, valid := decodeContainerState(state)
		if valid != test.valid || valid && got != test.status ||
			!valid && got != "" && test.status == testUnknownValue {
			t.Fatalf("decodeContainerState(%q) = %q, %t; want %t", test.status, got, valid, test.valid)
		}
	}
}

func TestDecodeOwnershipClassifiesManagedUnmanagedAndConflicting(t *testing.T) {
	t.Parallel()

	imageConfig := mustTestDigest(t, testImageConfig)
	manifest := mustTestDigest(t, testPlatformManifest)

	assertOwnershipBaseline(t, imageConfig, manifest)

	tests := []struct {
		name   string
		mutate func(map[string]string)
	}{
		{name: "missing service", mutate: func(labels map[string]string) { delete(labels, domain.LabelService) }},
		{name: "missing manifest", mutate: func(labels map[string]string) {
			delete(labels, domain.LabelPlatformManifestDigest)
		}},
		{name: "other reserved", mutate: func(labels map[string]string) { labels[maniudLabelPrefix+"other"] = "x" }},
		{name: "service empty", mutate: func(labels map[string]string) { labels[domain.LabelService] = "" }},
		{name: "service prefix", mutate: func(labels map[string]string) { labels[domain.LabelService] = "-api" }},
		{name: "service character", mutate: func(labels map[string]string) { labels[domain.LabelService] = "api service" }},
		{name: "service length", mutate: func(labels map[string]string) {
			labels[domain.LabelService] = strings.Repeat("a", maximumLabelValueBytes+1)
		}},
		{name: "transaction empty", mutate: func(labels map[string]string) { labels[domain.LabelTransaction] = "" }},
		{name: "transaction prefix", mutate: func(labels map[string]string) { labels[domain.LabelTransaction] = "-tx" }},
		{
			name:   "transaction character",
			mutate: func(labels map[string]string) { labels[domain.LabelTransaction] = "tx_value" },
		},
		{name: "transaction length", mutate: func(labels map[string]string) {
			labels[domain.LabelTransaction] = strings.Repeat("a", maximumLabelValueBytes+1)
		}},
		{name: "desired", mutate: func(labels map[string]string) {
			labels[domain.LabelDesiredStateDigest] = testInvalidValue
		}},
		{name: "reference", mutate: func(labels map[string]string) {
			labels[domain.LabelReferenceDigest] = testInvalidValue
		}},
		{name: "image", mutate: func(labels map[string]string) {
			labels[domain.LabelImageConfigDigest] = testInvalidValue
		}},
		{name: "image mismatch", mutate: func(labels map[string]string) {
			labels[domain.LabelImageConfigDigest] = testDesiredState
		}},
		{name: "manifest malformed", mutate: func(labels map[string]string) {
			labels[domain.LabelPlatformManifestDigest] = testInvalidValue
		}},
		{name: "manifest mismatch", mutate: func(labels map[string]string) {
			labels[domain.LabelPlatformManifestDigest] = testDesiredState
		}},
	}

	for _, test := range tests {
		labels := managedContainerLabels()
		test.mutate(labels)

		got := decodeOwnership(labels, imageConfig, manifest)
		if got.Status != domain.OwnershipConflicting {
			t.Fatalf("decodeOwnership(%s) = %#v", test.name, got)
		}
	}
}

func TestDecodeContainerOwnershipRejectsUnboundContainerdImageTargets(t *testing.T) {
	t.Parallel()

	reference := mustTestDigest(t, testReferenceDigest)
	manifest := mustTestDigest(t, testPlatformManifest)
	other := mustTestDigest(t, testDesiredState)
	tests := []struct {
		name           string
		imageReference string
		imageTarget    domain.Digest
		mutate         func(map[string]string)
	}{
		{name: "invalid reference", imageReference: testInvalidValue, imageTarget: reference},
		{
			name: "invalid config", imageReference: testContainerImage, imageTarget: reference,
			mutate: func(labels map[string]string) { labels[domain.LabelImageConfigDigest] = testInvalidValue },
		},
		{name: "target mismatch", imageReference: testContainerImage, imageTarget: other},
		{
			name: "conflicting labels", imageReference: testContainerImage, imageTarget: reference,
			mutate: func(labels map[string]string) { delete(labels, domain.LabelService) },
		},
		{
			name: "reference mismatch", imageReference: testContainerImage, imageTarget: reference,
			mutate: func(labels map[string]string) { labels[domain.LabelReferenceDigest] = testDesiredState },
		},
	}

	for _, test := range tests {
		labels := managedContainerLabels()
		if test.mutate != nil {
			test.mutate(labels)
		}

		ownership, imageConfig := decodeContainerOwnership(
			labels, test.imageReference, test.imageTarget, manifest,
		)
		if ownership.Status != domain.OwnershipConflicting || imageConfig != test.imageTarget {
			t.Errorf("decodeContainerOwnership(%s) = %#v, %s", test.name, ownership, imageConfig)
		}
	}
}

func TestContainerProbeMatchesRejectsIdentityDrift(t *testing.T) {
	t.Parallel()

	probe := observedContainerProbe(t)
	base := matchingContainerExpectation(t)
	tests := []struct {
		name   string
		mutate func(*ContainerExpectation)
	}{
		{name: "ID", mutate: func(value *ContainerExpectation) { value.ID = strings.Repeat("f", containerIDHexBytes) }},
		{name: containerNameQueryKey, mutate: func(value *ContainerExpectation) { value.Name = testOtherValue }},
		{name: "image reference", mutate: func(value *ContainerExpectation) { value.ImageReference = testOtherValue }},
		{name: "image", mutate: func(value *ContainerExpectation) { value.ImageConfig = domain.Hash(nil) }},
		{name: "manifest", mutate: func(value *ContainerExpectation) { value.PlatformManifest = domain.Hash(nil) }},
		{name: "entrypoint", mutate: func(value *ContainerExpectation) {
			value.WorkloadSpec.Entrypoint = []string{testOtherValue}
		}},
		{name: "command", mutate: func(value *ContainerExpectation) {
			value.WorkloadSpec.Command = []string{testOtherValue}
		}},
		{name: "network mode", mutate: func(value *ContainerExpectation) { value.WorkloadSpec.NetworkMode = "host" }},
		{name: "restart policy", mutate: func(value *ContainerExpectation) {
			value.WorkloadSpec.Restart = string(containertypes.RestartPolicyAlways)
		}},
		{name: "state", mutate: func(value *ContainerExpectation) {
			value.AllowedStates = []ContainerState{ContainerExited}
		}},
		{name: "service", mutate: func(value *ContainerExpectation) { value.Service = "worker" }},
		{name: "transaction", mutate: func(value *ContainerExpectation) { value.Transaction = "tx-other" }},
		{name: "desired", mutate: func(value *ContainerExpectation) { value.DesiredState = domain.Hash(nil) }},
		{name: "reference", mutate: func(value *ContainerExpectation) { value.Reference = domain.Hash(nil) }},
	}

	for _, test := range tests {
		expectation := base
		test.mutate(&expectation)

		if probe.Matches(expectation) {
			t.Fatalf("ContainerProbe.Matches(%s) = true", test.name)
		}
	}

	probe.State = ContainerProbeUnknown
	if probe.Matches(base) {
		t.Fatal("ContainerProbe.Matches(unknown) = true")
	}

	assertNilEmptyProcessDriftRejected(t)
}

func TestContainerProbeMatchesDockerGeneratedHostname(t *testing.T) {
	t.Parallel()

	probe := observedContainerProbe(t)
	expectation := matchingContainerExpectation(t)
	probe.Container.WorkloadSpec.Hostname = probe.Container.ID[:12]
	if !probe.Matches(expectation) {
		t.Fatal("ContainerProbe.Matches(Docker-generated hostname) = false")
	}

	probe.Container.WorkloadSpec.Hostname = testOtherValue
	if probe.Matches(expectation) {
		t.Fatal("ContainerProbe.Matches(unexpected hostname) = true")
	}

	expectation.WorkloadSpec.Hostname = testOtherValue
	if !probe.Matches(expectation) {
		t.Fatal("ContainerProbe.Matches(explicit hostname) = false")
	}
}

func assertNilEmptyProcessDriftRejected(t *testing.T) {
	t.Helper()

	probe := observedContainerProbe(t)
	probe.Container.WorkloadSpec.Entrypoint = []string{}
	base := matchingContainerExpectation(t)

	base.WorkloadSpec.Entrypoint = nil
	if probe.Matches(base) {
		t.Fatal("ContainerProbe.Matches(nil versus empty entrypoint) = true")
	}

	probe = observedContainerProbe(t)
	probe.Container.WorkloadSpec.Command = []string{}
	base = matchingContainerExpectation(t)

	base.WorkloadSpec.Command = nil
	if probe.Matches(base) {
		t.Fatal("ContainerProbe.Matches(nil versus empty command) = true")
	}
}
