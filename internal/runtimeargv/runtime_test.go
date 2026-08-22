package runtimeargv

import (
	"errors"
	"slices"
	"strings"
	"testing"

	publicargv "github.com/IceCodeNew/maniud/containerconfig/runtimeargv"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	wrapperWorkingDirectory = "/workspace/project"
	wrapperImage            = "team/app:1"
)

func TestProjectionBindsImmutableImage(t *testing.T) {
	t.Parallel()

	projection, err := Parse([]string{
		publicargv.RuntimeNerdctl, publicargv.OperationCreate,
		"--entrypoint=/debug", wrapperImage, "serve",
	}, "service", wrapperWorkingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	digest := domain.Hash([]byte("runtime argv wrapper"))
	reference, err := projection.Source().Pin(digest)
	if err != nil {
		t.Fatal(err)
	}
	image := domain.ImageIdentity{
		Origin: domain.ImageOriginRegistry, Reference: reference.String(), ReferenceDigest: digest,
		Platform: projection.Platform(), Entrypoint: []string{"/init"}, Command: []string{"default"},
	}
	workload, err := projection.Workload(image)
	if err != nil || workload.ServiceName != "service" ||
		!slices.Equal(workload.Entrypoint, []string{"/debug"}) || !slices.Equal(workload.Command, []string{"serve"}) {
		t.Fatalf("Workload() = %#v, %v", workload, err)
	}
	assertProjectionMetadata(t, projection)
}

func assertProjectionMetadata(t *testing.T, projection Projection) {
	t.Helper()

	if projection.Name() != "service" || projection.Runtime() != domain.RuntimeContainerd ||
		projection.Source().String() != "docker.io/team/app:1" || len(projection.EnvironmentFiles()) != 0 ||
		len(projection.Warnings()) != 0 {
		t.Fatalf("Projection = %#v", projection)
	}
}

func TestProjectionRejectsInvalidInputAndImageProof(t *testing.T) {
	t.Parallel()

	if _, err := Parse(nil, "", wrapperWorkingDirectory); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse(nil) error = %v", err)
	}
	if _, err := newProjection(domain.WorkloadSpec{}, "", domain.Platform{}, nil, nil, ""); !errors.Is(err, ErrInvalid) {
		t.Fatalf("newProjection(zero) error = %v", err)
	}
	if _, err := newProjection(
		domain.WorkloadSpec{}, wrapperImage, domain.Platform{}, nil, nil, "invalid",
	); !errors.Is(err, ErrInvalid) {
		t.Fatalf("newProjection(runtime) error = %v", err)
	}

	projection, err := ParseSource("docker://"+wrapperImage, "service")
	if err != nil {
		t.Fatal(err)
	}
	digest := domain.Hash([]byte("runtime argv invalid proof"))
	reference, err := projection.Source().Pin(digest)
	if err != nil {
		t.Fatal(err)
	}
	valid := domain.ImageIdentity{
		Origin: domain.ImageOriginRegistry, Reference: reference.String(), ReferenceDigest: digest,
		Platform: projection.Platform(),
	}
	otherDigest := domain.Hash([]byte("other"))
	for _, image := range []domain.ImageIdentity{
		{},
		{Origin: domain.ImageOriginDockerArchive, Reference: valid.Reference,
			ReferenceDigest: digest, Platform: valid.Platform},
		{Origin: domain.ImageOriginRegistry, Reference: valid.Reference,
			ReferenceDigest: otherDigest, Platform: valid.Platform},
		{Origin: domain.ImageOriginRegistry, Reference: "docker.io/team/other:1@" + digest.String(),
			ReferenceDigest: digest, Platform: valid.Platform},
	} {
		if _, err := projection.Workload(image); !errors.Is(err, ErrInvalid) {
			t.Fatalf("Workload(%#v) error = %v", image, err)
		}
	}
	if !strings.HasPrefix(valid.Reference, "docker.io/") {
		t.Fatalf("normalized reference = %q", valid.Reference)
	}
}

func TestParseSourceRejectsInvalidRuntimeQualification(t *testing.T) {
	t.Parallel()

	for _, value := range []string{
		"docker://bad@@reference",
		wrapperImage,
		"unknown://" + wrapperImage,
	} {
		if _, err := ParseSource(value, ""); !errors.Is(err, ErrInvalid) {
			t.Fatalf("ParseSource(%q) error = %v", value, err)
		}
	}
}

func TestParseSourceSelectsQualifiedRuntime(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		prefix  string
		runtime domain.RuntimeKind
	}{
		{prefix: "docker://", runtime: domain.RuntimeDocker},
		{prefix: "podman://", runtime: domain.RuntimePodman},
		{prefix: "containerd://", runtime: domain.RuntimeContainerd},
	} {
		projection, err := ParseSource(test.prefix+wrapperImage, "service")
		if err != nil || projection.Runtime() != test.runtime || projection.Source().String() != "docker.io/team/app:1" {
			t.Fatalf("ParseSource(%q) = %#v, %v", test.prefix, projection, err)
		}
	}
}

func TestTargetRuntimeDefaultsToDocker(t *testing.T) {
	t.Parallel()

	defaultRuntime, valid := targetRuntime("")
	if !valid || defaultRuntime != domain.RuntimeDocker {
		t.Fatalf("targetRuntime(empty) = %q, %t", defaultRuntime, valid)
	}
}

func TestProjectionSelectsClientAdapter(t *testing.T) {
	t.Parallel()

	dockerProjection, err := Parse([]string{
		publicargv.RuntimeDocker, publicargv.OperationCreate, wrapperImage,
	}, "", wrapperWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse(valid Docker) error = %v", err)
	}
	if dockerProjection.Runtime() != domain.RuntimeDocker {
		t.Fatalf("Docker projection runtime = %q", dockerProjection.Runtime())
	}

	podmanProjection, err := Parse([]string{
		publicargv.RuntimePodman, publicargv.OperationCreate, wrapperImage,
	}, "", wrapperWorkingDirectory)
	if err != nil {
		t.Fatalf("Parse(valid Podman) error = %v", err)
	}
	if podmanProjection.Runtime() != domain.RuntimePodman {
		t.Fatalf("Podman projection runtime = %q", podmanProjection.Runtime())
	}

	if _, err := Parse([]string{
		publicargv.RuntimeNerdctl, publicargv.OperationCreate,
		"--health-cmd=true", "--health-start-interval=1s", wrapperImage,
	}, "", wrapperWorkingDirectory); !errors.Is(err, ErrInvalid) {
		t.Fatalf("Parse(invalid nerdctl) error = %v", err)
	}
}
