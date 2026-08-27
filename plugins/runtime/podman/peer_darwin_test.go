//go:build darwin

package podman

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

func TestDarwinPeerHelpersRejectInvalidObjects(t *testing.T) {
	t.Parallel()

	if _, valid := socketMetadata(darwinPodmanFileInfo{}); valid {
		t.Fatal("socketMetadata(invalid) accepted")
	}
	if _, err := authenticateSocketMetadata(darwinPodmanFileInfo{}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("authenticateSocketMetadata(invalid) = %v", err)
	}
	invalidOwner := uint32(os.Geteuid() + 1) //nolint:gosec // Native Unix UIDs are non-negative.
	if _, err := authenticateSocketMetadata(darwinPodmanStatFileInfo{
		darwinPodmanFileInfo: darwinPodmanFileInfo{},
		stat:                 &syscall.Stat_t{Uid: invalidOwner},
	}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("authenticateSocketMetadata(invalid owner) = %v", err)
	}

	if _, err := connectedPeer(&net.UnixConn{}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(empty unix) = %v", err)
	}
	path := startPodmanTestServer(t, podmanNegotiationHandler(libpodAPIVersion, "5.0.0", libpodAPIVersion))
	connection, err := (&net.Dialer{}).DialContext(context.Background(), "unix", path)
	if err != nil {
		t.Fatal(err)
	}
	if err := connection.Close(); err != nil {
		t.Fatal(err)
	}
	if _, err := connectedPeer(connection); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(closed unix) = %v", err)
	}
}

func TestDarwinConnectedPeerRejectsUnconnectedDatagram(t *testing.T) {
	t.Parallel()

	directory, err := os.MkdirTemp("/tmp", "maniud-") //nolint:usetesting // Darwin test paths must fit sockaddr_un.
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = os.RemoveAll(directory) })
	connection, err := net.ListenUnixgram("unixgram", &net.UnixAddr{
		Name: filepath.Join(directory, "podman.sock"), Net: "unixgram",
	})
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = connection.Close() })

	if _, err = connectedPeer(connection); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(unconnected datagram) = %v", err)
	}
}

type darwinPodmanFileInfo struct{}

func (darwinPodmanFileInfo) Name() string       { return "fake" }
func (darwinPodmanFileInfo) Size() int64        { return 0 }
func (darwinPodmanFileInfo) Mode() os.FileMode  { return os.ModeSocket }
func (darwinPodmanFileInfo) ModTime() time.Time { return time.Time{} }
func (darwinPodmanFileInfo) IsDir() bool        { return false }
func (darwinPodmanFileInfo) Sys() any           { return nil }

type darwinPodmanStatFileInfo struct {
	darwinPodmanFileInfo

	stat *syscall.Stat_t
}

func (info darwinPodmanStatFileInfo) Sys() any { return info.stat }
