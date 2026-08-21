package containerd

import (
	"context"
	"errors"
	"reflect"
	"testing"

	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/types"

	"github.com/IceCodeNew/maniud/internal/domain"
)

type fakeNetworkExecutor struct {
	cached        []byte
	cachedRuntime *libcni.RuntimeConf
	plugins       []string
	cacheErr      error
	validateErr   error
	addErr        error
	checkErr      error
	deleteErr     error
	added         int
	checked       int
	deleted       int
}

//nolint:ireturn // The CNI interface requires the generic Result projection.
func (executor *fakeNetworkExecutor) AddNetworkList(
	context.Context,
	*libcni.NetworkConfigList,
	*libcni.RuntimeConf,
) (types.Result, error) {
	executor.added++

	return nil, executor.addErr
}

func (executor *fakeNetworkExecutor) CheckNetworkList(
	context.Context,
	*libcni.NetworkConfigList,
	*libcni.RuntimeConf,
) error {
	executor.checked++

	return executor.checkErr
}

func (executor *fakeNetworkExecutor) DelNetworkList(
	context.Context,
	*libcni.NetworkConfigList,
	*libcni.RuntimeConf,
) error {
	executor.deleted++

	return executor.deleteErr
}

func (executor *fakeNetworkExecutor) GetNetworkListCachedConfig(
	*libcni.NetworkConfigList,
	*libcni.RuntimeConf,
) ([]byte, *libcni.RuntimeConf, error) {
	return executor.cached, executor.cachedRuntime, executor.cacheErr
}

func (executor *fakeNetworkExecutor) ValidateNetworkList(
	context.Context,
	*libcni.NetworkConfigList,
) ([]string, error) {
	return executor.plugins, executor.validateErr
}

func testCNINetwork(executor networkExecutor) *cniNetwork {
	return &cniNetwork{
		configDirectory: "/etc/cni/net.d",
		networkName:     defaultCNINetworkName,
		executor:        executor,
		load: func(string, string) (*libcni.NetworkConfigList, error) {
			return &libcni.NetworkConfigList{
				Name: defaultCNINetworkName, CNIVersion: "1.0.0", Bytes: []byte(`{"name":"bridge"}`),
			}, nil
		},
	}
}

//nolint:cyclop // The test verifies complete CNI identity and port projection.
func TestCNINetworkInspectAndRuntimeProjection(t *testing.T) {
	t.Parallel()

	executor := &fakeNetworkExecutor{plugins: []string{defaultCNINetworkName, "portmap"}}
	network := testCNINetwork(executor)
	digest, err := network.Inspect(context.Background())
	if err != nil || digest == (domain.Digest{}) {
		t.Fatalf("Inspect() = %v, %v", digest, err)
	}
	networkNamespace := "/netns/" + testContainerValue
	configuration, runtime, err := network.runtimeConfiguration(
		testContainerValue,
		networkNamespace,
		[]domain.PortBinding{{HostIP: "127.0.0.1", PublishedPort: 8080, TargetPort: 80, Protocol: "tcp"}},
	)
	if err != nil || configuration.Name != defaultCNINetworkName || runtime.ContainerID != testContainerValue ||
		runtime.NetNS != networkNamespace || runtime.IfName != containerdNetworkInterface {
		t.Fatalf("runtimeConfiguration() = %#v, %#v, %v", configuration, runtime, err)
	}
	mappings, valid := runtime.CapabilityArgs["portMappings"].([]cniPortMapping)
	if !valid || len(mappings) != 1 || mappings[0].HostPort != 8080 || mappings[0].ContainerPort != 80 {
		t.Fatalf("runtimeConfiguration(port mappings) = %#v", runtime.CapabilityArgs)
	}
	first := slicesClone([]string{"a"})
	first[0] = "changed"
	if slicesClone(nil) != nil || reflect.DeepEqual(first, []string{"a"}) {
		t.Fatal("slicesClone() did not clone or preserve nil")
	}
	network.load = func(string, string) (*libcni.NetworkConfigList, error) {
		return nil, errContainerdTest
	}
	if _, err = network.Inspect(context.Background()); err == nil {
		t.Fatal("Inspect(configuration failure) succeeded")
	}
}

