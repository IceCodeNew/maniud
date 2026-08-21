//go:build !linux

package containerd

import (
	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func addHostDevices(*specs.Spec, []domain.DeviceMapping) error {
	return ErrUnsupportedWorkload
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
