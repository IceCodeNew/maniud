//go:build linux

package containerd

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"syscall"
	"testing"

	"github.com/containerd/containerd/v2/core/mount"
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/internal/domain"
)

type fileInfoWithSystem struct {
	os.FileInfo

	system any
}

func (info fileInfoWithSystem) Sys() any { return info.system }

//nolint:cyclop,funlen // The test covers device projection and privileged namespace fail-closed boundaries.
func TestLinuxHostDeviceAndNamespaceBoundaries(t *testing.T) {
	t.Parallel()

	if err := addHostDevices(nil, nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("addHostDevices(nil) = %v", err)
	}
	spec := &specs.Spec{Linux: &specs.Linux{Resources: &specs.LinuxResources{}}}
	if err := addHostDevices(spec, []domain.DeviceMapping{{
		Source: testNullDevice, Target: "/dev/example", Permissions: "rw",
	}}); err != nil || len(spec.Linux.Devices) != 1 || len(spec.Linux.Resources.Devices) != 1 {
		t.Fatalf("addHostDevices(/dev/null) = %#v, %v", spec.Linux, err)
	}
	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, nil, 0o600); err != nil {
		t.Fatal(err)
	}
	err := addHostDevices(spec, []domain.DeviceMapping{{Source: file, Target: "/dev/file"}})
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("addHostDevices(file) = %v", err)
	}
	if err := addHostDevices(spec, []domain.DeviceMapping{{
		Source: testMissingPath, Target: "/dev/missing",
	}}); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("addHostDevices(missing) = %v", err)
	}
	if kind, err := hostDeviceType(os.ModeDevice); err != nil || kind != "b" {
		t.Fatalf("hostDeviceType(block) = %q, %v", kind, err)
	}
	if kind, err := hostDeviceType(os.ModeDevice | os.ModeCharDevice); err != nil || kind != "c" {
		t.Fatalf("hostDeviceType(character) = %q, %v", kind, err)
	}
	if _, err := hostDeviceType(0); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("hostDeviceType(regular) = %v", err)
	}
	if metadata, err := hostDeviceMetadata(&syscall.Stat_t{}); err != nil || metadata == nil {
		t.Fatalf("hostDeviceMetadata(valid) = %#v, %v", metadata, err)
	}
	if _, err := hostDeviceMetadata(struct{}{}); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("hostDeviceMetadata(invalid) = %v", err)
	}
	info, err := os.Lstat(testNullDevice)
	if err != nil {
		t.Fatal(err)
	}
	err = addHostDevicesWith(spec, []domain.DeviceMapping{{Source: testNullDevice}}, func(
		string,
	) (os.FileInfo, error) {
		return fileInfoWithSystem{FileInfo: info, system: struct{}{}}, nil
	})
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("addHostDevicesWith(invalid metadata) = %v", err)
	}
	err = addHostDevicesWith(spec, []domain.DeviceMapping{{Source: testNullDevice}}, func(
		string,
	) (os.FileInfo, error) {
		//nolint:nilnil // The injected invalid contract verifies that the adapter fails closed.
		return nil, nil
	})
	if !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("addHostDevicesWith(nil metadata) = %v", err)
	}
	if networkNamespaceMount(file) {
		t.Fatal("networkNamespaceMount(file) accepted")
	}
	if err := ensureNetworkNamespace(file); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ensureNetworkNamespace(file) = %v", err)
	}
	missing := filepath.Join(t.TempDir(), "missing")
	if err := deleteNetworkNamespace(missing); err != nil {
		t.Fatalf("deleteNetworkNamespace(missing) = %v", err)
	}
	if err := deleteNetworkNamespace(file); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deleteNetworkNamespace(file) = %v", err)
	}
	if got := filepathDir("/one/two"); got != "/one" {
		t.Fatalf("filepathDir() = %q", got)
	}
	if filepathDir("/one") != "/" || filepathDir("one") != "." {
		t.Fatal("filepathDir() root or relative policy drift")
	}
	if err := copyVolumeInitialContents(context.Background(), nil, nil, nil); !errors.Is(err, ErrProtocol) {
		t.Fatalf("copyVolumeInitialContents(empty) = %v", err)
	}
	if err := copyVolumeInitialContents(
		context.Background(), []mount.Mount{{Type: bindMountType, Source: t.TempDir()}},
		[]domain.RuntimeMount{{Source: t.TempDir(), Target: testStateMount}},
		localWorkloadHost{}.WithRootfs,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("copyVolumeInitialContents(unmountable) = %v", err)
	}
	if privateContainerdLogContext(context.Background()) == nil {
		t.Fatal("privateContainerdLogContext() returned nil")
	}
}

