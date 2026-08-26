//nolint:goconst,lll // White-box boundary matrices keep malformed wire values beside each field.
package podman

import (
	"bytes"
	"encoding/json"
	"io"
	"math"
	"reflect"
	"slices"
	"strings"
	"testing"
	"time"
)

func inspectBytes(t *testing.T, document inspectDocument) []byte {
	t.Helper()
	encoded, err := json.Marshal(document)
	if err != nil {
		t.Fatal(err)
	}

	return encoded
}

func decodeDocument(t *testing.T, document inspectDocument) (Inspection, error) {
	t.Helper()
	encoded := inspectBytes(t, document)

	return DecodeInspect(bytes.NewReader(encoded), int64(len(encoded)), testPodmanAPIVersion)
}

func TestDecodeRejectsFramingAndRequiredFieldFailures(t *testing.T) {
	t.Parallel()

	base := richInspectDocument()
	tests := []struct {
		name  string
		input io.Reader
		limit int64
	}{
		{"nil", nil, 1}, {"zero limit", strings.NewReader("{}"), 0},
		{"malformed", strings.NewReader("{"), 1}, {"trailing", strings.NewReader("{}{}"), 4},
		{"duplicate", strings.NewReader(`{"Id":1,"Id":2}`), 15},
		{"oversized", strings.NewReader("{}"), 1}, {"wrong shape", strings.NewReader("[]"), 2},
	}
	for _, test := range tests {
		if _, err := DecodeInspect(test.input, test.limit, testPodmanAPIVersion); err == nil {
			t.Fatalf("DecodeInspect(%s) error = nil", test.name)
		}
	}
	encoded := inspectBytes(t, base)
	var fields map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &fields); err != nil {
		t.Fatal(err)
	}
	for _, name := range []string{"Id", "Name", "Image", "ImageName", "ImageDigest", "State", "Mounts", "Config", "HostConfig"} {
		clone := make(map[string]json.RawMessage, len(fields)-1)
		for key, value := range fields {
			if key != name {
				clone[key] = value
			}
		}
		missing, err := json.Marshal(clone)
		if err != nil {
			t.Fatal(err)
		}
		if _, err = DecodeInspect(
			bytes.NewReader(missing),
			int64(len(missing)),
			testPodmanAPIVersion,
		); err == nil {
			t.Fatalf("DecodeInspect(missing %s) error = nil", name)
		}
	}
}

func TestDecodeRejectsInvalidCoreAndLifecycle(t *testing.T) {
	t.Parallel()

	if !validID(strings.Repeat("1", 64)) {
		t.Fatal("validID(digits) = false")
	}
	mutations := []func(*inspectDocument){
		func(value *inspectDocument) { value.ID = "short" },
		func(value *inspectDocument) { value.ID = strings.Repeat("g", 64) },
		func(value *inspectDocument) { value.Name = "-bad" },
		func(value *inspectDocument) { value.Image = "" },
		func(value *inspectDocument) { value.ImageName = "other" },
		func(value *inspectDocument) { value.ImageDigest = "bad\x00" },
		func(value *inspectDocument) { value.State = nil },
		func(value *inspectDocument) { value.Config = nil },
		func(value *inspectDocument) { value.HostConfig = nil },
		func(value *inspectDocument) { value.State.Status = "bad" },
		func(value *inspectDocument) { value.State.Restarting = true },
		func(value *inspectDocument) { value.State.Dead = true },
	}
	for index, mutate := range mutations {
		value := richInspectDocument()
		mutate(&value)
		if _, err := decodeDocument(t, value); err == nil {
			t.Fatalf("DecodeInspect(core %d) error = nil", index)
		}
	}
	states := []struct {
		state inspectState
		want  State
	}{
		{inspectState{Status: "created"}, StateCreated},
		{inspectState{Status: "initialized"}, StateCreated},
		{inspectState{Status: "running", Running: true}, StateRunning},
		{inspectState{Status: "paused", Paused: true}, StatePaused},
		{inspectState{Status: "stopped"}, StateExited},
		{inspectState{Status: "exited"}, StateExited},
		{inspectState{Status: "removing"}, StateRemoving},
		{inspectState{Status: "stopping"}, StateRemoving},
		{inspectState{Status: "unknown"}, StateUnknown},
	}
	for _, test := range states {
		if got, valid := observedState(&test.state); !valid || got != test.want {
			t.Fatalf("observedState(%#v) = %d, %t", test.state, got, valid)
		}
	}
	for _, state := range []*inspectState{
		nil, {Status: "running"}, {Status: "paused"}, {Status: "created", Running: true},
		{Status: "stopped", Paused: true}, {Status: "removing", Running: true},
	} {
		if _, valid := observedState(state); valid {
			t.Fatalf("observedState(%#v) = true", state)
		}
	}
}

