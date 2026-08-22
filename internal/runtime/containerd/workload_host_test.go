package containerd

import (
	"archive/tar"
	"bytes"
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/containerd/containerd/v2/pkg/archive"
	"github.com/opencontainers/runtime-spec/specs-go"

	containerdconfig "github.com/IceCodeNew/maniud/containerconfig/containerd"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func testHostWorkloadOptions(t *testing.T) WorkloadOptions {
	t.Helper()

	root := testHostDirectory(t)
	options := DefaultWorkloadOptions()
	options.StateRoot = filepath.Join(root, "state")
	options.NetworkNamespaceRoot = filepath.Join(root, "netns")
	options.CNIConfigDirectory = filepath.Join(root, "cni")
	options.CNIPluginDirectories = []string{filepath.Join(root, "plugins")}
	options.CNICacheDirectory = filepath.Join(root, "cache")

	return options
}

func testHostDirectory(t *testing.T) string {
	t.Helper()

	directory, err := filepath.EvalSymlinks(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}

	return directory
}

//nolint:cyclop,funlen // The test exercises host preparation and every mount identity boundary together.
func TestPrepareHostWorkloadAndRuntimeMountIdentity(t *testing.T) {
	t.Parallel()

	options := testHostWorkloadOptions(t)
	desired := testContainerdDesiredWorkload(t)
	desired.Hostname = testWorkloadService
	desired.ExtraHosts = []string{"database=192.0.2.10"}
	desired.DNS = []string{testDNSAddress}
	desired.DNSSearch = []string{"example.test"}
	desired.DNSOptions = []string{"ndots:1"}
	bindSource := t.TempDir()
	desired.Mounts = []domain.Mount{
		{Kind: domain.MountBind, Source: bindSource, Target: testDataMount, ReadOnly: true},
		{Kind: domain.MountVolume, Target: testStateMount},
	}
	configuration, err := containerdconfig.Encode(desired.WorkloadSpec)
	if err != nil {
		t.Fatal(err)
	}
	identifier := workloadIdentifier(desired.ContainerName, testWorkloadTransaction)
	prepared, err := prepareHostWorkload(
		context.Background(), options, identifier, configuration, nil, false,
		localWorkloadHost{}.WithRootfs,
	)
	if err != nil || len(prepared.RuntimeMounts) != 2 {
		t.Fatalf("prepareHostWorkload() = %#v, %v", prepared, err)
	}
	if !validRuntimeMountEvidence(options, identifier, prepared.RuntimeMounts) ||
		!runtimeMountsMatchConfiguration(options, identifier, desired.Mounts, prepared.RuntimeMounts) {
		t.Fatalf("runtime mount evidence rejected: %#v", prepared.RuntimeMounts)
	}
	if prepared.Configuration.Control.ServiceName != desired.ServiceName ||
		len(prepared.Configuration.Control.Mounts) != 2 || prepared.Configuration.OCI.Linux == nil {
		t.Fatalf("prepared configuration = %#v", prepared.Configuration)
	}
	source, sourceErr := containerdconfig.Decode(configuration)
	if sourceErr != nil || !reflect.DeepEqual(source, desired.WorkloadSpec) {
		t.Fatalf("source configuration mutated = %#v, %v", source, sourceErr)
	}
	for _, name := range []string{"hostname", "hosts", "resolv.conf"} {
		if _, statErr := os.Stat(filepath.Join(workloadStateDirectory(options, identifier), name)); statErr != nil {
			t.Fatalf("generated %s: %v", name, statErr)
		}
	}
	if _, err = prepareHostWorkload(
		context.Background(), options, identifier, containerdconfig.Configuration{}, nil, false,
		localWorkloadHost{}.WithRootfs,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("prepareHostWorkload(invalid) = %v", err)
	}
	configuration.OCI.Linux = nil
	if _, err = prepareHostWorkload(
		context.Background(), testHostWorkloadOptions(t), "other", configuration, nil, false,
		localWorkloadHost{}.WithRootfs,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("prepareHostWorkload(no Linux) = %v", err)
	}

	invalid := append([]domain.RuntimeMount(nil), prepared.RuntimeMounts...)
	invalid[0].Name = "unexpected"
	if validRuntimeMountEvidence(options, identifier, invalid) {
		t.Fatal("validRuntimeMountEvidence() accepted named bind")
	}
	invalid = append([]domain.RuntimeMount(nil), prepared.RuntimeMounts...)
	invalid[1].Source = testWrongPath
	if validRuntimeMountEvidence(options, identifier, invalid) {
		t.Fatal("validRuntimeMountEvidence() accepted wrong volume source")
	}
	if validRuntimeMountEvidence(options, identifier, []domain.RuntimeMount{{Kind: 255}}) {
		t.Fatal("validRuntimeMountEvidence() accepted unknown mount")
	}
	if runtimeMountsMatchConfiguration(options, identifier, desired.Mounts, nil) {
		t.Fatal("runtimeMountsMatchConfiguration() accepted missing mounts")
	}
	duplicate := append([]domain.RuntimeMount(nil), prepared.RuntimeMounts...)
	duplicate[1].Target = duplicate[0].Target
	if runtimeMountsMatchConfiguration(options, identifier, desired.Mounts, duplicate) {
		t.Fatal("runtimeMountsMatchConfiguration() accepted duplicate targets")
	}

	if err = removeWorkloadState(options, identifier, prepared.RuntimeMounts); err != nil {
		t.Fatalf("removeWorkloadState() = %v", err)
	}
	if _, err = os.Stat(prepared.RuntimeMounts[1].Source); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("anonymous volume survived removal: %v", err)
	}
}

