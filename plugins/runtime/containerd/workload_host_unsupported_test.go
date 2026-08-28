//go:build !linux

package containerd

import (
	"errors"
	"testing"

	"github.com/opencontainers/runtime-spec/specs-go"

	"github.com/IceCodeNew/maniud/internal/domain"
)

func TestUnsupportedHostOperations(t *testing.T) {
	t.Parallel()

	if err := addHostDevices(&specs.Spec{}, nil); err != nil {
		t.Fatalf("addHostDevices(empty) = %v", err)
	}
	if err := addHostDevices(&specs.Spec{}, []domain.DeviceMapping{{
		Source: testMissingPath,
	}}); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("addHostDevices(device) = %v", err)
	}
	host := localWorkloadHost{}
	if err := host.EnsureNetworkNamespace(testMissingPath); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("EnsureNetworkNamespace() = %v", err)
	}
	if host.NetworkNamespaceMounted(testMissingPath) {
		t.Fatal("NetworkNamespaceMounted() accepted unsupported host")
	}
	if err := host.DeleteNetworkNamespace(testMissingPath); !errors.Is(err, ErrUnsupportedWorkload) {
		t.Fatalf("DeleteNetworkNamespace() = %v", err)
	}
}
