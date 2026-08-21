package containerd

import (
	"context"
	"errors"
	"strings"
	"testing"

	containersapi "github.com/containerd/containerd/api/services/containers/v1"
	imagesapi "github.com/containerd/containerd/api/services/images/v1"
	introspectionapi "github.com/containerd/containerd/api/services/introspection/v1"
	leasesapi "github.com/containerd/containerd/api/services/leases/v1"
	snapshotsapi "github.com/containerd/containerd/api/services/snapshots/v1"
	tasksapi "github.com/containerd/containerd/api/services/tasks/v1"
	api "github.com/containerd/containerd/api/types"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
	"google.golang.org/protobuf/types/known/emptypb"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/domain"
)

//nolint:cyclop,funlen // The test verifies each emitted transaction identity in one success path.
func TestNativeCreateWorkloadTransaction(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	configuration := testContainerdConfiguration(t, desired)
	parent := domain.Hash([]byte("parent")).String()
	request := createWorkloadRequest{
		Workload: desired, Transaction: testWorkloadTransaction,
		Configuration: configuration, SnapshotParent: parent,
	}
	identifier := workloadIdentifier(desired.ContainerName, request.Transaction)
	host := &fakeWorkloadHost{mounted: true, prepared: preparedHostWorkload{Configuration: configuration}}
	network := &fakeWorkloadNetwork{digest: domain.Hash([]byte("network"))}
	created := 0
	leaseDeletes := 0
	backend := &nativeWorkloadBackendV1{
		containers: fakeContainersAPI{
			list: func(*containersapi.ListContainersRequest) (*containersapi.ListContainersResponse, error) {
				return &containersapi.ListContainersResponse{}, nil
			},
			get: func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
				return nil, status.Error(codes.NotFound, "missing")
			},
			create: func(value *containersapi.CreateContainerRequest) (*containersapi.CreateContainerResponse, error) {
				created++
				if value.GetContainer().GetID() != identifier ||
					value.GetContainer().GetLabels()[domain.LabelTransaction] != request.Transaction {
					t.Fatalf("Create request = %#v", value)
				}

				return &containersapi.CreateContainerResponse{Container: value.GetContainer()}, nil
			},
		},
		tasks: fakeTasksAPI{get: func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			return nil, status.Error(codes.NotFound, "missing")
		}},
		snapshots: fakeSnapshotsAPI{
			stat: func(*snapshotsapi.StatSnapshotRequest) (*snapshotsapi.StatSnapshotResponse, error) {
				return &snapshotsapi.StatSnapshotResponse{Info: &snapshotsapi.Info{
					Name: parent, Kind: snapshotsapi.Kind_COMMITTED,
				}}, nil
			},
			prepare: func(value *snapshotsapi.PrepareSnapshotRequest) (*snapshotsapi.PrepareSnapshotResponse, error) {
				if value.GetKey() != workloadSnapshotKey(identifier) || value.GetParent() != parent {
					t.Fatalf("Prepare request = %#v", value)
				}

				return &snapshotsapi.PrepareSnapshotResponse{}, nil
			},
		},
		leases: fakeLeasesAPI{
			create: func(value *leasesapi.CreateRequest) (*leasesapi.CreateResponse, error) {
				return &leasesapi.CreateResponse{Lease: &leasesapi.Lease{ID: value.GetID()}}, nil
			},
			delete: func(*leasesapi.DeleteRequest) (*emptypb.Empty, error) {
				leaseDeletes++

				return &emptypb.Empty{}, nil
			},
		},
		plugins: fakePluginsAPI{plugins: func(*introspectionapi.PluginsRequest) (*introspectionapi.PluginsResponse, error) {
			return &introspectionapi.PluginsResponse{Plugins: []*introspectionapi.Plugin{{
				Type: containerdSnapshotterPluginType, ID: defaultContainerdSnapshotter,
			}}}, nil
		}},
		images: fakeImagesClient{response: &imagesapi.GetImageResponse{Image: &imagesapi.Image{
			Name:   desired.Image.Reference,
			Target: &api.Descriptor{Digest: desired.Image.ReferenceDigest.String()},
		}}},
		options: DefaultWorkloadOptions(), network: network, host: host,
		platform: desired.Platform,
	}
	got, err := backend.Create(context.Background(), request)
	if err != nil || got != identifier || created != 1 || leaseDeletes != 1 ||
		host.prepareCalls != 1 || host.ensureCalls != 1 {
		t.Fatalf(
			"Create() = %q, %v, creates %d, lease deletes %d, host %d/%d",
			got, err, created, leaseDeletes, host.prepareCalls, host.ensureCalls,
		)
	}
}

