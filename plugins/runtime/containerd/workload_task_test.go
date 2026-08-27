package containerd

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	api "github.com/containerd/containerd/api/types"
	tasktypes "github.com/containerd/containerd/api/types/task"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/proto"
	"google.golang.org/protobuf/types/known/anypb"
	"google.golang.org/protobuf/types/known/emptypb"
	"google.golang.org/protobuf/types/known/timestamppb"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func configureContainerUpdate(fixture nativeManagedFixture) {
	fixture.containers.update = func(
		request *containersapi.UpdateContainerRequest,
	) (*containersapi.UpdateContainerResponse, error) {
		for _, path := range request.GetUpdateMask().GetPaths() {
			key, found := strings.CutPrefix(path, "labels.")
			if !found {
				continue
			}
			value, present := request.GetContainer().GetLabels()[key]
			if present {
				fixture.container.Labels[key] = value
			} else {
				delete(fixture.container.Labels, key)
			}
		}

		return &containersapi.UpdateContainerResponse{Container: fixture.container}, nil
	}
}

//nolint:cyclop // The test keeps every start lifecycle transition in one stateful fixture.
func TestNativeTaskStartLifecycle(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	configureContainerUpdate(fixture)
	created := 0
	started := 0
	fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
		return &snapshotsapi.MountsResponse{Mounts: []*api.Mount{{
			Type: bindMountType, Source: testSourcePath,
		}}}, nil
	}
	fixture.tasks.create = func(request *tasksapi.CreateTaskRequest) (*tasksapi.CreateTaskResponse, error) {
		created++

		return &tasksapi.CreateTaskResponse{ContainerID: request.GetContainerID(), Pid: 1}, nil
	}
	fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
		started++

		return &tasksapi.StartResponse{Pid: 1}, nil
	}
	if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err != nil ||
		created != 1 || started != 1 || fixture.host.ensureCalls != 0 || fixture.network.ensureCalls != 1 {
		t.Fatalf(
			"Start() = %v, task create/start %d/%d, host/network %d/%d",
			err, created, started, fixture.host.ensureCalls, fixture.network.ensureCalls,
		)
	}

	fixture.task.Process = &tasktypes.Process{
		ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_RUNNING,
	}
	if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err != nil || created != 1 {
		t.Fatalf("Start(running) = %v, creates %d", err, created)
	}
	fixture.task.Process.Status = tasktypes.Status_CREATED
	if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err != nil || created != 1 {
		t.Fatalf("Start(created) = %v, creates %d", err, created)
	}
	fixture.task.Process.Status = tasktypes.Status_PAUSED
	if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Start(paused) = %v", err)
	}
	fixture.task.Process.Status = tasktypes.Status_STOPPED
	fixture.tasks.delete = func(request *tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error) {
		return &tasksapi.DeleteResponse{ID: request.GetContainerID()}, nil
	}
	if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err != nil || created != 2 {
		t.Fatalf("Start(exited) = %v, creates %d", err, created)
	}
}

