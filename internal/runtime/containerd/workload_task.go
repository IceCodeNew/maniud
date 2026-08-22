package containerd

import (
	"context"
	"errors"
	"slices"
	"syscall"
	"time"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/fieldmaskpb"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

type removableWorkload struct {
	candidate     *nativeWorkload
	ports         []domain.PortBinding
	runtimeMounts []domain.RuntimeMount
}

//nolint:cyclop // Start reconciles task, network, and restart-monitor state in one mutation boundary.
func (backend *nativeWorkloadBackendV1) Start(ctx context.Context, identifier string) (resultErr error) {
	workload, err := backend.Workload(ctx, identifier)
	if err != nil || workload == nil || !workload.ConfigurationMatches {
		return ErrProtocol
	}
	networkNamespace := workloadNetworkNamespace(backend.options, identifier)
	networkAbsent, err := backend.network.Absent(ctx, identifier, networkNamespace)
	if err != nil {
		return wrapWorkloadBackendError("CNI setup probe", err)
	}
	preserveNetwork := !networkAbsent
	defer func() {
		if preserveNetwork {
			return
		}
		cleanupContext, cancel := context.WithTimeout(context.WithoutCancel(ctx), containerdRequestTimeout)
		defer cancel()
		cleanupErr := backend.network.Delete(
			cleanupContext, identifier, networkNamespace, workload.Ports,
		)
		resultErr = errors.Join(resultErr, wrapWorkloadBackendError("CNI setup rollback", cleanupErr))
	}()
	if err = backend.network.Ensure(ctx, identifier, networkNamespace, workload.Ports); err != nil {
		return wrapWorkloadBackendError("CNI setup", err)
	}

	lifecycle, found, err := backend.taskLifecycle(ctx, identifier)
	if err != nil {
		return err
	}
	if lifecycle == application.WorkloadLifecycleRunning {
		preserveNetwork = true

		return backend.setRestartState(ctx, identifier, containerdRestartDesiredRunning, false)
	}
	if lifecycle == application.WorkloadLifecyclePaused {
		preserveNetwork = true

		return ErrProtocol
	}
	if found && lifecycle == application.WorkloadLifecycleExited {
		if err = backend.deleteTask(ctx, identifier); err != nil {
			return err
		}
		found = false
	}
	if !found {
		if err = backend.createTask(ctx, identifier); err != nil {
			return err
		}
	}
	preserveNetwork = true
	response, err := backend.tasks.Start(ctx, &tasksapi.StartRequest{ContainerID: identifier})
	if err != nil {
		return classifyRPCError(err)
	}
	if response.GetPid() == 0 {
		return ErrProtocol
	}

	return backend.setRestartState(ctx, identifier, containerdRestartDesiredRunning, false)
}

func (backend *nativeWorkloadBackendV1) createTask(ctx context.Context, identifier string) error {
	response, err := backend.snapshots.Mounts(ctx, &snapshotsapi.MountsRequest{
		Snapshotter: backend.options.Snapshotter, Key: workloadSnapshotKey(identifier),
	})
	if err != nil {
		return classifyRPCError(err)
	}
	mounts, err := protoMounts(response.GetMounts())
	if err != nil {
		return err
	}
	created, err := backend.tasks.Create(ctx, &tasksapi.CreateTaskRequest{
		ContainerID: identifier, Rootfs: mounts,
	})
	if err != nil {
		return classifyRPCError(err)
	}
	if created.GetContainerID() != identifier {
		return ErrProtocol
	}

	return nil
}

//nolint:cyclop // Stop handles each task lifecycle and the TERM-to-KILL timeout sequence.
func (backend *nativeWorkloadBackendV1) Stop(
	ctx context.Context,
	identifier string,
	timeout time.Duration,
) error {
	workload, err := backend.Workload(ctx, identifier)
	if err != nil || workload == nil || timeout <= 0 {
		return ErrProtocol
	}
	if err = backend.setRestartState(ctx, identifier, containerdRestartDesiredStopped, true); err != nil {
		return err
	}
	lifecycle, found, err := backend.taskLifecycle(ctx, identifier)
	if err != nil || !found || lifecycle == application.WorkloadLifecycleCreated ||
		lifecycle == application.WorkloadLifecycleExited {
		return err
	}
	if lifecycle == application.WorkloadLifecyclePaused {
		if _, err = backend.tasks.Resume(ctx, &tasksapi.ResumeTaskRequest{ContainerID: identifier}); err != nil {
			return classifyRPCError(err)
		}
	}
	if _, err = backend.tasks.Kill(ctx, &tasksapi.KillRequest{
		ContainerID: identifier, Signal: uint32(syscall.SIGTERM), All: true,
	}); err != nil {
		return classifyRPCError(err)
	}
	waitContext, cancel := context.WithTimeout(ctx, timeout)
	wait, waitErr := backend.tasks.Wait(waitContext, &tasksapi.WaitRequest{ContainerID: identifier})
	cancel()
	if errors.Is(waitErr, context.DeadlineExceeded) || status.Code(waitErr) == codes.DeadlineExceeded {
		if _, err = backend.tasks.Kill(ctx, &tasksapi.KillRequest{
			ContainerID: identifier, Signal: uint32(syscall.SIGKILL), All: true,
		}); err != nil {
			return classifyRPCError(err)
		}
		wait, waitErr = backend.tasks.Wait(ctx, &tasksapi.WaitRequest{ContainerID: identifier})
	}
	if waitErr != nil {
		return classifyRPCError(waitErr)
	}
	if wait.GetExitedAt() == nil {
		return ErrProtocol
	}

	return nil
}

func (backend *nativeWorkloadBackendV1) Rename(
	ctx context.Context,
	identifier string,
	name string,
) error {
	workload, err := backend.Workload(ctx, identifier)
	if err != nil || workload == nil || !validContainerdName(name) {
		return ErrProtocol
	}

	return backend.updateLabels(ctx, identifier, func(labels map[string]string) {
		labels[containerNameLabel] = name
	})
}

//nolint:cyclop // Removal orders task, network, metadata, snapshot, and host cleanup fail closed.
func (backend *nativeWorkloadBackendV1) Remove(
	ctx context.Context,
	identifier string,
	force bool,
) error {
	workload, found, err := backend.removableWorkload(ctx, identifier)
	if err != nil || !found {
		return err
	}
	if force && (workload.candidate.Lifecycle == application.WorkloadLifecycleRunning ||
		workload.candidate.Lifecycle == application.WorkloadLifecyclePaused) {
		if err = backend.Stop(ctx, identifier, containerdStopTimeout); err != nil {
			return err
		}
	}
	lifecycle, found, err := backend.taskLifecycle(ctx, identifier)
	if err != nil {
		return err
	}
	if lifecycle == application.WorkloadLifecycleRunning || lifecycle == application.WorkloadLifecyclePaused {
		return ErrProtocol
	}
	if found {
		if err = backend.deleteTask(ctx, identifier); err != nil {
			return err
		}
	}
	networkNamespace := workloadNetworkNamespace(backend.options, identifier)
	if err = backend.network.Delete(ctx, identifier, networkNamespace, workload.ports); err != nil {
		return wrapWorkloadBackendError("CNI removal", err)
	}
	if err = backend.host.DeleteNetworkNamespace(networkNamespace); err != nil {
		return wrapWorkloadBackendError("network namespace removal", err)
	}
	if _, err = backend.snapshots.Remove(ctx, &snapshotsapi.RemoveSnapshotRequest{
		Snapshotter: backend.options.Snapshotter, Key: workloadSnapshotKey(identifier),
	}); err != nil && status.Code(err) != codes.NotFound {
		return classifyRPCError(err)
	}
	if err = backend.host.Remove(backend.options, identifier, workload.runtimeMounts); err != nil {
		return wrapWorkloadBackendError("workload host-state removal", err)
	}
	if _, err = backend.containers.Delete(
		ctx, &containersapi.DeleteContainerRequest{ID: identifier},
	); err != nil && status.Code(err) != codes.NotFound {
		return classifyRPCError(err)
	}

	return nil
}

//nolint:cyclop // Removal accepts only complete, independently verified managed metadata.
func (backend *nativeWorkloadBackendV1) removableWorkload(
	ctx context.Context,
	identifier string,
) (removableWorkload, bool, error) {
	var empty removableWorkload
	if backend == nil || backend.containers == nil || backend.tasks == nil ||
		backend.snapshots == nil || backend.network == nil || backend.host == nil {
		return empty, false, ErrUnavailable
	}
	response, err := backend.containers.Get(ctx, &containersapi.GetContainerRequest{ID: identifier})
	if status.Code(err) == codes.NotFound {
		return empty, false, nil
	}
	if err != nil {
		return empty, false, classifyRPCError(err)
	}
	container := response.GetContainer()
	if container == nil || container.GetID() != identifier || !validContainerdID(identifier) ||
		container.GetImage() == "" || container.GetSandbox() != "" {
		return empty, false, ErrProtocol
	}
	extension, found := container.GetExtensions()[containerConfigurationExtension]
	if !found {
		return empty, false, ErrProtocol
	}
	metadata, err := backend.managedContainerMetadata(
		container, container.GetLabels()[containerNameLabel], extension,
	)
	if err != nil {
		return empty, false, ErrProtocol
	}
	lifecycle, _, err := backend.taskLifecycle(ctx, identifier)
	if err != nil {
		return empty, false, err
	}
	candidate := managedNativeWorkload(container, container.GetLabels()[containerNameLabel], metadata, lifecycle, false)

	return removableWorkload{
		candidate: candidate, ports: slices.Clone(metadata.sourceSpec.Ports),
		runtimeMounts: slices.Clone(metadata.extension.RuntimeMounts),
	}, true, nil
}

func (backend *nativeWorkloadBackendV1) RemovalCandidate(
	ctx context.Context,
	identifier string,
) (*nativeWorkload, error) {
	workload, found, err := backend.removableWorkload(ctx, identifier)
	if err != nil || !found {
		return nil, err
	}

	return workload.candidate, nil
}

func (backend *nativeWorkloadBackendV1) deleteTask(ctx context.Context, identifier string) error {
	response, err := backend.tasks.Delete(ctx, &tasksapi.DeleteTaskRequest{ContainerID: identifier})
	if status.Code(err) == codes.NotFound {
		return nil
	}
	if err != nil {
		return classifyRPCError(err)
	}
	if response.GetID() != "" && response.GetID() != identifier {
		return ErrProtocol
	}

	return nil
}

func (backend *nativeWorkloadBackendV1) setRestartState(
	ctx context.Context,
	identifier string,
	state string,
	explicitlyStopped bool,
) error {
	return backend.updateLabels(ctx, identifier, func(labels map[string]string) {
		if _, configured := labels[containerdRestartPolicyLabel]; !configured {
			return
		}
		labels[containerdRestartStatusLabel] = state
		if explicitlyStopped {
			labels[containerdExplicitlyStoppedLabel] = containerdRestartExplicitlyStopped
		} else {
			labels[containerdExplicitlyStoppedLabel] = containerdRestartNotExplicitlyStopped
		}
	})
}

func (backend *nativeWorkloadBackendV1) updateLabels(
	ctx context.Context,
	identifier string,
	update func(map[string]string),
) error {
	response, err := backend.containers.Get(ctx, &containersapi.GetContainerRequest{ID: identifier})
	if err != nil {
		return classifyRPCError(err)
	}
	container := response.GetContainer()
	if container == nil || container.GetID() != identifier || update == nil {
		return ErrProtocol
	}
	labels := cloneStringLabels(container.GetLabels())
	update(labels)
	updated, err := backend.containers.Update(ctx, &containersapi.UpdateContainerRequest{
		Container:  &containersapi.Container{ID: identifier, Labels: labels},
		UpdateMask: &fieldmaskpb.FieldMask{Paths: []string{"labels"}},
	})
	if err != nil {
		return classifyRPCError(err)
	}
	if updated.GetContainer() == nil || updated.GetContainer().GetID() != identifier {
		return ErrProtocol
	}

	return nil
}
