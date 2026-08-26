package podman

import (
	"context"
	"os"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	livePodmanSocketEnvironment  = "MANIUD_TEST_PODMAN_SOCKET"
	livePodmanVersionEnvironment = "MANIUD_TEST_PODMAN_VERSION"
	livePodmanTransaction        = "7bc82cd0-3593-4f2d-aecd-4f9de9f32864"
	livePodmanReferenceDigest    = "sha256:ee6521f290b2168b6e0935a181d4cff9be1ac3f505666ef0e3c98fae8199917a"
	livePodmanImageReference     = "registry.k8s.io/pause@" + livePodmanReferenceDigest
	livePodmanManifestDigest     = "sha256:7c38f24774e3cbd906d2d33c38354ccf787635581c122965132c9bd309754d4a"
	livePodmanImageConfig        = "sha256:873ed75102791e5b0b8a7fcd41606c92fcec98d56d05ead4ac5131650004c136"
)

func TestLivePodmanCompatibility(t *testing.T) {
	t.Parallel()

	socketPath := os.Getenv(livePodmanSocketEnvironment)
	if socketPath == "" {
		t.Skip(livePodmanSocketEnvironment + " is not set")
	}
	expectedVersion := os.Getenv(livePodmanVersionEnvironment)
	if expectedVersion == "" {
		t.Fatal(livePodmanVersionEnvironment + " is not set")
	}

	ctx, cancel := context.WithTimeout(t.Context(), 8*time.Minute)
	defer cancel()
	client := connectLivePodman(ctx, t, socketPath, expectedVersion)
	image := pullLivePodmanImage(ctx, t, client)
	exerciseLivePodmanWorkload(ctx, t, client, image)
}

func connectLivePodman(ctx context.Context, t *testing.T, socketPath, expectedVersion string) *Client {
	t.Helper()
	client, version, err := Connect(ctx, socketPath)
	if err != nil {
		t.Fatalf("Connect() error = %v", err)
	}
	t.Cleanup(client.CloseIdleConnections)
	if version.Product != expectedVersion || version.Protocol != expectedVersion {
		t.Fatalf("Connect() version = %#v, want product and protocol %q", version, expectedVersion)
	}
	evidence, err := client.Inspect(ctx)
	if err != nil {
		t.Fatalf("Inspect() error = %v", err)
	}
	if evidence.Kind != domain.RuntimePodman || evidence.Digest == (domain.Digest{}) {
		t.Fatalf("Inspect() evidence = %#v, want Podman with a nonzero digest", evidence)
	}

	return client
}

func pullLivePodmanImage(ctx context.Context, t *testing.T, client *Client) domain.ImageIdentity {
	t.Helper()
	image := livePodmanImage(t)
	if err := client.PullImage(ctx, image, emptyCredentialProvider{}); err != nil {
		t.Fatalf("PullImage() error = %v", err)
	}
	imageProbe, err := client.ProbeImage(ctx, image)
	if err != nil {
		t.Fatalf("ProbeImage() error = %v", err)
	}
	if !imageProbe.Matches(image) {
		t.Fatalf("ProbeImage() = %#v, want %#v", imageProbe, image)
	}

	return image
}

func exerciseLivePodmanWorkload(
	ctx context.Context,
	t *testing.T,
	client *Client,
	image domain.ImageIdentity,
) {
	t.Helper()
	workload := livePodmanWorkload(t, image)
	livePodmanCleanup(ctx, t, client, workload)
	identifier := createLivePodmanWorkload(ctx, t, client, workload)
	startLivePodmanWorkload(ctx, t, client, workload, identifier)
	discardLivePodmanWorkload(ctx, t, client, workload)
}

func createLivePodmanWorkload(
	ctx context.Context,
	t *testing.T,
	client *Client,
	workload domain.DesiredWorkload,
) string {
	t.Helper()
	observation, err := client.ObserveWorkload(ctx, workload)
	if err != nil {
		t.Fatalf("ObserveWorkload(missing) error = %v", err)
	}
	if observation.State != application.WorkloadObservationMissing {
		t.Fatalf("ObserveWorkload(missing) state = %v, want %v", observation.State, application.WorkloadObservationMissing)
	}
	identifier, err := client.CreateWorkload(
		ctx,
		workload,
		livePodmanTransaction,
		application.WorkloadCreateOptions{CopyImageVolumes: true},
	)
	if err != nil {
		t.Fatalf("CreateWorkload() error = %v", err)
	}

	return identifier
}