func TestObservedSpecRejectsEachMappingBoundary(t *testing.T) {
	t.Parallel()

	mutations := []func(*inspectDocument){
		func(value *inspectDocument) { value.HostConfig.RestartPolicy = nil },
		func(value *inspectDocument) { value.HostConfig.NetworkMode = "host" },
		func(value *inspectDocument) { value.HostConfig.IPCMode = "shareable" },
		func(value *inspectDocument) { value.HostConfig.PIDMode = "host" },
		func(value *inspectDocument) { value.HostConfig.UTSMode = "host" },
		func(value *inspectDocument) { value.HostConfig.Cgroups = cgroupsDisabled },
		func(value *inspectDocument) { value.Config.Labels = map[string]string{"": "bad"} },
		func(value *inspectDocument) { value.Config.Environment = []string{"bad"} },
		func(value *inspectDocument) { value.HostConfig.RestartPolicy.Name = "bad" },
		func(value *inspectDocument) { value.Config.StopSignal = json.RawMessage(`"bad"`) },
		func(value *inspectDocument) { value.HostConfig.NanoCPUs = -1 },
		func(value *inspectDocument) { value.HostConfig.BlkioWeight = 1 },
		func(value *inspectDocument) { value.HostConfig.PidsLimit = -2 },
		func(value *inspectDocument) { value.HostConfig.DNS = []string{"01.1.1.1"} },
		func(value *inspectDocument) { value.HostConfig.ExtraHosts = []string{"bad"} },
		func(value *inspectDocument) { value.HostConfig.Tmpfs = map[string]string{"": "rw"} },
		func(value *inspectDocument) { value.HostConfig.Ulimits = []inspectUlimit{{Name: "NOFILE"}} },
		func(value *inspectDocument) { value.Config.ExposedPorts = map[string]any{"bad": nil} },
		func(value *inspectDocument) { value.Mounts[0].Mode = "bad" },
		func(value *inspectDocument) { value.Config.Healthcheck = &healthConfig{Test: []string{"bad\x00"}} },
		func(value *inspectDocument) { value.HostConfig.SecurityOpt = []string{"seccomp=unconfined"} },
		func(value *inspectDocument) { value.Config.StopTimeout = ^uint(0) },
	}
	for index, mutate := range mutations {
		value := richInspectDocument()
		mutate(&value)
		if _, err := decodeDocument(t, value); err == nil {
			t.Fatalf("DecodeInspect(mapping %d) error = nil", index)
		}
	}
}

func TestDecodeAcceptsPodman431ShareableIPCOnly(t *testing.T) {
	t.Parallel()

	document := richInspectDocument()
	document.HostConfig.IPCMode = "shareable"
	document.Config.Entrypoint = json.RawMessage(`"/usr/local/bin/app"`)
	document.Config.StopSignal = json.RawMessage(`2`)
	encoded := inspectBytes(t, document)
	if _, err := DecodeInspect(bytes.NewReader(encoded), int64(len(encoded)), podmanAPI431); err != nil {
		t.Fatalf("DecodeInspect(Podman 4.3.1 shareable IPC) error = %v", err)
	}
	document.Config.Entrypoint = json.RawMessage(`["/usr/local/bin/app"]`)
	document.Config.StopSignal = json.RawMessage(`"SIGINT"`)
	encoded = inspectBytes(t, document)
	for _, apiVersion := range []string{"5.0.0", testPodmanAPIVersion} {
		if _, err := DecodeInspect(bytes.NewReader(encoded), int64(len(encoded)), apiVersion); err == nil {
			t.Fatalf("DecodeInspect(Podman %s shareable IPC) error = nil", apiVersion)
		}
	}
	document.Config.Entrypoint = json.RawMessage(`"/usr/local/bin/app"`)
	document.Config.StopSignal = json.RawMessage(`2`)
	for _, mode := range []string{"host", "container:other", "ns:/proc/1/ns/ipc", "none"} {
		document.HostConfig.IPCMode = mode
		encoded = inspectBytes(t, document)
		if _, err := DecodeInspect(bytes.NewReader(encoded), int64(len(encoded)), podmanAPI431); err == nil {
			t.Fatalf("DecodeInspect(Podman 4.3.1 IPC mode %q) error = nil", mode)
		}
	}
}

