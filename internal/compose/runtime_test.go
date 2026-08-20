package compose

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

var errRuntimeRenderTest = errors.New("runtime render test failed")

func TestRenderRuntimeProducesStrictCompose(t *testing.T) {
	t.Parallel()

	projection, err := runtimeargv.ParseSource("example.com/team/image:1", "service")
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	digest, err := domain.ParseDigest("sha256:" + strings.Repeat("d", 64))
	if err != nil {
		t.Fatalf("ParseDigest() error = %v", err)
	}
	image := domain.ImageIdentity{
		Origin:          domain.ImageOriginRegistry,
		Reference:       "example.com/team/image:1@" + digest.String(),
		ReferenceDigest: digest,
		Platform:        projection.Platform(),
		Entrypoint:      []string{testImageEntrypoint},
		Command:         []string{testImageCommand},
	}

	rendered, err := RenderRuntime(context.Background(), projection, image, t.TempDir())
	if err != nil {
		t.Fatalf("RenderRuntime() error = %v", err)
	}
	for _, expected := range []string{"services:", "service:", "container_name: service", "network_mode: bridge"} {
		if !strings.Contains(string(rendered), expected) {
			t.Fatalf("RenderRuntime() = %q, missing %q", rendered, expected)
		}
	}
	project, err := Load(context.Background(), Source{
		Content: rendered, WorkingDir: "/workspace",
		Environment: map[string]string{}, Profiles: nil,
	})
	if err != nil {
		t.Fatalf("Load(generated %q) error = %v", rendered, err)
	}
	workload, err := project.Workload("service", image)
	if err != nil || workload.ContainerName != "service" || workload.Image.Platform != projection.Platform() {
		t.Fatalf("Workload(generated) = %#v, %v", workload, err)
	}

	testRuntimeRenderBoundaries(t, projection, image, digest)
}

func TestRenderRuntimeRoundTripsSharedWorkload(t *testing.T) {
	t.Parallel()

	projection, err := runtimeargv.Parse([]string{
		composePodmanRuntime, "create", "--cpus=1.5", "--memory=512m", "--restart=always",
		"--cap-add=net_admin", "--dns=1.1.1.1", "--device=/dev/fuse:/dev/fuse:rw",
		"--tmpfs=/cache:ro,size=2m", "--ulimit=nofile=1024:2048", "--env=FOO=bar",
		"--label=team=platform", "--publish=127.0.0.1:8080:80", "--security-opt=no-new-privileges",
		"--volume=/host/data:/data:ro", "--health-cmd=true", "--health-interval=30s",
		"example.com/team/image:1", testServeCommand,
	}, "service", "/workspace")
	if err != nil {
		t.Fatalf("Parse() error = %v", err)
	}
	digest, err := domain.ParseDigest("sha256:" + strings.Repeat("e", 64))
	if err != nil {
		t.Fatal(err)
	}
	image := domain.ImageIdentity{
		Origin: domain.ImageOriginRegistry, Reference: "example.com/team/image:1@" + digest.String(),
		ReferenceDigest: digest, Platform: projection.Platform(), Entrypoint: []string{testInitEntrypoint},
	}
	want, err := projection.Workload(image)
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}
	want.Entrypoint = []string{testInitEntrypoint}

	rendered, err := RenderRuntime(context.Background(), projection, image, "/workspace")
	if err != nil {
		t.Fatalf("RenderRuntime() error = %v", err)
	}
	project, err := Load(context.Background(), Source{
		Content: rendered, WorkingDir: "/workspace", Environment: map[string]string{},
	})
	if err != nil {
		t.Fatalf("Load(%s) error = %v", rendered, err)
	}
	workload, err := project.Workload("service", image)
	if err != nil {
		t.Fatalf("Workload(%s) error = %v", rendered, err)
	}
	if !reflect.DeepEqual(workload.WorkloadSpec, want) {
		t.Fatalf("round trip = %#v, want %#v\n%s", workload.WorkloadSpec, want, rendered)
	}
}