func TestNativeTaskStopLifecycle(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	configureContainerUpdate(fixture)
	fixture.container.Labels[containerdRestartPolicyLabel] = testRestartPolicy
	fixture.task.Process = &tasktypes.Process{
		ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_RUNNING,
	}
	kills := make([]uint32, 0, 2)
	fixture.tasks.kill = func(request *tasksapi.KillRequest) (*emptypb.Empty, error) {
		kills = append(kills, request.GetSignal())

		return &emptypb.Empty{}, nil
	}
	fixture.tasks.wait = func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error) {
		return &tasksapi.WaitResponse{ExitedAt: timestamppb.Now()}, nil
	}
	if err := fixture.backend.Stop(context.Background(), fixture.container.GetID(), time.Second); err != nil ||
		len(kills) != 1 ||
		fixture.container.Labels[containerdExplicitlyStoppedLabel] != containerdRestartExplicitlyStopped {
		t.Fatalf("Stop() = %v, signals %#v, labels %#v", err, kills, fixture.container.Labels)
	}

	fixture.task.Process.Status = tasktypes.Status_PAUSED
	fixture.tasks.resume = func(*tasksapi.ResumeTaskRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	if err := fixture.backend.Stop(context.Background(), fixture.container.GetID(), time.Second); err != nil {
		t.Fatalf("Stop(paused) = %v", err)
	}

	fixture.task.Process.Status = tasktypes.Status_RUNNING
	waits := 0
	fixture.tasks.wait = func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error) {
		waits++
		if waits == 1 {
			return nil, context.DeadlineExceeded
		}

		return &tasksapi.WaitResponse{ExitedAt: timestamppb.Now()}, nil
	}
	if err := fixture.backend.Stop(context.Background(), fixture.container.GetID(), time.Nanosecond); err != nil ||
		waits != 2 || len(kills) != 4 {
		t.Fatalf("Stop(timeout) = %v, waits %d, signals %#v", err, waits, kills)
	}
}

func TestNativeTaskRenameAndRemove(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	configureContainerUpdate(fixture)
	if err := fixture.backend.Rename(
		context.Background(), fixture.container.GetID(), testRenamedWorkloadName,
	); err != nil || fixture.container.GetLabels()[containerNameLabel] != testRenamedWorkloadName {
		t.Fatalf("Rename() = %v, labels %#v", err, fixture.container.GetLabels())
	}
	if err := fixture.backend.Rename(
		context.Background(), fixture.container.GetID(), "bad/name",
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Rename(invalid name) = %v", err)
	}

	containerDeletes := 0
	snapshotDeletes := 0
	fixture.containers.delete = func(*containersapi.DeleteContainerRequest) (*emptypb.Empty, error) {
		containerDeletes++

		return &emptypb.Empty{}, nil
	}
	fixture.snapshots.remove = func(*snapshotsapi.RemoveSnapshotRequest) (*emptypb.Empty, error) {
		snapshotDeletes++

		return &emptypb.Empty{}, nil
	}
	if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), false); err != nil ||
		containerDeletes != 1 || snapshotDeletes != 1 || fixture.network.deleteCalls != 1 ||
		fixture.host.deleteCalls != 1 || fixture.host.removeCalls != 1 {
		t.Fatalf(
			"Remove() = %v, container/snapshot %d/%d, network %d, host %d/%d",
			err, containerDeletes, snapshotDeletes, fixture.network.deleteCalls,
			fixture.host.deleteCalls, fixture.host.removeCalls,
		)
	}
}

func TestNativeTaskForceRemoveRunningWorkload(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	configureContainerUpdate(fixture)
	fixture.task.Process = &tasktypes.Process{
		ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_RUNNING,
	}
	fixture.tasks.kill = func(*tasksapi.KillRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	fixture.tasks.wait = func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error) {
		fixture.task.Process.Status = tasktypes.Status_STOPPED

		return &tasksapi.WaitResponse{ExitedAt: timestamppb.Now()}, nil
	}
	fixture.tasks.delete = func(request *tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error) {
		return &tasksapi.DeleteResponse{ID: request.GetContainerID()}, nil
	}
	fixture.containers.delete = func(*containersapi.DeleteContainerRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	fixture.snapshots.remove = func(*snapshotsapi.RemoveSnapshotRequest) (*emptypb.Empty, error) {
		return &emptypb.Empty{}, nil
	}
	if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), true); err != nil {
		t.Fatalf("Remove(force running) = %v", err)
	}
}

