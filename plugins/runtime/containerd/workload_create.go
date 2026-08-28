package containerd

import (
	"context"
	"encoding/hex"
	"maps"
	"reflect"
	"slices"
	"strings"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	leasesapi "github.com/containerd/containerd/api/services/leases/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	coreleases "github.com/containerd/containerd/v2/core/leases"
	"github.com/opencontainers/go-digest"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/anypb"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	workloadIdentifierPrefix  = "maniud-"
	workloadLeaseSuffix       = "-create"
	workloadSnapshotSuffix    = "-rootfs"
	workloadManagedLabelCount = 8
)

//nolint:cyclop,funlen // Creation owns one rollback boundary for lease, snapshot, host state, and metadata.
func (backend *nativeWorkloadBackendV1) Create(
	ctx context.Context,
	request createWorkloadRequest,
) (string, error) {
	if backend == nil || !validCreateWorkloadRequest(request) {
		return "", ErrProtocol
	}
	if backend.host == nil {
		return "", ErrUnavailable
	}
	identifier := workloadIdentifier(request.Workload.ContainerName, request.Transaction)
	candidates, err := backend.Candidates(
		ctx, request.Workload.ContainerName, request.Workload.ServiceName, request.Transaction,
	)
	if err != nil || candidates.Named != nil || candidates.Owned != nil {
		return "", ErrProtocol
	}
	existing, err := backend.Workload(ctx, identifier)
	if err != nil || existing != nil {
		return "", ErrProtocol
	}
	if err = backend.requireImage(
		ctx, request.Workload.Image.Reference, request.Workload.Image.ReferenceDigest.String(),
	); err != nil {
		return "", err
	}
	if err = backend.requireCommittedSnapshot(ctx, request.SnapshotParent); err != nil {
		return "", err
	}
	info, err := backend.Inspect(ctx)
	if err != nil {
		return "", err
	}
	if request.Workload.Restart != "" && request.Workload.Restart != "no" && !info.Restart {
		return "", ErrUnsupportedWorkload
	}

	leaseID := identifier + workloadLeaseSuffix
	lease, err := backend.leases.Create(ctx, &leasesapi.CreateRequest{
		ID: leaseID,
		Labels: map[string]string{
			domain.LabelService:     request.Workload.ServiceName,
			domain.LabelTransaction: request.Transaction,
		},
	})
	if err != nil {
		return "", classifyRPCError(err)
	}
	if lease.GetLease() == nil || lease.GetLease().GetID() != leaseID {
		return "", ErrProtocol
	}
	leaseContext := coreleases.WithLease(ctx, leaseID)
	snapshotKey := workloadSnapshotKey(identifier)
	snapshot, err := backend.snapshots.Prepare(leaseContext, &snapshotsapi.PrepareSnapshotRequest{
		Snapshotter: backend.options.Snapshotter,
		Key:         snapshotKey,
		Parent:      request.SnapshotParent,
		Labels: map[string]string{
			domain.LabelService:     request.Workload.ServiceName,
			domain.LabelTransaction: request.Transaction,
		},
	})
	if err != nil {
		backend.deleteCreateLease(ctx, leaseID)

		return "", classifyRPCError(err)
	}
	snapshotMounts, err := apiMounts(snapshot.GetMounts())
	if err != nil {
		backend.cleanupCreate(ctx, identifier, leaseID, nil)

		return "", err
	}
	prepared, err := backend.host.Prepare(
		ctx, backend.options, identifier, request.Configuration,
		snapshotMounts, request.CopyImageVolumes,
	)
	if err != nil {
		backend.cleanupCreate(ctx, identifier, leaseID, nil)

		return "", wrapWorkloadBackendError("host preparation", err)
	}
	if err = backend.host.EnsureNetworkNamespace(
		workloadNetworkNamespace(backend.options, identifier),
	); err != nil {
		backend.cleanupCreate(ctx, identifier, leaseID, prepared.RuntimeMounts)

		return "", wrapWorkloadBackendError("network namespace setup", err)
	}
	runtimeSpec, runtimeSpecDigest, err := encodeRuntimeSpec(prepared.Configuration)
	if err != nil {
		backend.cleanupCreate(ctx, identifier, leaseID, prepared.RuntimeMounts)

		return "", err
	}
	extension, err := encodeWorkloadExtension(workloadExtensionV1{
		Version:           containerExtensionVersion,
		Configuration:     request.Configuration,
		ImageReference:    request.Workload.Image.Reference,
		ImageConfig:       request.Workload.Image.ImageConfig.String(),
		PlatformManifest:  request.Workload.Image.PlatformManifest.String(),
		RuntimeMounts:     slices.Clone(prepared.RuntimeMounts),
		RuntimeSpecDigest: runtimeSpecDigest.String(),
		SnapshotParent:    request.SnapshotParent,
		NetworkDigest:     info.NetworkDigest.String(),
	})
	if err != nil {
		backend.cleanupCreate(ctx, identifier, leaseID, prepared.RuntimeMounts)

		return "", err
	}
	labels := userContainerLabels(request.Workload.Labels)
	maps.Copy(labels, workloadLabels(request.Workload, request.Transaction))
	maps.Copy(labels, restartLabels(request.Workload.Restart))
	response, err := backend.containers.Create(leaseContext, &containersapi.CreateContainerRequest{
		Container: &containersapi.Container{
			ID: identifier, Labels: labels, Image: request.Workload.Image.Reference,
			Runtime: &containersapi.Container_Runtime{Name: backend.options.Runtime},
			Spec:    runtimeSpec, Snapshotter: backend.options.Snapshotter, SnapshotKey: snapshotKey,
			Extensions: map[string]*anypb.Any{containerConfigurationExtension: extension},
		},
	})
	if err != nil || response.GetContainer() == nil || response.GetContainer().GetID() != identifier {
		backend.cleanupCreate(ctx, identifier, leaseID, prepared.RuntimeMounts)
		if err != nil {
			return "", classifyRPCError(err)
		}

		return "", ErrProtocol
	}
	if err = backend.deleteLease(ctx, leaseID); err != nil {
		backend.cleanupCreate(ctx, identifier, leaseID, prepared.RuntimeMounts)

		return "", err
	}

	return identifier, nil
}

