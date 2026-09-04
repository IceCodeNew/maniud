//nolint:goconst // Serialized field values stay explicit in projection fixtures.
package application

import (
	json "encoding/json/v2"
	"errors"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testAssistCPUs              = "cpus"
	testAssistMemory            = "mem_limit"
	testAssistPIDs              = "pids_limit"
	testAssistRestart           = "restart"
	testAssistRestartUnlessStop = "unless-stopped"
	testAssistStopGrace         = "stop_grace_period"
	testAssistThirtySeconds     = "30s"
	testAssistInit              = "init"
	testAssistReadOnly          = "read_only"
	testAssistNoNewPrivileges   = "no_new_privileges"
	testAssistHealthRetries     = "healthcheck.retries"
	testAssistNotNumber         = "not-a-number"
	testAssistTrue              = "true"
)

//nolint:cyclop // The assertions keep the projection identity and forbidden-value checks in one fixture.
func TestAssistProjectionUsesOnlyBoundedEditableFields(t *testing.T) {
	t.Parallel()
	request := newTestRequest(t)
	request.Source.Content = []byte(`name: example
services:
  api:
    container_name: example-api
    image: registry.private.example/team/api:secret-tag
    network_mode: bridge
    command: ["/private/command", "serve"]
    environment:
      - PRIVATE_TOKEN=credential-value
    cpus: "1.5"
    mem_limit: 536870912
    restart: unless-stopped
`)
	snapshot := validEvidenceTestSnapshot()
	snapshot.Plan.Platform = snapshot.Runtime.Platform
	snapshot.Plan.Source = assistSourceDigest(request.Source)
	projection, err := AssistProjectionFor(t.Context(), request, snapshot)
	if err != nil {
		t.Fatalf("AssistProjectionFor() error = %v", err)
	}
	if projection.Project != testProjectName || projection.Service != testServiceName ||
		projection.PlatformOS != testOperatingSystem || projection.PlatformArch != testArchitectureAMD64 ||
		len(projection.Fields) != len(DeploymentFields()) || len(projection.Identity) != 64 {
		t.Fatalf("projection = %#v", projection)
	}
	encoded, err := json.Marshal(projection, json.Deterministic(true))
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{
		"registry.private.example", "secret-tag", "credential-value", "/private/command",
	} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("projection contains %q: %s", forbidden, encoded)
		}
	}
	again, err := AssistProjectionFor(t.Context(), request, snapshot)
	if err != nil || projection.Identity != again.Identity {
		t.Fatalf("deterministic projection = %#v, %v", again, err)
	}
}

func TestAssistProjectionRejectsMismatchedSnapshot(t *testing.T) {
	t.Parallel()
	request := newTestRequest(t)
	snapshot := validEvidenceTestSnapshot()
	snapshot.Plan.Service = "different"
	if _, err := AssistProjectionFor(t.Context(), request, snapshot); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("AssistProjectionFor() error = %v", err)
	}
}

func TestAssistProjectionRejectsStaleSourceWithStableNames(t *testing.T) {
	t.Parallel()
	request := newTestRequest(t)
	snapshot := validEvidenceTestSnapshot()
	snapshot.Plan.Source = assistSourceDigest(request.Source)
	request.Source.Content = append(request.Source.Content, []byte("# changed source\n")...)

	if _, err := AssistProjectionFor(t.Context(), request, snapshot); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("stale source error = %v", err)
	}
}

func TestAssistSourceDigestUsesRepositorySnapshot(t *testing.T) {
	t.Parallel()
	want := domain.Hash([]byte("committed repository source"))
	source := compose.Source{Repository: &compose.RepositorySnapshot{Digest: want}}

	if got := assistSourceDigest(source); got != want {
		t.Fatalf("assistSourceDigest() = %s, want %s", got, want)
	}
}

func TestParseDeploymentPatchSharesManualAndLLMGrammar(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		field string
		value string
		unset bool
	}{
		{field: testAssistCPUs, value: "2.5"},
		{field: testAssistMemory, value: "536870912"},
		{field: "shm_size", value: "67108864"},
		{field: testAssistPIDs, value: "-1"},
		{field: testAssistRestart, value: testAssistRestartUnlessStop},
		{field: testAssistStopGrace, value: testAssistThirtySeconds},
		{field: "healthcheck.interval", value: testAssistThirtySeconds},
		{field: "healthcheck.timeout", value: "5s"},
		{field: "healthcheck.start_period", value: "10s"},
		{field: "healthcheck.start_interval", value: "2s"},
		{field: testAssistInit, value: testAssistTrue},
		{field: testAssistReadOnly, value: "false"},
		{field: testAssistNoNewPrivileges, value: testAssistTrue},
		{field: testAssistReadOnly, unset: true},
		{field: testAssistHealthRetries, value: "3"},
	} {
		patch, err := ParseDeploymentPatch(test.field, test.value, test.unset)
		if err != nil || patch.Field().ID() != test.field || patch.unset != test.unset {
			t.Fatalf("ParseDeploymentPatch(%q, %q, %t) = %#v, %v", test.field, test.value, test.unset, patch, err)
		}
	}
	for _, invalid := range []struct {
		field string
		value string
		unset bool
	}{
		{field: "unknown", value: "1"},
		{field: testAssistCPUs, value: ""},
		{field: testAssistCPUs, value: "invalid"},
		{field: testAssistNoNewPrivileges, unset: true},
		{field: testAssistNoNewPrivileges, value: "false"},
		{field: testAssistRestart, value: strings.Repeat("x", 100)},
	} {
		if _, err := ParseDeploymentPatch(invalid.field, invalid.value, invalid.unset); err == nil {
			t.Fatalf("ParseDeploymentPatch(%q, %q, %t) succeeded", invalid.field, invalid.value, invalid.unset)
		}
	}
}

