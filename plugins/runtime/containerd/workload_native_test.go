package containerd

import (
	"context"
	"errors"
	"reflect"
	"slices"
	"testing"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	api "github.com/containerd/containerd/api/types"
	tasktypes "github.com/containerd/containerd/api/types/task"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestNativeRuntimeInspectionRequiresSnapshotterAndCNI(t *testing.T) {
	t.Parallel()

	network := &fakeWorkloadNetwork{digest: domain.Hash([]byte("network"))}
	plugins := []*introspectionapi.Plugin{
		nil,
		{Type: containerdSnapshotterPluginType, ID: defaultContainerdSnapshotter},
		{
			Type: containerdRestartPluginType, ID: containerdRestartPluginID,
			Capabilities: []string{testRestartPolicy, "unless-stopped", "on-failure"},
		},
	}
	backend := &nativeWorkloadBackendV1{
		plugins: fakePluginsAPI{plugins: func(*introspectionapi.PluginsRequest) (*introspectionapi.PluginsResponse, error) {
			return &introspectionapi.PluginsResponse{Plugins: plugins}, nil
		}},
		options: DefaultWorkloadOptions(), network: network,
		platform: domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureAMD64},
	}
	info, err := backend.Inspect(context.Background())
	if err != nil || !info.Restart || info.NetworkDigest != network.digest ||
		info.Runtime != defaultContainerdRuntime || info.Snapshotter != defaultContainerdSnapshotter {
		t.Fatalf("Inspect() = %#v, %v", info, err)
	}

	backend.options.Snapshotter = testOtherValue
	if _, err = backend.Inspect(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Inspect(missing snapshotter) = %v", err)
	}
	backend.options.Snapshotter = defaultContainerdSnapshotter
	network.inspectErr = errContainerdTest
	if _, err = backend.Inspect(context.Background()); !errors.Is(err, errContainerdTest) {
		t.Fatalf("Inspect(CNI failure) = %v", err)
	}
	if _, err = (&nativeWorkloadBackendV1{}).Inspect(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Inspect(empty) = %v", err)
	}
}

//nolint:cyclop // One test verifies candidate identity, uniqueness, and unmanaged projection.
func TestNativeCandidatesRecognizeContainerdIDsAndManagedLabels(t *testing.T) {
	t.Parallel()

	unmanaged := &containersapi.Container{
		ID: testWorkloadName, Image: "example.com/unmanaged:latest",
		Spec:       &anypb.Any{TypeUrl: containerRuntimeSpecTypeURL, Value: []byte(`{"ociVersion":"1.1.0"}`)},
		Extensions: map[string]*anypb.Any{"example.test/extension": {TypeUrl: "example.test/extension"}},
	}
	tasks := fakeTasksAPI{get: func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}}
	containers := make([]*containersapi.Container, 0, 3)
	containers = append(containers, unmanaged, &containersapi.Container{ID: "ignored", Image: "ignored"})
	backend := &nativeWorkloadBackendV1{
		containers: fakeContainersAPI{list: func(
			*containersapi.ListContainersRequest,
		) (*containersapi.ListContainersResponse, error) {
			return &containersapi.ListContainersResponse{Containers: containers}, nil
		}},
		tasks: tasks, options: DefaultWorkloadOptions(),
	}
	candidates, err := backend.Candidates(context.Background(), testWorkloadName, "", "")
	if err != nil || candidates.Named == nil || candidates.Named.Name != testWorkloadName ||
		candidates.Named.Ownership.Status != domain.OwnershipUnmanaged || candidates.Owned != nil {
		t.Fatalf("Candidates(unmanaged) = %#v, %v", candidates, err)
	}
	available, err := backend.NameAvailable(context.Background(), testWorkloadName, "")
	if err != nil || available {
		t.Fatalf("NameAvailable(occupied) = %v, %v", available, err)
	}
	available, err = backend.NameAvailable(context.Background(), testWorkloadName, testWorkloadName)
	if err != nil || !available {
		t.Fatalf("NameAvailable(excepted) = %v, %v", available, err)
	}

	containers = append(containers, unmanaged)
	if _, err = backend.Candidates(context.Background(), testWorkloadName, "", ""); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Candidates(duplicate) = %v", err)
	}
	containers = []*containersapi.Container{nil}
	if _, err = backend.Candidates(context.Background(), testWorkloadName, "", ""); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Candidates(nil) = %v", err)
	}
	if _, err = backend.NameAvailable(context.Background(), testWorkloadName, ""); !errors.Is(err, ErrProtocol) {
		t.Fatalf("NameAvailable(nil) = %v", err)
	}
}