//nolint:cyclop // The test exercises each injected namespace lifecycle boundary.
func TestEnsureNetworkNamespaceInjectedLifecycle(t *testing.T) {
	t.Parallel()

	existing := filepath.Join(t.TempDir(), "netns")
	if err := os.WriteFile(existing, nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	mounted := func(string) bool { return true }
	create := func(_ string, result chan<- error) { result <- nil }
	if err := ensureNetworkNamespaceWith(existing, create, mounted); err != nil {
		t.Fatalf("ensureNetworkNamespaceWith(existing) = %v", err)
	}
	created := filepath.Join(t.TempDir(), "netns")
	if err := ensureNetworkNamespaceWith(created, create, mounted); err != nil {
		t.Fatalf("ensureNetworkNamespaceWith(created) = %v", err)
	}
	failed := filepath.Join(t.TempDir(), "netns")
	createFailure := func(_ string, result chan<- error) { result <- errContainerdTest }
	if err := ensureNetworkNamespaceWith(failed, createFailure, mounted); !errors.Is(err, errContainerdTest) {
		t.Fatalf("ensureNetworkNamespaceWith(create failure) = %v", err)
	}
	if _, err := os.Stat(failed); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("failed namespace path remains: %v", err)
	}
	unmounted := filepath.Join(t.TempDir(), "netns")
	if err := ensureNetworkNamespaceWith(
		unmounted, create, func(string) bool { return false },
	); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ensureNetworkNamespaceWith(unmounted) = %v", err)
	}
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	if err := ensureNetworkNamespaceWith(
		filepath.Join(loop, "netns"), create, mounted,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ensureNetworkNamespaceWith(stat failure) = %v", err)
	}
	if err := ensureNetworkNamespaceWith("relative", create, mounted); !errors.Is(err, ErrProtocol) {
		t.Fatalf("ensureNetworkNamespaceWith(relative) = %v", err)
	}
	if err := ensureNetworkNamespaceWith(
		filepath.Join("/proc", "maniud-netns-test"), create, mounted,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("ensureNetworkNamespaceWith(create file failure) = %v", err)
	}
}

func TestCreateNetworkNamespaceInjectedSyscalls(t *testing.T) {
	t.Parallel()

	success := linuxNetworkNamespaceOperations{
		open:    func(string, int, uint32) (int, error) { return 1, nil },
		unshare: func(int) error { return nil },
		mount:   func(string, string, string, uintptr, string) error { return nil },
		setns:   func(int, int) error { return nil },
		close:   func(int) error { return nil },
	}
	run := func(operations linuxNetworkNamespaceOperations) error {
		result := make(chan error, 1)
		go createNetworkNamespaceWith(testMissingPath, result, operations)

		return <-result
	}
	if err := run(success); err != nil {
		t.Fatalf("createNetworkNamespaceWith() = %v", err)
	}
	openFailure := success
	openFailure.open = func(string, int, uint32) (int, error) { return 0, errContainerdTest }
	if err := run(openFailure); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("createNetworkNamespaceWith(open) = %v", err)
	}
	unshareFailure := success
	unshareFailure.unshare = func(int) error { return errContainerdTest }
	if err := run(unshareFailure); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("createNetworkNamespaceWith(unshare) = %v", err)
	}
	setnsFailure := success
	setnsFailure.setns = func(int, int) error { return errContainerdTest }
	if err := run(setnsFailure); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("createNetworkNamespaceWith(setns) = %v", err)
	}
	mountFailure := success
	mountFailure.mount = func(string, string, string, uintptr, string) error { return errContainerdTest }
	if err := run(mountFailure); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("createNetworkNamespaceWith(mount) = %v", err)
	}
	result := make(chan error, 1)
	go createNetworkNamespace(testMissingPath, result)
	if err := <-result; !errors.Is(err, ErrUnavailable) {
		t.Fatalf("createNetworkNamespace(real syscalls) = %v", err)
	}
}

//nolint:cyclop // The test exercises each injected namespace cleanup boundary.
func TestDeleteNetworkNamespaceInjectedLifecycle(t *testing.T) {
	t.Parallel()

	createFile := func() string {
		path := filepath.Join(t.TempDir(), "netns")
		if err := os.WriteFile(path, nil, privateFileMode); err != nil {
			t.Fatal(err)
		}

		return path
	}
	mounted := func(string) bool { return true }
	unmount := func(string) error { return nil }
	path := createFile()
	if err := deleteNetworkNamespaceWith(path, mounted, unmount); err != nil {
		t.Fatalf("deleteNetworkNamespaceWith() = %v", err)
	}
	path = createFile()
	if err := deleteNetworkNamespaceWith(
		path, func(string) bool { return false }, unmount,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deleteNetworkNamespaceWith(unmounted) = %v", err)
	}
	path = createFile()
	if err := deleteNetworkNamespaceWith(
		path, mounted, func(string) error { return errContainerdTest },
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deleteNetworkNamespaceWith(unmount) = %v", err)
	}
	nonempty := filepath.Join(t.TempDir(), "netns")
	if err := os.Mkdir(nonempty, privateDirectoryMode); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(nonempty, testFileName), nil, privateFileMode); err != nil {
		t.Fatal(err)
	}
	if err := deleteNetworkNamespaceWith(nonempty, mounted, unmount); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deleteNetworkNamespaceWith(remove) = %v", err)
	}
	if !networkNamespaceMount("/proc/self/ns/net") {
		t.Fatal("networkNamespaceMount() rejected the process network namespace")
	}
	if err := unmountNetworkNamespace(testMissingPath); err == nil {
		t.Fatal("unmountNetworkNamespace(missing) succeeded")
	}
	if err := unmountNetworkNamespaceWith(testMissingPath, func(string, int) error {
		return nil
	}); err != nil {
		t.Fatalf("unmountNetworkNamespaceWith() = %v", err)
	}
	root := t.TempDir()
	loop := filepath.Join(root, "loop")
	if err := os.Symlink("loop", loop); err != nil {
		t.Fatal(err)
	}
	if err := deleteNetworkNamespaceWith(
		filepath.Join(loop, "netns"), mounted, unmount,
	); !errors.Is(err, ErrUnavailable) {
		t.Fatalf("deleteNetworkNamespaceWith(stat failure) = %v", err)
	}
}