func TestDecodeUsesNegotiatedInspectScalarShapes(t *testing.T) {
	t.Parallel()

	modern := richInspectDocument()
	legacy := richInspectDocument()
	legacy.Config.Entrypoint = json.RawMessage(`"/usr/local/bin/app"`)
	legacy.Config.StopSignal = json.RawMessage(`2`)
	for _, test := range []struct {
		name       string
		document   inspectDocument
		apiVersion string
	}{
		{"Podman 4.3.1", legacy, podmanAPI431},
		{"Podman 5", modern, "5.0.0"},
		{"Podman 6.1", modern, testPodmanAPIVersion},
	} {
		inspection, err := DecodeInspect(
			bytes.NewReader(inspectBytes(t, test.document)),
			int64(len(inspectBytes(t, test.document))),
			test.apiVersion,
		)
		if err != nil {
			t.Fatalf("DecodeInspect(%s) error = %v", test.name, err)
		}
		if !slices.Equal(inspection.Spec.Entrypoint, []string{"/usr/local/bin/app"}) ||
			inspection.Spec.StopSignal != signalInterruptName {
			t.Fatalf("DecodeInspect(%s) process = %#v, %q", test.name, inspection.Spec.Entrypoint, inspection.Spec.StopSignal)
		}
	}

	invalid := []struct {
		name       string
		entrypoint json.RawMessage
		stopSignal json.RawMessage
		apiVersion string
	}{
		{"legacy flattened argv", json.RawMessage(`"/bin/sh -c"`), json.RawMessage(`15`), podmanAPI431},
		{"legacy modern shape", json.RawMessage(`["/bin/sh"]`), json.RawMessage(`"SIGTERM"`), podmanAPI431},
		{"legacy null entrypoint", json.RawMessage(`null`), json.RawMessage(`15`), podmanAPI431},
		{"legacy null signal", json.RawMessage(`"/bin/sh"`), json.RawMessage(`null`), podmanAPI431},
		{"legacy fractional signal", json.RawMessage(`"/bin/sh"`), json.RawMessage(`15.5`), podmanAPI431},
		{"modern legacy shape", json.RawMessage(`"/bin/sh"`), json.RawMessage(`15`), "5.0.0"},
		{"modern mixed entrypoint", json.RawMessage(`["/bin/sh",1]`), json.RawMessage(`"SIGTERM"`), "5.0.0"},
		{"unsupported API", json.RawMessage(`["/bin/sh"]`), json.RawMessage(`"SIGTERM"`), "6.1.1"},
	}
	for _, test := range invalid {
		document := richInspectDocument()
		document.Config.Entrypoint = test.entrypoint
		document.Config.StopSignal = test.stopSignal
		encoded := inspectBytes(t, document)
		if _, err := DecodeInspect(bytes.NewReader(encoded), int64(len(encoded)), test.apiVersion); err == nil {
			t.Fatalf("DecodeInspect(%s) error = nil", test.name)
		}
	}
}

