//nolint:goconst,lll // Contract matrices keep complete field IDs and calls adjacent.
package compose

import (
	"bytes"
	"context"
	"errors"
	"sync/atomic"
	"testing"
	"time"

	"go.yaml.in/yaml/v4"

	"github.com/IceCodeNew/maniud/containerconfig"
)

type cancelComposeAfterChecksContext struct {
	after int64
	calls atomic.Int64
}

func (*cancelComposeAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (*cancelComposeAfterChecksContext) Done() <-chan struct{} { return nil }

func (ctx *cancelComposeAfterChecksContext) Err() error {
	if ctx.calls.Add(1) >= ctx.after {
		return context.Canceled
	}

	return nil
}

func (*cancelComposeAfterChecksContext) Value(any) any { return nil }

func TestDeploymentSemanticProofRejectsUnselectedChanges(t *testing.T) {
	t.Parallel()

	before := []byte("services:\n  api:\n    cpus: 1\n  worker:\n    image: worker:stable\n")
	path := [][]string{{"services", "api", "cpus"}}
	approved := bytes.Replace(before, []byte("cpus: 1"), []byte("cpus: 2"), 1)
	if !sameUntouchedYAMLSemantics(before, approved, path) {
		t.Fatal("semantic proof rejected the approved path")
	}
	drifted := bytes.Replace(approved, []byte("worker:stable"), []byte("worker:latest"), 1)
	if sameUntouchedYAMLSemantics(before, drifted, path) {
		t.Fatal("semantic proof accepted an unselected path change")
	}
	if sameUntouchedYAMLSemantics([]byte("bad: ["), approved, path) ||
		sameUntouchedYAMLSemantics(before, []byte("bad: ["), path) {
		t.Fatal("semantic proof accepted malformed YAML")
	}
}

func TestPatchServiceFieldsRejectsInvalidContract(t *testing.T) {
	t.Parallel()

	source := testSource(t, "name: example\nservices:\n  api:\n    image: busybox:stable\n")
	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := source.PatchServiceFields(ctx, "api", containerconfig.Spec{}, []string{"cpus"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PatchServiceFields(canceled) error = %v", err)
	}
	for _, input := range []struct {
		service  string
		expected containerconfig.Spec
		fields   []string
	}{
		{service: "", expected: containerconfig.Spec{ServiceName: "api"}, fields: []string{"cpus"}},
		{service: "api", expected: containerconfig.Spec{ServiceName: "worker"}, fields: []string{"cpus"}},
		{service: "api", expected: containerconfig.Spec{ServiceName: "api"}},
		{service: "api", expected: containerconfig.Spec{ServiceName: "api"}, fields: []string{"unknown"}},
		{service: "api", expected: containerconfig.Spec{ServiceName: "api"}, fields: []string{"cpus", "cpus"}},
	} {
		if _, err := source.PatchServiceFields(t.Context(), input.service, input.expected, input.fields); !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("PatchServiceFields(invalid contract) error = %v", err)
		}
	}
}

//nolint:funlen // One fixture proves every closed field through the Compose-owned adapter.
func TestPatchServiceFieldsSetsAndUnsetsClosedFieldSet(t *testing.T) {
	t.Parallel()

	source := testSource(t, `name: example
services:
  api:
    container_name: example-api
    image: busybox:stable
    network_mode: bridge
    cpus: 1
    mem_limit: 512
    pids_limit: 100
    restart: always
    shm_size: 1024
    stop_grace_period: 10s
    init: true
    read_only: false
    healthcheck:
      test: ["CMD", "true"]
      interval: 30s
      timeout: 3s
      retries: 3
      start_period: 4s
      start_interval: 2s
`)
	project, err := Load(t.Context(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	expected, err := project.ServiceSpec("api")
	if err != nil {
		t.Fatalf("ServiceSpec() error = %v", err)
	}
	expected.CPUs = "2.5"
	expected.MemoryBytes = 2048
	expected.PidsLimit = new(int64(-1))
	expected.Restart = "on-failure:3"
	expected.SharedMemoryBytes = 4096
	expected.StopTimeout = new(int64(20))
	expected.Init = new(false)
	expected.ReadOnly = new(true)
	expected.NoNewPrivileges = true
	expected.Healthcheck.Interval = "5s"
	expected.Healthcheck.Timeout = "6s"
	expected.Healthcheck.Retries = new(4)
	expected.Healthcheck.StartPeriod = "8s"
	expected.Healthcheck.StartInterval = "9s"
	fields := []string{
		"cpus", "mem_limit", "pids_limit", "restart", "shm_size", "stop_grace_period",
		"init", "read_only", "no_new_privileges", "healthcheck.interval", "healthcheck.timeout",
		"healthcheck.retries", "healthcheck.start_period", "healthcheck.start_interval",
	}
	candidate, err := source.PatchServiceFields(t.Context(), "api", expected, fields)
	if err != nil {
		t.Fatalf("PatchServiceFields(set) error = %v", err)
	}

	expected.CPUs = ""
	expected.MemoryBytes = 0
	expected.PidsLimit = nil
	expected.Restart = ""
	expected.SharedMemoryBytes = 0
	expected.StopTimeout = nil
	expected.Init = nil
	expected.ReadOnly = nil
	expected.Healthcheck.Interval = ""
	expected.Healthcheck.Timeout = ""
	expected.Healthcheck.Retries = nil
	expected.Healthcheck.StartPeriod = ""
	expected.Healthcheck.StartInterval = ""
	withoutOneWay := append([]string(nil), fields...)
	withoutOneWay = append(withoutOneWay[:8], withoutOneWay[9:]...)
	if _, err = candidate.PatchServiceFields(t.Context(), "api", expected, withoutOneWay); err != nil {
		t.Fatalf("PatchServiceFields(unset) error = %v", err)
	}
}

//nolint:cyclop // Each assertion exercises a distinct fail-closed adapter stage.
func TestPatchServiceFieldsRejectsInvalidSourceAndSemanticMismatch(t *testing.T) {
	t.Parallel()

	const content = `name: example
services:
  api:
    container_name: example-api
    image: busybox:stable
    network_mode: bridge
    cpus: 1
`
	source := testSource(t, content)
	project, err := Load(t.Context(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	expected, err := project.ServiceSpec("api")
	if err != nil {
		t.Fatalf("ServiceSpec() error = %v", err)
	}

	var document yaml.Node
	if err = yaml.Load(source.Content, &document, yaml.WithUniqueKeys()); err != nil {
		t.Fatalf("yaml.Load() error = %v", err)
	}
	canonical, err := yaml.Marshal(&document)
	if err != nil {
		t.Fatalf("yaml.Marshal() error = %v", err)
	}
	canonicalSource := source
	canonicalSource.Content = canonical
	if _, err = canonicalSource.PatchServiceFields(t.Context(), "api", expected, []string{"cpus"}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("PatchServiceFields(no-op) error = %v", err)
	}
	expected.CPUs = "2"
	expected.ContainerName = "different"
	if _, err = source.PatchServiceFields(t.Context(), "api", expected, []string{"cpus"}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("PatchServiceFields(mismatch) error = %v", err)
	}
	expected.ContainerName = "example-api"
	expected.CPUs = "not-a-number"
	if _, err = source.PatchServiceFields(t.Context(), "api", expected, []string{"cpus"}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("PatchServiceFields(invalid candidate) error = %v", err)
	}
	expected.CPUs = "2"
	cancelDuringLoad := &cancelComposeAfterChecksContext{after: 2}
	if _, err = source.PatchServiceFields(cancelDuringLoad, "api", expected, []string{"cpus"}); !errors.Is(err, context.Canceled) {
		t.Fatalf("PatchServiceFields(canceled candidate load) error = %v", err)
	}

	invalidRepository := source
	invalidRepository.Repository = new(RepositorySnapshot)
	if _, err = invalidRepository.PatchServiceFields(t.Context(), "api", expected, []string{"cpus"}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("PatchServiceFields(invalid repository) error = %v", err)
	}

	malformed := Source{Content: []byte("services: ["), WorkingDir: t.TempDir()}
	if _, err = malformed.PatchServiceFields(t.Context(), "api", expected, []string{"cpus"}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("PatchServiceFields(malformed) error = %v", err)
	}
}

//nolint:cyclop,funlen // Each assertion rejects a distinct unsupported YAML shape.
func TestDeploymentEditorRejectsUnsafeYAMLShapes(t *testing.T) {
	t.Parallel()

	if _, err := entryLocalServiceNode(nil, "api"); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("entryLocalServiceNode(nil) error = %v", err)
	}
	for _, content := range []string{
		"plain\n",
		"services: plain\n",
		"services:\n  api: plain\n",
	} {
		var document yaml.Node
		if err := yaml.Load([]byte(content), &document, yaml.WithUniqueKeys()); err != nil {
			t.Fatalf("yaml.Load(%q) error = %v", content, err)
		}
		if _, err := entryLocalServiceNode(&document, "api"); !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("entryLocalServiceNode(%q) error = %v", content, err)
		}
	}

	oddMapping := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "key"}}}
	if _, _, valid := deploymentMappingValue(oddMapping, "key"); valid {
		t.Fatal("deploymentMappingValue() accepted an odd mapping")
	}
	nonScalarKey := &yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.SequenceNode}, {Kind: yaml.ScalarNode},
	}}
	if _, _, valid := deploymentMappingValue(nonScalarKey, "key"); valid {
		t.Fatal("deploymentMappingValue() accepted a non-scalar key")
	}

	health := &containerconfig.Healthcheck{Interval: "1s"}
	expected := containerconfig.Spec{ServiceName: "api", Healthcheck: health}
	if _, err := mutateDeploymentNode(&yaml.Node{Kind: yaml.MappingNode}, "api", expected, "healthcheck.interval"); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("mutateDeploymentNode(absent healthcheck) error = %v", err)
	}
	if _, err := mutateDeploymentNode(oddMapping, "api", containerconfig.Spec{ServiceName: "api", CPUs: "2"}, "cpus"); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("mutateDeploymentNode(odd mapping) error = %v", err)
	}

	unsafeChild := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.AliasNode}}}
	if safeDeploymentTarget(unsafeChild) {
		t.Fatal("safeDeploymentTarget() accepted an unsafe sequence child")
	}
	unsafeScalar := &yaml.Node{Kind: yaml.ScalarNode, Value: "${VALUE}"}
	if safeDeploymentTarget(unsafeScalar) {
		t.Fatal("safeDeploymentTarget() accepted interpolation")
	}
	safeChild := &yaml.Node{Kind: yaml.SequenceNode, Content: []*yaml.Node{{Kind: yaml.ScalarNode, Value: "value"}}}
	if !safeDeploymentTarget(safeChild) {
		t.Fatal("safeDeploymentTarget() rejected a safe sequence child")
	}
	removeDeploymentMappingValue(&yaml.Node{Kind: yaml.MappingNode, Content: []*yaml.Node{
		{Kind: yaml.ScalarNode, Value: "other"}, {Kind: yaml.ScalarNode, Value: "value"},
	}}, "missing")

	missingService := testSource(t, "services: {}\n")
	if _, err := missingService.PatchServiceFields(
		t.Context(), "api", containerconfig.Spec{ServiceName: "api", CPUs: "2"}, []string{"cpus"},
	); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("PatchServiceFields(missing service) error = %v", err)
	}
}

