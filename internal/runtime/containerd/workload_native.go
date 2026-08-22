package containerd

import (
	"context"
	"runtime"
	"slices"
	"strings"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	leasesapi "github.com/containerd/containerd/api/services/leases/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	api "github.com/containerd/containerd/api/types"
	"github.com/containerd/containerd/v2/core/mount"
	"github.com/opencontainers/go-digest"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

const (
	containerdSnapshotterPluginType = "io.containerd.snapshotter.v1"
	containerdRestartPluginType     = "io.containerd.monitor.container.v1"
	containerdRestartPluginID       = "restart"
	maximumListedContainers         = 1 << 14
)

type nativeWorkloadBackendV1 struct {
	containers containersClient
	tasks      tasksClient
	snapshots  snapshotsClient
	leases     leasesClient
	plugins    pluginClient
	images     imagesClient
	options    WorkloadOptions
	network    workloadNetwork
	host       workloadHost
	platform   domain.Platform
}

func newNativeWorkloadBackend(
	connection grpc.ClientConnInterface,
	_ string,
	options WorkloadOptions,
) *nativeWorkloadBackendV1 {
	return &nativeWorkloadBackendV1{
		containers: containersapi.NewContainersClient(connection),
		tasks:      tasksapi.NewTasksClient(connection),
		snapshots:  snapshotsapi.NewSnapshotsClient(connection),
		leases:     leasesapi.NewLeasesClient(connection),
		plugins:    introspectionapi.NewIntrospectionClient(connection),
		images:     imagesapi.NewImagesClient(connection),
		options:    options,
		network:    newCNINetwork(options),
		host:       localWorkloadHost{},
		platform:   hostContainerdPlatform(),
	}
}

func hostContainerdPlatform() domain.Platform {
	return containerdPlatform(runtime.GOOS, runtime.GOARCH)
}

func containerdPlatform(goos, goarch string) domain.Platform {
	if goos != containerdPlatformOS {
		return domain.Platform{}
	}
	switch goarch {
	case containerdArchitectureAMD64:
		return domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureAMD64}
	case containerdArchitectureARM64:
		return domain.Platform{OS: containerdPlatformOS, Architecture: containerdArchitectureARM64, Variant: "v8"}
	default:
		return domain.Platform{}
	}
}

//nolint:cyclop // Runtime inspection rejects each missing capability independently.
func (backend *nativeWorkloadBackendV1) Inspect(ctx context.Context) (workloadRuntimeInfo, error) {
	if backend == nil || backend.plugins == nil || backend.network == nil ||
		!validContainerdPlatform(backend.platform) {
		return workloadRuntimeInfo{}, ErrUnavailable
	}
	response, err := backend.plugins.Plugins(ctx, &introspectionapi.PluginsRequest{})
	if err != nil {
		return workloadRuntimeInfo{}, classifyRPCError(err)
	}
	snapshotter := false
	restart := false
	for _, plugin := range response.GetPlugins() {
		if plugin == nil || plugin.GetInitErr() != nil {
			continue
		}
		if plugin.GetType() == containerdSnapshotterPluginType && plugin.GetID() == backend.options.Snapshotter {
			snapshotter = true
		}
		if plugin.GetType() == containerdRestartPluginType && plugin.GetID() == containerdRestartPluginID &&
			slices.Contains(plugin.GetCapabilities(), "always") &&
			slices.Contains(plugin.GetCapabilities(), "unless-stopped") &&
			slices.Contains(plugin.GetCapabilities(), "on-failure") {
			restart = true
		}
	}
	if !snapshotter {
		return workloadRuntimeInfo{}, ErrUnavailable
	}
	networkDigest, err := backend.network.Inspect(ctx)
	if err != nil {
		return workloadRuntimeInfo{}, wrapWorkloadBackendError("CNI inspection", err)
	}

	return workloadRuntimeInfo{
		Platform: backend.platform, Runtime: backend.options.Runtime,
		Snapshotter: backend.options.Snapshotter, NetworkDigest: networkDigest, Restart: restart,
	}, nil
}

