//go:build linux

package containerd

import (
	"errors"
	"fmt"
	"os"
	"runtime"
	"strconv"
	"syscall"

	"github.com/opencontainers/runtime-spec/specs-go"
	"golang.org/x/sys/unix"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func addHostDevices(spec *specs.Spec, devices []domain.DeviceMapping) error {
	return addHostDevicesWith(spec, devices, os.Lstat)
}

func addHostDevicesWith(
	spec *specs.Spec,
	devices []domain.DeviceMapping,
	lstat func(string) (os.FileInfo, error),
) error {
	if spec == nil || spec.Linux == nil || spec.Linux.Resources == nil {
		return ErrProtocol
	}
	for _, device := range devices {
		info, err := lstat(device.Source)
		if err != nil || info == nil || info.Mode()&os.ModeSymlink != 0 {
			return ErrUnsupportedWorkload
		}
		kind, err := hostDeviceType(info.Mode())
		if err != nil {
			return err
		}
		metadata, err := hostDeviceMetadata(info.Sys())
		if err != nil {
			return err
		}
		major := int64(unix.Major(metadata.Rdev))
		minor := int64(unix.Minor(metadata.Rdev))
		mode := info.Mode().Perm()
		uid := metadata.Uid
		gid := metadata.Gid
		spec.Linux.Devices = append(spec.Linux.Devices, specs.LinuxDevice{
			Path: device.Target, Type: kind, Major: major, Minor: minor,
			FileMode: &mode, UID: &uid, GID: &gid,
		})
		spec.Linux.Resources.Devices = append(spec.Linux.Resources.Devices, specs.LinuxDeviceCgroup{
			Allow: true, Type: kind, Major: &major, Minor: &minor, Access: device.Permissions,
		})
	}

	return nil
}

func hostDeviceType(mode os.FileMode) (string, error) {
	if mode&os.ModeDevice == 0 {
		return "", ErrUnsupportedWorkload
	}
	if mode&os.ModeCharDevice != 0 {
		return "c", nil
	}

	return "b", nil
}

func hostDeviceMetadata(value any) (*syscall.Stat_t, error) {
	metadata, valid := value.(*syscall.Stat_t)
	if !valid {
		return nil, ErrUnsupportedWorkload
	}

	return metadata, nil
}

func ensureNetworkNamespace(path string) error {
	return ensureNetworkNamespaceWith(path, createNetworkNamespace, networkNamespaceMount)
}

func ensureNetworkNamespaceWith(
	path string,
	create func(string, chan<- error),
	mounted func(string) bool,
) error {
	if info, err := os.Lstat(path); err == nil {
		if info.Mode().IsRegular() && mounted(path) {
			return nil
		}

		return ErrProtocol
	} else if !errors.Is(err, os.ErrNotExist) {
		return ErrUnavailable
	}
	if err := secureDirectory(filepathDir(path)); err != nil {
		return err
	}
	// path is a validated deterministic child of NetworkNamespaceRoot.
	//nolint:gosec // The path is derived from a validated fixed root and deterministic container ID.
	file, err := os.OpenFile(path, os.O_CREATE|os.O_EXCL|os.O_RDONLY, privateFileMode)
	if err != nil {
		return ErrUnavailable
	}
	_ = file.Close()

	result := make(chan error, 1)
	go create(path, result)
	err = <-result
	if err != nil {
		_ = os.Remove(path)

		return err
	}
	if !mounted(path) {
		return ErrProtocol
	}

	return nil
}

func createNetworkNamespace(path string, result chan<- error) {
	createNetworkNamespaceWith(path, result, linuxNetworkNamespaceOperations{
		open: unix.Open, unshare: unix.Unshare, mount: unix.Mount, setns: unix.Setns, close: unix.Close,
	})
}

type linuxNetworkNamespaceOperations struct {
	open    func(string, int, uint32) (int, error)
	unshare func(int) error
	mount   func(string, string, string, uintptr, string) error
	setns   func(int, int) error
	close   func(int) error
}

func createNetworkNamespaceWith(
	path string,
	result chan<- error,
	operations linuxNetworkNamespaceOperations,
) {
	runtime.LockOSThread()
	restored := false
	defer func() {
		if restored {
			runtime.UnlockOSThread()
		}
	}()
	original, err := operations.open(
		"/proc/self/task/"+strconv.Itoa(unix.Gettid())+"/ns/net", unix.O_RDONLY|unix.O_CLOEXEC, 0,
	)
	if err != nil {
		result <- ErrUnavailable

		return
	}
	defer func() { _ = operations.close(original) }()
	if operations.unshare(unix.CLONE_NEWNET) != nil {
		result <- ErrUnavailable

		return
	}
	threadNamespace := "/proc/self/task/" + strconv.Itoa(unix.Gettid()) + "/ns/net"
	mountErr := operations.mount(threadNamespace, path, "none", unix.MS_BIND, "")
	restoreErr := operations.setns(original, unix.CLONE_NEWNET)
	if restoreErr != nil {
		result <- ErrUnavailable

		return
	}
	restored = true
	if mountErr != nil {
		result <- ErrUnavailable

		return
	}
	result <- nil
}

func networkNamespaceMount(path string) bool {
	var stat unix.Statfs_t

	return unix.Statfs(path, &stat) == nil && stat.Type == unix.NSFS_MAGIC
}

func deleteNetworkNamespace(path string) error {
	return deleteNetworkNamespaceWith(path, networkNamespaceMount, unmountNetworkNamespace)
}

func unmountNetworkNamespace(path string) error {
	return unmountNetworkNamespaceWith(path, unix.Unmount)
}

func unmountNetworkNamespaceWith(path string, unmount func(string, int) error) error {
	if err := unmount(path, unix.MNT_DETACH); err != nil {
		return fmt.Errorf("unmount network namespace: %w", err)
	}

	return nil
}

func deleteNetworkNamespaceWith(
	path string,
	mounted func(string) bool,
	unmount func(string) error,
) error {
	if _, err := os.Lstat(path); errors.Is(err, os.ErrNotExist) {
		return nil
	} else if err != nil {
		return ErrUnavailable
	}
	if !mounted(path) || unmount(path) != nil || os.Remove(path) != nil {
		return ErrUnavailable
	}

	return nil
}

func filepathDir(path string) string {
	for index := len(path) - 1; index >= 0; index-- {
		if path[index] == '/' {
			if index == 0 {
				return "/"
			}

			return path[:index]
		}
	}

	return "."
}