//nolint:cyclop,funlen // The table covers cached, new, invalid, and failed CNI lifecycle operations.
func TestCNINetworkLifecycleAndFailures(t *testing.T) {
	t.Parallel()

	executor := &fakeNetworkExecutor{plugins: []string{defaultCNINetworkName}}
	network := testCNINetwork(executor)
	ctx := context.Background()
	if err := network.Ensure(ctx, "container", "/netns/container", nil); err != nil ||
		executor.added != 1 || executor.checked != 1 {
		t.Fatalf("Ensure(new) = %v, add %d, check %d", err, executor.added, executor.checked)
	}
	executor.cached = []byte("cached")
	executor.cachedRuntime = &libcni.RuntimeConf{ContainerID: "container"}
	if err := network.Ensure(ctx, "container", "/netns/container", nil); err != nil || executor.added != 1 {
		t.Fatalf("Ensure(cached) = %v, add %d", err, executor.added)
	}
	if err := network.Check(ctx, "container", "/netns/container", nil); err != nil {
		t.Fatalf("Check() = %v", err)
	}
	if err := network.Delete(ctx, "container", "/netns/container", nil); err != nil || executor.deleted != 1 {
		t.Fatalf("Delete() = %v, calls %d", err, executor.deleted)
	}
	executor.cached = nil
	if err := network.Delete(ctx, "container", "/netns/container", nil); err != nil {
		t.Fatalf("Delete(missing) = %v", err)
	}
	absent, err := network.Absent(ctx, "container", "/netns/container")
	if err != nil || !absent {
		t.Fatalf("Absent() = %v, %v", absent, err)
	}

	executor.cacheErr = errContainerdTest
	if err := network.Ensure(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Ensure(cache failure) = %v", err)
	}
	if err := network.Check(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Check(cache failure) = %v", err)
	}
	if err := network.Delete(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Delete(cache failure) = %v", err)
	}
	if _, err := network.Absent(ctx, "container", "/netns/container"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Absent(cache failure) = %v", err)
	}
	executor.cacheErr = nil
	executor.addErr = errContainerdTest
	if err := network.Ensure(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Ensure(add failure) = %v", err)
	}
	executor.addErr = nil
	executor.cached = []byte("cached")
	executor.checkErr = errContainerdTest
	if err := network.Ensure(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Ensure(check failure) = %v", err)
	}
	if err := network.Check(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Check(check failure) = %v", err)
	}
	executor.checkErr = nil
	executor.cachedRuntime = nil
	if err := network.Delete(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Delete(missing runtime) = %v", err)
	}
	executor.cachedRuntime = &libcni.RuntimeConf{}
	executor.deleteErr = errContainerdTest
	if err := network.Delete(ctx, "container", "/netns/container", nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Delete(failure) = %v", err)
	}
}

func TestCNINetworkConfigurationRejectsIncompleteAndUnavailableInputs(t *testing.T) {
	t.Parallel()

	if _, err := (*cniNetwork)(nil).configuration(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("configuration(nil) = %v", err)
	}
	executor := &fakeNetworkExecutor{}
	network := testCNINetwork(executor)
	network.load = func(string, string) (*libcni.NetworkConfigList, error) {
		return nil, errContainerdTest
	}
	if _, err := network.configuration(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("configuration(load failure) = %v", err)
	}
	network = testCNINetwork(executor)
	network.load = func(string, string) (*libcni.NetworkConfigList, error) {
		return &libcni.NetworkConfigList{Name: testOtherValue, CNIVersion: "1.0.0", Bytes: []byte("x")}, nil
	}
	if _, err := network.configuration(); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("configuration(identity drift) = %v", err)
	}
	network = testCNINetwork(executor)
	if _, err := network.Inspect(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Inspect(no plugins) = %v", err)
	}
	executor.validateErr = errContainerdTest
	if _, err := network.Inspect(context.Background()); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Inspect(validation failure) = %v", err)
	}
	network.load = func(string, string) (*libcni.NetworkConfigList, error) {
		return nil, errContainerdTest
	}
	ctx := context.Background()
	for operation, err := range map[string]error{
		"ensure": network.Ensure(ctx, testContainerValue, "/netns/container", nil),
		"check":  network.Check(ctx, testContainerValue, "/netns/container", nil),
		"delete": network.Delete(ctx, testContainerValue, "/netns/container", nil),
	} {
		if !errors.Is(err, ErrUnavailable) {
			t.Fatalf("%s(configuration failure) = %v", operation, err)
		}
	}
	if _, err := network.Absent(ctx, testContainerValue, "/netns/container"); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Absent(configuration failure) = %v", err)
	}
}