//nolint:cyclop // Each assertion covers one closed deployment projection state.
func TestAssistProjectionCoversEveryFieldState(t *testing.T) {
	t.Parallel()
	pids := int64(-1)
	initValue := true
	readOnly := false
	stopTimeout := int64(30)
	retries := 3
	spec := containerconfig.Spec{
		CPUs: "2.5", MemoryBytes: 1024, PidsLimit: &pids, Restart: testAssistRestartUnlessStop,
		SharedMemoryBytes: 2048, StopTimeout: &stopTimeout, Init: &initValue, ReadOnly: &readOnly,
		NoNewPrivileges: true,
		Healthcheck: &containerconfig.Healthcheck{
			Interval: testAssistThirtySeconds, Timeout: "5s", Retries: &retries,
			StartPeriod: "10s", StartInterval: "2s",
		},
	}
	fields := DeploymentFieldStates(spec)
	if len(fields) != len(DeploymentFields()) {
		t.Fatalf("fields = %#v", fields)
	}
	for _, field := range fields {
		if field.ID == "" || !field.Present {
			t.Fatalf("field = %#v", field)
		}
	}
	for _, field := range DeploymentFieldStates(containerconfig.Spec{}) {
		if field.ID == "" || field.Present || field.Value != "" {
			t.Fatalf("empty field = %#v", field)
		}
	}
	disabledFields := DeploymentFieldStates(containerconfig.Spec{
		Healthcheck: &containerconfig.Healthcheck{Disabled: true},
	})
	for _, field := range disabledFields {
		if strings.HasPrefix(field.ID, "healthcheck.") && field.Available {
			t.Fatalf("disabled healthcheck field = %#v", field)
		}
	}
	if field := deploymentFieldState(containerconfig.Spec{}, DeploymentField(255)); field.Available {
		t.Fatalf("unknown field = %#v", field)
	}
	if value, present := deploymentOptionalInt(nil); value != "" || present {
		t.Fatalf("nil optional int = %q, %t", value, present)
	}
	if value, present, available := deploymentHealth(nil, nil); value != "" || present || available {
		t.Fatalf("nil healthcheck = %q, %t, %t", value, present, available)
	}
	if _, _, available := deploymentHealth(&containerconfig.Healthcheck{Disabled: true}, nil); available {
		t.Fatal("disabled healthcheck is available")
	}
}

func TestAssistProjectionRejectsInvalidSourceAndStaleProject(t *testing.T) {
	t.Parallel()
	request := newTestRequest(t)
	snapshot := validEvidenceTestSnapshot()
	snapshot.Plan.Platform = snapshot.Runtime.Platform
	request.Source.Content = []byte("not: [valid")
	if _, err := AssistProjectionFor(t.Context(), request, snapshot); err == nil {
		t.Fatal("invalid source succeeded")
	}
	request = newTestRequest(t)
	snapshot.Plan.Source = assistSourceDigest(request.Source)
	snapshot.Plan.Project = "different"
	if _, err := AssistProjectionFor(t.Context(), request, snapshot); !errors.Is(err, ErrSnapshotStale) {
		t.Fatalf("stale project error = %v", err)
	}
	request.Service = testMissingValue
	snapshot.Plan.Service = testMissingValue
	snapshot.Plan.Project = testProjectName
	if _, err := AssistProjectionFor(t.Context(), request, snapshot); err == nil {
		t.Fatal("missing service succeeded")
	}
}

func TestDeploymentPatchParseFailures(t *testing.T) {
	t.Parallel()
	for _, test := range []struct {
		field string
		value string
	}{
		{field: testAssistCPUs, value: testAssistNotNumber},
		{field: testAssistMemory, value: testAssistNotNumber},
		{field: testAssistPIDs, value: testAssistNotNumber},
		{field: testAssistHealthRetries, value: testAssistNotNumber},
		{field: testAssistStopGrace, value: "not-a-duration"},
		{field: testAssistInit, value: "not-a-boolean"},
	} {
		if _, err := ParseDeploymentPatch(test.field, test.value, false); err == nil {
			t.Fatalf("ParseDeploymentPatch(%q, %q) succeeded", test.field, test.value)
		}
	}
}

func TestAssistProjectionRejectsOversizedEnvelope(t *testing.T) {
	t.Parallel()
	projection := AssistProjection{Project: strings.Repeat("p", maximumAssistProjection)}
	if _, err := finalizeAssistProjection(projection); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("oversized projection error = %v", err)
	}
}