//nolint:cyclop // The test covers valid files and every unsafe source or residual volume shape.
func TestPrepareRuntimeMountsRejectsUnsafeSourcesAndResidue(t *testing.T) {
	t.Parallel()

	options := testHostWorkloadOptions(t)
	identifier := testWorkloadName
	mounts, created, err := prepareRuntimeMounts(options, identifier, nil)
	if err != nil || mounts != nil || created != nil {
		t.Fatalf("prepareRuntimeMounts(nil) = %#v, %#v, %v", mounts, created, err)
	}
	if !runtimeMountsMatchConfiguration(options, identifier, nil, nil) ||
		runtimeMountsMatchConfiguration(options, identifier, nil, []domain.RuntimeMount{}) {
		t.Fatal("runtimeMountsMatchConfiguration() lost the no-mount evidence contract")
	}
	if _, _, err := prepareRuntimeMounts(options, identifier, []domain.Mount{{
		Kind: domain.MountBind, Source: testMissingPath, Target: testDataMount,
	}}); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("prepareRuntimeMounts(missing bind) = %v", err)
	}
	if _, _, err := prepareRuntimeMounts(options, identifier, []domain.Mount{{
		Kind: 255, Target: testDataMount,
	}}); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("prepareRuntimeMounts(unknown) = %v", err)
	}
	name := workloadVolumeName(identifier, testStateMount)
	path := filepath.Join(options.StateRoot, "volumes", name)
	if err := os.MkdirAll(path, 0o700); err != nil {
		t.Fatal(err)
	}
	if _, _, err := prepareRuntimeMounts(options, identifier, []domain.Mount{{
		Kind: domain.MountVolume, Target: testStateMount,
	}}); !errors.Is(err, ErrProtocol) {
		t.Fatalf("prepareRuntimeMounts(existing volume) = %v", err)
	}
	if validHostPersistentSource("relative") || validHostPersistentSource(testMissingPath) {
		t.Fatal("validHostPersistentSource() accepted unsafe input")
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil || !validHostPersistentSource(file) {
		t.Fatalf("validHostPersistentSource(file) = %v, %v", validHostPersistentSource(file), err)
	}
	symlink := filepath.Join(t.TempDir(), "link")
	if err := os.Symlink(file, symlink); err != nil || validHostPersistentSource(symlink) {
		t.Fatalf("validHostPersistentSource(symlink) = %v, %v", validHostPersistentSource(symlink), err)
	}
}