func TestInspectVersionAndProcessScalarBoundaries(t *testing.T) {
	t.Parallel()

	for _, version := range []string{"4.3", "4.x.1"} {
		if _, _, valid := inspectAPIMode(version); valid {
			t.Fatalf("inspectAPIMode(%q) = valid", version)
		}
	}

	if entrypoint, valid := observedEntrypoint(json.RawMessage(`""`), true); !valid || entrypoint != nil {
		t.Fatalf("observedEntrypoint(legacy empty) = %#v, %t", entrypoint, valid)
	}
	if _, valid := observedEntrypoint(nil, false); valid {
		t.Fatal("observedEntrypoint(modern missing) = valid")
	}
	if entrypoint, valid := observedEntrypoint(json.RawMessage(`null`), false); !valid || entrypoint != nil {
		t.Fatalf("observedEntrypoint(modern null) = %#v, %t", entrypoint, valid)
	}
	if _, valid := observedInspectStopSignal(nil, false); valid {
		t.Fatal("observedInspectStopSignal(missing) = valid")
	}
	if _, valid := observedInspectStopSignal(json.RawMessage(`""`), false); valid {
		t.Fatal("observedInspectStopSignal(empty) = valid")
	}
}

//nolint:cyclop // The assertion exhausts independent native scalar mappings.
func TestObservedScalarBranches(t *testing.T) {
	t.Parallel()

	if normalizedHostname(testContainerID, testContainerID) != "" ||
		normalizedHostname(testContainerID, testContainerID[:12]) != "" ||
		normalizedHostname("short", "custom") != "custom" {
		t.Fatal("normalizedHostname() returned an invalid value")
	}
	if labels, valid := observedLabels(nil); !valid || labels != nil {
		t.Fatalf("observedLabels(nil) = %#v, %t", labels, valid)
	}
	if _, valid := observedLabels(map[string]string{"bad\x00": "value"}); valid {
		t.Fatal("observedLabels(invalid) = true")
	}
	for _, values := range [][]string{
		{"bad"}, {"A=1", "A=2"}, {"A=bad\x00"}, {podmanEnvironmentKey + "=custom"},
	} {
		if _, valid := observedEnvironment(values); valid {
			t.Fatalf("observedEnvironment(%#v) = true", values)
		}
	}
	if values, valid := observedEnvironment([]string{"A=1", podmanEnvironmentKey + "=" + podmanEnvironmentValue}); !valid || !slices.Equal(values, []string{"A=1"}) {
		t.Fatalf("observedEnvironment(runtime default) = %#v, %t", values, valid)
	}
	if !validCgroupsMode(&inspectHost{Cgroups: cgroupsDisabled}) ||
		validCgroupsMode(&inspectHost{Cgroups: cgroupsDisabled, Memory: 1}) ||
		!validCgroupsMode(&inspectHost{Cgroups: cgroupsDefault}) {
		t.Fatal("validCgroupsMode() returned an invalid result")
	}
	for _, restart := range []inspectRestart{
		{}, {Name: "no"}, {Name: "always"}, {Name: "unless-stopped"},
		{Name: "on-failure"}, {Name: "on-failure", MaximumRetryCount: 2},
	} {
		if _, valid := observedRestart(restart); !valid {
			t.Fatalf("observedRestart(%#v) = false", restart)
		}
	}
	for _, restart := range []inspectRestart{
		{Name: "no", MaximumRetryCount: 1}, {Name: "always", MaximumRetryCount: 1},
		{Name: "bad"}, {Name: "on-failure", MaximumRetryCount: math.MaxUint32},
	} {
		if _, valid := observedRestart(restart); valid {
			t.Fatalf("observedRestart(%#v) = true", restart)
		}
	}
	if signal, valid := observedStopSignal("SIGTERM"); !valid || signal != "" {
		t.Fatalf("observedStopSignal(SIGTERM) = %q, %t", signal, valid)
	}
	if signal, valid := observedStopSignal("SIGINT"); !valid || signal != "SIGINT" {
		t.Fatalf("observedStopSignal(SIGINT) = %q, %t", signal, valid)
	}
	if _, valid := observedStopSignal("bad"); valid {
		t.Fatal("observedStopSignal(bad) = true")
	}
	if timeout, valid := observedStopTimeout(20); !valid || timeout == nil || *timeout != 20 {
		t.Fatalf("observedStopTimeout(20) = %#v, %t", timeout, valid)
	}
}