//nolint:cyclop // Candidate selection proves name and ownership uniqueness in one snapshot.
func (backend *nativeWorkloadBackendV1) Candidates(
	ctx context.Context,
	name string,
	service string,
	transaction string,
) (workloadCandidates, error) {
	containers, err := backend.listContainers(ctx)
	if err != nil {
		return workloadCandidates{}, err
	}
	var result workloadCandidates
	for _, container := range containers {
		if container == nil {
			return workloadCandidates{}, ErrProtocol
		}
		labels := container.GetLabels()
		named := labels[containerNameLabel] == name ||
			labels[containerNameLabel] == "" && container.GetID() == name
		owned := service != "" && transaction != "" && labels[domain.LabelService] == service &&
			labels[domain.LabelTransaction] == transaction
		if !named && !owned {
			continue
		}
		workload, decodeErr := backend.decodeContainer(ctx, container)
		if decodeErr != nil {
			return workloadCandidates{}, decodeErr
		}
		if named {
			if result.Named != nil {
				return workloadCandidates{}, ErrProtocol
			}
			result.Named = workload
		}
		if owned {
			if result.Owned != nil {
				return workloadCandidates{}, ErrProtocol
			}
			result.Owned = workload
		}
	}

	return result, nil
}

func (backend *nativeWorkloadBackendV1) Workload(
	ctx context.Context,
	identifier string,
) (*nativeWorkload, error) {
	response, err := backend.containers.Get(ctx, &containersapi.GetContainerRequest{ID: identifier})
	if status.Code(err) == codes.NotFound {
		// Absence is the successful read result used by effect probes.
		//nolint:nilnil // A nil workload with a nil error is the explicit not-found result.
		return nil, nil
	}
	if err != nil {
		return nil, classifyRPCError(err)
	}
	if response.GetContainer() == nil || response.GetContainer().GetID() != identifier {
		return nil, ErrProtocol
	}

	return backend.decodeContainer(ctx, response.GetContainer())
}

func (backend *nativeWorkloadBackendV1) NameAvailable(
	ctx context.Context,
	name string,
	exceptIdentifier string,
) (bool, error) {
	containers, err := backend.listContainers(ctx)
	if err != nil {
		return false, err
	}
	for _, container := range containers {
		if container == nil {
			return false, ErrProtocol
		}
		logicalName := container.GetLabels()[containerNameLabel]
		if logicalName == "" {
			logicalName = container.GetID()
		}
		if logicalName == name && container.GetID() != exceptIdentifier {
			return false, nil
		}
	}

	return true, nil
}

func (backend *nativeWorkloadBackendV1) RemovalComplete(
	ctx context.Context,
	identifier string,
) (bool, error) {
	if backend == nil || backend.snapshots == nil || backend.network == nil || backend.host == nil {
		return false, ErrUnavailable
	}
	_, err := backend.snapshots.Stat(ctx, &snapshotsapi.StatSnapshotRequest{
		Snapshotter: backend.options.Snapshotter, Key: workloadSnapshotKey(identifier),
	})
	if status.Code(err) != codes.NotFound {
		if err != nil {
			return false, classifyRPCError(err)
		}

		return false, nil
	}
	networkAbsent, err := backend.network.Absent(
		ctx, identifier, workloadNetworkNamespace(backend.options, identifier),
	)
	if err != nil || !networkAbsent {
		return false, wrapWorkloadBackendError("CNI removal proof", err)
	}
	hostAbsent, err := backend.host.Absent(backend.options, identifier)
	if err != nil {
		return false, wrapWorkloadBackendError("host-state removal proof", err)
	}

	return hostAbsent, nil
}

func (backend *nativeWorkloadBackendV1) listContainers(
	ctx context.Context,
) ([]*containersapi.Container, error) {
	if backend == nil || backend.containers == nil {
		return nil, ErrUnavailable
	}
	response, err := backend.containers.List(ctx, &containersapi.ListContainersRequest{})
	if err != nil {
		return nil, classifyRPCError(err)
	}
	containers := response.GetContainers()
	if len(containers) > maximumListedContainers {
		return nil, ErrProtocol
	}

	return containers, nil
}