//nolint:cyclop // The test covers empty, valid, and invalid generated host configuration.
func TestHostConfigurationContentAndNamespaceProjection(t *testing.T) {
	t.Parallel()

	if hostnameContent("") != nil ||
		string(hostnameContent(testWorkloadService)) != testWorkloadService+"\n" {
		t.Fatal("hostnameContent() drifted")
	}
	if hostsContent("", nil) != nil || hostsContent("", []string{testBadValue}) != nil {
		t.Fatal("hostsContent() accepted an invalid empty or extra host set")
	}
	hosts := string(hostsContent(testWorkloadService, []string{"database=192.0.2.10"}))
	if !strings.Contains(hosts, "127.0.1.1 api") || !strings.Contains(hosts, "192.0.2.10 database") {
		t.Fatalf("hostsContent() = %q", hosts)
	}
	if resolverContent(domain.WorkloadSpec{}) != nil {
		t.Fatal("resolverContent(empty) returned content")
	}
	resolver := string(resolverContent(domain.WorkloadSpec{
		DNS: []string{testDNSAddress}, DNSSearch: []string{"example.test"}, DNSOptions: []string{"rotate"},
	}))
	if resolver != "nameserver 1.1.1.1\nsearch example.test\noptions rotate\n" {
		t.Fatalf("resolverContent() = %q", resolver)
	}

	namespaces := []specs.LinuxNamespace{{Type: specs.NetworkNamespace}}
	got, err := withNetworkNamespace(namespaces, "/netns/api")
	if err != nil || got[0].Path != "/netns/api" || namespaces[0].Path != "" {
		t.Fatalf("withNetworkNamespace() = %#v, %v", got, err)
	}
	if _, err = withNetworkNamespace(got, "/other"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("withNetworkNamespace(existing) = %v", err)
	}
	if _, err = withNetworkNamespace(nil, "/other"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("withNetworkNamespace(missing) = %v", err)
	}
}

//nolint:cyclop // The test covers generated mounts and private filesystem safety boundaries.
func TestGeneratedMountsAndPrivateFilesystemOperations(t *testing.T) {
	t.Parallel()

	root := testHostDirectory(t)
	spec := domain.WorkloadSpec{Hostname: testWorkloadService, DNS: []string{testDNSAddress}}
	base := []specs.Mount{{Destination: "/existing"}}
	mounts, err := appendGeneratedHostMounts(root, base, spec)
	if err != nil || len(mounts) != 4 || len(base) != 1 {
		t.Fatalf("appendGeneratedHostMounts() = %#v, %v", mounts, err)
	}
	volume := domain.RuntimeMount{Kind: domain.MountVolume, Source: "/volume", Target: testStateMount}
	got := appendRuntimeVolumeMounts(base, []domain.RuntimeMount{
		{Kind: domain.MountBind, Source: "/bind", Target: "/bind"}, volume,
	})
	if len(got) != 2 || got[1].Source != volume.Source || len(base) != 1 {
		t.Fatalf("appendRuntimeVolumeMounts() = %#v", got)
	}
	if err = writePrivateFile(filepath.Join(root, "nested", "file"), []byte("value")); err != nil {
		t.Fatalf("writePrivateFile() = %v", err)
	}
	created, err := secureNewDirectory(filepath.Join(root, "private"))
	if err != nil || !created {
		t.Fatalf("secureNewDirectory(new) = %v, %v", created, err)
	}
	created, err = secureNewDirectory(filepath.Join(root, "private"))
	if err != nil || created {
		t.Fatalf("secureNewDirectory(existing) = %v, %v", created, err)
	}
	if _, err = secureNewDirectory("relative"); !errors.Is(err, ErrProtocol) {
		t.Fatalf("secureNewDirectory(relative) = %v", err)
	}
	symlink := filepath.Join(root, "link")
	if err = os.Symlink(filepath.Join(root, "private"), symlink); err != nil {
		t.Fatal(err)
	}
	if _, err = secureNewDirectory(symlink); !errors.Is(err, ErrProtocol) {
		t.Fatalf("secureNewDirectory(symlink) = %v", err)
	}
	blockingFile := filepath.Join(root, "blocking")
	if err = os.WriteFile(blockingFile, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err = secureNewDirectory(filepath.Join(blockingFile, "child")); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("secureNewDirectory(blocked) = %v", err)
	}
	if err = writePrivateFile(filepath.Join(blockingFile, "child"), nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writePrivateFile(blocked) = %v", err)
	}
}