func TestDeploymentYAMLValuesRejectInvalidOrAbsentState(t *testing.T) {
	t.Parallel()

	for _, field := range []string{"pids_limit", "init", "read_only"} {
		if _, present, valid := deploymentYAMLValue(containerconfig.Spec{}, field); present || !valid {
			t.Fatalf("deploymentYAMLValue(%q) = present %t, valid %t", field, present, valid)
		}
	}
	if _, present, valid := deploymentYAMLValue(containerconfig.Spec{}, "stop_grace_period"); present || !valid {
		t.Fatalf("deploymentYAMLValue(stop grace) = present %t, valid %t", present, valid)
	}
	for _, field := range []string{"no_new_privileges", "healthcheck.retries", "healthcheck.interval"} {
		if _, _, valid := deploymentYAMLValue(containerconfig.Spec{}, field); valid {
			t.Fatalf("deploymentYAMLValue(%q) accepted absent state", field)
		}
	}
	disabled := containerconfig.Spec{Healthcheck: &containerconfig.Healthcheck{Disabled: true}}
	if _, _, valid := deploymentYAMLValue(disabled, "healthcheck.timeout"); valid {
		t.Fatal("deploymentYAMLValue() accepted a disabled healthcheck")
	}
}

func TestDeploymentSemanticHelpersHandleMissingPathsAndNonMappings(t *testing.T) {
	t.Parallel()

	if _, valid := deploymentSemanticTree([]byte("- item\n")); valid {
		t.Fatal("deploymentSemanticTree() decoded a sequence as a mapping")
	}
	tree := map[string]any{"services": "not-a-map"}
	removeDeploymentSemanticPath(tree, nil)
	removeDeploymentSemanticPath(tree, []string{"services", "api"})
	if tree["services"] != "not-a-map" {
		t.Fatalf("removeDeploymentSemanticPath() changed an unavailable path: %#v", tree)
	}
}
