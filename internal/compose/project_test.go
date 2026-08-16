package compose

import (
	"context"
	"encoding/base64"
	"errors"
	"reflect"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testReferenceDigest        = "sha256:0123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef"
	testPlatformManifestDigest = "sha256:123456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef0"
	testImageConfigDigest      = "sha256:23456789abcdef0123456789abcdef0123456789abcdef0123456789abcdef01"
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

	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	image := resolvedImageForService(t, project, "")

	workload, err := project.Workload("", image)
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}

	want := domain.DesiredWorkload{
		ServiceName:     "api",
		ContainerName:   "example-api",
		Image:           image,
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

	if !reflect.DeepEqual(inherited.Entrypoint, []string{"/image-entrypoint"}) ||
		!reflect.DeepEqual(inherited.Command, []string{"image-command"}) {
		t.Fatalf("inherited process = %#v", inherited)
	}

	if cleared.Entrypoint == nil || cleared.Command == nil {
		t.Fatalf("cleared process = %#v", cleared)
	}

	if inherited.EffectiveDigest == cleared.EffectiveDigest {
		t.Fatal("EffectiveDigest ignored explicit process clearing")
	}
}

func TestWorkloadPreservesAbsentImageProcessDefaults(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	image := resolvedImageForService(t, project, "api")
	image.Entrypoint = nil
	image.Command = nil

	workload, err := project.Workload("api", image)
	if err != nil || workload.Entrypoint != nil || workload.Command != nil ||
		workload.EffectiveDigest == (domain.Digest{}) {
		t.Fatalf("Workload(absent image process) = %#v, %v", workload, err)
	}
}

func TestWorkloadEntrypointOverrideClearsImageCommand(t *testing.T) {
	t.Parallel()

	workload := loadWorkload(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    entrypoint: ["/override-entrypoint"]
`)

	if !reflect.DeepEqual(workload.Entrypoint, []string{"/override-entrypoint"}) ||
		workload.Command == nil || len(workload.Command) != 0 {
		t.Fatalf("overridden process = %#v", workload)
	}
}

func TestWorkloadClonesResolvedImageProcessEvidence(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	image := resolvedImageForService(t, project, "api")

	workload, err := project.Workload("api", image)
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}

	image.Entrypoint[0] = "mutated-entrypoint"
	image.Command[0] = "mutated-command"

	if reflect.DeepEqual(workload.Image.Entrypoint, image.Entrypoint) ||
		reflect.DeepEqual(workload.Image.Command, image.Command) ||
		reflect.DeepEqual(workload.Entrypoint, image.Entrypoint) || reflect.DeepEqual(workload.Command, image.Command) {
		t.Fatalf("Workload() retained caller-owned process slices: %#v", workload)
	}
}

func TestWorkloadSelectsOneActiveService(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, `
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
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	_, err = project.ImageSource("")
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Workload(empty) error = %v, want ErrInvalidSource", err)
	}

	workerImage := resolvedImageForService(t, project, "worker")

	workload, err := project.Workload("worker", workerImage)
	if err != nil {
		t.Fatalf("Workload(worker) error = %v", err)
	}

	if workload.ServiceName != "worker" || workload.ContainerName != "example-worker" {
		t.Fatalf("Workload(worker) = %#v", workload)
	}

	_, err = project.ImageSource("missing")
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

	if workload.Image.Reference != "example.com/team/api:1@"+testReferenceDigest ||
		workload.ContainerName != "example-api" {
		t.Fatalf("Workload(api) = %#v", workload)
	}
}

func TestImageSourceNormalizesDockerCompatibleInput(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		image string
		want  string
	}{
		{name: "short implicit latest", image: "api", want: "docker.io/library/api:latest"},
		{name: "Docker Hub alias", image: "registry-1.docker.io/api:1", want: "docker.io/library/api:1"},
		{name: "uppercase registry", image: "EXAMPLE.com/team/api:1", want: "example.com/team/api:1"},
		{
			name:  "tag and digest",
			image: "example.com/team/api:1@" + testReferenceDigest,
			want:  "example.com/team/api:1@" + testReferenceDigest,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			project, err := Load(context.Background(), testSource(t, `
name: example
services:
  api:
    container_name: example-api
    image: `+test.image+`
    network_mode: bridge
`))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			source, err := project.ImageSource("")
			if err != nil || source.String() != test.want {
				t.Fatalf("ImageSource() = %q, %v, want %q", source.String(), err, test.want)
			}
		})
	}
}