//nolint:cyclop,funlen // The test covers file, link, missing, bounded, short-write, and invalid tar paths.
func TestArchiveFilesystemRoundTripAndBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	file := filepath.Join(root, testFileName)
	if err := os.WriteFile(file, []byte("value"), 0o600); err != nil {
		t.Fatal(err)
	}
	stat, err := archiveStatPath(file)
	if err != nil || stat.Name != testFileName || stat.Size != 5 {
		t.Fatalf("archiveStatPath(file) = %#v, %v", stat, err)
	}
	if _, err = archiveStatPath(filepath.Join(root, "missing")); !errors.Is(err, application.ErrArchivePathMissing) {
		t.Fatalf("archiveStatPath(missing) = %v", err)
	}
	link := filepath.Join(root, "link")
	if err = os.Symlink("file", link); err != nil {
		t.Fatal(err)
	}
	if stat, err = archiveStatPath(link); err != nil || stat.LinkTarget != "file" {
		t.Fatalf("archiveStatPath(link) = %#v, %v", stat, err)
	}

	var archive bytes.Buffer
	if err = writePathArchive(context.Background(), file, &archive, 1<<20); err != nil {
		t.Fatalf("writePathArchive() = %v", err)
	}
	destination := t.TempDir()
	if err = applyPathArchive(context.Background(), destination, bytes.NewReader(archive.Bytes())); err != nil {
		t.Fatalf("applyPathArchive() = %v", err)
	}
	if _, err = os.Stat(filepath.Join(destination, testFileName)); err != nil {
		t.Fatalf("archive did not restore file: %v", err)
	}
	if err = writePathArchive(context.Background(), file, io.Discard, 1); !errors.Is(err, ErrProtocol) {
		t.Fatalf("writePathArchive(limit) = %v", err)
	}
	if err = writePathArchive(context.Background(), file, errorArchiveWriter{}, 1<<20); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writePathArchive(writer) = %v", err)
	}

	var bounded bytes.Buffer
	writer := &boundedArchiveWriter{destination: &bounded, remaining: 5}
	if written, writeErr := writer.Write([]byte("value")); writeErr != nil || written != 5 {
		t.Fatalf("boundedArchiveWriter.Write() = %d, %v", written, writeErr)
	}
	if _, writeErr := writer.Write([]byte("x")); !errors.Is(writeErr, ErrProtocol) || !writer.exceeded {
		t.Fatalf("boundedArchiveWriter.Write(limit) = %v", writeErr)
	}
	writer = &boundedArchiveWriter{destination: shortArchiveWriter{}, remaining: 5}
	if _, writeErr := writer.Write([]byte("value")); !errors.Is(writeErr, ErrUnavailable) {
		t.Fatalf("boundedArchiveWriter.Write(short) = %v", writeErr)
	}

	var malicious bytes.Buffer
	tarWriter := tar.NewWriter(&malicious)
	if err = tarWriter.WriteHeader(&tar.Header{Name: "../escape", Size: 0, Mode: 0o600}); err != nil {
		t.Fatal(err)
	}
	if err = tarWriter.Close(); err != nil {
		t.Fatal(err)
	}
	if err = applyPathArchive(context.Background(), destination, &malicious); !errors.Is(err, ErrProtocol) {
		t.Fatalf("applyPathArchive(malicious) = %v", err)
	}
	err = applyPathArchive(context.Background(), destination, strings.NewReader("invalid"))
	if !errors.Is(err, ErrProtocol) {
		t.Fatalf("applyPathArchive(invalid) = %v", err)
	}
}

type errorArchiveWriter struct{}

func (errorArchiveWriter) Write([]byte) (int, error) { return 0, errContainerdTest }

type shortArchiveWriter struct{}

func (shortArchiveWriter) Write(value []byte) (int, error) { return len(value) - 1, nil }

//nolint:cyclop // The test covers longest, exact, absent, and unsafe runtime mount paths.
func TestRuntimeArchivePathSelectsLongestMount(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	nested := filepath.Join(root, "nested")
	if err := os.Mkdir(nested, 0o700); err != nil {
		t.Fatal(err)
	}
	mounts := []domain.RuntimeMount{
		{Source: root, Target: "/data"},
		{Source: nested, Target: "/data/nested"},
	}
	path, found, err := runtimeArchivePath(mounts, "/data/nested/file")
	if err != nil || !found || path != filepath.Join(nested, "file") {
		t.Fatalf("runtimeArchivePath() = %q, %v, %v", path, found, err)
	}
	path, found, err = runtimeArchivePath([]domain.RuntimeMount{mounts[1], mounts[0]}, "/data/nested/file")
	if err != nil || !found || path != filepath.Join(nested, "file") {
		t.Fatalf("runtimeArchivePath(reverse) = %q, %v, %v", path, found, err)
	}
	path, found, err = runtimeArchivePath(mounts, "/data")
	if err != nil || !found || path != root {
		t.Fatalf("runtimeArchivePath(root) = %q, %v, %v", path, found, err)
	}
	if _, found, err = runtimeArchivePath(mounts, "/other"); err != nil || found {
		t.Fatalf("runtimeArchivePath(missing) = %v, %v", found, err)
	}
	if err = os.RemoveAll(nested); err != nil {
		t.Fatal(err)
	}
	if _, found, err = runtimeArchivePath(mounts, "/data/nested/file"); !found || !errors.Is(err, ErrProtocol) {
		t.Fatalf("runtimeArchivePath(missing source) = %v, %v", found, err)
	}
}

