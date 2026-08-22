package containerd

import (
	"context"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	leasesapi "github.com/containerd/containerd/api/services/leases/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	"github.com/containerd/containerd/v2/core/mount"
	"google.golang.org/grpc"
	"google.golang.org/protobuf/types/known/emptypb"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/domain"
)

type fakeContainersAPI struct {
	get    func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error)
	list   func(*containersapi.ListContainersRequest) (*containersapi.ListContainersResponse, error)
	create func(*containersapi.CreateContainerRequest) (*containersapi.CreateContainerResponse, error)
	update func(*containersapi.UpdateContainerRequest) (*containersapi.UpdateContainerResponse, error)
	delete func(*containersapi.DeleteContainerRequest) (*emptypb.Empty, error)
}

func (client fakeContainersAPI) Get(
	_ context.Context,
	request *containersapi.GetContainerRequest,
	_ ...grpc.CallOption,
) (*containersapi.GetContainerResponse, error) {
	return client.get(request)
}

func (client fakeContainersAPI) List(
	_ context.Context,
	request *containersapi.ListContainersRequest,
	_ ...grpc.CallOption,
) (*containersapi.ListContainersResponse, error) {
	return client.list(request)
}

func (client fakeContainersAPI) Create(
	_ context.Context,
	request *containersapi.CreateContainerRequest,
	_ ...grpc.CallOption,
) (*containersapi.CreateContainerResponse, error) {
	return client.create(request)
}

func (client fakeContainersAPI) Update(
	_ context.Context,
	request *containersapi.UpdateContainerRequest,
	_ ...grpc.CallOption,
) (*containersapi.UpdateContainerResponse, error) {
	return client.update(request)
}

func (client fakeContainersAPI) Delete(
	_ context.Context,
	request *containersapi.DeleteContainerRequest,
	_ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	return client.delete(request)
}

type fakeTasksAPI struct {
	create func(*tasksapi.CreateTaskRequest) (*tasksapi.CreateTaskResponse, error)
	get    func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error)
	start  func(*tasksapi.StartRequest) (*tasksapi.StartResponse, error)
	kill   func(*tasksapi.KillRequest) (*emptypb.Empty, error)
	resume func(*tasksapi.ResumeTaskRequest) (*emptypb.Empty, error)
	wait   func(*tasksapi.WaitRequest) (*tasksapi.WaitResponse, error)
	delete func(*tasksapi.DeleteTaskRequest) (*tasksapi.DeleteResponse, error)
}

func (client fakeTasksAPI) Create(
	_ context.Context,
	request *tasksapi.CreateTaskRequest,
	_ ...grpc.CallOption,
) (*tasksapi.CreateTaskResponse, error) {
	return client.create(request)
}

func (client fakeTasksAPI) Get(
	_ context.Context,
	request *tasksapi.GetRequest,
	_ ...grpc.CallOption,
) (*tasksapi.GetResponse, error) {
	return client.get(request)
}

func (client fakeTasksAPI) Start(
	_ context.Context,
	request *tasksapi.StartRequest,
	_ ...grpc.CallOption,
) (*tasksapi.StartResponse, error) {
	return client.start(request)
}

func (client fakeTasksAPI) Kill(
	_ context.Context,
	request *tasksapi.KillRequest,
	_ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	return client.kill(request)
}

func (client fakeTasksAPI) Resume(
	_ context.Context,
	request *tasksapi.ResumeTaskRequest,
	_ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	return client.resume(request)
}

func (client fakeTasksAPI) Wait(
	_ context.Context,
	request *tasksapi.WaitRequest,
	_ ...grpc.CallOption,
) (*tasksapi.WaitResponse, error) {
	return client.wait(request)
}

func (client fakeTasksAPI) Delete(
	_ context.Context,
	request *tasksapi.DeleteTaskRequest,
	_ ...grpc.CallOption,
) (*tasksapi.DeleteResponse, error) {
	return client.delete(request)
}

type fakeSnapshotsAPI struct {
	prepare func(*snapshotsapi.PrepareSnapshotRequest) (*snapshotsapi.PrepareSnapshotResponse, error)
	mounts  func(*snapshotsapi.MountsRequest) (*snapshotsapi.MountsResponse, error)
	stat    func(*snapshotsapi.StatSnapshotRequest) (*snapshotsapi.StatSnapshotResponse, error)
	remove  func(*snapshotsapi.RemoveSnapshotRequest) (*emptypb.Empty, error)
}

func (client fakeSnapshotsAPI) Prepare(
	_ context.Context,
	request *snapshotsapi.PrepareSnapshotRequest,
	_ ...grpc.CallOption,
) (*snapshotsapi.PrepareSnapshotResponse, error) {
	return client.prepare(request)
}