//nolint:cyclop // The test proves partial cleanup, retry, and idempotent completion together.
func TestNativeTaskRemoveRetriesCleanupBeforeDeletingMetadata(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	containerPresent := true
	fixture.containers.get = func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
		if !containerPresent {
			return nil, status.Error(codes.NotFound, "missing")
		}

		return &containersapi.GetContainerResponse{Container: fixture.container}, nil
	}
	containerDeletes := 0
	fixture.containers.delete = func(*containersapi.DeleteContainerRequest) (*emptypb.Empty, error) {
		containerDeletes++
		containerPresent = false

		return &emptypb.Empty{}, nil
	}
	snapshotPresent := true
	fixture.snapshots.remove = func(*snapshotsapi.RemoveSnapshotRequest) (*emptypb.Empty, error) {
		if !snapshotPresent {
			return nil, status.Error(codes.NotFound, "missing")
		}
		snapshotPresent = false

		return &emptypb.Empty{}, nil
	}
	fixture.host.removeErr = errContainerdTest
	if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), false); err == nil ||
		containerDeletes != 0 || snapshotPresent {
		t.Fatalf(
			"Remove(partial cleanup) = %v, container deletes %d, snapshot present %t",
			err, containerDeletes, snapshotPresent,
		)
	}
	candidate, err := fixture.backend.RemovalCandidate(context.Background(), fixture.container.GetID())
	if err != nil || candidate == nil || candidate.ID != fixture.container.GetID() ||
		candidate.ConfigurationDigest == (domain.Digest{}) {
		t.Fatalf("RemovalCandidate(partial cleanup) = %#v, %v", candidate, err)
	}

	fixture.host.removeErr = nil
	if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), false); err != nil ||
		containerDeletes != 1 || containerPresent {
		t.Fatalf(
			"Remove(retry) = %v, container deletes %d, container present %t",
			err, containerDeletes, containerPresent,
		)
	}
	candidate, err = fixture.backend.RemovalCandidate(context.Background(), fixture.container.GetID())
	if err != nil || candidate != nil {
		t.Fatalf("RemovalCandidate(complete) = %#v, %v", candidate, err)
	}
	if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), false); err != nil ||
		containerDeletes != 1 {
		t.Fatalf("Remove(completed retry) = %v, container deletes %d", err, containerDeletes)
	}
}

func TestNativeTaskDeleteEvidence(t *testing.T) {
	t.Parallel()

	identifier := testWorkloadName
	backend := &nativeWorkloadBackendV1{tasks: fakeTasksAPI{delete: func(
		*tasksapi.DeleteTaskRequest,
	) (*tasksapi.DeleteResponse, error) {
		return &tasksapi.DeleteResponse{ID: identifier}, nil
	}}}
	if err := backend.deleteTask(context.Background(), identifier); err != nil {
		t.Fatalf("deleteTask() = %v", err)
	}
	backend.tasks = fakeTasksAPI{delete: func(*tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error) {
		return &tasksapi.DeleteResponse{}, nil
	}}
	if err := backend.deleteTask(context.Background(), identifier); err != nil {
		t.Fatalf("deleteTask(empty task ID) = %v", err)
	}
	backend.tasks = fakeTasksAPI{delete: func(*tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error) {
		return &tasksapi.DeleteResponse{ID: testOtherValue}, nil
	}}
	if err := backend.deleteTask(context.Background(), identifier); !errors.Is(err, ErrProtocol) {
		t.Fatalf("deleteTask(mismatch) = %v", err)
	}
	backend.tasks = fakeTasksAPI{delete: func(*tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}}
	if err := backend.deleteTask(context.Background(), identifier); err != nil {
		t.Fatalf("deleteTask(missing) = %v", err)
	}
	if lifecycle, err := taskLifecycle(tasktypes.Status_UNKNOWN, true); err == nil ||
		lifecycle != application.WorkloadLifecycleUnknown {
		t.Fatalf("taskLifecycle(unknown) = %v, %v", lifecycle, err)
	}
}