func validCreateWorkloadRequest(request createWorkloadRequest) bool {
	sourceSpec, decodeErr := containerdconfig.Decode(request.Configuration)
	if !validTransaction(request.Transaction) || !validContainerdName(request.Workload.ContainerName) ||
		request.SnapshotParent != "" && digest.Digest(request.SnapshotParent).Validate() != nil ||
		decodeErr != nil || !reflect.DeepEqual(sourceSpec, request.Workload.WorkloadSpec) {
		return false
	}

	return validUserContainerLabels(request.Workload.Labels)
}

func validUserContainerLabels(labels []string) bool {
	seen := make(map[string]struct{}, len(labels))
	for _, label := range labels {
		name, _, found := strings.Cut(label, "=")
		if !found || name == "" || strings.HasPrefix(name, maniudLabelPrefix) ||
			strings.HasPrefix(name, "containerd.io/restart.") {
			return false
		}
		if _, found = seen[name]; found {
			return false
		}
		seen[name] = struct{}{}
	}

	return true
}

func (backend *nativeWorkloadBackendV1) requireCommittedSnapshot(
	ctx context.Context,
	parent string,
) error {
	if parent == "" {
		return nil
	}
	response, err := backend.snapshots.Stat(ctx, &snapshotsapi.StatSnapshotRequest{
		Snapshotter: backend.options.Snapshotter, Key: parent,
	})
	if status.Code(err) == codes.NotFound {
		return ErrUnsupportedWorkload
	}
	if err != nil {
		return classifyRPCError(err)
	}
	if response.GetInfo() == nil || response.GetInfo().GetName() != parent ||
		response.GetInfo().GetKind() != snapshotsapi.Kind_COMMITTED {
		return ErrProtocol
	}

	return nil
}

func (backend *nativeWorkloadBackendV1) deleteLease(ctx context.Context, identifier string) error {
	_, err := backend.leases.Delete(ctx, &leasesapi.DeleteRequest{ID: identifier, Sync: false})
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return classifyRPCError(err)
	}

	return nil
}

func (backend *nativeWorkloadBackendV1) deleteCreateLease(ctx context.Context, identifier string) {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerdRequestTimeout)
	defer cancel()

	_ = backend.deleteLease(cleanupContext, identifier)
}

func (backend *nativeWorkloadBackendV1) cleanupCreate(
	ctx context.Context,
	identifier string,
	leaseID string,
	mounts []domain.RuntimeMount,
) {
	cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerdRequestTimeout)
	defer cancel()

	backend.rollbackCreate(cleanupContext, identifier, mounts)
	_ = backend.deleteLease(cleanupContext, leaseID)
}

func (backend *nativeWorkloadBackendV1) rollbackCreate(
	ctx context.Context,
	identifier string,
	mounts []domain.RuntimeMount,
) {
	_, _ = backend.containers.Delete(ctx, &containersapi.DeleteContainerRequest{ID: identifier})
	_, _ = backend.snapshots.Remove(ctx, &snapshotsapi.RemoveSnapshotRequest{
		Snapshotter: backend.options.Snapshotter, Key: workloadSnapshotKey(identifier),
	})
	if backend.host == nil {
		return
	}
	_ = backend.host.DeleteNetworkNamespace(workloadNetworkNamespace(backend.options, identifier))
	_ = backend.host.Remove(backend.options, identifier, mounts)
}

func userContainerLabels(values []string) map[string]string {
	result := make(map[string]string, len(values)+workloadManagedLabelCount)
	for _, selected := range values {
		name, value, _ := strings.Cut(selected, "=")
		result[name] = value
	}

	return result
}

func workloadIdentifier(name, transaction string) string {
	digest := domain.Hash([]byte(name + "\x00" + transaction))

	return workloadIdentifierPrefix + hex.EncodeToString(digest[:24])
}

func workloadSnapshotKey(identifier string) string {
	return identifier + workloadSnapshotSuffix
}
