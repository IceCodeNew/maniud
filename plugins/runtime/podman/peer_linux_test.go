//go:build linux

package podman

import (
	"context"
	"errors"
	"net"
	"os"
	"syscall"
	"testing"
	"time"
)

func TestLinuxPeerHelpersRejectInvalidObjects(t *testing.T) {
	t.Parallel()

	if _, valid := socketMetadata(fakePodmanFileInfo{}); valid {
		t.Fatal("socketMetadata(invalid) accepted")
	}
	if _, err := authenticateSocketMetadata(fakePodmanFileInfo{}); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("authenticateSocketMetadata(invalid) = %v", err)
	}
	left, right := net.Pipe()
	defer func() { _ = left.Close() }()
	defer func() { _ = right.Close() }()
	if _, err := connectedPeer(left); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("connectedPeer(non-unix) = %v", err)
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

func TestPeerCredentialDecoderRejectsInvalidProcesses(t *testing.T) {
	t.Parallel()

	for _, credentials := range []*syscall.Ucred{nil, {Pid: -1}, {Pid: 1<<30 + 1}} {
		if _, err := peerFromCredentials(credentials); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("peerFromCredentials(%#v) = %v", credentials, err)
		}
	}
}

type fakePodmanFileInfo struct{}

func (fakePodmanFileInfo) Name() string       { return "fake" }
func (fakePodmanFileInfo) Size() int64        { return 0 }
func (fakePodmanFileInfo) Mode() os.FileMode  { return os.ModeSocket }
func (fakePodmanFileInfo) ModTime() time.Time { return time.Time{} }
func (fakePodmanFileInfo) IsDir() bool        { return false }
func (fakePodmanFileInfo) Sys() any           { return nil }