//nolint:cyclop // The assertion exhausts independent resource mappings.
func TestObservedResourceAndCollectionBranches(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		nano   int64
		period uint64
		quota  int64
		valid  bool
	}{
		{0, 0, 0, true}, {1_500_000_000, cpuPeriod, 150_000, true},
		{-1, cpuPeriod, 1, false}, {1, 1, 1, false}, {1, cpuPeriod, 0, false},
		{1, cpuPeriod, math.MaxInt64, false}, {1, cpuPeriod, 1, false},
	} {
		if _, valid := observedCPUs(test.nano, test.period, test.quota); valid != test.valid {
			t.Fatalf("observedCPUs(%#v) = %t", test, valid)
		}
	}
	if cpuString(1_000_000_000) != "1" || cpuString(1_500_000_000) != "1.5" {
		t.Fatal("cpuString() returned an invalid value")
	}
	if value, valid := observedBlkio(0); !valid || value != nil {
		t.Fatalf("observedBlkio(0) = %#v, %t", value, valid)
	}
	if _, valid := observedBlkio(1); valid {
		t.Fatal("observedBlkio(1) = true")
	}
	if value, valid := observedPids(0); !valid || value != nil {
		t.Fatalf("observedPids(0) = %#v, %t", value, valid)
	}
	if _, valid := observedPids(-2); valid {
		t.Fatal("observedPids(-2) = true")
	}
	if hosts, valid := observedExtraHosts([]string{"db:192.0.2.1"}); !valid || !slices.Equal(hosts, []string{"db=192.0.2.1"}) {
		t.Fatalf("observedExtraHosts() = %#v, %t", hosts, valid)
	}
	if tmpfs, valid := observedTmpfs(map[string]string{"/run": ""}); !valid || len(tmpfs) != 1 || tmpfs[0].Options != nil {
		t.Fatalf("observedTmpfs() = %#v, %t", tmpfs, valid)
	}
	if tmpfs, valid := observedTmpfs(nil); !valid || tmpfs != nil {
		t.Fatalf("observedTmpfs(nil) = %#v, %t", tmpfs, valid)
	}
	if _, valid := observedUlimits([]inspectUlimit{{Name: "RLIMIT_NOFILE", Soft: 3, Hard: 2}}); valid {
		t.Fatal("observedUlimits(invalid) = true")
	}
}

//nolint:cyclop // The table exhausts independent port and mount rejection paths.
func TestObservedPortAndMountBranches(t *testing.T) {
	t.Parallel()

	invalidPorts := []struct {
		exposed  map[string]any
		bindings map[string][]inspectPortBinding
	}{
		{exposed: map[string]any{"bad": nil}},
		{bindings: map[string][]inspectPortBinding{"80/tcp": {}}},
		{bindings: map[string][]inspectPortBinding{"80/sctp": {{HostPort: "80"}}}},
		{bindings: map[string][]inspectPortBinding{"80/tcp": {{HostPort: "0"}}}},
		{bindings: map[string][]inspectPortBinding{"80/tcp": {{HostIP: "bad", HostPort: "80"}}}},
	}
	for _, test := range invalidPorts {
		if _, _, valid := observedPorts(test.exposed, test.bindings); valid {
			t.Fatalf("observedPorts(%#v) = true", test)
		}
	}
	if port, protocol, valid := portKey("80/tcp"); !valid || port != 80 || protocol != protocolTCP {
		t.Fatalf("portKey() = %d, %q, %t", port, protocol, valid)
	}
	if _, ports, valid := observedPorts(nil, map[string][]inspectPortBinding{
		"80/tcp": {{HostPort: "8080"}},
	}); !valid || len(ports) != 1 || ports[0].HostIP != "" {
		t.Fatalf("observedPorts(any host) = %#v, %t", ports, valid)
	}
	invalidMounts := []struct {
		mounts []inspectMount
		binds  []string
	}{
		{binds: []string{"bad\x00"}}, {binds: []string{"same", "same"}},
		{mounts: []inspectMount{{Type: mountBind, Source: "/a", Destination: "/b", Mode: "bad"}}},
		{mounts: []inspectMount{{Type: mountBind, Name: "bad", Source: "/a", Destination: "/b", Options: []string{recursiveBind}, Propagation: propagationPrivate}}},
		{mounts: []inspectMount{{Type: mountBind, Source: "/a", Destination: "/b", Options: []string{recursiveBind}, Propagation: propagationPrivate, ReadWrite: true}}},
		{mounts: []inspectMount{{Type: mountVolume, Name: "volume", Source: "/a", Destination: "/b", Driver: volumeDriverLocal, ReadWrite: true, SubPath: "nested"}}},
		{mounts: []inspectMount{{Type: mountVolume, Name: "", Source: "/a", Destination: "/b", Driver: volumeDriverLocal, ReadWrite: true}}},
		{mounts: []inspectMount{{Type: mountVolume, Name: "volume", Source: "relative", Destination: "/b", Driver: volumeDriverLocal, ReadWrite: true}}},
		{mounts: []inspectMount{{Type: mountVolume, Name: "volume", Source: "/a/../b", Destination: "/b", Driver: volumeDriverLocal, ReadWrite: true}}},
		{mounts: []inspectMount{{Type: "bad", Source: "/a", Destination: "/b"}}},
		{binds: []string{"/a:/b:rbind,rw,rprivate"}},
		{mounts: []inspectMount{{Type: mountVolume, Name: "a", Source: "/a", Destination: "/same", Driver: volumeDriverLocal, ReadWrite: true}, {Type: mountVolume, Name: "b", Source: "/b", Destination: "/same", Driver: volumeDriverLocal, ReadWrite: true}}},
	}
	for _, test := range invalidMounts {
		if _, _, valid := observedMounts(test.mounts, test.binds); valid {
			t.Fatalf("observedMounts(%#v) = true", test)
		}
	}
	if mounts, runtime, valid := observedMounts(nil, nil); !valid || mounts == nil || runtime != nil {
		t.Fatalf("observedMounts(nil) = %#v, %#v, %t", mounts, runtime, valid)
	}
}

