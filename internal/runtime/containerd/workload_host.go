package containerd

import (
	"context"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"path/filepath"
	"slices"
	"strings"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/archive"
	continuityfs "github.com/containerd/continuity/fs"
	containerdlog "github.com/containerd/log"
	"github.com/opencontainers/runtime-spec/specs-go"
	"github.com/sirupsen/logrus"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const maximumGeneratedHostFileBytes = 64 << 10

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
	if err := mount.WithTempMount(privateContainerdLogContext(ctx), mounts, operation); err != nil {
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
		for _, volume := range createdVolumes {
			_ = os.RemoveAll(volume.Source)
		}
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

func prepareRuntimeMounts(
	options WorkloadOptions,
	identifier string,
	mounts []domain.Mount,
) ([]domain.RuntimeMount, []domain.RuntimeMount, error) {
	if len(mounts) == 0 {
		return nil, nil, nil
	}
	result := make([]domain.RuntimeMount, len(mounts))
	created := make([]domain.RuntimeMount, 0, len(mounts))
	complete := false
	defer func() {
		if complete {
			return
		}
		for _, volume := range created {
			_ = os.RemoveAll(volume.Source)
		}
	}()
	for index, selected := range mounts {
		switch selected.Kind {
		case domain.MountBind:
			if !validHostPersistentSource(selected.Source) {
				return nil, nil, ErrUnsupportedWorkload
			}
			result[index] = domain.RuntimeMount{
				Kind: selected.Kind, Name: "", Source: selected.Source,
				Target: selected.Target, ReadOnly: selected.ReadOnly,
			}
		case domain.MountVolume:
			name := workloadVolumeName(identifier, selected.Target)
			source := filepath.Join(options.StateRoot, "volumes", name)
			wasCreated, err := secureNewDirectory(source)
			if err != nil || !wasCreated {
				return nil, nil, ErrProtocol
			}
			result[index] = domain.RuntimeMount{
				Kind: selected.Kind, Name: name, Source: source,
				Target: selected.Target, ReadOnly: false,
			}
			created = append(created, result[index])
		default:
			return nil, nil, ErrUnsupportedWorkload
		}
	}
	complete = true

	return result, created, nil
}

func validRuntimeMountEvidence(
	options WorkloadOptions,
	identifier string,
	mounts []domain.RuntimeMount,
) bool {
	return !slices.ContainsFunc(mounts, func(selected domain.RuntimeMount) bool {
		switch selected.Kind {
		case domain.MountBind:
			return selected.Name != "" || !validHostPersistentSource(selected.Source)
		case domain.MountVolume:
			expectedName := workloadVolumeName(identifier, selected.Target)

			return selected.Name != expectedName || selected.ReadOnly ||
				selected.Source != filepath.Join(options.StateRoot, "volumes", expectedName) ||
				!validHostPersistentSource(selected.Source)
		default:
			return true
		}
	})
}

//nolint:cyclop // Mount comparison rejects every unsupported or mismatched source property.
func runtimeMountsMatchConfiguration(
	options WorkloadOptions,
	identifier string,
	desired []domain.Mount,
	observed []domain.RuntimeMount,
) bool {
	if len(desired) == 0 && observed != nil {
		return false
	}
	if len(desired) != len(observed) {
		return false
	}
	byTarget := make(map[string]domain.RuntimeMount, len(observed))
	for _, selected := range observed {
		if _, duplicate := byTarget[selected.Target]; duplicate {
			return false
		}
		byTarget[selected.Target] = selected
	}
	for _, selected := range desired {
		actual, found := byTarget[selected.Target]
		if !found || actual.Kind != selected.Kind || actual.ReadOnly != selected.ReadOnly {
			return false
		}
		switch selected.Kind {
		case domain.MountBind:
			if actual.Name != "" || actual.Source != selected.Source {
				return false
			}
		case domain.MountVolume:
			name := workloadVolumeName(identifier, selected.Target)
			if selected.Source != "" || selected.ReadOnly || actual.Name != name ||
				actual.Source != filepath.Join(options.StateRoot, "volumes", name) {
				return false
			}
		default:
			return false
		}
	}

	return true
}

func validHostPersistentSource(value string) bool {
	if !filepath.IsAbs(value) || filepath.Clean(value) != value {
		return false
	}
	info, err := os.Lstat(value)
	if err != nil {
		return false
	}

	return info.Mode()&os.ModeSymlink == 0 &&
		(info.IsDir() || info.Mode().IsRegular())
}

func appendRuntimeVolumeMounts(
	mounts []specs.Mount,
	runtimeMounts []domain.RuntimeMount,
) []specs.Mount {
	result := slices.Clone(mounts)
	for _, selected := range runtimeMounts {
		if selected.Kind != domain.MountVolume {
			continue
		}
		result = append(result, specs.Mount{
			Destination: selected.Target, Type: bindMountType, Source: selected.Source,
			Options: []string{"rbind", "rw", "rprivate"},
		})
	}

	return result
}

func appendGeneratedHostMounts(
	stateDirectory string,
	mounts []specs.Mount,
	spec domain.WorkloadSpec,
) ([]specs.Mount, error) {
	files := []struct {
		name        string
		destination string
		content     []byte
	}{
		{name: "hostname", destination: "/etc/hostname", content: hostnameContent(spec.Hostname)},
		{name: "hosts", destination: "/etc/hosts", content: hostsContent(spec.Hostname, spec.ExtraHosts)},
		{name: "resolv.conf", destination: "/etc/resolv.conf", content: resolverContent(spec)},
	}
	result := slices.Clone(mounts)
	for _, file := range files {
		if file.content == nil {
			continue
		}
		if len(file.content) > maximumGeneratedHostFileBytes {
			return nil, ErrUnsupportedWorkload
		}
		source := filepath.Join(stateDirectory, file.name)
		if err := writePrivateFile(source, file.content); err != nil {
			return nil, err
		}
		result = append(result, specs.Mount{
			Destination: file.destination, Type: bindMountType, Source: source,
			Options: []string{bindMountType, "ro", "rprivate"},
		})
	}

	return result, nil
}

func hostnameContent(hostname string) []byte {
	if hostname == "" {
		return nil
	}

	return []byte(hostname + "\n")
}

func hostsContent(hostname string, extra []string) []byte {
	if hostname == "" && len(extra) == 0 {
		return nil
	}
	var output strings.Builder
	output.WriteString("127.0.0.1 localhost\n::1 localhost ip6-localhost ip6-loopback\n")
	if hostname != "" {
		output.WriteString("127.0.1.1 ")
		output.WriteString(hostname)
		output.WriteByte('\n')
	}
	for _, selected := range extra {
		name, address, found := strings.Cut(selected, "=")
		parsed, err := netip.ParseAddr(address)
		if !found || name == "" || err != nil || parsed.String() != address {
			return nil
		}
		output.WriteString(address)
		output.WriteByte(' ')
		output.WriteString(name)
		output.WriteByte('\n')
	}

	return []byte(output.String())
}

func resolverContent(spec domain.WorkloadSpec) []byte {
	if len(spec.DNS)+len(spec.DNSSearch)+len(spec.DNSOptions) == 0 {
		return nil
	}
	var output strings.Builder
	for _, address := range spec.DNS {
		output.WriteString("nameserver ")
		output.WriteString(address)
		output.WriteByte('\n')
	}
	if len(spec.DNSSearch) != 0 {
		output.WriteString("search ")
		output.WriteString(strings.Join(spec.DNSSearch, " "))
		output.WriteByte('\n')
	}
	if len(spec.DNSOptions) != 0 {
		output.WriteString("options ")
		output.WriteString(strings.Join(spec.DNSOptions, " "))
		output.WriteByte('\n')
	}

	return []byte(output.String())
}

func withNetworkNamespace(namespaces []specs.LinuxNamespace, path string) ([]specs.LinuxNamespace, error) {
	result := slices.Clone(namespaces)
	for index := range result {
		if result[index].Type == specs.NetworkNamespace {
			if result[index].Path != "" {
				return nil, ErrProtocol
			}
			result[index].Path = path

			return result, nil
		}
	}

	return nil, ErrProtocol
}

func copyVolumeInitialContents(
	ctx context.Context,
	mounts []mount.Mount,
	volumes []domain.RuntimeMount,
	withRootfs func(context.Context, []mount.Mount, func(string) error) error,
) error {
	return copyVolumeInitialContentsWith(ctx, mounts, volumes, withRootfs, os.Lstat)
}

func copyVolumeInitialContentsWith(
	ctx context.Context,
	mounts []mount.Mount,
	volumes []domain.RuntimeMount,
	withRootfs func(context.Context, []mount.Mount, func(string) error) error,
	lstat func(string) (os.FileInfo, error),
) error {
	if len(mounts) == 0 || withRootfs == nil {
		return ErrProtocol
	}
	private := privateContainerdLogContext(ctx)
	err := withRootfs(private, mounts, func(root string) error {
		for _, volume := range volumes {
			source, err := continuityfs.RootPath(root, volume.Target)
			if err != nil {
				return ErrProtocol
			}
			if _, err = lstat(source); errors.Is(err, os.ErrNotExist) {
				continue
			}
			if err != nil {
				return ErrUnavailable
			}
			reader := archive.Diff(private, "", source)
			_, applyErr := archive.Apply(private, volume.Source, reader)
			closeErr := reader.Close()
			if applyErr != nil || closeErr != nil {
				return ErrUnavailable
			}
		}

		return nil
	})
	if err != nil {
		return ErrUnavailable
	}

	return nil
}

func privateContainerdLogContext(ctx context.Context) context.Context {
	logger := logrus.New()
	logger.SetOutput(io.Discard)

	return containerdlog.WithLogger(ctx, logrus.NewEntry(logger))
}
