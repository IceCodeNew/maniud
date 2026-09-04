//go:build darwin

package containerd

import (
	"context"
	"errors"
	"net"
	"os"
	"path/filepath"
	"syscall"
	"testing"
	"time"
)

func TestDarwinSocketAndPeerIdentity(t *testing.T) {
	t.Parallel()

	directory, err := os.MkdirTemp("/tmp", "maniud-") //nolint:usetesting // Darwin test paths must fit sockaddr_un.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	path := filepath.Join(directory, "containerd.sock")
	listener, err := (&net.ListenConfig{}).Listen(t.Context(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = listener.Close() })

	identity, err := inspectSocket(path)
	if err != nil || identity.changeTime == 0 {
		t.Fatalf("inspectSocket() = %#v, %v", identity, err)
	}
	connection, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })
	peer, err := connectedPeer(connection)
	//nolint:gosec // Native process IDs are non-negative.
	if err != nil || peer.process != uint64(os.Getpid()) || peer.owner != uint32(os.Geteuid()) ||
		peer.generation == 0 {
		t.Fatalf("connectedPeer() = %#v, %v", peer, err)
	}
}

func TestDarwinPeerHelpersRejectInvalidObjects(t *testing.T) {
	t.Parallel()

	if _, valid := socketMetadata(darwinFakeFileInfo{}); valid {
		t.Fatal("socketMetadata(fake) accepted")
	}
	invalidOwner := uint32(os.Geteuid() + 1) //nolint:gosec // Native Unix UIDs are non-negative.
	if _, err := authenticateSocketMetadata(darwinStatFileInfo{
		darwinFakeFileInfo: darwinFakeFileInfo{},
		stat:               &syscall.Stat_t{Uid: invalidOwner},
	}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("authenticateSocketMetadata(invalid owner) = %v", err)
	}
	if _, err := connectedPeer(nil); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(nil) = %v", err)
	}
	if _, err := connectedPeer(&net.UnixConn{}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(empty) = %v", err)
	}
	if _, err := connectedPeerToken(^uintptr(0)); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeerToken(invalid) = %v", err)
	}
}

type darwinFakeFileInfo struct{}

func (darwinFakeFileInfo) Name() string       { return "fake" }
func (darwinFakeFileInfo) Size() int64        { return 0 }
func (darwinFakeFileInfo) Mode() os.FileMode  { return os.ModeSocket }
func (darwinFakeFileInfo) ModTime() time.Time { return time.Time{} }
func (darwinFakeFileInfo) IsDir() bool        { return false }
func (darwinFakeFileInfo) Sys() any           { return nil }

type darwinStatFileInfo struct {
	darwinFakeFileInfo

	stat *syscall.Stat_t
}

func (info darwinStatFileInfo) Sys() any { return info.stat }