func TestNativeWorkloadReadAndTaskLifecycle(t *testing.T) {
	t.Parallel()

	identifier := testWorkloadName
	container := &containersapi.Container{ID: identifier, Image: "example.com/unmanaged:latest"}
	containerResponse := &containersapi.GetContainerResponse{Container: container}
	taskResponse := &tasksapi.GetResponse{Process: &tasktypes.Process{
		ContainerID: identifier, ID: identifier, Status: tasktypes.Status_RUNNING,
	}}
	backend := &nativeWorkloadBackendV1{
		containers: fakeContainersAPI{
			get: func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
				return containerResponse, nil
			},
			list: func(*containersapi.ListContainersRequest) (*containersapi.ListContainersResponse, error) {
				return &containersapi.ListContainersResponse{Containers: []*containersapi.Container{container}}, nil
			},
		},
		tasks: fakeTasksAPI{get: func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			return taskResponse, nil
		}},
		options: DefaultWorkloadOptions(),
	}
	workload, err := backend.Workload(context.Background(), identifier)
	if err != nil || workload == nil || workload.Lifecycle != application.WorkloadLifecycleRunning ||
		workload.ConfigurationMatches || workload.Name != identifier {
		t.Fatalf("Workload() = %#v, %v", workload, err)
	}

	taskResponse.Process.ID = testOtherValue
	if _, err = backend.Workload(context.Background(), identifier); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Workload(task mismatch) = %v", err)
	}
	taskResponse.Process.ID = identifier
	containerResponse.Container = &containersapi.Container{ID: testOtherValue, Image: testImageValue}
	if _, err = backend.Workload(context.Background(), identifier); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Workload(response mismatch) = %v", err)
	}
	backend.containers = fakeContainersAPI{get: func(
		*containersapi.GetContainerRequest,
	) (*containersapi.GetContainerResponse, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}}
	workload, err = backend.Workload(context.Background(), identifier)
	if err != nil || workload != nil {
		t.Fatalf("Workload(missing) = %#v, %v", workload, err)
	}
}

func TestNativeTaskLifecycleContainerIdentity(t *testing.T) {
	t.Parallel()

	process := &tasktypes.Process{ID: testWorkloadName, Status: tasktypes.Status_RUNNING}
	backend := &nativeWorkloadBackendV1{tasks: fakeTasksAPI{get: func(
		*tasksapi.GetRequest,
	) (*tasksapi.GetResponse, error) {
		return &tasksapi.GetResponse{Process: process}, nil
	}}}
	for _, containerID := range []string{"", testWorkloadName} {
		process.ContainerID = containerID
		lifecycle, found, err := backend.taskLifecycle(context.Background(), testWorkloadName)
		if err != nil || !found || lifecycle != application.WorkloadLifecycleRunning {
			t.Fatalf("taskLifecycle(container ID %q) = %v, %t, %v", containerID, lifecycle, found, err)
		}
	}
	process.ContainerID = testOtherValue
	if _, _, err := backend.taskLifecycle(
		context.Background(), testWorkloadName,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("taskLifecycle(conflicting container ID) = %v", err)
	}
}

//nolint:cyclop // The test mutates independent managed-container evidence fields.
func TestNativeManagedContainerEvidence(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	available, err := fixture.backend.NameAvailable(context.Background(), testOtherValue, "")
	if err != nil || !available {
		t.Fatalf("NameAvailable(managed other name) = %v, %v", available, err)
	}
	workload, err := fixture.backend.Workload(context.Background(), fixture.container.GetID())
	if err != nil || workload == nil || !workload.ConfigurationMatches ||
		workload.Ownership.Status != domain.OwnershipManaged ||
		workload.Lifecycle != application.WorkloadLifecycleCreated {
		t.Fatalf("Workload(managed) = %#v, %v", workload, err)
	}
	fixture.task.Process = &tasktypes.Process{
		ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_RUNNING,
	}
	workload, err = fixture.backend.Workload(context.Background(), fixture.container.GetID())
	if err != nil || workload == nil || workload.Lifecycle != application.WorkloadLifecycleRunning ||
		fixture.network.checkCalls != 1 {
		t.Fatalf("Workload(running) = %#v, %v", workload, err)
	}
	fixture.network.checkErr = errContainerdTest
	workload, err = fixture.backend.Workload(context.Background(), fixture.container.GetID())
	if err != nil || workload == nil || workload.ConfigurationMatches {
		t.Fatalf("Workload(CNI drift) = %#v, %v", workload, err)
	}

	fixture.host.mounted = false
	if _, err = fixture.backend.Workload(context.Background(), fixture.container.GetID()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Workload(netns drift) = %v", err)
	}
}

