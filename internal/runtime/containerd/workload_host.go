package containerd

import (
	"context"
	"errors"
	"fmt"
	"os"

	"github.com/containerd/containerd/v2/core/mount"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	privateDirectoryMode os.FileMode = 0o700
	privateFileMode      os.FileMode = 0o600
	bindMountType                    = "bind"
)

type preparedHostWorkload struct {
	Configuration containerdconfig.Configuration
	RuntimeMounts []domain.RuntimeMount
}

type workloadHost interface {
	Prepare(
		ctx context.Context,
		options WorkloadOptions,
		identifier string,
		configuration containerdconfig.Configuration,
		snapshotMounts []mount.Mount,
		copyImageVolumes bool,
	) (preparedHostWorkload, error)
	WithRootfs(ctx context.Context, mounts []mount.Mount, operation func(string) error) error
	EnsureNetworkNamespace(path string) error
	NetworkNamespaceMounted(path string) bool
	DeleteNetworkNamespace(path string) error
	Remove(options WorkloadOptions, identifier string, mounts []domain.RuntimeMount) error
	Absent(options WorkloadOptions, identifier string) (bool, error)
}

type localWorkloadHost struct{}

func (localWorkloadHost) Prepare(
	ctx context.Context,
	options WorkloadOptions,
	identifier string,
	configuration containerdconfig.Configuration,
	snapshotMounts []mount.Mount,
	copyImageVolumes bool,
) (preparedHostWorkload, error) {
	return prepareHostWorkload(
		ctx, options, identifier, configuration, snapshotMounts, copyImageVolumes,
		localWorkloadHost{}.WithRootfs,
	)
}

func (localWorkloadHost) WithRootfs(
	ctx context.Context,
	mounts []mount.Mount,
	operation func(string) error,
) error {
	return withTemporaryRootfs(ctx, mounts, operation, mount.WithTempMount)
}

func withTemporaryRootfs(
	ctx context.Context,
	mounts []mount.Mount,
	operation func(string) error,
	temporaryMount func(context.Context, []mount.Mount, func(string) error) error,
) error {
	if err := temporaryMount(privateContainerdLogContext(ctx), mounts, operation); err != nil {
		return fmt.Errorf("temporary rootfs mount: %w", err)
	}

	return nil
}

func (localWorkloadHost) EnsureNetworkNamespace(path string) error {
	return ensureNetworkNamespace(path)
}

func (localWorkloadHost) NetworkNamespaceMounted(path string) bool {
	return networkNamespaceMount(path)
}

func (localWorkloadHost) DeleteNetworkNamespace(path string) error {
	return deleteNetworkNamespace(path)
}

func (localWorkloadHost) Remove(
	options WorkloadOptions,
	identifier string,
	mounts []domain.RuntimeMount,
) error {
	return removeWorkloadState(options, identifier, mounts)
}

func (localWorkloadHost) Absent(options WorkloadOptions, identifier string) (bool, error) {
	for _, path := range []string{
		workloadStateDirectory(options, identifier),
		workloadNetworkNamespace(options, identifier),
	} {
		_, err := os.Lstat(path)
		if err == nil {
			return false, nil
		}
		if !errors.Is(err, os.ErrNotExist) {
			return false, ErrUnavailable
		}
	}

	return true, nil
}

func prepareHostWorkload(
	ctx context.Context,
	options WorkloadOptions,
	identifier string,
	configuration containerdconfig.Configuration,
	snapshotMounts []mount.Mount,
	copyImageVolumes bool,
	withRootfs func(context.Context, []mount.Mount, func(string) error) error,
) (preparedHostWorkload, error) {
	spec, err := containerdconfig.Decode(configuration)
	if err != nil {
		return preparedHostWorkload{}, ErrProtocol
	}
	stateDirectory := workloadStateDirectory(options, identifier)
	if err = secureDirectory(stateDirectory); err != nil {
		return preparedHostWorkload{}, err
	}
	runtimeMounts, createdVolumes, err := prepareRuntimeMounts(options, identifier, spec.Mounts)
	if err != nil {
		return preparedHostWorkload{}, err
	}
	complete := false
	defer func() {
		if complete {
			return
		}
		discardRuntimeMountSources(createdVolumes)
	}()
	if copyImageVolumes && len(createdVolumes) != 0 {
		if err = copyVolumeInitialContents(ctx, snapshotMounts, createdVolumes, withRootfs); err != nil {
			return preparedHostWorkload{}, err
		}
	}

	runtimeConfiguration, err := configureOwnedHostWorkload(
		spec, stateDirectory, workloadNetworkNamespace(options, identifier), runtimeMounts,
	)
	if err != nil {
		return preparedHostWorkload{}, err
	}
	complete = true

	return preparedHostWorkload{Configuration: runtimeConfiguration, RuntimeMounts: runtimeMounts}, nil
}

func configureOwnedHostWorkload(
	spec domain.WorkloadSpec,
	stateDirectory string,
	networkNamespace string,
	runtimeMounts []domain.RuntimeMount,
) (containerdconfig.Configuration, error) {
	configuration, err := containerdconfig.Encode(spec)
	if err != nil {
		return containerdconfig.Configuration{}, ErrProtocol
	}

	return configureHostWorkload(configuration, spec, stateDirectory, networkNamespace, runtimeMounts)
}

func configureHostWorkload(
	configuration containerdconfig.Configuration,
	spec domain.WorkloadSpec,
	stateDirectory string,
	networkNamespace string,
	runtimeMounts []domain.RuntimeMount,
) (containerdconfig.Configuration, error) {
	configuration.OCI.Mounts = appendRuntimeVolumeMounts(configuration.OCI.Mounts, runtimeMounts)
	var err error
	configuration.OCI.Mounts, err = appendGeneratedHostMounts(stateDirectory, configuration.OCI.Mounts, spec)
	if err != nil {
		return containerdconfig.Configuration{}, err
	}
	configuration.OCI.Linux.Namespaces, err = withNetworkNamespace(
		configuration.OCI.Linux.Namespaces, networkNamespace,
	)
	if err != nil {
		return containerdconfig.Configuration{}, err
	}
	if err = addHostDevices(&configuration.OCI, spec.Devices); err != nil {
		return containerdconfig.Configuration{}, err
	}

	return configuration, nil
}