func TestRenderArchiveRoundTripsCompleteImageConfiguration(t *testing.T) {
	t.Parallel()

	analysis := archiveRenderAnalysis(t)
	rendered, name, err := RenderArchive(context.Background(), analysis, "", t.TempDir())
	if err != nil {
		t.Fatalf("RenderArchive() error = %v", err)
	}
	if name != "archive" || strings.Contains(string(rendered), analysis.Source.Path()) {
		t.Fatalf("RenderArchive() = %q, %q", name, rendered)
	}
	project, err := Load(context.Background(), Source{
		Content: rendered, WorkingDir: t.TempDir(), Environment: map[string]string{},
	})
	if err != nil {
		t.Fatalf("Load(archive) error = %v\n%s", err, rendered)
	}
	input, err := project.ImageInput(name)
	if err != nil {
		t.Fatalf("ImageInput(archive) error = %v", err)
	}
	identity, valid := input.ArchiveIdentity()
	if !valid {
		t.Fatal("ArchiveIdentity() was not selected")
	}
	workload, err := project.Workload(name, identity)
	if err != nil {
		t.Fatalf("Workload(archive) error = %v", err)
	}
	want := domain.ResolveWorkloadSpec(domain.WorkloadSpec{
		ServiceName: name, ContainerName: name, Platform: analysis.Identity.Platform,
		NetworkMode: composeBridgeNetwork,
	}, analysis.Identity)
	if !reflect.DeepEqual(workload.WorkloadSpec, want) {
		t.Fatalf("archive round trip = %#v, want %#v\n%s", workload.WorkloadSpec, want, rendered)
	}
}

func TestRenderArchiveRejectsInvalidInputs(t *testing.T) {
	t.Parallel()

	analysis := archiveRenderAnalysis(t)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, _, err := RenderArchive(ctx, analysis, "", t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderArchive(canceled) error = %v", err)
	}
	if _, _, err := RenderArchive(
		context.Background(), imagearchive.Analysis{}, "", t.TempDir(),
	); !errors.Is(err, imagearchive.ErrInvalidArchive) {
		t.Fatalf("RenderArchive(invalid) error = %v", err)
	}
	invalidAnalysis := analysis
	invalidAnalysis.Identity.Volumes = []string{"relative"}
	if _, _, err := RenderArchive(
		context.Background(), invalidAnalysis, "", t.TempDir(),
	); err == nil {
		t.Fatal("RenderArchive(invalid image configuration) succeeded")
	}
	nonrepresentableAnalysis := analysis
	nonrepresentableAnalysis.Identity.Environment = []string{"INHERIT_FROM_HOST"}
	if _, _, err := RenderArchive(
		context.Background(), nonrepresentableAnalysis, "", t.TempDir(),
	); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("RenderArchive(nonrepresentable image configuration) error = %v", err)
	}
	invalidHealthcheck := analysis
	invalidHealthcheck.Identity.Healthcheck = &domain.Healthcheck{Test: []string{"INVALID"}}
	if _, _, err := RenderArchive(
		context.Background(), invalidHealthcheck, "", t.TempDir(),
	); err == nil {
		t.Fatal("RenderArchive(invalid healthcheck) succeeded")
	}
	if _, _, err := RenderArchive(
		context.Background(), analysis, "", string([]byte{0}),
	); err == nil {
		t.Fatal("RenderArchive(invalid working directory) succeeded")
	}
}

