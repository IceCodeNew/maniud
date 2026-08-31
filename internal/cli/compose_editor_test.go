//nolint:lll // Complete Compose fixtures keep field spellings and expectations readable.
package cli

import (
	"bytes"
	"context"
	"errors"
	"reflect"
	"slices"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
)

const deploymentComposeEntry = "deploy/compose.yaml"

var errDeploymentFixturePath = errors.New("deployment fixture path is unavailable")

type cancelAfterChecksContext struct {
	after int64
	calls atomic.Int64
}

func (*cancelAfterChecksContext) Deadline() (time.Time, bool) { return time.Time{}, false }

func (*cancelAfterChecksContext) Done() <-chan struct{} { return nil }

func (ctx *cancelAfterChecksContext) Err() error {
	if ctx.calls.Add(1) >= ctx.after {
		return context.Canceled
	}

	return nil
}

func (*cancelAfterChecksContext) Value(any) any { return nil }

//nolint:cyclop,gocyclo // The test proves the complete closed field set against one repository snapshot.
func TestPrepareDeploymentEditChangesOnlyTypedFields(t *testing.T) {
	t.Parallel()

	originalContent := deploymentComposeFixture()
	source := captureDeploymentSource(t, map[string][]byte{deploymentComposeEntry: originalContent})
	originalDigest := source.Repository.Digest
	originalFile := bytes.Clone(source.Repository.Files[deploymentComposeEntry].Content)
	patches := []application.DeploymentPatch{
		deploymentPatch(t, application.DeploymentCPUs, application.DeploymentCPU(2.5)),
		deploymentPatch(t, application.DeploymentMemory, application.DeploymentBytes(1024)),
		deploymentPatch(t, application.DeploymentPIDs, application.DeploymentInteger(-1)),
		deploymentPatch(t, application.DeploymentRestart, application.DeploymentRestartPolicy("on-failure:3")),
		deploymentPatch(t, application.DeploymentSharedMemory, application.DeploymentBytes(2048)),
		deploymentPatch(t, application.DeploymentStopGrace, application.DeploymentDuration(20*time.Second)),
		deploymentPatch(t, application.DeploymentInit, application.DeploymentBoolean(false)),
		deploymentPatch(t, application.DeploymentReadOnly, application.DeploymentBoolean(true)),
		deploymentPatch(t, application.DeploymentNoNewPrivileges, application.DeploymentEnabled{}),
		deploymentPatch(t, application.DeploymentHealthInterval, application.DeploymentDuration(5*time.Second)),
		deploymentPatch(t, application.DeploymentHealthTimeout, application.DeploymentDuration(6*time.Second)),
		deploymentPatch(t, application.DeploymentHealthRetries, application.DeploymentRetries(4)),
		deploymentPatch(t, application.DeploymentHealthStartPeriod, application.DeploymentDuration(8*time.Second)),
		deploymentPatch(t, application.DeploymentHealthStartInterval, application.DeploymentDuration(9*time.Second)),
	}

	candidate, err := prepareDeploymentEdit(t.Context(), source, "api", patches)
	if err != nil {
		t.Fatalf("prepareDeploymentEdit() error = %v", err)
	}
	if !slices.Equal(candidate.fields, application.DeploymentFields()) {
		t.Fatalf("changed fields = %v", candidate.fields)
	}
	if candidate.source.Repository == nil || candidate.source.Repository.Digest == originalDigest ||
		!bytes.Equal(candidate.source.Repository.Files[deploymentComposeEntry].Content, candidate.source.Content) {
		t.Fatalf("candidate repository = %#v", candidate.source.Repository)
	}
	if !bytes.Equal(source.Content, originalContent) ||
		!bytes.Equal(source.Repository.Files[deploymentComposeEntry].Content, originalFile) {
		t.Fatal("prepareDeploymentEdit() mutated the input source")
	}
	if !bytes.Contains(candidate.source.Content, []byte("# CPU budget")) ||
		!bytes.Contains(candidate.source.Content, []byte("# Health timeout")) ||
		!bytes.Contains(candidate.source.Content, []byte("worker:stable")) {
		t.Fatalf("candidate did not preserve comments or the unselected service:\n%s", candidate.source.Content)
	}

	project, err := compose.Load(t.Context(), candidate.source)
	if err != nil {
		t.Fatalf("Load(candidate) error = %v", err)
	}
	spec, err := project.ServiceSpec("api")
	if err != nil || spec.CPUs != "2.5" || spec.MemoryBytes != 1024 || spec.PidsLimit == nil || *spec.PidsLimit != -1 ||
		spec.Restart != "on-failure:3" || spec.SharedMemoryBytes != 2048 || spec.StopTimeout == nil ||
		*spec.StopTimeout != 20 || spec.Init == nil || *spec.Init || spec.ReadOnly == nil || !*spec.ReadOnly ||
		!spec.NoNewPrivileges || spec.Healthcheck == nil || spec.Healthcheck.Interval != "5s" ||
		spec.Healthcheck.Timeout != "6s" || spec.Healthcheck.Retries == nil || *spec.Healthcheck.Retries != 4 ||
		spec.Healthcheck.StartPeriod != "8s" || spec.Healthcheck.StartInterval != "9s" {
		t.Fatalf("ServiceSpec(candidate) = %#v, %v", spec, err)
	}
}

