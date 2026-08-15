package compose

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testReferenceDigest = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testAPIImage        = "example.com/team/api:1@" + testReferenceDigest
	testWorkerImage     = "example.com/team/worker:1@" + testReferenceDigest
)

func TestWorkloadProjectsCoreService(t *testing.T) {
	t.Parallel()

	source := testSource(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    entrypoint: ["/bin/api"]
    command: ["serve", "--port", "8080"]
`)
	source.Content = []byte(pinTestImages(string(source.Content)))

	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	workload, err := project.Workload("")
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}

	referenceDigest, err := domain.ParseDigest(testReferenceDigest)
	if err != nil {
		t.Fatalf("ParseDigest(testReferenceDigest) error = %v", err)
	}

	want := domain.DesiredWorkload{
		ServiceName:     "api",
		ContainerName:   "example-api",
		Image:           testAPIImage,
		ReferenceDigest: referenceDigest,
		Entrypoint:      []string{"/bin/api"},
		Command:         []string{"serve", "--port", "8080"},
		SourceDigest:    domain.Hash(source.Content),
		EffectiveDigest: workload.EffectiveDigest,
	}
	if !reflect.DeepEqual(workload, want) {
		t.Fatalf("Workload() = %#v, want %#v", workload, want)
	}

	if workload.EffectiveDigest == (domain.Digest{}) {
		t.Fatal("EffectiveDigest is empty")
	}
}

func TestWorkloadDigestUsesNormalizedSemantics(t *testing.T) {
	t.Parallel()

	left := loadWorkload(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`)
	right := loadWorkload(t, `
# A source-only comment must not alter desired runtime state.
services:
  api: {network_mode: bridge, image: example.com/team/api:1, container_name: example-api}
name: example
`)

	if left.SourceDigest == right.SourceDigest {
		t.Fatal("SourceDigest did not identify source bytes")
	}

	if left.EffectiveDigest != right.EffectiveDigest {
		t.Fatalf("EffectiveDigest = %s and %s for equivalent projects", left.EffectiveDigest, right.EffectiveDigest)
	}
}

func TestWorkloadPreservesClearedProcessValues(t *testing.T) {
	t.Parallel()

	inherited := loadWorkload(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`)
	cleared := loadWorkload(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    entrypoint: []
    command: []
`)

	if inherited.Entrypoint != nil || inherited.Command != nil {
		t.Fatalf("inherited process = %#v", inherited)
	}

	if cleared.Entrypoint == nil || cleared.Command == nil {
		t.Fatalf("cleared process = %#v", cleared)
	}

	if inherited.EffectiveDigest == cleared.EffectiveDigest {
		t.Fatal("EffectiveDigest ignored explicit process clearing")
	}
}

func TestWorkloadSelectsOneActiveService(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, pinTestImages(`
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
  worker:
    container_name: example-worker
    image: example.com/team/worker:1
    network_mode: bridge
`)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	_, err = project.Workload("")
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Workload(empty) error = %v, want ErrInvalidSource", err)
	}

	workload, err := project.Workload("worker")
	if err != nil {
		t.Fatalf("Workload(worker) error = %v", err)
	}

	if workload.ServiceName != "worker" || workload.ContainerName != "example-worker" {
		t.Fatalf("Workload(worker) = %#v", workload)
	}

	_, err = project.Workload("missing")
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Workload(missing) error = %v, want ErrInvalidSource", err)
	}
}

func TestWorkloadProjectsSameDocumentExtends(t *testing.T) {
	t.Parallel()

	workload := loadSelectedWorkload(t, "api", `
name: example
services:
  base:
    container_name: example-base
    image: example.com/team/api:1
    network_mode: bridge
  api:
    extends: base
    container_name: example-api
`)

	if workload.Image != testAPIImage || workload.ContainerName != "example-api" {
		t.Fatalf("Workload(api) = %#v", workload)
	}
}

func FuzzWorkloadDigestIgnoresComments(f *testing.F) {
	f.Add([]byte("maniud"))

	f.Fuzz(func(t *testing.T, comment []byte) {
		if len(comment) > 256 {
			t.Skip()
		}

		const source = `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`

		encodedComment := base64.RawStdEncoding.EncodeToString(comment)
		plain := loadWorkload(t, source)
		annotated := loadWorkload(t, "# "+encodedComment+"\n"+source)

		if plain.EffectiveDigest != annotated.EffectiveDigest {
			t.Fatalf("comment changed EffectiveDigest from %s to %s", plain.EffectiveDigest, annotated.EffectiveDigest)
		}

		if plain.SourceDigest == annotated.SourceDigest {
			t.Fatal("comment did not change SourceDigest")
		}
	})
}

func loadWorkload(t *testing.T, content string) domain.DesiredWorkload {
	t.Helper()

	return loadSelectedWorkload(t, "", content)
}

func loadSelectedWorkload(t *testing.T, serviceName string, content string) domain.DesiredWorkload {
	t.Helper()

	project, err := Load(context.Background(), testSource(t, pinTestImages(content)))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	workload, err := project.Workload(serviceName)
	if err != nil {
		t.Fatalf("Workload(%q) error = %v", serviceName, err)
	}

	return workload
}

func pinTestImages(content string) string {
	content = strings.ReplaceAll(content, "example.com/team/api:1", testAPIImage)

	return strings.ReplaceAll(content, "example.com/team/worker:1", testWorkerImage)
}