func startLivePodmanWorkload(
	ctx context.Context,
	t *testing.T,
	client *Client,
	workload domain.DesiredWorkload,
	identifier string,
) {
	t.Helper()
	created, err := client.ProbeCreatedWorkload(ctx, workload, livePodmanTransaction, identifier)
	if err != nil {
		t.Fatalf("ProbeCreatedWorkload() error = %v", err)
	}
	if created.State != application.WorkloadEffectProbeObserved ||
		created.Workload.Lifecycle != application.WorkloadLifecycleCreated {
		t.Fatalf("ProbeCreatedWorkload() = %#v, want observed created workload", created)
	}
	requireLivePodmanConfiguration(ctx, t, client, workload, identifier, created)
	if !validStartedWorkload(
		created.Workload,
		workload,
		livePodmanTransaction,
		application.WorkloadLifecycleCreated,
	) {
		storageDigest, storageValid := domain.ComputeStorageDigest(workload, created.Workload.RuntimeMounts)
		t.Fatalf(
			"created workload evidence is inconsistent: evidence=%#v storage=%s valid=%t",
			created.Workload,
			storageDigest,
			storageValid,
		)
	}
	if err := client.StartWorkload(ctx, workload, livePodmanTransaction); err != nil {
		t.Fatalf("StartWorkload() error = %v", err)
	}
	started, err := client.ProbeStartedWorkload(ctx, workload, livePodmanTransaction)
	if err != nil {
		t.Fatalf("ProbeStartedWorkload() error = %v", err)
	}
	if started.State != application.WorkloadEffectProbeObserved ||
		started.Workload.Lifecycle != application.WorkloadLifecycleRunning {
		t.Fatalf("ProbeStartedWorkload() = %#v, want observed running workload", started)
	}
}

func requireLivePodmanConfiguration(
	ctx context.Context,
	t *testing.T,
	client *Client,
	workload domain.DesiredWorkload,
	identifier string,
	created application.WorkloadEffectProbe,
) {
	t.Helper()
	if created.Workload.ConfigurationMatches {
		return
	}
	probe, err := client.ProbeContainer(ctx, identifier)
	if err != nil || probe.State != ContainerProbeObserved {
		t.Fatalf("ProbeContainer(configuration mismatch) = %#v, %v", probe, err)
	}
	t.Fatalf(
		"created workload configuration mismatch: want %#v, got %#v",
		workload.WorkloadSpec,
		probe.Container.WorkloadSpec,
	)
}

func discardLivePodmanWorkload(
	ctx context.Context,
	t *testing.T,
	client *Client,
	workload domain.DesiredWorkload,
) {
	t.Helper()
	if err := client.DiscardWorkload(ctx, workload, livePodmanTransaction); err != nil {
		t.Fatalf("DiscardWorkload() error = %v", err)
	}
	discarded, err := client.ProbeDiscardedWorkload(ctx, workload, livePodmanTransaction)
	if err != nil {
		t.Fatalf("ProbeDiscardedWorkload() error = %v", err)
	}
	if discarded.State != application.WorkloadEffectProbeMissing {
		t.Fatalf("ProbeDiscardedWorkload() state = %v, want %v", discarded.State, application.WorkloadEffectProbeMissing)
	}
}

func livePodmanImage(t *testing.T) domain.ImageIdentity {
	t.Helper()

	return domain.ImageIdentity{
		Origin:           domain.ImageOriginRegistry,
		Reference:        livePodmanImageReference,
		ReferenceDigest:  mustPodmanDigest(t, livePodmanReferenceDigest),
		Platform:         domain.Platform{OS: podmanOSLinux, Architecture: podmanArchAMD64},
		PlatformManifest: mustPodmanDigest(t, livePodmanManifestDigest),
		ImageConfig:      mustPodmanDigest(t, livePodmanImageConfig),
		User:             "65535:65535",
		Environment:      []string{"PATH=/usr/local/sbin:/usr/local/bin:/usr/sbin:/usr/bin:/sbin:/bin"},
		Entrypoint:       []string{"/pause"},
	}
}

func livePodmanWorkload(t *testing.T, image domain.ImageIdentity) domain.DesiredWorkload {
	t.Helper()
	workload := domain.DesiredWorkload{
		WorkloadSpec: domain.ResolveWorkloadSpec(domain.WorkloadSpec{
			ServiceName: "compatibility", ContainerName: "maniud-podman-compatibility",
			Platform: image.Platform, Entrypoint: []string{"/pause"}, NetworkMode: podmanNetworkBridge,
		}, image),
		Image: image, SourceDigest: domain.Hash([]byte("live Podman compatibility")),
	}
	workload.EffectiveDigest = domain.ComputeEffectiveDigest(workload)

	return workload
}

func livePodmanCleanup(
	ctx context.Context,
	t *testing.T,
	client *Client,
	workload domain.DesiredWorkload,
) {
	t.Helper()
	t.Cleanup(func() {
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), 30*time.Second)
		defer cancel()
		probe, err := client.ProbeDiscardedWorkload(cleanupContext, workload, livePodmanTransaction)
		if err != nil {
			t.Errorf("cleanup ProbeDiscardedWorkload() error = %v", err)

			return
		}
		if probe.State == application.WorkloadEffectProbeObserved {
			if err := client.DiscardWorkload(cleanupContext, workload, livePodmanTransaction); err != nil {
				t.Errorf("cleanup DiscardWorkload() error = %v", err)
			}
		}
	})
}