func TestRemoveWorkloadStateRejectsUnboundVolume(t *testing.T) {
	t.Parallel()

	options := testHostWorkloadOptions(t)
	if err := removeWorkloadState(options, testWorkloadService, []domain.RuntimeMount{{
		Kind: domain.MountVolume, Name: "volume", Source: testWrongPath,
	}}); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("removeWorkloadState(unbound) = %v", err)
	}
	if workloadStateDirectory(options, testWorkloadService) !=
		filepath.Join(options.StateRoot, "workloads", testWorkloadService) ||
		workloadNetworkNamespace(options, testWorkloadService) !=
			filepath.Join(options.NetworkNamespaceRoot, testWorkloadService) ||
		workloadVolumeName(testWorkloadService, testStateMount) ==
			workloadVolumeName(testWorkloadService, "/other") {
		t.Fatal("workload host path derivation drifted")
	}
	if got := appendRuntimeVolumeMounts(nil, nil); !reflect.DeepEqual(got, []specs.Mount(nil)) {
		t.Fatalf("appendRuntimeVolumeMounts(nil) = %#v", got)
	}
}

func TestCopyVolumeInitialContents(t *testing.T) {
	t.Parallel()

	rootfs := t.TempDir()
	source := filepath.Join(rootfs, "state")
	destination := t.TempDir()
	if err := os.Mkdir(source, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(source, testFileName), []byte(testChangedValue), privateFileMode,
	); err != nil {
		t.Fatal(err)
	}
	withRootfs := func(
		_ context.Context,
		_ []mount.Mount,
		operation func(string) error,
	) error {
		return operation(rootfs)
	}
	err := copyVolumeInitialContents(
		context.Background(), []mount.Mount{{Type: bindMountType, Source: rootfs}},
		[]domain.RuntimeMount{
			{Source: destination, Target: testStateMount},
			{Source: t.TempDir(), Target: testMissingPath},
		},
		withRootfs,
	)
	if err != nil {
		t.Fatalf("copyVolumeInitialContents() = %v", err)
	}
	//nolint:gosec // The path is derived entirely from this test's temporary directory.
	value, err := os.ReadFile(filepath.Join(destination, testFileName))
	if err != nil || string(value) != testChangedValue {
		t.Fatalf("copied contents = %q, %v", value, err)
	}
}

