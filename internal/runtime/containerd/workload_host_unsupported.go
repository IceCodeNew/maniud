//go:build !linux

package containerd

import (
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func addHostDevices(_ *specs.Spec, devices []domain.DeviceMapping) error {
	if len(devices) != 0 {
		return ErrUnsupportedWorkload
	}

	return nil
}

func ensureNetworkNamespace(string) error {
	return ErrUnsupportedWorkload
}

func networkNamespaceMount(string) bool {
	return false
}

func deleteNetworkNamespace(string) error {
	return ErrUnsupportedWorkload
}