//nolint:cyclop,funlen,gocyclo // Decoding keeps all independent runtime evidence fail closed.
func (backend *nativeWorkloadBackendV1) decodeContainer(
	ctx context.Context,
	container *containersapi.Container,
) (*nativeWorkload, error) {
	if container == nil || !validContainerdID(container.GetID()) || container.GetImage() == "" ||
		container.GetSandbox() != "" {
		return nil, ErrProtocol
	}
	name := container.GetLabels()[containerNameLabel]
	ownership := parseWorkloadOwnership(container.GetLabels())
	extension, found := container.GetExtensions()[containerConfigurationExtension]
	if !found {
		if ownership.Status != domain.OwnershipUnmanaged {
			return nil, ErrProtocol
		}

		return backend.unmanagedContainer(ctx, container, container.GetID())
	}
	if !backend.validManagedContainer(container, name) {
		return nil, ErrProtocol
	}
	decoded, err := decodeWorkloadExtension(extension)
	if err != nil {
		return nil, err
	}
	runtimeSpec, err := runtimeSpecDigest(container.GetSpec())
	if err != nil || runtimeSpec.String() != decoded.RuntimeSpecDigest ||
		container.GetImage() != decoded.ImageReference {
		return nil, ErrProtocol
	}
	imageConfig, imageErr := domain.ParseDigest(decoded.ImageConfig)
	manifest, manifestErr := domain.ParseDigest(decoded.PlatformManifest)
	parent := digest.Digest(decoded.SnapshotParent)
	if imageErr != nil || manifestErr != nil ||
		decoded.SnapshotParent != "" && parent.Validate() != nil {
		return nil, ErrProtocol
	}
	if err = backend.requireSnapshot(ctx, container.GetSnapshotKey(), parent.String()); err != nil {
		return nil, err
	}
	if ownership.Status != domain.OwnershipManaged || ownership.ImageConfig != imageConfig ||
		ownership.PlatformManifest != manifest {
		return nil, ErrProtocol
	}
	if err = backend.requireImage(ctx, decoded.ImageReference, ownership.Reference.String()); err != nil {
		return nil, err
	}
	sourceSpec, decodeErr := containerdconfig.Decode(decoded.Configuration)
	if decodeErr != nil || !validRuntimeMountEvidence(backend.options, container.GetID(), decoded.RuntimeMounts) ||
		!runtimeMountsMatchConfiguration(
			backend.options, container.GetID(), sourceSpec.Mounts, decoded.RuntimeMounts,
		) || backend.host == nil ||
		!backend.host.NetworkNamespaceMounted(workloadNetworkNamespace(backend.options, container.GetID())) {
		return nil, ErrProtocol
	}
	networkDigest, err := backend.network.Inspect(ctx)
	if err != nil || networkDigest.String() != decoded.NetworkDigest {
		return nil, ErrProtocol
	}
	lifecycle, foundTask, err := backend.taskLifecycle(ctx, container.GetID())
	if err != nil {
		return nil, err
	}
	configurationMatches := foundTask || lifecycle == application.WorkloadLifecycleCreated
	if foundTask && lifecycle == application.WorkloadLifecycleRunning {
		if backend.network.Check(
			ctx, container.GetID(), workloadNetworkNamespace(backend.options, container.GetID()), sourceSpec.Ports,
		) != nil {
			configurationMatches = false
		}
	}
	workload := &nativeWorkload{
		ID: container.GetID(), Name: name, ImageReference: decoded.ImageReference,
		ImageConfig: imageConfig, PlatformManifest: manifest, Configuration: decoded.Configuration,
		Ports:         slices.Clone(sourceSpec.Ports),
		RuntimeMounts: slices.Clone(decoded.RuntimeMounts), ConfigurationMatches: configurationMatches,
		Lifecycle: lifecycle, Ownership: ownership,
	}
	workload.ConfigurationDigest = containerdConfigurationDigest(*workload)

	return workload, nil
}

func (backend *nativeWorkloadBackendV1) validManagedContainer(
	container *containersapi.Container,
	name string,
) bool {
	return len(container.GetExtensions()) == 1 && validContainerdName(name) &&
		container.GetRuntime() != nil && container.GetRuntime().GetName() == backend.options.Runtime &&
		container.GetSnapshotter() == backend.options.Snapshotter &&
		container.GetSnapshotKey() == workloadSnapshotKey(container.GetID())
}