//nolint:cyclop // The matrix verifies snapshot, image, and mount wire evidence.
func TestNativeSnapshotImageAndMountEvidence(t *testing.T) {
	t.Parallel()

	parent := domain.Hash([]byte("parent")).String()
	identifier := testWorkloadName
	imageReference := "example.com/team/api@" + parent
	backend := &nativeWorkloadBackendV1{
		snapshots: fakeSnapshotsAPI{stat: func(
			request *snapshotsapi.StatSnapshotRequest,
		) (*snapshotsapi.StatSnapshotResponse, error) {
			return &snapshotsapi.StatSnapshotResponse{Info: &snapshotsapi.Info{
				Name: request.GetKey(), Parent: parent, Kind: snapshotsapi.Kind_ACTIVE,
			}}, nil
		}},
		images: fakeImagesClient{response: &imagesapi.GetImageResponse{Image: &imagesapi.Image{
			Name: imageReference, Target: &api.Descriptor{Digest: parent},
		}}},
		options: DefaultWorkloadOptions(),
	}
	if err := backend.requireSnapshot(context.Background(), identifier, parent); err != nil {
		t.Fatalf("requireSnapshot() = %v", err)
	}
	if err := backend.requireImage(context.Background(), imageReference, parent); err != nil {
		t.Fatalf("requireImage() = %v", err)
	}
	backend.images = fakeImagesClient{response: &imagesapi.GetImageResponse{Image: &imagesapi.Image{
		Name: imageReference, Target: &api.Descriptor{Digest: domain.Hash([]byte("other image")).String()},
	}}}
	if err := backend.requireImage(
		context.Background(), imageReference, parent,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("requireImage(digest mismatch) = %v", err)
	}

	values := []*api.Mount{{Type: bindMountType, Source: testSourcePath, Options: []string{"ro"}}}
	converted, err := apiMounts(values)
	if err != nil || converted[0].Type != bindMountType || converted[0].Source != testSourcePath {
		t.Fatalf("apiMounts() = %#v, %v", converted, err)
	}
	roundTrip, err := protoMounts(values)
	if err != nil || !reflect.DeepEqual(roundTrip, values) || &roundTrip[0].Options[0] == &values[0].Options[0] {
		t.Fatalf("protoMounts() = %#v, %v", roundTrip, err)
	}
	for _, invalid := range [][]*api.Mount{nil, {nil}, {{Source: testSourcePath}}, {{Type: bindMountType}}} {
		_, err = apiMounts(invalid)
		if len(invalid) != 0 && !errors.Is(err, ErrProtocol) {
			t.Fatalf("apiMounts(%#v) = %v", invalid, err)
		}
	}
}

func TestNativeRemovalCompletionRequiresAllResidueAbsent(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	complete, err := fixture.backend.RemovalComplete(context.Background(), fixture.container.GetID())
	if err != nil || complete {
		t.Fatalf("RemovalComplete(snapshot present) = %v, %v", complete, err)
	}
	fixture.snapshots.stat = func(*snapshotsapi.StatSnapshotRequest) (*snapshotsapi.StatSnapshotResponse, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}
	fixture.network.absent = true
	fixture.host.absent = true
	complete, err = fixture.backend.RemovalComplete(context.Background(), fixture.container.GetID())
	if err != nil || !complete || fixture.network.absentCalls != 1 || fixture.host.absentCalls != 1 {
		t.Fatalf("RemovalComplete() = %v, %v", complete, err)
	}
	fixture.network.absent = false
	complete, err = fixture.backend.RemovalComplete(context.Background(), fixture.container.GetID())
	if err != nil || complete {
		t.Fatalf("RemovalComplete(CNI residue) = %v, %v", complete, err)
	}
}

type nativeManagedFixture struct {
	backend    *nativeWorkloadBackendV1
	container  *containersapi.Container
	containers *fakeContainersAPI
	task       *tasksapi.GetResponse
	tasks      *fakeTasksAPI
	snapshots  *fakeSnapshotsAPI
	network    *fakeWorkloadNetwork
	host       *fakeWorkloadHost
}