func TestLocalWorkloadHostProvesStateAbsence(t *testing.T) {
	t.Parallel()

	options := testHostWorkloadOptions(t)
	host := localWorkloadHost{}
	absent, err := host.Absent(options, testWorkloadName)
	if err != nil || !absent {
		t.Fatalf("Absent() = %v, %v", absent, err)
	}
	if err = os.MkdirAll(workloadStateDirectory(options, testWorkloadName), privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	absent, err = host.Absent(options, testWorkloadName)
	if err != nil || absent {
		t.Fatalf("Absent(present) = %v, %v", absent, err)
	}
	if err = host.Remove(options, testOtherValue, nil); err != nil {
		t.Fatalf("Remove(missing) = %v", err)
	}
}

func TestLocalWorkloadHostRejectsInvalidPreparationAndNamespace(t *testing.T) {
	t.Parallel()

	host := localWorkloadHost{}
	options := testHostWorkloadOptions(t)
	if _, err := host.Prepare(
		context.Background(), options, testWorkloadName, containerdconfig.Configuration{}, nil, false,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("Prepare(invalid) = %v", err)
	}
}

//nolint:cyclop,funlen // The matrix covers host preparation, rollback, and image-volume copy outcomes.
func TestPrepareHostWorkloadFailureMatrix(t *testing.T) {
	t.Parallel()

	desired := testContainerdDesiredWorkload(t)
	options := testHostWorkloadOptions(t)
	configuration := testContainerdConfiguration(t, desired)
	invalidOptions := options
	invalidOptions.StateRoot = testRelativePath
	if _, err := prepareHostWorkload(
		context.Background(), invalidOptions, testWorkloadName, configuration, nil, false,
		localWorkloadHost{}.WithRootfs,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("prepareHostWorkload(state root) = %v", err)
	}
	desired.Mounts = []domain.Mount{{Kind: domain.MountBind, Source: testMissingPath, Target: testDataMount}}
	configuration = testContainerdConfiguration(t, desired)
	if _, err := prepareHostWorkload(
		context.Background(), options, testWorkloadName, configuration, nil, false,
		localWorkloadHost{}.WithRootfs,
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("prepareHostWorkload(mount) = %v", err)
	}
	desired.Mounts = []domain.Mount{{Kind: domain.MountVolume, Target: testStateMount}}
	configuration = testContainerdConfiguration(t, desired)
	withRootfs := func(context.Context, []mount.Mount, func(string) error) error {
		return errContainerdTest
	}
	if _, err := prepareHostWorkload(
		context.Background(), options, testOtherValue, configuration,
		[]mount.Mount{{Type: bindMountType, Source: t.TempDir()}}, true, withRootfs,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("prepareHostWorkload(volume copy) = %v", err)
	}
	volumeSource := filepath.Join(
		options.StateRoot, "volumes", workloadVolumeName(testOtherValue, testStateMount),
	)
	if _, err := os.Stat(volumeSource); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("rolled-back volume remains: %v", err)
	}
	rootfs := t.TempDir()
	imageVolume := filepath.Join(rootfs, strings.TrimPrefix(testStateMount, "/"))
	if err := os.Mkdir(imageVolume, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(
		filepath.Join(imageVolume, testFileName), []byte(testChangedValue), privateFileMode,
	); err != nil {
		t.Fatal(err)
	}
	withRootfs = func(_ context.Context, _ []mount.Mount, operation func(string) error) error {
		return operation(rootfs)
	}
	prepared, err := prepareHostWorkload(
		context.Background(), options, "copied", configuration,
		[]mount.Mount{{Type: bindMountType, Source: rootfs}}, true, withRootfs,
	)
	if err != nil {
		t.Fatalf("prepareHostWorkload(copied volume) = %v", err)
	}
	value, err := os.ReadFile(filepath.Join(prepared.RuntimeMounts[0].Source, testFileName))
	if err != nil || string(value) != testChangedValue {
		t.Fatalf("prepareHostWorkload(copied volume) content = %q, %v", value, err)
	}
	if _, _, err := prepareRuntimeMounts(options, "cleanup", []domain.Mount{
		{Kind: domain.MountVolume, Target: testStateMount}, {Kind: 255, Target: testDataMount},
	}); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("prepareRuntimeMounts(partial) = %v", err)
	}
}

func TestConfigureHostWorkloadFailureMatrix(t *testing.T) {
	t.Parallel()

	if _, err := configureOwnedHostWorkload(
		domain.WorkloadSpec{}, t.TempDir(), testSourcePath, nil,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("configureOwnedHostWorkload(invalid) = %v", err)
	}
	desired := testContainerdDesiredWorkload(t)
	configuration := testContainerdConfiguration(t, desired)
	for index := range configuration.OCI.Linux.Namespaces {
		if configuration.OCI.Linux.Namespaces[index].Type == specs.NetworkNamespace {
			configuration.OCI.Linux.Namespaces[index].Path = testSourcePath
		}
	}
	if _, err := configureHostWorkload(
		configuration, desired.WorkloadSpec, t.TempDir(), testSourcePath, nil,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("configureHostWorkload(namespace) = %v", err)
	}

	configuration = testContainerdConfiguration(t, desired)
	desired.Hostname = testWorkloadService
	if _, err := configureHostWorkload(
		configuration, desired.WorkloadSpec, "/proc", testSourcePath, nil,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("configureHostWorkload(generated file) = %v", err)
	}

	configuration = testContainerdConfiguration(t, desired)
	file := filepath.Join(t.TempDir(), testFileName)
	if err := os.WriteFile(file, nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	desired.Hostname = ""
	desired.Devices = []domain.DeviceMapping{{Source: file, Target: "/dev/example", Permissions: "rw"}}
	if _, err := configureHostWorkload(
		configuration, desired.WorkloadSpec, t.TempDir(), testSourcePath, nil,
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("configureHostWorkload(device) = %v", err)
	}
	configuration = testContainerdConfiguration(t, desired)
	if _, err := prepareHostWorkload(
		context.Background(), testHostWorkloadOptions(t), testOtherValue, configuration, nil, false,
		localWorkloadHost{}.WithRootfs,
	); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("prepareHostWorkload(device) = %v", err)
	}
}