func TestValidateRenderedArchiveRejectsInvalidRoundTrips(t *testing.T) {
	t.Parallel()

	workingDirectory := t.TempDir()
	analysis := archiveRenderAnalysis(t)
	rendered, name, err := RenderArchive(context.Background(), analysis, "", workingDirectory)
	if err != nil {
		t.Fatalf("RenderArchive() error = %v", err)
	}
	want := domain.ResolveWorkloadSpec(domain.WorkloadSpec{
		ServiceName: name, ContainerName: name, Platform: analysis.Identity.Platform,
		NetworkMode: composeBridgeNetwork,
	}, analysis.Identity)

	tests := []struct {
		name     string
		content  []byte
		service  string
		expected domain.WorkloadSpec
	}{
		{name: "invalid document", content: []byte("{"), service: name, expected: want},
		{name: "missing service", content: rendered, service: "missing", expected: want},
		{
			name: "registry source", content: []byte(`
name: registry
services:
  registry:
    image: example.com/team/image:1
    container_name: registry
    network_mode: bridge
`),
			service: "registry", expected: want,
		},
		{
			name: "workload mismatch", content: rendered, service: name,
			expected: func() domain.WorkloadSpec {
				changed := want.Clone()
				changed.Hostname = "different"

				return changed
			}(),
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if err := validateRenderedArchive(
				context.Background(), test.content, workingDirectory, test.service, test.expected,
			); !errors.Is(err, ErrInvalidSource) {
				t.Fatalf("validateRenderedArchive() error = %v", err)
			}
		})
	}
}

func archiveRenderAnalysis(t *testing.T) imagearchive.Analysis {
	t.Helper()

	archivePath := filepath.Join(t.TempDir(), "private-image.tar")
	if err := os.WriteFile(archivePath, []byte("fixture"), 0o600); err != nil {
		t.Fatal(err)
	}
	source, err := imagearchive.ParseSource("docker-archive:" + archivePath + "@0")
	if err != nil {
		t.Fatalf("ParseSource() error = %v", err)
	}
	archiveDigest := domain.Hash([]byte("archive bytes"))
	manifestDigest := domain.Hash([]byte("archive manifest"))
	platformManifest := domain.Hash([]byte("platform manifest"))
	imageConfig := domain.Hash([]byte("image config"))
	retries := 3
	reference := "localhost/maniud/archive:source-" + strings.TrimPrefix(manifestDigest.String(), "sha256:")

	return imagearchive.Analysis{
		Source: source, ArchiveDigest: archiveDigest, ArchiveSize: 10240,
		ManifestDigest: manifestDigest, MemberIndex: 0, ComposeReference: reference,
		Identity: domain.ImageIdentity{
			Origin: domain.ImageOriginDockerArchive, Reference: reference, ReferenceDigest: manifestDigest,
			Platform:         domain.Platform{OS: archiveLinuxOS, Architecture: archiveAMD64},
			PlatformManifest: platformManifest, ImageConfig: imageConfig,
			User: "1000", Environment: []string{"A=1"}, Entrypoint: []string{"/init"},
			Command:      []string{"serve"},
			ExposedPorts: []domain.ExposedPort{{TargetPort: 8080, Protocol: composeProtocolTCP}},
			Volumes:      []string{"/data"}, WorkingDirectory: "/work", Labels: []string{"team=platform"},
			StopSignal: "SIGTERM", Healthcheck: &domain.Healthcheck{
				Test: []string{contractHealth, contractTrue}, Interval: "30s", Retries: &retries,
			},
		},
	}
}

func testRuntimeRenderBoundaries(
	t *testing.T,
	projection runtimeargv.Projection,
	image domain.ImageIdentity,
	digest domain.Digest,
) {
	t.Helper()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := RenderRuntime(ctx, projection, image, t.TempDir()); !errors.Is(err, context.Canceled) {
		t.Fatalf("RenderRuntime(canceled) error = %v", err)
	}
	invalidImage := image
	invalidImage.Reference = "example.com/team/other:1@" + digest.String()
	_, err := RenderRuntime(context.Background(), projection, invalidImage, t.TempDir())
	if !errors.Is(err, runtimeargv.ErrInvalid) {
		t.Fatalf("RenderRuntime(mismatched image) error = %v", err)
	}
	rendered, err := finishRuntimeRender([]byte(testInvalidArchive), errRuntimeRenderTest)
	if err == nil || rendered != nil {
		t.Fatalf("finishRuntimeRender() = %q, %v", rendered, err)
	}
	rendered, err = finishRuntimeRender(make([]byte, maxSourceBytes+1), nil)
	if !errors.Is(err, ErrInvalidSource) || rendered != nil {
		t.Fatalf("finishRuntimeRender(oversized) = %q, %v", rendered, err)
	}
}