func TestCreateWorkloadRequestValidation(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	request := createWorkloadRequest{
		Workload: desired, Transaction: testWorkloadTransaction,
		Configuration:  testContainerdConfiguration(t, desired),
		SnapshotParent: domain.Hash([]byte("parent")).String(),
	}
	if !validCreateWorkloadRequest(request) {
		t.Fatal("validCreateWorkloadRequest(valid) rejected")
	}
	scratch := request
	scratch.SnapshotParent = ""
	if !validCreateWorkloadRequest(scratch) {
		t.Fatal("validCreateWorkloadRequest(scratch) rejected")
	}
	mutations := []func(*createWorkloadRequest){
		func(value *createWorkloadRequest) { value.Transaction = "bad\x00transaction" },
		func(value *createWorkloadRequest) { value.Workload.ContainerName = "bad/name" },
		func(value *createWorkloadRequest) { value.SnapshotParent = testBadValue },
		func(value *createWorkloadRequest) { value.Configuration = containerdconfig.Configuration{} },
		func(value *createWorkloadRequest) { value.Workload.Labels = []string{testMissingValue} },
		func(value *createWorkloadRequest) { value.Workload.Labels = []string{domain.LabelService + "=value"} },
		func(value *createWorkloadRequest) {
			value.Workload.Labels = []string{containerdRestartPolicyLabel + "=value"}
		},
	}
	for _, mutate := range mutations {
		invalid := request
		mutate(&invalid)
		if validCreateWorkloadRequest(invalid) {
			t.Fatalf("validCreateWorkloadRequest(%#v) accepted", invalid)
		}
	}
}

func TestUserContainerLabelValidation(t *testing.T) {
	t.Parallel()

	if !validUserContainerLabels([]string{"example=value"}) ||
		validUserContainerLabels([]string{testMissingValue}) ||
		validUserContainerLabels([]string{containerNameLabel + "=bad"}) ||
		validUserContainerLabels([]string{containerdRestartPolicyLabel + "=always"}) {
		t.Fatal("validUserContainerLabels() policy drift")
	}
}

func TestCommittedSnapshotAndLeaseDeletionEvidence(t *testing.T) {
	t.Parallel()

	parent := domain.Hash([]byte("parent")).String()
	info := &snapshotsapi.Info{Name: parent, Kind: snapshotsapi.Kind_COMMITTED}
	backend := &nativeWorkloadBackendV1{
		snapshots: fakeSnapshotsAPI{stat: func(
			*snapshotsapi.StatSnapshotRequest,
		) (*snapshotsapi.StatSnapshotResponse, error) {
			return &snapshotsapi.StatSnapshotResponse{Info: info}, nil
		}},
		leases: fakeLeasesAPI{delete: func(*leasesapi.DeleteRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		}},
		options: DefaultWorkloadOptions(),
	}
	if err := backend.requireCommittedSnapshot(context.Background(), parent); err != nil {
		t.Fatalf("requireCommittedSnapshot() = %v", err)
	}
	if err := backend.requireCommittedSnapshot(context.Background(), ""); err != nil {
		t.Fatalf("requireCommittedSnapshot(scratch) = %v", err)
	}
	if err := backend.deleteLease(context.Background(), "lease"); err != nil {
		t.Fatalf("deleteLease() = %v", err)
	}

	info.Kind = snapshotsapi.Kind_ACTIVE
	if err := backend.requireCommittedSnapshot(context.Background(), parent); !errors.Is(err, ErrProtocol) {
		t.Fatalf("requireCommittedSnapshot(active) = %v", err)
	}
	backend.snapshots = fakeSnapshotsAPI{stat: func(
		*snapshotsapi.StatSnapshotRequest,
	) (*snapshotsapi.StatSnapshotResponse, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}}
	if err := backend.requireCommittedSnapshot(context.Background(), parent); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("requireCommittedSnapshot(missing) = %v", err)
	}
	backend.leases = fakeLeasesAPI{delete: func(*leasesapi.DeleteRequest) (*emptypb.Empty, error) {
		return nil, status.Error(codes.NotFound, "missing")
	}}
	if err := backend.deleteLease(context.Background(), "lease"); err != nil {
		t.Fatalf("deleteLease(missing) = %v", err)
	}
}