//nolint:cyclop // The test exercises each private-file and host-absence failure boundary.
func TestPrivateFileAndHostAbsenceFailures(t *testing.T) {
	t.Parallel()

	root := testHostDirectory(t)
	if err := writePrivateFileWith(
		filepath.Join(root, testFileName), nil,
		func(string, string) (*os.File, error) { return nil, errContainerdTest },
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writePrivateFile(create failure) = %v", err)
	}
	temporary, err := os.CreateTemp(t.TempDir(), "closed-*")
	if err != nil {
		t.Fatal(err)
	}
	if err = temporary.Close(); err != nil {
		t.Fatal(err)
	}
	if err = writePrivateFileContents(
		temporary, []byte("value"), filepath.Join(t.TempDir(), testFileName),
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writePrivateFileContents(closed) = %v", err)
	}

	loopRoot := t.TempDir()
	loop := filepath.Join(loopRoot, "loop")
	if err = os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	options := testHostWorkloadOptions(t)
	options.StateRoot = loop
	if _, err = (localWorkloadHost{}).Absent(options, testWorkloadName); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Absent(loop) = %v", err)
	}
	if err = removeWorkloadState(options, testWorkloadName, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("removeWorkloadState(loop) = %v", err)
	}

	validDirectory := t.TempDir()
	info, err := os.Lstat(validDirectory)
	if err != nil {
		t.Fatal(err)
	}
	if _, err = validateSecureDirectory(
		validDirectory, true, validDirectory, info, nil,
		func(string, os.FileMode) error { return errContainerdTest },
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("validateSecureDirectory(chmod) = %v", err)
	}
	if _, err = validateSecureDirectory(
		validDirectory, false, validDirectory, nil, errContainerdTest, os.Chmod,
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("validateSecureDirectory(stat) = %v", err)
	}
}

func TestHostMountComparisonAndGeneratedFileFailures(t *testing.T) {
	t.Parallel()

	options := testHostWorkloadOptions(t)
	identifier := testWorkloadName
	name := workloadVolumeName(identifier, testStateMount)
	volume := domain.RuntimeMount{
		Kind: domain.MountVolume, Name: name,
		Source: filepath.Join(options.StateRoot, "volumes", name), Target: testStateMount,
	}
	tests := []struct {
		desired  []domain.Mount
		observed []domain.RuntimeMount
	}{
		{desired: []domain.Mount{{Kind: domain.MountBind, Source: testSourcePath, Target: testDataMount}},
			observed: []domain.RuntimeMount{{Kind: domain.MountVolume, Target: testDataMount}}},
		{desired: []domain.Mount{{Kind: domain.MountBind, Source: testSourcePath, Target: testDataMount}},
			observed: []domain.RuntimeMount{{Kind: domain.MountBind, Name: testOtherValue, Target: testDataMount}}},
		{desired: []domain.Mount{{Kind: domain.MountVolume, Source: testSourcePath, Target: testStateMount}},
			observed: []domain.RuntimeMount{volume}},
		{desired: []domain.Mount{{Kind: 255, Target: testStateMount}}, observed: []domain.RuntimeMount{{
			Kind: 255, Target: testStateMount,
		}}},
	}
	for _, test := range tests {
		if runtimeMountsMatchConfiguration(options, identifier, test.desired, test.observed) {
			t.Fatalf("runtimeMountsMatchConfiguration(%#v, %#v) accepted", test.desired, test.observed)
		}
	}
	if _, err := appendGeneratedHostMounts(testHostDirectory(t), nil, domain.WorkloadSpec{
		Hostname: strings.Repeat("x", maximumGeneratedHostFileBytes),
	}); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("appendGeneratedHostMounts(large) = %v", err)
	}
	root := testHostDirectory(t)
	if err := writePrivateFile(root, nil); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writePrivateFile(directory target) = %v", err)
	}
}

func TestLocalWorkloadHostTemporaryRootfs(t *testing.T) {
	t.Parallel()

	called := false
	err := (localWorkloadHost{}).WithRootfs(context.Background(), nil, func(root string) error {
		called = filepath.IsAbs(root)

		return nil
	})
	if err != nil || !called {
		t.Fatalf("WithRootfs(empty) = %v, called %v", err, called)
	}
	err = withTemporaryRootfs(
		context.Background(), nil, func(string) error { return nil },
		func(context.Context, []mount.Mount, func(string) error) error { return errContainerdTest },
	)
	if !errors.Is(err, errContainerdTest) {
		t.Fatalf("withTemporaryRootfs(failure) = %v", err)
	}
}

