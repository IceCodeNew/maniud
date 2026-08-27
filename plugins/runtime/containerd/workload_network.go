package containerd

import (
	"context"
	"io"
	"slices"

	"github.com/containernetworking/cni/libcni"
	"github.com/containernetworking/cni/pkg/invoke"
	"github.com/containernetworking/cni/pkg/types"
	"github.com/containernetworking/cni/pkg/version"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	containerdNetworkInterface = "eth0"
	cniPortMappingsCapability  = "portMappings"
)

type networkExecutor interface {
	AddNetworkList(
		ctx context.Context,
		configuration *libcni.NetworkConfigList,
		runtime *libcni.RuntimeConf,
	) (types.Result, error)
	CheckNetworkList(ctx context.Context, configuration *libcni.NetworkConfigList, runtime *libcni.RuntimeConf) error
	DelNetworkList(ctx context.Context, configuration *libcni.NetworkConfigList, runtime *libcni.RuntimeConf) error
	GetNetworkListCachedConfig(
		configuration *libcni.NetworkConfigList,
		runtime *libcni.RuntimeConf,
	) ([]byte, *libcni.RuntimeConf, error)
	ValidateNetworkList(ctx context.Context, configuration *libcni.NetworkConfigList) ([]string, error)
}

type workloadNetwork interface {
	Inspect(ctx context.Context) (domain.Digest, error)
	Ensure(ctx context.Context, identifier, netNS string, ports []domain.PortBinding) error
	Check(ctx context.Context, identifier, netNS string, ports []domain.PortBinding) error
	Delete(ctx context.Context, identifier, netNS string, ports []domain.PortBinding) error
	Absent(ctx context.Context, identifier, netNS string) (bool, error)
}

type cniNetwork struct {
	configDirectory string
	networkName     string
	executor        networkExecutor
	load            func(string, string) (*libcni.NetworkConfigList, error)
}

type cniPortMapping struct {
	//nolint:tagliatelle // CNI defines this capability key in camel case.
	HostPort int `json:"hostPort"`
	//nolint:tagliatelle // CNI defines this capability key in camel case.
	ContainerPort int    `json:"containerPort"`
	Protocol      string `json:"protocol"`
	//nolint:tagliatelle // CNI defines this capability key in camel case.
	HostIP string `json:"hostIP,omitempty"`
}

func newCNINetwork(options WorkloadOptions) *cniNetwork {
	executor := &invoke.DefaultExec{
		RawExec:       &invoke.RawExec{Stderr: io.Discard},
		PluginDecoder: version.PluginDecoder{},
	}

	return &cniNetwork{
		configDirectory: options.CNIConfigDirectory,
		networkName:     options.CNINetworkName,
		executor: libcni.NewCNIConfigWithCacheDir(
			slices.Clone(options.CNIPluginDirectories), options.CNICacheDirectory, executor,
		),
		load: libcni.LoadNetworkConf,
	}
}

func (network *cniNetwork) Inspect(ctx context.Context) (domain.Digest, error) {
	configuration, err := network.configuration()
	if err != nil {
		return domain.Digest{}, err
	}
	capabilities, err := network.executor.ValidateNetworkList(ctx, configuration)
	if err != nil || !slices.Contains(capabilities, cniPortMappingsCapability) {
		return domain.Digest{}, ErrUnavailable
	}
	capabilities = slices.Clone(capabilities)
	slices.Sort(capabilities)
	value := appendContainerdString([]byte{1}, configuration.Name)
	value = appendContainerdString(value, configuration.CNIVersion)
	value = appendContainerdString(value, string(configuration.Bytes))
	for _, capability := range capabilities {
		value = appendContainerdString(value, capability)
	}

	return domain.Hash(value), nil
}

func (network *cniNetwork) Ensure(
	ctx context.Context,
	identifier string,
	netNS string,
	ports []domain.PortBinding,
) error {
	configuration, runtime, err := network.runtimeConfiguration(identifier, netNS, ports)
	if err != nil {
		return err
	}
	cached, _, err := network.executor.GetNetworkListCachedConfig(configuration, runtime)
	if err != nil {
		return ErrProtocol
	}
	if cached == nil {
		if _, err = network.executor.AddNetworkList(ctx, configuration, runtime); err != nil {
			return ErrUnavailable
		}
	}
	if err = network.executor.CheckNetworkList(ctx, configuration, runtime); err != nil {
		return ErrProtocol
	}

	return nil
}

func (network *cniNetwork) Check(
	ctx context.Context,
	identifier string,
	netNS string,
	ports []domain.PortBinding,
) error {
	configuration, runtime, err := network.runtimeConfiguration(identifier, netNS, ports)
	if err != nil {
		return err
	}
	cached, _, err := network.executor.GetNetworkListCachedConfig(configuration, runtime)
	if err != nil || cached == nil {
		return ErrProtocol
	}
	if err = network.executor.CheckNetworkList(ctx, configuration, runtime); err != nil {
		return ErrProtocol
	}

	return nil
}

func (network *cniNetwork) Delete(
	ctx context.Context,
	identifier string,
	netNS string,
	ports []domain.PortBinding,
) error {
	configuration, runtime, err := network.runtimeConfiguration(identifier, netNS, ports)
	if err != nil {
		return err
	}
	cached, cachedRuntime, err := network.executor.GetNetworkListCachedConfig(configuration, runtime)
	if err != nil {
		return ErrProtocol
	}
	if cached == nil {
		return nil
	}
	if cachedRuntime == nil || network.executor.DelNetworkList(ctx, configuration, cachedRuntime) != nil {
		return ErrUnavailable
	}

	return nil
}

func (network *cniNetwork) Absent(
	_ context.Context,
	identifier string,
	netNS string,
) (bool, error) {
	configuration, runtime, err := network.runtimeConfiguration(identifier, netNS, nil)
	if err != nil {
		return false, err
	}
	cached, _, err := network.executor.GetNetworkListCachedConfig(configuration, runtime)
	if err != nil {
		return false, ErrProtocol
	}

	return cached == nil, nil
}

func (network *cniNetwork) runtimeConfiguration(
	identifier string,
	netNS string,
	ports []domain.PortBinding,
) (*libcni.NetworkConfigList, *libcni.RuntimeConf, error) {
	configuration, err := network.configuration()
	if err != nil {
		return nil, nil, err
	}
	mappings := make([]cniPortMapping, len(ports))
	for index, port := range ports {
		mappings[index] = cniPortMapping{
			HostPort: int(port.PublishedPort), ContainerPort: int(port.TargetPort),
			Protocol: port.Protocol, HostIP: port.HostIP,
		}
	}
	runtime := &libcni.RuntimeConf{
		ContainerID: identifier,
		NetNS:       netNS,
		IfName:      containerdNetworkInterface,
		Args:        [][2]string{{"IgnoreUnknown", "1"}},
		CapabilityArgs: map[string]any{
			"portMappings": mappings,
		},
		CacheDir: "",
	}

	return configuration, runtime, nil
}

//nolint:cyclop // CNI configuration is accepted only when each required identity field is present.
func (network *cniNetwork) configuration() (*libcni.NetworkConfigList, error) {
	if network == nil || network.executor == nil || network.load == nil ||
		network.configDirectory == "" || network.networkName == "" {
		return nil, ErrUnavailable
	}
	configuration, err := network.load(network.configDirectory, network.networkName)
	if err != nil || configuration == nil || configuration.Name != network.networkName ||
		configuration.CNIVersion == "" || len(configuration.Bytes) == 0 {
		return nil, ErrUnavailable
	}

	return configuration, nil
}
