package containerd

import (
	"context"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"time"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	defaultContainerdRuntime     = "io.containerd.runc.v2"
	defaultContainerdSnapshotter = "overlayfs"
	defaultCNINetworkName        = "bridge"
	containerdPlatformOS         = "linux"
	containerdArchitectureAMD64  = "amd64"
	containerdArchitectureARM64  = "arm64"
	containerdRootFSType         = "layers"
	workloadOptionBasePathCount  = 4
)

// WorkloadOptions selects explicit host resources used by the containerd
// workload adapter. All paths refer to the host running the local daemon.
type WorkloadOptions struct {
	StateRoot            string
	NetworkNamespaceRoot string
	CNIConfigDirectory   string
	CNIPluginDirectories []string
	CNICacheDirectory    string
	CNINetworkName       string
	Runtime              string
	Snapshotter          string
}

// DefaultWorkloadOptions returns the conventional rootful containerd and CNI
// locations. Callers running rootless containerd should provide explicit
// writable state and network namespace roots.
func DefaultWorkloadOptions() WorkloadOptions {
	return WorkloadOptions{
		StateRoot:            "/var/lib/maniud/containerd",
		NetworkNamespaceRoot: "/run/maniud/netns",
		CNIConfigDirectory:   "/etc/cni/net.d",
		CNIPluginDirectories: []string{"/opt/cni/bin"},
		CNICacheDirectory:    "/var/lib/cni",
		CNINetworkName:       defaultCNINetworkName,
		Runtime:              defaultContainerdRuntime,
		Snapshotter:          defaultContainerdSnapshotter,
	}
}

func (options WorkloadOptions) valid() bool {
	paths := make([]string, 0, workloadOptionBasePathCount+len(options.CNIPluginDirectories))
	paths = append(paths,
		options.StateRoot,
		options.NetworkNamespaceRoot,
		options.CNIConfigDirectory,
		options.CNICacheDirectory,
	)
	paths = append(paths, options.CNIPluginDirectories...)
	if len(options.CNIPluginDirectories) == 0 || slices.ContainsFunc(paths, func(value string) bool {
		return !filepath.IsAbs(value) || filepath.Clean(value) != value || strings.ContainsRune(value, 0)
	}) {
		return false
	}

	return validNamespace(options.CNINetworkName) && validNamespace(options.Snapshotter) &&
		options.Runtime != "" && strings.TrimSpace(options.Runtime) == options.Runtime &&
		!strings.ContainsRune(options.Runtime, 0)
}

type workloadRuntimeInfo struct {
	Platform      domain.Platform
	Runtime       string
	Snapshotter   string
	NetworkDigest domain.Digest
	Restart       bool
}

type nativeWorkload struct {
	ID                   string
	Name                 string
	ImageReference       string
	ImageConfig          domain.Digest
	PlatformManifest     domain.Digest
	Configuration        containerdconfig.Configuration
	ConfigurationDigest  domain.Digest
	Ports                []domain.PortBinding
	RuntimeMounts        []domain.RuntimeMount
	ConfigurationMatches bool
	Lifecycle            application.WorkloadLifecycle
	Ownership            domain.WorkloadOwnership
}

type workloadCandidates struct {
	Named *nativeWorkload
	Owned *nativeWorkload
}

type createWorkloadRequest struct {
	Workload         domain.DesiredWorkload
	Transaction      string
	Configuration    containerdconfig.Configuration
	SnapshotParent   string
	CopyImageVolumes bool
}

type workloadReader interface {
	Inspect(ctx context.Context) (workloadRuntimeInfo, error)
	Candidates(ctx context.Context, name, service, transaction string) (workloadCandidates, error)
	Workload(ctx context.Context, identifier string) (*nativeWorkload, error)
	RemovalCandidate(ctx context.Context, identifier string) (*nativeWorkload, error)
	NameAvailable(ctx context.Context, name, exceptIdentifier string) (bool, error)
	RemovalComplete(ctx context.Context, identifier string) (bool, error)
}

type workloadMutator interface {
	Create(ctx context.Context, request createWorkloadRequest) (string, error)
	Start(ctx context.Context, identifier string) error
	Stop(ctx context.Context, identifier string, timeout time.Duration) error
	Rename(ctx context.Context, identifier, name string) error
	Remove(ctx context.Context, identifier string, force bool) error
}

type workloadArchiver interface {
	ArchiveStat(ctx context.Context, identifier, path string) (application.ArchivePathStat, error)
	ArchiveGet(
		ctx context.Context,
		identifier, path string,
		destination io.Writer,
		maximumBytes int64,
	) (application.ArchivePathStat, error)
	ArchivePut(ctx context.Context, identifier, path string, source io.Reader) error
}

type workloadBackend interface {
	workloadReader
	workloadMutator
	workloadArchiver
}

func validArchivePath(value string) bool {
	return value != "" && filepath.IsAbs(value) && filepath.Clean(value) == value &&
		!strings.ContainsRune(value, 0)
}

func archivePathStat(info os.FileInfo, linkTarget string) application.ArchivePathStat {
	return application.ArchivePathStat{
		Name:       info.Name(),
		Size:       info.Size(),
		Mode:       info.Mode(),
		ModTime:    info.ModTime(),
		LinkTarget: linkTarget,
	}
}