func testNativeManagedBackend(t *testing.T) nativeManagedFixture {
	t.Helper()
	desired := testContainerdDesiredWorkload(t)

	return testNativeManagedBackendFor(t, desired, DefaultWorkloadOptions(), nil)
}

//nolint:funlen // The fixture constructs one complete protobuf evidence graph.
func testNativeManagedBackendFor(
	t *testing.T,
	desired domain.DesiredWorkload,
	options WorkloadOptions,
	runtimeMounts []domain.RuntimeMount,
) nativeManagedFixture {
	t.Helper()
	configuration := testContainerdConfiguration(t, desired)
	runtimeSpec, runtimeDigest, err := encodeRuntimeSpec(configuration)
	if err != nil {
		t.Fatal(err)
	}
	identifier := workloadIdentifier(desired.ContainerName, testWorkloadTransaction)
	parent := domain.Hash([]byte("parent")).String()
	network := &fakeWorkloadNetwork{digest: domain.Hash([]byte("network"))}
	host := &fakeWorkloadHost{mounted: true}
	extension, err := encodeWorkloadExtension(workloadExtensionV1{
		Version: containerExtensionVersion, Configuration: configuration,
		ImageReference: desired.Image.Reference, ImageConfig: desired.Image.ImageConfig.String(),
		PlatformManifest: desired.Image.PlatformManifest.String(), RuntimeSpecDigest: runtimeDigest.String(),
		RuntimeMounts: runtimeMounts, SnapshotParent: parent, NetworkDigest: network.digest.String(),
	})
	if err != nil {
		t.Fatal(err)
	}
	container := &containersapi.Container{
		ID: identifier, Labels: workloadLabels(desired, testWorkloadTransaction), Image: desired.Image.Reference,
		Runtime: &containersapi.Container_Runtime{Name: defaultContainerdRuntime}, Spec: runtimeSpec,
		Snapshotter: defaultContainerdSnapshotter, SnapshotKey: workloadSnapshotKey(identifier),
		Extensions: map[string]*anypb.Any{containerConfigurationExtension: extension},
	}
	task := &tasksapi.GetResponse{}
	containers := &fakeContainersAPI{
		get: func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
			return &containersapi.GetContainerResponse{Container: container}, nil
		},
		list: func(*containersapi.ListContainersRequest) (*containersapi.ListContainersResponse, error) {
			return &containersapi.ListContainersResponse{Containers: []*containersapi.Container{container}}, nil
		},
	}
	tasks := &fakeTasksAPI{get: func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
		if task.GetProcess() == nil {
			return nil, status.Error(codes.NotFound, "missing")
		}

		return task, nil
	}}
	snapshots := &fakeSnapshotsAPI{stat: func(
		request *snapshotsapi.StatSnapshotRequest,
	) (*snapshotsapi.StatSnapshotResponse, error) {
		return &snapshotsapi.StatSnapshotResponse{Info: &snapshotsapi.Info{
			Name: request.GetKey(), Parent: parent, Kind: snapshotsapi.Kind_ACTIVE,
		}}, nil
	}}
	backend := &nativeWorkloadBackendV1{
		containers: containers,
		tasks:      tasks,
		snapshots:  snapshots,
		images: fakeImagesClient{response: &imagesapi.GetImageResponse{Image: &imagesapi.Image{
			Name:   desired.Image.Reference,
			Target: &api.Descriptor{Digest: desired.Image.ReferenceDigest.String()},
		}}},
		options: options, network: network, host: host,
	}

	return nativeManagedFixture{
		backend: backend, container: container, containers: containers,
		task: task, tasks: tasks, snapshots: snapshots, network: network, host: host,
	}
}

func mutateNativeWorkloadExtension(
	t *testing.T,
	fixture nativeManagedFixture,
	mutate func(*workloadExtensionV1),
) {
	t.Helper()

	extension, err := decodeWorkloadExtension(
		fixture.container.GetExtensions()[containerConfigurationExtension],
	)
	if err != nil {
		t.Fatal(err)
	}
	mutate(&extension)
	encoded, err := encodeWorkloadExtension(extension)
	if err != nil {
		t.Fatal(err)
	}
	fixture.container.Extensions[containerConfigurationExtension] = encoded
}