func configureNativeTaskStart(fixture nativeManagedFixture) {
	configureContainerUpdate(fixture)
	fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
		return &snapshotsapi.MountsResponse{Mounts: []*api.Mount{{
			Type: bindMountType, Source: testSourcePath,
		}}}, nil
	}
	fixture.tasks.create = func(request *tasksapi.CreateTaskRequest) (*tasksapi.CreateTaskResponse, error) {
		return &tasksapi.CreateTaskResponse{ContainerID: request.GetContainerID(), Pid: 1}, nil
	}
	fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
		return &tasksapi.StartResponse{Pid: 1}, nil
	}
}

//nolint:funlen // The table covers each native task-start protocol boundary independently.
func TestNativeTaskStartFailureMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(nativeManagedFixture)
	}{
		{name: "workload read", mutate: func(fixture nativeManagedFixture) {
			fixture.containers.get = func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: testHostUnavailableCase, mutate: func(fixture nativeManagedFixture) { fixture.backend.host = nil }},
		{name: "CNI probe", mutate: func(fixture nativeManagedFixture) {
			fixture.network.absentErr = errContainerdTest
		}},
		{name: "CNI", mutate: func(fixture nativeManagedFixture) { fixture.network.ensureErr = errContainerdTest }},
		{name: testTaskLifecycleCase, mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "snapshot mounts", mutate: func(fixture nativeManagedFixture) {
			fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "snapshot mount protocol", mutate: func(fixture nativeManagedFixture) {
			fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
				return &snapshotsapi.MountsResponse{Mounts: []*api.Mount{nil}}, nil
			}
		}},
		{name: "task create", mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.create = func(*tasksapi.CreateTaskRequest) (*tasksapi.CreateTaskResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "task create protocol", mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.create = func(*tasksapi.CreateTaskRequest) (*tasksapi.CreateTaskResponse, error) {
				return &tasksapi.CreateTaskResponse{ContainerID: testOtherValue}, nil
			}
		}},
		{name: "task start", mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "task start protocol", mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
				return &tasksapi.StartResponse{}, nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := testNativeManagedBackend(t)
			configureNativeTaskStart(fixture)
			test.mutate(fixture)
			if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil {
				t.Fatal("Start() succeeded")
			}
		})
	}
}

//nolint:cyclop,funlen // The subtests distinguish definitive and ambiguous task-start outcomes.
func TestNativeTaskStartRollsBackOnlyUnownedNewNetwork(t *testing.T) {
	t.Parallel()

	t.Run("pre-existing network", func(t *testing.T) {
		t.Parallel()

		fixture := testNativeManagedBackend(t)
		configureNativeTaskStart(fixture)
		fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
			return nil, errContainerdTest
		}
		if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil ||
			fixture.network.deleteCalls != 0 {
			t.Fatalf("Start(pre-existing network) = %v, network deletes %d", err, fixture.network.deleteCalls)
		}
	})

	t.Run("pre-start failure", func(t *testing.T) {
		t.Parallel()

		fixture := testNativeManagedBackend(t)
		configureNativeTaskStart(fixture)
		fixture.network.absent = true
		fixture.snapshots.mounts = func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error) {
			return nil, errContainerdTest
		}
		if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil ||
			fixture.network.ensureCalls != 1 || fixture.network.deleteCalls != 1 || fixture.host.deleteCalls != 0 {
			t.Fatalf(
				"Start(pre-start failure) = %v, network ensure/delete %d/%d, namespace deletes %d",
				err, fixture.network.ensureCalls, fixture.network.deleteCalls, fixture.host.deleteCalls,
			)
		}
	})

	t.Run("definitive task start failure", func(t *testing.T) {
		t.Parallel()

		fixture := testNativeManagedBackend(t)
		configureNativeTaskStart(fixture)
		fixture.network.absent = true
		fixture.task.Process = &tasktypes.Process{
			ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_CREATED,
		}
		fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
			return nil, status.Error(codes.FailedPrecondition, "not started")
		}
		if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil ||
			fixture.network.deleteCalls != 1 {
			t.Fatalf("Start(definitive task start failure) = %v, network deletes %d", err, fixture.network.deleteCalls)
		}
	})

	t.Run("zero PID without running task", func(t *testing.T) {
		t.Parallel()

		fixture := testNativeManagedBackend(t)
		configureNativeTaskStart(fixture)
		fixture.network.absent = true
		fixture.task.Process = &tasktypes.Process{
			ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_CREATED,
		}
		fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
			return &tasksapi.StartResponse{}, nil
		}
		if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil ||
			fixture.network.deleteCalls != 1 {
			t.Fatalf("Start(zero PID) = %v, network deletes %d", err, fixture.network.deleteCalls)
		}
	})

	for _, test := range []struct {
		name   string
		status tasktypes.Status
		probe  error
	}{
		{name: "observed running task", status: tasktypes.Status_RUNNING},
		{name: "ambiguous task probe", status: tasktypes.Status_CREATED, probe: errContainerdTest},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			fixture := testNativeManagedBackend(t)
			configureNativeTaskStart(fixture)
			fixture.network.absent = true
			started := false
			fixture.tasks.create = func(request *tasksapi.CreateTaskRequest) (*tasksapi.CreateTaskResponse, error) {
				started = true

				return &tasksapi.CreateTaskResponse{ContainerID: request.GetContainerID(), Pid: 1}, nil
			}
			fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
				if !started {
					return nil, status.Error(codes.NotFound, "missing")
				}
				if test.probe != nil {
					return nil, test.probe
				}

				return &tasksapi.GetResponse{Process: &tasktypes.Process{
					ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: test.status,
				}}, nil
			}
			fixture.tasks.start = func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error) {
				return nil, errContainerdTest
			}
			if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil ||
				fixture.network.deleteCalls != 0 {
				t.Fatalf("Start(%s) = %v, network deletes %d", test.name, err, fixture.network.deleteCalls)
			}
		})
	}
}