func TestPrepareDeploymentEditHandlesNoOpDuplicateAndUnset(t *testing.T) {
	t.Parallel()

	source := captureDeploymentSource(t, map[string][]byte{deploymentComposeEntry: deploymentComposeFixture()})
	noOp, err := prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{
		deploymentPatch(t, application.DeploymentCPUs, application.DeploymentCPU(1)),
	})
	if err != nil || len(noOp.fields) != 0 || !reflect.DeepEqual(noOp.source, source) {
		t.Fatalf("prepareDeploymentEdit(no-op) = %#v, %v", noOp, err)
	}

	duplicate := deploymentPatch(t, application.DeploymentCPUs, application.DeploymentCPU(2))
	if _, err = prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{duplicate, duplicate}); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("prepareDeploymentEdit(duplicate) error = %v", err)
	}

	unset, err := prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{
		deploymentPatch(t, application.DeploymentCPUs, application.DeploymentUnset{}),
		deploymentPatch(t, application.DeploymentHealthStartInterval, application.DeploymentUnset{}),
	})
	if err != nil || !slices.Equal(unset.fields, []application.DeploymentField{
		application.DeploymentCPUs, application.DeploymentHealthStartInterval,
	}) || bytes.Contains(unset.source.Content, []byte("cpus:")) ||
		bytes.Contains(unset.source.Content, []byte("start_interval:")) {
		t.Fatalf("prepareDeploymentEdit(unset) = %#v, %v\n%s", unset.fields, err, unset.source.Content)
	}
}

func TestPrepareDeploymentEditRejectsUnsafeTargetsAndHealthStates(t *testing.T) {
	t.Parallel()

	cpuPatch := deploymentPatch(t, application.DeploymentCPUs, application.DeploymentCPU(2))
	for name, content := range map[string]string{
		"interpolation": strings.Replace(string(deploymentComposeFixture()), "cpus: 1", "cpus: ${CPUS}", 1),
		"explicit tag":  strings.Replace(string(deploymentComposeFixture()), "cpus: 1", "cpus: !!float 1", 1),
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			source := compose.Source{
				Content: []byte(content), WorkingDir: t.TempDir(), Environment: map[string]string{"CPUS": "1"},
			}
			if _, err := prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{cpuPatch}); !errors.Is(err, errDeploymentEditInvalid) {
				t.Fatalf("prepareDeploymentEdit() error = %v", err)
			}
		})
	}

	healthPatch := deploymentPatch(t, application.DeploymentHealthTimeout, application.DeploymentDuration(time.Second))
	for name, content := range map[string]string{
		"absent":               strings.Replace(string(deploymentComposeFixture()), deploymentHealthFixture(), "", 1),
		"disabled healthcheck": strings.Replace(string(deploymentComposeFixture()), "healthcheck:\n", "healthcheck:\n      disable: true\n", 1),
	} {
		t.Run("health "+name, func(t *testing.T) {
			t.Parallel()
			source := compose.Source{Content: []byte(content), WorkingDir: t.TempDir(), Environment: map[string]string{}}
			if _, err := prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{healthPatch}); !errors.Is(err, errDeploymentEditInvalid) {
				t.Fatalf("prepareDeploymentEdit() error = %v", err)
			}
		})
	}
}

func TestPrepareDeploymentEditRequiresServiceInEntryDocument(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		deploymentComposeEntry: []byte("name: example\ninclude:\n  - included.yaml\nservices: {}\n"),
		"deploy/included.yaml": []byte(`services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    cpus: 1
`),
	}
	source := captureDeploymentSource(t, files)
	patch := deploymentPatch(t, application.DeploymentCPUs, application.DeploymentCPU(2))
	if _, err := prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{patch}); !errors.Is(err, errDeploymentEditInvalid) {
		t.Fatalf("prepareDeploymentEdit(include-only service) error = %v", err)
	}
}