//nolint:cyclop // The assertion covers independent health, security, and optional values.
func TestObservedHealthSecurityAndPointerBranches(t *testing.T) {
	t.Parallel()

	if health, valid := observedHealthcheck(nil); !valid || health != nil {
		t.Fatalf("observedHealthcheck(nil) = %#v, %t", health, valid)
	}
	if health, valid := observedHealthcheck(&healthConfig{Test: []string{healthcheckNone}}); !valid || health == nil || !health.Disabled {
		t.Fatalf("observedHealthcheck(disabled) = %#v, %t", health, valid)
	}
	for _, health := range []*healthConfig{
		{Test: []string{healthcheckNone}, Interval: time.Second},
		{Test: []string{"bad\x00"}}, {Retries: -1},
	} {
		if _, valid := observedHealthcheck(health); valid {
			t.Fatalf("observedHealthcheck(%#v) = true", health)
		}
	}
	if health, valid := observedHealthcheck(&healthConfig{Test: []string{"CMD", "true"}, Retries: 2}); !valid || health == nil || health.Retries == nil || *health.Retries != 2 {
		t.Fatalf("observedHealthcheck(command) = %#v, %t", health, valid)
	}
	if health, valid := observedHealthcheck(&healthConfig{Test: []string{"CMD", "true"}}); !valid || health == nil || health.Retries != nil {
		t.Fatalf("observedHealthcheck(no retries) = %#v, %t", health, valid)
	}
	for _, values := range [][]string{nil, {"no-new-privileges"}, {"no-new-privileges=true"}, {"no-new-privileges:true"}} {
		if _, valid := observedSecurity(values); !valid {
			t.Fatalf("observedSecurity(%#v) = false", values)
		}
	}
	if _, valid := observedSecurity([]string{"bad"}); valid {
		t.Fatal("observedSecurity(bad) = true")
	}
	if durationString(0) != "" || durationString(time.Second) != "1s" || optionalInt(0) != nil ||
		optionalInt(1) == nil || truePointer(false) != nil || truePointer(true) == nil ||
		cloneStringMap(nil) != nil || !reflect.DeepEqual(cloneStringMap(map[string]string{"a": "b"}), map[string]string{"a": "b"}) {
		t.Fatal("observed optional helper returned an invalid result")
	}
}