//nolint:funlen // The table covers each native task-stop protocol boundary independently.
func TestNativeTaskStopFailureMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(nativeManagedFixture)
	}{
		{name: "restart labels", mutate: func(fixture nativeManagedFixture) {
			fixture.container.Labels[containerdRestartPolicyLabel] = testRestartPolicy
			fixture.containers.update = func(
				*containersapi.UpdateContainerRequest,
			) (*containersapi.UpdateContainerResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: testTaskLifecycleCase, mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "unexpected lifecycle", mutate: func(fixture nativeManagedFixture) {
			fixture.task.Process.Status = tasktypes.Status_PAUSING
		}},
		{name: "resume", mutate: func(fixture nativeManagedFixture) {
			fixture.task.Process.Status = tasktypes.Status_PAUSED
			fixture.tasks.resume = func(*tasksapi.ResumeTaskRequest) (*emptypb.Empty, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "TERM", mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.kill = func(*tasksapi.KillRequest) (*emptypb.Empty, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "wait", mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.wait = func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "wait protocol", mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.wait = func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error) {
				return &tasksapi.WaitResponse{}, nil
			}
		}},
		{name: "KILL", mutate: func(fixture nativeManagedFixture) {
			calls := 0
			fixture.tasks.wait = func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error) {
				return nil, status.Error(codes.DeadlineExceeded, "timeout")
			}
			fixture.tasks.kill = func(*tasksapi.KillRequest) (*emptypb.Empty, error) {
				calls++
				if calls == 2 {
					return nil, errContainerdTest
				}

				return &emptypb.Empty{}, nil
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := testNativeManagedBackend(t)
			configureContainerUpdate(fixture)
			fixture.task.Process = &tasktypes.Process{
				ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(),
				Status: tasktypes.Status_RUNNING,
			}
			fixture.tasks.kill = func(*tasksapi.KillRequest) (*emptypb.Empty, error) {
				return &emptypb.Empty{}, nil
			}
			fixture.tasks.wait = func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error) {
				return &tasksapi.WaitResponse{ExitedAt: timestamppb.Now()}, nil
			}
			test.mutate(fixture)
			if err := fixture.backend.Stop(context.Background(), fixture.container.GetID(), time.Second); err == nil {
				t.Fatal("Stop() succeeded")
			}
		})
	}
}

//nolint:funlen // The table covers each independently failing removal boundary.
func TestNativeTaskRemoveFailureMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(nativeManagedFixture)
	}{
		{name: "container read", mutate: func(fixture nativeManagedFixture) {
			fixture.containers.get = func(
				*containersapi.GetContainerRequest,
			) (*containersapi.GetContainerResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "missing container payload", mutate: func(fixture nativeManagedFixture) {
			fixture.containers.get = func(
				*containersapi.GetContainerRequest,
			) (*containersapi.GetContainerResponse, error) {
				return &containersapi.GetContainerResponse{}, nil
			}
		}},
		{name: "container identifier", mutate: func(fixture nativeManagedFixture) {
			container := new(containersapi.Container)
			proto.Merge(container, fixture.container)
			container.ID = testOtherValue
			fixture.containers.get = func(
				*containersapi.GetContainerRequest,
			) (*containersapi.GetContainerResponse, error) {
				return &containersapi.GetContainerResponse{Container: container}, nil
			}
		}},
		{name: "invalid container identifier", mutate: func(fixture nativeManagedFixture) {
			fixture.container.ID = testBadIdentifier
		}},
		{name: "container image", mutate: func(fixture nativeManagedFixture) {
			fixture.container.Image = ""
		}},
		{name: "container sandbox", mutate: func(fixture nativeManagedFixture) {
			fixture.container.Sandbox = testOtherValue
		}},
		{name: "workload extension", mutate: func(fixture nativeManagedFixture) {
			delete(fixture.container.Extensions, containerConfigurationExtension)
		}},
		{name: "workload metadata", mutate: func(fixture nativeManagedFixture) {
			fixture.container.Extensions[containerConfigurationExtension] = &anypb.Any{}
		}},
		{name: testTaskLifecycleCase, mutate: func(fixture nativeManagedFixture) {
			fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "running", mutate: func(fixture nativeManagedFixture) {
			fixture.task.Process = &tasktypes.Process{
				ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_RUNNING,
			}
		}},
		{name: "task delete", mutate: func(fixture nativeManagedFixture) {
			fixture.task.Process = &tasktypes.Process{
				ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_STOPPED,
			}
			fixture.tasks.delete = func(*tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "CNI", mutate: func(fixture nativeManagedFixture) { fixture.network.deleteErr = errContainerdTest }},
		{name: testHostUnavailableCase, mutate: func(fixture nativeManagedFixture) { fixture.backend.host = nil }},
		{name: "namespace", mutate: func(fixture nativeManagedFixture) { fixture.host.deleteErr = errContainerdTest }},
		{name: testContainerValue, mutate: func(fixture nativeManagedFixture) {
			fixture.containers.delete = func(*containersapi.DeleteContainerRequest) (*emptypb.Empty, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "snapshot", mutate: func(fixture nativeManagedFixture) {
			fixture.snapshots.remove = func(*snapshotsapi.RemoveSnapshotRequest) (*emptypb.Empty, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "host state", mutate: func(fixture nativeManagedFixture) { fixture.host.removeErr = errContainerdTest }},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := testNativeManagedBackend(t)
			fixture.containers.delete = func(*containersapi.DeleteContainerRequest) (*emptypb.Empty, error) {
				return &emptypb.Empty{}, nil
			}
			fixture.snapshots.remove = func(*snapshotsapi.RemoveSnapshotRequest) (*emptypb.Empty, error) {
				return &emptypb.Empty{}, nil
			}
			test.mutate(fixture)
			if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), false); err == nil {
				t.Fatal("Remove() succeeded")
			}
		})
	}
}

func TestNativeContainerLabelUpdateFailures(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	if err := fixture.backend.updateLabels(
		context.Background(), fixture.container.GetID(), nil,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("updateLabels(nil) = %v", err)
	}
	fixture.containers.get = func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
		return nil, errContainerdTest
	}
	if err := fixture.backend.updateLabels(
		context.Background(), fixture.container.GetID(), func(map[string]string) {},
	); err == nil {
		t.Fatal("updateLabels(read failure) succeeded")
	}
	fixture.containers.get = func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
		return &containersapi.GetContainerResponse{Container: fixture.container}, nil
	}
	fixture.containers.update = func(
		*containersapi.UpdateContainerRequest,
	) (*containersapi.UpdateContainerResponse, error) {
		return nil, errContainerdTest
	}
	if err := fixture.backend.updateLabels(
		context.Background(), fixture.container.GetID(), func(labels map[string]string) {
			labels[containerNameLabel] = testRenamedWorkloadName
		},
	); err == nil {
		t.Fatal("updateLabels(write failure) succeeded")
	}
	fixture.containers.update = func(
		*containersapi.UpdateContainerRequest,
	) (*containersapi.UpdateContainerResponse, error) {
		return &containersapi.UpdateContainerResponse{}, nil
	}
	if err := fixture.backend.updateLabels(
		context.Background(), fixture.container.GetID(), func(labels map[string]string) {
			labels[containerNameLabel] = testRenamedWorkloadName
		},
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("updateLabels(response protocol) = %v", err)
	}
}

func TestNativeContainerLabelUpdatePreservesIndependentWriters(t *testing.T) {
	t.Parallel()

	fixture := testNativeManagedBackend(t)
	identifier := fixture.container.GetID()
	requests := make([]*containersapi.UpdateContainerRequest, 0, 2)
	fixture.containers.update = func(
		request *containersapi.UpdateContainerRequest,
	) (*containersapi.UpdateContainerResponse, error) {
		requests = append(requests, request)
		if value, found := request.GetContainer().GetLabels()[containerNameLabel]; found {
			fixture.container.Labels[containerNameLabel] = value
		} else {
			delete(fixture.container.Labels, containerNameLabel)
		}

		return &containersapi.UpdateContainerResponse{Container: fixture.container}, nil
	}

	if err := fixture.backend.updateLabels(context.Background(), identifier, func(labels map[string]string) {
		labels[containerNameLabel] = testRenamedWorkloadName
	}); err != nil {
		t.Fatalf("updateLabels(rename) error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("rename update request = %#v", requests)
	}
	assertContainerNameLabelUpdate(t, requests[0], testRenamedWorkloadName, true)

	if err := fixture.backend.updateLabels(context.Background(), identifier, func(labels map[string]string) {
		labels[containerNameLabel] = testRenamedWorkloadName
	}); err != nil {
		t.Fatalf("updateLabels(no-op) error = %v", err)
	}
	if len(requests) != 1 {
		t.Fatalf("updateLabels(no-op) requests = %d", len(requests))
	}
	if err := fixture.backend.updateLabels(context.Background(), identifier, func(labels map[string]string) {
		delete(labels, containerNameLabel)
	}); err != nil {
		t.Fatalf("updateLabels(delete) error = %v", err)
	}
	if len(requests) != 2 {
		t.Fatalf("updateLabels(delete) requests = %#v", requests)
	}
	assertContainerNameLabelUpdate(t, requests[1], "", false)
	if _, found := fixture.container.Labels[containerNameLabel]; found {
		t.Fatalf("updateLabels(delete) labels = %#v", fixture.container.Labels)
	}
}

func assertContainerNameLabelUpdate(
	t *testing.T,
	request *containersapi.UpdateContainerRequest,
	want string,
	present bool,
) {
	t.Helper()

	value, found := request.GetContainer().GetLabels()[containerNameLabel]
	if found != present || value != want {
		t.Fatalf("container name label = %q, %t; want %q, %t", value, found, want, present)
	}
	paths := request.GetUpdateMask().GetPaths()
	if len(paths) != 1 || paths[0] != "labels."+containerNameLabel {
		t.Fatalf("container name label mask = %q", paths)
	}
}

//nolint:cyclop,funlen // The test changes task state between workload proof and the mutation-time recheck.
func TestNativeTaskMutationTimeLifecycleFailures(t *testing.T) {
	t.Parallel()

	t.Run("start lifecycle read", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		configureNativeTaskStart(fixture)
		calls := 0
		fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			calls++
			if calls == 1 {
				return nil, status.Error(codes.NotFound, "missing")
			}

			return nil, errContainerdTest
		}
		if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil {
			t.Fatal("Start(second lifecycle read) succeeded")
		}
	})

	t.Run("start exited delete", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		configureNativeTaskStart(fixture)
		fixture.task.Process = &tasktypes.Process{
			ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_STOPPED,
		}
		fixture.tasks.delete = func(*tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error) {
			return nil, errContainerdTest
		}
		if err := fixture.backend.Start(context.Background(), fixture.container.GetID()); err == nil {
			t.Fatal("Start(exited task delete) succeeded")
		}
	})

	t.Run("stop lifecycle changes", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		configureContainerUpdate(fixture)
		calls := 0
		fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			calls++
			statusValue := tasktypes.Status_RUNNING
			if calls == 2 {
				statusValue = tasktypes.Status_PAUSING
			}

			return &tasksapi.GetResponse{Process: &tasktypes.Process{
				ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: statusValue,
			}}, nil
		}
		if err := fixture.backend.Stop(
			context.Background(), fixture.container.GetID(), time.Second,
		); !errors.Is(err, ErrProtocol) {
			t.Fatalf("Stop(changed lifecycle) = %v", err)
		}
	})

	t.Run("stop task disappears", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		configureContainerUpdate(fixture)
		calls := 0
		fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			calls++
			if calls == 2 {
				return nil, status.Error(codes.NotFound, "missing")
			}

			return &tasksapi.GetResponse{Process: &tasktypes.Process{
				ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_RUNNING,
			}}, nil
		}
		if err := fixture.backend.Stop(context.Background(), fixture.container.GetID(), time.Second); err != nil {
			t.Fatalf("Stop(disappeared task) = %v", err)
		}
	})

	t.Run("remove lifecycle read", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		calls := 0
		fixture.tasks.get = func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			calls++
			if calls == 1 {
				return nil, status.Error(codes.NotFound, "missing")
			}

			return nil, errContainerdTest
		}
		if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), false); err == nil {
			t.Fatal("Remove(second lifecycle read) succeeded")
		}
	})

	t.Run("force remove stop", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		configureContainerUpdate(fixture)
		fixture.task.Process = &tasktypes.Process{
			ContainerID: fixture.container.GetID(), ID: fixture.container.GetID(), Status: tasktypes.Status_RUNNING,
		}
		fixture.tasks.kill = func(*tasksapi.KillRequest) (*emptypb.Empty, error) {
			return nil, errContainerdTest
		}
		if err := fixture.backend.Remove(context.Background(), fixture.container.GetID(), true); err == nil {
			t.Fatal("Remove(force stop failure) succeeded")
		}
	})

	t.Run("restart running label", func(t *testing.T) {
		t.Parallel()
		fixture := testNativeManagedBackend(t)
		configureContainerUpdate(fixture)
		fixture.container.Labels[containerdRestartPolicyLabel] = testRestartPolicy
		if err := fixture.backend.setRestartState(
			context.Background(), fixture.container.GetID(), containerdRestartDesiredRunning, false,
		); err != nil ||
			fixture.container.Labels[containerdExplicitlyStoppedLabel] != containerdRestartNotExplicitlyStopped {
			t.Fatalf("setRestartState(running) = %v, %#v", err, fixture.container.Labels)
		}
	})
}