func TestPrepareDeploymentEditWritesExtendsOverrideToEntry(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		deploymentComposeEntry: []byte(`
name: example
services:
  api:
    extends:
      file: base.yaml
      service: base
`),
		"deploy/base.yaml": []byte(`
services:
  base:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    cpus: 1
`),
	}
	source := captureDeploymentSource(t, files)
	candidate, err := prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{
		deploymentPatch(t, application.DeploymentCPUs, application.DeploymentCPU(2)),
	})
	if err != nil {
		t.Fatalf("prepareDeploymentEdit() error = %v", err)
	}
	if !bytes.Contains(candidate.source.Content, []byte("cpus: 2")) ||
		!bytes.Equal(candidate.source.Repository.Files["deploy/base.yaml"].Content, files["deploy/base.yaml"]) {
		t.Fatalf("extends candidate =\n%s", candidate.source.Content)
	}
}

func TestPrepareDeploymentEditRejectsCancellationAndInvalidInput(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	if _, err := prepareDeploymentEdit(ctx, compose.Source{}, "api", []application.DeploymentPatch{{}}); !errors.Is(err, context.Canceled) {
		t.Fatalf("prepareDeploymentEdit(canceled) error = %v", err)
	}
	for _, input := range []struct {
		source  compose.Source
		service string
		patches []application.DeploymentPatch
	}{
		{source: compose.Source{}, service: "api", patches: []application.DeploymentPatch{{}}},
		{source: compose.Source{Content: deploymentComposeFixture(), WorkingDir: t.TempDir()}, service: "", patches: []application.DeploymentPatch{{}}},
		{source: compose.Source{Content: deploymentComposeFixture(), WorkingDir: t.TempDir()}, service: "api"},
	} {
		if _, err := prepareDeploymentEdit(t.Context(), input.source, input.service, input.patches); err == nil {
			t.Fatal("prepareDeploymentEdit(invalid input) error = nil")
		}
	}
}

func TestPrepareDeploymentEditPreservesCancellationDuringCandidateBuild(t *testing.T) {
	t.Parallel()

	source := captureDeploymentSource(t, map[string][]byte{deploymentComposeEntry: deploymentComposeFixture()})
	patch := deploymentPatch(t, application.DeploymentCPUs, application.DeploymentCPU(2))
	for check := int64(2); check < 256; check++ {
		ctx := &cancelAfterChecksContext{after: check}
		_, err := prepareDeploymentEdit(ctx, source, "api", []application.DeploymentPatch{patch})
		if err != nil && err.Error() == "prepare deployment edit: context canceled" {
			return
		}
	}

	t.Fatal("prepareDeploymentEdit() did not preserve cancellation during candidate construction")
}

func FuzzPrepareDeploymentEdit(f *testing.F) {
	f.Add(string(deploymentComposeFixture()), uint16(4))
	f.Add("services: {}\n", uint16(1))
	f.Add("bad: [\n", uint16(0))
	f.Fuzz(func(t *testing.T, content string, units uint16) {
		if len(content) > 1<<20 {
			return
		}
		cpu := application.DeploymentCPU(float32(units%64+1) / 2)
		patch := deploymentPatch(t, application.DeploymentCPUs, cpu)
		source := compose.Source{Content: []byte(content), WorkingDir: t.TempDir(), Environment: map[string]string{}}
		candidate, err := prepareDeploymentEdit(t.Context(), source, "api", []application.DeploymentPatch{patch})
		if err != nil {
			return
		}
		if _, err = compose.Load(t.Context(), candidate.source); err != nil {
			t.Fatalf("Load(successful candidate) error = %v", err)
		}
	})
}

func deploymentPatch(t *testing.T, field application.DeploymentField, value application.DeploymentValue) application.DeploymentPatch {
	t.Helper()
	patch, err := application.NewDeploymentPatch(field, value)
	if err != nil {
		t.Fatalf("NewDeploymentPatch(%s) error = %v", field.ID(), err)
	}

	return patch
}

func captureDeploymentSource(t *testing.T, files map[string][]byte) compose.Source {
	t.Helper()
	source, err := compose.CaptureRepositorySource(
		t.TempDir(),
		deploymentComposeEntry,
		map[string]string{"COMPOSE_DISABLE_ENV_FILE": "true"},
		func(path string) (compose.RepositoryFile, bool, error) {
			content, found := files[path]

			return compose.RepositoryFile{Content: bytes.Clone(content)}, found, nil
		},
		func(string) (compose.RepositoryPathSnapshot, error) {
			return compose.RepositoryPathSnapshot{}, errDeploymentFixturePath
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}

	return source
}

func deploymentComposeFixture() []byte {
	return []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    cpus: 1 # CPU budget
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
      timeout: 3s # Health timeout
      retries: 3
      start_period: 4s
      start_interval: 2s
  worker:
    container_name: example-worker
    image: example.com/team/worker:stable
    network_mode: bridge
`)
}

func deploymentHealthFixture() string {
	return `    healthcheck:
      test: ["CMD", "true"]
      interval: 30s
      timeout: 3s # Health timeout
      retries: 3
      start_period: 4s
      start_interval: 2s
`
}