//nolint:funlen // The table mutates each independent managed-container identity boundary.
func TestNativeManagedContainerFailureMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, nativeManagedFixture)
	}{
		{name: "identifier", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.ID = testBadIdentifier
		}},
		{name: testImageValue, mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.Image = ""
		}},
		{name: "sandbox", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.Sandbox = testOtherValue
		}},
		{name: "managed extension missing", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			delete(fixture.container.Extensions, containerConfigurationExtension)
		}},
		{name: "managed shape", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.Runtime = nil
		}},
		{name: "extension", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.Extensions[containerConfigurationExtension] = &anypb.Any{
				TypeUrl: containerConfigurationTypeURL, Value: []byte("{}"),
			}
		}},
		{name: "runtime spec", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.Spec = &anypb.Any{TypeUrl: testOtherValue, Value: []byte("{}")}
		}},
		{name: "image reference", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.Image = testOtherValue
		}},
		{name: "invalid extension image reference", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.ImageReference = testBadValue
				fixture.container.Image = extension.ImageReference
			})
		}},
		{name: "unpinned extension image reference", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.ImageReference = "example.com/team/api:latest"
				fixture.container.Image = extension.ImageReference
			})
		}},
		{name: "conflicting extension image digest", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.ImageReference = "example.com/team/api:latest@" + domain.Hash([]byte(testOtherValue)).String()
				fixture.container.Image = extension.ImageReference
			})
		}},
		{name: "image digest", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.ImageConfig = testBadValue
			})
		}},
		{name: "manifest digest", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.PlatformManifest = testBadValue
			})
		}},
		{name: "snapshot parent", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.SnapshotParent = testBadValue
			})
		}},
		{name: "snapshot", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.snapshots.stat = func(
				*snapshotsapi.StatSnapshotRequest,
			) (*snapshotsapi.StatSnapshotResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "ownership", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.container.Labels[domain.LabelImageConfigDigest] = domain.Hash([]byte(testOtherValue)).String()
		}},
		{name: "preloaded image", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.backend.images = fakeImagesClient{err: errContainerdTest}
		}},
		{name: "configuration", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.Configuration = containerdconfig.Configuration{}
			})
		}},
		{name: "runtime mounts", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.RuntimeMounts = []domain.RuntimeMount{{Kind: domain.MountVolume, Source: testWrongPath}}
			})
		}},
		{name: testHostUnavailableCase, mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.backend.host = nil
		}},
		{name: "CNI digest", mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.network.digest = domain.Hash([]byte(testOtherValue))
		}},
		{name: "invalid recorded CNI digest", mutate: func(t *testing.T, fixture nativeManagedFixture) {
			t.Helper()

			mutateNativeWorkloadExtension(t, fixture, func(extension *workloadExtensionV1) {
				extension.NetworkDigest = testBadValue
			})
		}},
		{name: testTaskLifecycleCase, mutate: func(_ *testing.T, fixture nativeManagedFixture) {
			fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
				return nil, errContainerdTest
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := testNativeManagedBackend(t)
			test.mutate(t, fixture)
			if _, err := fixture.backend.decodeContainer(
				context.Background(), fixture.container,
			); err == nil {
				t.Fatal("decodeContainer() succeeded")
			}
		})
	}
}

func TestNativeUnmanagedContainerRejectsManagedEvidenceAndTaskFailure(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	delete(fixture.container.Extensions, containerConfigurationExtension)
	fixture.container.Labels = nil
	fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
		return nil, errContainerdTest
	}
	if _, err := fixture.backend.decodeContainer(context.Background(), fixture.container); err == nil {
		t.Fatal("decodeContainer(unmanaged task failure) succeeded")
	}
}

func TestNativeBackendReadFailureAndResidueMatrix(t *testing.T) {
	t.Parallel()

	if _, err := (*nativeWorkloadBackendV1)(nil).listContainers(
		context.Background(),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("listContainers(nil) = %v", err)
	}
	backend := &nativeWorkloadBackendV1{containers: fakeContainersAPI{list: func(
		*containersapi.ListContainersRequest,
	) (*containersapi.ListContainersResponse, error) {
		return &containersapi.ListContainersResponse{
			Containers: slices.Repeat([]*containersapi.Container{nil}, maximumListedContainers+1),
		}, nil
	}}}
	if _, err := backend.listContainers(context.Background()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("listContainers(oversized) = %v", err)
	}
	fixture := testNativeManagedBackend(t)
	fixture.backend.snapshots = fakeSnapshotsAPI{stat: func(
		*snapshotsapi.StatSnapshotRequest,
	) (*snapshotsapi.StatSnapshotResponse, error) {
		return nil, errContainerdTest
	}}
	if _, err := fixture.backend.RemovalComplete(
		context.Background(), fixture.container.GetID(),
	); err == nil {
		t.Fatal("RemovalComplete(snapshot error) succeeded")
	}
	fixture.backend.snapshots = fakeSnapshotsAPI{stat: func(
		*snapshotsapi.StatSnapshotRequest,
	) (*snapshotsapi.StatSnapshotResponse, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}}
	fixture.network.absentErr = errContainerdTest
	if _, err := fixture.backend.RemovalComplete(
		context.Background(), fixture.container.GetID(),
	); err == nil {
		t.Fatal("RemovalComplete(CNI error) succeeded")
	}
	fixture.network.absentErr = nil
	fixture.network.absent = true
	fixture.host.absentErr = errContainerdTest
	if _, err := fixture.backend.RemovalComplete(
		context.Background(), fixture.container.GetID(),
	); err == nil {
		t.Fatal("RemovalComplete(host error) succeeded")
	}
	if _, err := (&nativeWorkloadBackendV1{}).RemovalComplete(
		context.Background(), testWorkloadName,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("RemovalComplete(unavailable) = %v", err)
	}
}