type nativeCreateFixture struct {
	backend    *nativeWorkloadBackendV1
	request    createWorkloadRequest
	host       *fakeWorkloadHost
	containers *fakeContainersAPI
	snapshots  *fakeSnapshotsAPI
	leases     *fakeLeasesAPI
}

//nolint:funlen // The fixture supplies one complete successful native create transaction.
func testNativeCreateFixture(t *testing.T) nativeCreateFixture {
	t.Helper()

	desired := testContainerdDesiredWorkload(t)
	configuration := testContainerdConfiguration(t, desired)
	parent := domain.Hash([]byte("parent")).String()
	request := createWorkloadRequest{
		Workload: desired, Transaction: testWorkloadTransaction,
		Configuration: configuration, SnapshotParent: parent,
	}
	host := &fakeWorkloadHost{mounted: true, prepared: preparedHostWorkload{Configuration: configuration}}
	network := &fakeWorkloadNetwork{digest: domain.Hash([]byte("network"))}
	containers := &fakeContainersAPI{
		list: func(*containersapi.ListContainersRequest) (*containersapi.ListContainersResponse, error) {
			return &containersapi.ListContainersResponse{}, nil
		},
		get: func(*containersapi.GetContainerRequest) (*containersapi.GetContainerResponse, error) {
			return nil, status.Error(codes.NotFound, "missing")
		},
		create: func(value *containersapi.CreateContainerRequest) (*containersapi.CreateContainerResponse, error) {
			return &containersapi.CreateContainerResponse{Container: value.GetContainer()}, nil
		},
		delete: func(*containersapi.DeleteContainerRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		},
	}
	snapshots := &fakeSnapshotsAPI{
		stat: func(*snapshotsapi.StatSnapshotRequest) (*snapshotsapi.StatSnapshotResponse, error) {
			return &snapshotsapi.StatSnapshotResponse{Info: &snapshotsapi.Info{
				Name: parent, Kind: snapshotsapi.Kind_COMMITTED,
			}}, nil
		},
		prepare: func(*snapshotsapi.PrepareSnapshotRequest) (*snapshotsapi.PrepareSnapshotResponse, error) {
			return &snapshotsapi.PrepareSnapshotResponse{}, nil
		},
		remove: func(*snapshotsapi.RemoveSnapshotRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		},
	}
	leases := &fakeLeasesAPI{
		create: func(value *leasesapi.CreateRequest) (*leasesapi.CreateResponse, error) {
			return &leasesapi.CreateResponse{Lease: &leasesapi.Lease{ID: value.GetID()}}, nil
		},
		delete: func(*leasesapi.DeleteRequest) (*emptypb.Empty, error) {
			return &emptypb.Empty{}, nil
		},
	}
	backend := &nativeWorkloadBackendV1{
		containers: containers,
		tasks: fakeTasksAPI{get: func(*tasksapi.GetRequest) (*tasksapi.GetResponse, error) {
			return nil, status.Error(codes.NotFound, "missing")
		}},
		snapshots: snapshots,
		leases:    leases,
		plugins: fakePluginsAPI{plugins: func(
			*introspectionapi.PluginsRequest,
		) (*introspectionapi.PluginsResponse, error) {
			return &introspectionapi.PluginsResponse{Plugins: []*introspectionapi.Plugin{{
				Type: containerdSnapshotterPluginType, ID: defaultContainerdSnapshotter,
			}}}, nil
		}},
		images: fakeImagesClient{response: &imagesapi.GetImageResponse{Image: &imagesapi.Image{
			Name: desired.Image.Reference, Target: &api.Descriptor{Digest: desired.Image.ReferenceDigest.String()},
		}}},
		options: DefaultWorkloadOptions(), network: network, host: host, platform: desired.Platform,
	}

	return nativeCreateFixture{
		backend: backend, request: request, host: host,
		containers: containers, snapshots: snapshots, leases: leases,
	}
}

