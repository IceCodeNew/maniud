package containerd

import (
	"context"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	leasesapi "github.com/containerd/containerd/api/services/leases/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"
)

type containersClient interface {
	Get(
		ctx context.Context,
		request *containersapi.GetContainerRequest,
		options ...grpc.CallOption,
	) (*containersapi.GetContainerResponse, error)
	List(
		ctx context.Context,
		request *containersapi.ListContainersRequest,
		options ...grpc.CallOption,
	) (*containersapi.ListContainersResponse, error)
	Create(
		ctx context.Context,
		request *containersapi.CreateContainerRequest,
		options ...grpc.CallOption,
	) (*containersapi.CreateContainerResponse, error)
	Update(
		ctx context.Context,
		request *containersapi.UpdateContainerRequest,
		options ...grpc.CallOption,
	) (*containersapi.UpdateContainerResponse, error)
	Delete(
		ctx context.Context,
		request *containersapi.DeleteContainerRequest,
		options ...grpc.CallOption,
	) (*emptypb.Empty, error)
}

type tasksClient interface {
	Create(
		ctx context.Context,
		request *tasksapi.CreateTaskRequest,
		options ...grpc.CallOption,
	) (*tasksapi.CreateTaskResponse, error)
	Get(ctx context.Context, request *tasksapi.GetRequest, options ...grpc.CallOption) (*tasksapi.GetResponse, error)
	Start(ctx context.Context, request *tasksapi.StartRequest, options ...grpc.CallOption) (*tasksapi.StartResponse, error)
	Kill(ctx context.Context, request *tasksapi.KillRequest, options ...grpc.CallOption) (*emptypb.Empty, error)
	Resume(ctx context.Context, request *tasksapi.ResumeTaskRequest, options ...grpc.CallOption) (*emptypb.Empty, error)
	Wait(ctx context.Context, request *tasksapi.WaitRequest, options ...grpc.CallOption) (*tasksapi.WaitResponse, error)
	Delete(
		ctx context.Context,
		request *tasksapi.DeleteTaskRequest,
		options ...grpc.CallOption,
	) (*tasksapi.DeleteResponse, error)
}

type snapshotsClient interface {
	Prepare(
		ctx context.Context,
		request *snapshotsapi.PrepareSnapshotRequest,
		options ...grpc.CallOption,
	) (*snapshotsapi.PrepareSnapshotResponse, error)
	Mounts(
		ctx context.Context,
		request *snapshotsapi.MountsRequest,
		options ...grpc.CallOption,
	) (*snapshotsapi.MountsResponse, error)
	Stat(
		ctx context.Context,
		request *snapshotsapi.StatSnapshotRequest,
		options ...grpc.CallOption,
	) (*snapshotsapi.StatSnapshotResponse, error)
	Remove(
		ctx context.Context,
		request *snapshotsapi.RemoveSnapshotRequest,
		options ...grpc.CallOption,
	) (*emptypb.Empty, error)
}

type leasesClient interface {
	Create(
		ctx context.Context,
		request *leasesapi.CreateRequest,
		options ...grpc.CallOption,
	) (*leasesapi.CreateResponse, error)
	Delete(ctx context.Context, request *leasesapi.DeleteRequest, options ...grpc.CallOption) (*emptypb.Empty, error)
}

type pluginClient interface {
	Plugins(
		ctx context.Context,
		request *introspectionapi.PluginsRequest,
		options ...grpc.CallOption,
	) (*introspectionapi.PluginsResponse, error)
}