func (client fakeSnapshotsAPI) Mounts(
	_ context.Context,
	request *snapshotsapi.MountsRequest,
	_ ...grpc.CallOption,
) (*snapshotsapi.MountsResponse, error) {
	return client.mounts(request)
}

func (client fakeSnapshotsAPI) Stat(
	_ context.Context,
	request *snapshotsapi.StatSnapshotRequest,
	_ ...grpc.CallOption,
) (*snapshotsapi.StatSnapshotResponse, error) {
	return client.stat(request)
}

func (client fakeSnapshotsAPI) Remove(
	_ context.Context,
	request *snapshotsapi.RemoveSnapshotRequest,
	_ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	return client.remove(request)
}

type fakeLeasesAPI struct {
	create func(*leasesapi.CreateRequest) (*leasesapi.CreateResponse, error)
	delete func(*leasesapi.DeleteRequest) (*emptypb.Empty, error)
}

func (client fakeLeasesAPI) Create(
	_ context.Context,
	request *leasesapi.CreateRequest,
	_ ...grpc.CallOption,
) (*leasesapi.CreateResponse, error) {
	return client.create(request)
}

func (client fakeLeasesAPI) Delete(
	_ context.Context,
	request *leasesapi.DeleteRequest,
	_ ...grpc.CallOption,
) (*emptypb.Empty, error) {
	return client.delete(request)
}

type fakePluginsAPI struct {
	plugins func(*introspectionapi.PluginsRequest) (*introspectionapi.PluginsResponse, error)
}

func (client fakePluginsAPI) Plugins(
	_ context.Context,
	request *introspectionapi.PluginsRequest,
	_ ...grpc.CallOption,
) (*introspectionapi.PluginsResponse, error) {
	return client.plugins(request)
}

type fakeWorkloadNetwork struct {
	digest      domain.Digest
	inspectErr  error
	ensureErr   error
	checkErr    error
	deleteErr   error
	absentErr   error
	absent      bool
	ensureCalls int
	checkCalls  int
	deleteCalls int
	absentCalls int
}

type fakeWorkloadHost struct {
	prepared     preparedHostWorkload
	prepareErr   error
	rootfs       string
	rootfsErr    error
	ensureErr    error
	deleteErr    error
	removeErr    error
	absentErr    error
	mounted      bool
	absent       bool
	prepareCalls int
	ensureCalls  int
	deleteCalls  int
	removeCalls  int
	absentCalls  int
}

func (host *fakeWorkloadHost) WithRootfs(
	_ context.Context,
	_ []mount.Mount,
	operation func(string) error,
) error {
	if host.rootfsErr != nil {
		return host.rootfsErr
	}

	return operation(host.rootfs)
}

func (host *fakeWorkloadHost) Prepare(
	context.Context,
	WorkloadOptions,
	string,
	containerdconfig.Configuration,
	[]mount.Mount,
	bool,
) (preparedHostWorkload, error) {
	host.prepareCalls++

	return host.prepared, host.prepareErr
}

func (host *fakeWorkloadHost) EnsureNetworkNamespace(string) error {
	host.ensureCalls++

	return host.ensureErr
}

func (host *fakeWorkloadHost) NetworkNamespaceMounted(string) bool {
	return host.mounted
}

func (host *fakeWorkloadHost) DeleteNetworkNamespace(string) error {
	host.deleteCalls++

	return host.deleteErr
}

func (host *fakeWorkloadHost) Remove(WorkloadOptions, string, []domain.RuntimeMount) error {
	host.removeCalls++

	return host.removeErr
}

func (host *fakeWorkloadHost) Absent(WorkloadOptions, string) (bool, error) {
	host.absentCalls++

	return host.absent, host.absentErr
}

func (network *fakeWorkloadNetwork) Inspect(context.Context) (domain.Digest, error) {
	return network.digest, network.inspectErr
}

func (network *fakeWorkloadNetwork) Ensure(context.Context, string, string, []domain.PortBinding) error {
	network.ensureCalls++

	return network.ensureErr
}

func (network *fakeWorkloadNetwork) Check(context.Context, string, string, []domain.PortBinding) error {
	network.checkCalls++

	return network.checkErr
}

func (network *fakeWorkloadNetwork) Delete(context.Context, string, string, []domain.PortBinding) error {
	network.deleteCalls++

	return network.deleteErr
}

func (network *fakeWorkloadNetwork) Absent(context.Context, string, string) (bool, error) {
	network.absentCalls++

	return network.absent, network.absentErr
}
