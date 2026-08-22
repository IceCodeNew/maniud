//go:build linux

package containerd

import (
	"errors"
	"os"
	"syscall"
	"testing"
)

func TestLinuxSocketOwnerValidation(t *testing.T) {
	t.Parallel()

	invalidOwner := uint32(os.Geteuid() + 1) //nolint:gosec // Native Unix UIDs are non-negative.
	if _, err := authenticateSocketMetadata(containerdStatFileInfo{
		fakeFileInfo: fakeFileInfo{},
		stat:         &syscall.Stat_t{Uid: invalidOwner},
	}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("authenticateSocketMetadata(invalid owner) = %v", err)
	}
}

func TestLinuxPeerProcessGeneration(t *testing.T) {
	t.Parallel()

	for _, credentials := range []*syscall.Ucred{nil, {Pid: -1}, {Pid: 1<<30 + 1}} {
		if _, err := peerFromCredentials(credentials); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("peerFromCredentials(%#v) = %v", credentials, err)
		}
	}
}

type containerdStatFileInfo struct {
	fakeFileInfo

	stat *syscall.Stat_t
}

func (info containerdStatFileInfo) Sys() any { return info.stat }