//nolint:funlen // The table covers each native create transaction rollback boundary independently.
func TestNativeCreateWorkloadFailureMatrix(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*testing.T, *nativeCreateFixture)
	}{
		{name: testHostUnavailableCase, mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.backend.host = nil
		}},
		{name: "candidate read", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.containers.list = func(
				*containersapi.ListContainersRequest,
			) (*containersapi.ListContainersResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "existing ID read", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.containers.get = func(
				*containersapi.GetContainerRequest,
			) (*containersapi.GetContainerResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: testImageValue, mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.backend.images = fakeImagesClient{err: errContainerdTest}
		}},
		{name: "snapshot parent", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.snapshots.stat = func(
				*snapshotsapi.StatSnapshotRequest,
			) (*snapshotsapi.StatSnapshotResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "inspection", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.backend.plugins = fakePluginsAPI{plugins: func(
				*introspectionapi.PluginsRequest,
			) (*introspectionapi.PluginsResponse, error) {
				return nil, errContainerdTest
			}}
		}},
		{name: "restart support", mutate: func(t *testing.T, fixture *nativeCreateFixture) {
			t.Helper()

			fixture.request.Workload.Restart = testRestartPolicy
			fixture.request.Workload.EffectiveDigest = domain.ComputeEffectiveDigest(fixture.request.Workload)
			fixture.request.Configuration = testContainerdConfiguration(t, fixture.request.Workload)
		}},
		{name: "lease create", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.leases.create = func(*leasesapi.CreateRequest) (*leasesapi.CreateResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "lease protocol", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.leases.create = func(*leasesapi.CreateRequest) (*leasesapi.CreateResponse, error) {
				return &leasesapi.CreateResponse{}, nil
			}
		}},
		{name: "snapshot prepare", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.snapshots.prepare = func(
				*snapshotsapi.PrepareSnapshotRequest,
			) (*snapshotsapi.PrepareSnapshotResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "snapshot mount protocol", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.snapshots.prepare = func(
				*snapshotsapi.PrepareSnapshotRequest,
			) (*snapshotsapi.PrepareSnapshotResponse, error) {
				return &snapshotsapi.PrepareSnapshotResponse{Mounts: []*api.Mount{nil}}, nil
			}
		}},
		{name: "host preparation", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.host.prepareErr = errContainerdTest
		}},
		{name: "network namespace", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.host.ensureErr = errContainerdTest
		}},
		{name: "runtime spec", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.host.prepared.Configuration.OCI.Annotations = map[string]string{
				"large": strings.Repeat("x", maximumContainerExtensionBytes),
			}
		}},
		{name: "extension", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.host.prepared.RuntimeMounts = []domain.RuntimeMount{{
				Kind: domain.MountVolume, Name: strings.Repeat("x", maximumContainerExtensionBytes),
			}}
		}},
		{name: "container create", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.containers.create = func(
				*containersapi.CreateContainerRequest,
			) (*containersapi.CreateContainerResponse, error) {
				return nil, errContainerdTest
			}
		}},
		{name: "container protocol", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.containers.create = func(
				*containersapi.CreateContainerRequest,
			) (*containersapi.CreateContainerResponse, error) {
				return &containersapi.CreateContainerResponse{}, nil
			}
		}},
		{name: "lease delete", mutate: func(_ *testing.T, fixture *nativeCreateFixture) {
			fixture.leases.delete = func(*leasesapi.DeleteRequest) (*emptypb.Empty, error) {
				return nil, errContainerdTest
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			fixture := testNativeCreateFixture(t)
			test.mutate(t, &fixture)
			if _, err := fixture.backend.Create(context.Background(), fixture.request); err == nil {
				t.Fatal("Create() succeeded")
			}
		})
	}
}

func TestNativeCreateRejectsNilBackendAndRollsBackWithoutHost(t *testing.T) {
	t.Parallel()

	fixture := testNativeCreateFixture(t)
	if _, err := (*nativeWorkloadBackendV1)(nil).Create(
		context.Background(), fixture.request,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Create(nil) = %v", err)
	}
	fixture.backend.host = nil
	fixture.backend.rollbackCreate(context.Background(), testWorkloadName, nil)
}