func (backend *nativeWorkloadBackendV1) unmanagedContainer(
	ctx context.Context,
	container *containersapi.Container,
	name string,
) (*nativeWorkload, error) {
	lifecycle, _, err := backend.taskLifecycle(ctx, container.GetID())
	if err != nil {
		return nil, err
	}

	return &nativeWorkload{
		ID: container.GetID(), Name: name, ImageReference: container.GetImage(),
		ImageConfig: domain.Digest{}, PlatformManifest: domain.Digest{},
		Configuration: containerdconfig.Configuration{}, ConfigurationDigest: domain.Hash(container.GetSpec().GetValue()),
		RuntimeMounts: nil, ConfigurationMatches: false, Lifecycle: lifecycle,
		Ownership: parseWorkloadOwnership(container.GetLabels()),
	}, nil
}

func (backend *nativeWorkloadBackendV1) requireSnapshot(
	ctx context.Context,
	key string,
	parent string,
) error {
	response, err := backend.snapshots.Stat(ctx, &snapshotsapi.StatSnapshotRequest{
		Snapshotter: backend.options.Snapshotter, Key: key,
	})
	if err != nil {
		return classifyRPCError(err)
	}
	if response.GetInfo() == nil || response.GetInfo().GetName() != key ||
		response.GetInfo().GetParent() != parent || response.GetInfo().GetKind() != snapshotsapi.Kind_ACTIVE {
		return ErrProtocol
	}

	return nil
}

func (backend *nativeWorkloadBackendV1) requireImage(
	ctx context.Context,
	reference string,
	referenceDigest string,
) error {
	source, err := imageref.Normalize(reference)
	digestValue, digestErr := domain.ParseDigest(referenceDigest)
	pinned, pinErr := source.Pin(digestValue)
	if err != nil || digestErr != nil || pinErr != nil || pinned.String() != reference {
		return ErrProtocol
	}
	image, err := localImageRecord(ctx, backend.images, source)
	if err != nil {
		return err
	}
	if image.GetTarget().GetDigest() != referenceDigest {
		return ErrProtocol
	}

	return nil
}

func (backend *nativeWorkloadBackendV1) taskLifecycle(
	ctx context.Context,
	identifier string,
) (application.WorkloadLifecycle, bool, error) {
	response, err := backend.tasks.Get(ctx, &tasksapi.GetRequest{ContainerID: identifier})
	if status.Code(err) == codes.NotFound {
		lifecycle, lifecycleErr := taskLifecycle(0, false)

		return lifecycle, false, lifecycleErr
	}
	if err != nil {
		return application.WorkloadLifecycleUnknown, false, classifyRPCError(err)
	}
	process := response.GetProcess()
	if process == nil || process.GetID() != identifier ||
		process.GetContainerID() != "" && process.GetContainerID() != identifier {
		return application.WorkloadLifecycleUnknown, false, ErrProtocol
	}
	lifecycle, err := taskLifecycle(process.GetStatus(), true)

	return lifecycle, true, err
}

func apiMounts(values []*api.Mount) ([]mount.Mount, error) {
	result := make([]mount.Mount, len(values))
	for index, selected := range values {
		if selected == nil || selected.GetType() == "" || selected.GetSource() == "" ||
			strings.ContainsRune(selected.GetSource(), 0) || strings.ContainsRune(selected.GetType(), 0) {
			return nil, ErrProtocol
		}
		result[index] = mount.Mount{
			Type: selected.GetType(), Source: selected.GetSource(),
			Options: slices.Clone(selected.GetOptions()),
		}
	}

	return result, nil
}

func protoMounts(values []*api.Mount) ([]*api.Mount, error) {
	if _, err := apiMounts(values); err != nil {
		return nil, err
	}
	result := make([]*api.Mount, len(values))
	for index, selected := range values {
		result[index] = &api.Mount{
			Type: selected.GetType(), Source: selected.GetSource(),
			Options: slices.Clone(selected.GetOptions()),
		}
	}

	return result, nil
}