func TestCopyVolumeInitialContentsRejectsUnsafeAndUnwritableTargets(t *testing.T) {
	t.Parallel()

	rootfs := t.TempDir()
	withRootfs := func(
		_ context.Context,
		_ []mount.Mount,
		operation func(string) error,
	) error {
		return operation(rootfs)
	}
	if err := copyVolumeInitialContents(
		context.Background(), []mount.Mount{{Type: bindMountType, Source: rootfs}},
		[]domain.RuntimeMount{{Source: t.TempDir(), Target: "bad\x00target"}}, withRootfs,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyVolumeInitialContents(unsafe) = %v", err)
	}
	source := filepath.Join(rootfs, "state")
	if err := os.Mkdir(source, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(source, testFileName), nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	destination := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(destination, nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	if err := copyVolumeInitialContents(
		context.Background(), []mount.Mount{{Type: bindMountType, Source: rootfs}},
		[]domain.RuntimeMount{{Source: destination, Target: testStateMount}}, withRootfs,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyVolumeInitialContents(unwritable) = %v", err)
	}
	if err := copyVolumeInitialContentsWith(
		context.Background(), []mount.Mount{{Type: bindMountType, Source: rootfs}},
		[]domain.RuntimeMount{{Source: t.TempDir(), Target: testStateMount}}, withRootfs,
		func(string) (os.FileInfo, error) { return nil, errContainerdTest },
		copyVolumeContents,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyVolumeInitialContents(stat failure) = %v", err)
	}
	if err := copyVolumeInitialContentsWith(
		context.Background(), []mount.Mount{{Type: bindMountType, Source: rootfs}},
		[]domain.RuntimeMount{{Source: t.TempDir(), Target: testStateMount}}, withRootfs,
		os.Lstat, func(context.Context, string, string) error { return errContainerdTest },
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyVolumeInitialContents(copy failure) = %v", err)
	}
}

func TestCopyVolumeContentsClassifiesArchiveFailures(t *testing.T) {
	t.Parallel()

	diff := func(context.Context, string, string, ...archive.WriteDiffOpt) io.ReadCloser {
		return containerdTestReadCloser{Reader: strings.NewReader("archive")}
	}
	apply := func(context.Context, string, io.Reader, ...archive.ApplyOpt) (int64, error) {
		return 0, nil
	}
	if err := copyVolumeContentsWith(context.Background(), "source", "destination", diff, apply); err != nil {
		t.Fatalf("copyVolumeContentsWith() = %v", err)
	}
	apply = func(context.Context, string, io.Reader, ...archive.ApplyOpt) (int64, error) {
		return 0, errContainerdTest
	}
	if err := copyVolumeContentsWith(
		context.Background(), "source", "destination", diff, apply,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyVolumeContentsWith(apply failure) = %v", err)
	}
	diff = func(context.Context, string, string, ...archive.WriteDiffOpt) io.ReadCloser {
		return containerdTestReadCloser{Reader: strings.NewReader("archive"), closeErr: errContainerdTest}
	}
	apply = func(context.Context, string, io.Reader, ...archive.ApplyOpt) (int64, error) {
		return 0, nil
	}
	if err := copyVolumeContentsWith(
		context.Background(), "source", "destination", diff, apply,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyVolumeContentsWith(close failure) = %v", err)
	}
}

type containerdTestReadCloser struct {
	io.Reader

	closeErr error
}

func (reader containerdTestReadCloser) Close() error { return reader.closeErr }

func TestArchiveFilesystemCancellationAndDirectoryTarget(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	file := filepath.Join(t.TempDir(), testFileName)
	if err := os.WriteFile(file, nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	if err := writePathArchive(ctx, file, io.Discard, 1<<20); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writePathArchive(cancelled) = %v", err)
	}
	if err := writePathArchive(context.Background(), ".", io.Discard, 1<<20); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("writePathArchive(relative root) = %v", err)
	}
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveStatPath(filepath.Join(loop, testFileName)); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("archiveStatPath(stat failure) = %v", err)
	}
	link := filepath.Join(root, "link")
	if err := os.Symlink(testFileName, link); err != nil {
		t.Fatal(err)
	}
	info, err := os.Lstat(link)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Remove(link); err != nil {
		t.Fatal(err)
	}
	if _, err := archiveStatInfo(link, info); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("archiveStatInfo(stale link) = %v", err)
	}
}