func TestWorkloadRejectsImageResolvedFromAnotherSource(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	image := resolvedImageForService(t, project, "")
	image.Reference = "example.com/team/other:1@" + image.ReferenceDigest.String()

	_, err = project.Workload("", image)
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Workload() error = %v, want ErrInvalidSource", err)
	}
}

func TestImageResolvesSourceRejectsInvalidProof(t *testing.T) {
	t.Parallel()

	image := domain.ImageIdentity{
		Reference:       "example.com/team/api:1@" + testImageConfigDigest,
		ReferenceDigest: mustTestDigest(t, testImageConfigDigest),
		Platform: domain.Platform{
			OS:           "linux",
			Architecture: "amd64",
			Variant:      "",
		},
		PlatformManifest: mustTestDigest(t, testPlatformManifestDigest),
		ImageConfig:      mustTestDigest(t, testImageConfigDigest),
		Entrypoint:       nil,
		Command:          nil,
	}

	if imageResolvesSource("https://example.com/team/api:1", image) {
		t.Fatal("imageResolvesSource() accepted URL source")
	}

	if imageResolvesSource("example.com/team/api:1@"+testReferenceDigest, image) {
		t.Fatal("imageResolvesSource() accepted conflicting digest")
	}
}

func TestWorkloadDigestIncludesResolvedImageProof(t *testing.T) {
	t.Parallel()

	const source = `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
`

	baseline := loadWorkload(t, source)

	tests := []struct {
		name   string
		mutate func(*domain.ImageIdentity)
	}{
		{name: "platform OS", mutate: func(value *domain.ImageIdentity) { value.Platform.OS = "darwin" }},
		{name: "platform architecture", mutate: func(value *domain.ImageIdentity) { value.Platform.Architecture = "arm64" }},
		{name: "platform variant", mutate: func(value *domain.ImageIdentity) { value.Platform.Variant = "v8" }},
		{
			name: "platform manifest",
			mutate: func(value *domain.ImageIdentity) {
				value.PlatformManifest = domain.Hash([]byte("other platform manifest"))
			},
		},
		{
			name: "image config",
			mutate: func(value *domain.ImageIdentity) {
				value.ImageConfig = domain.Hash([]byte("other image config"))
			},
		},
		{name: "image entrypoint", mutate: func(value *domain.ImageIdentity) { value.Entrypoint = []string{"other"} }},
		{name: "image command", mutate: func(value *domain.ImageIdentity) { value.Command = []string{"other"} }},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			project, err := Load(context.Background(), testSource(t, source))
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}

			image := resolvedImageForService(t, project, "")
			test.mutate(&image)

			workload, err := project.Workload("", image)
			if err != nil {
				t.Fatalf("Workload() error = %v", err)
			}

			if workload.EffectiveDigest == baseline.EffectiveDigest {
				t.Fatal("resolved image proof did not change EffectiveDigest")
			}
		})
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

	project, err := Load(context.Background(), testSource(t, content))
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}

	image := resolvedImageForService(t, project, serviceName)

	workload, err := project.Workload(serviceName, image)
	if err != nil {
		t.Fatalf("Workload(%q) error = %v", serviceName, err)
	}

	return workload
}

func resolvedImageForService(t *testing.T, project Project, serviceName string) domain.ImageIdentity {
	t.Helper()

	source, err := project.ImageSource(serviceName)
	if err != nil {
		t.Fatalf("ImageSource(%q) error = %v", serviceName, err)
	}

	referenceDigest := mustTestDigest(t, testReferenceDigest)

	reference, err := source.Pin(referenceDigest)
	if err != nil {
		t.Fatalf("Pin(%q) error = %v", source.String(), err)
	}

	return domain.ImageIdentity{
		Reference:       reference.String(),
		ReferenceDigest: referenceDigest,
		Platform: domain.Platform{
			OS:           "linux",
			Architecture: "amd64",
			Variant:      "",
		},
		PlatformManifest: mustTestDigest(t, testPlatformManifestDigest),
		ImageConfig:      mustTestDigest(t, testImageConfigDigest),
		Entrypoint:       []string{"/image-entrypoint"},
		Command:          []string{"image-command"},
	}
}

func mustTestDigest(t *testing.T, value string) domain.Digest {
	t.Helper()

	digest, err := domain.ParseDigest(value)
	if err != nil {
		t.Fatalf("ParseDigest(%q) error = %v", value, err)
	}

	return digest
}