func TestNativeSnapshotImageAndTaskProtocolFailures(t *testing.T) {
	t.Parallel()

	backend := &nativeWorkloadBackendV1{
		snapshots: fakeSnapshotsAPI{stat: func(
			*snapshotsapi.StatSnapshotRequest,
		) (*snapshotsapi.StatSnapshotResponse, error) {
			return &snapshotsapi.StatSnapshotResponse{}, nil
		}},
		images: fakeImagesClient{response: &imagesapi.GetImageResponse{}},
		tasks: fakeTasksAPI{get: func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			return &tasksapi.GetResponse{}, nil
		}},
		options: DefaultWorkloadOptions(),
	}
	if err := backend.requireSnapshot(
		context.Background(), testWorkloadName, testOtherValue,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("requireSnapshot(protocol) = %v", err)
	}
	if err := backend.requireImage(
		context.Background(), testImageValue, testOtherValue,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("requireImage(protocol) = %v", err)
	}
	if _, _, err := backend.taskLifecycle(
		context.Background(), testWorkloadName,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("taskLifecycle(protocol) = %v", err)
	}
}

func TestNativeCandidateSelectionFailureMatrix(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	fixture.containers.list = func(
		*containersapi.ListContainersRequest,
	) (*containersapi.ListContainersResponse, error) {
		return &containersapi.ListContainersResponse{Containers: []*containersapi.Container{
			fixture.container, fixture.container,
		}}, nil
	}
	if _, err := fixture.backend.Candidates(
		context.Background(), fixture.container.GetLabels()[containerNameLabel], "", "",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Candidates(duplicate name) = %v", err)
	}
	if _, err := fixture.backend.Candidates(
		context.Background(), testOtherValue, testWorkloadService, testWorkloadTransaction,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Candidates(duplicate ownership) = %v", err)
	}
	fixture.containers.list = func(
		*containersapi.ListContainersRequest,
	) (*containersapi.ListContainersResponse, error) {
		return nil, errContainerdTest
	}
	if _, err := fixture.backend.NameAvailable(
		context.Background(), testWorkloadName, "",
	); err == nil {
		t.Fatal("NameAvailable(list failure) succeeded")
	}
	fixture.containers.list = func(
		*containersapi.ListContainersRequest,
	) (*containersapi.ListContainersResponse, error) {
		return &containersapi.ListContainersResponse{Containers: []*containersapi.Container{{
			ID: "bad/name", Image: testImageValue,
		}}}, nil
	}
	if _, err := fixture.backend.Candidates(
		context.Background(), "bad/name", "", "",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Candidates(decode failure) = %v", err)
	}
}

func TestContainerdPlatform(t *testing.T) {
	t.Parallel()

	tests := []struct {
		goos     string
		goarch   string
		expected domain.Platform
	}{
		{goos: "darwin", goarch: containerdArchitectureAMD64},
		{
			goos: containerdPlatformOS, goarch: containerdArchitectureAMD64,
			expected: domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureAMD64},
		},
		{
			goos: containerdPlatformOS, goarch: containerdArchitectureARM64,
			expected: domain.Platform{
				OS: containerdPlatformOS, Architecture: containerdArchitectureARM64, Variant: "v8",
			},
		},
		{goos: containerdPlatformOS, goarch: "riscv64"},
	}
	for _, test := range tests {
		if actual := containerdPlatform(test.goos, test.goarch); actual != test.expected {
			t.Fatalf("containerdPlatform(%q, %q) = %#v", test.goos, test.goarch, actual)
		}
	}
}
