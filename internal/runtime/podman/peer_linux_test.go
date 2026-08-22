//go:build linux

package podman

import (
	"bytes"
	"context"
	"errors"
	"io"
	"net"
	"os"
	"strings"
	"syscall"
	"testing"
	"time"
)

var errPodmanPeerTest = errors.New("podman peer test failure")

func TestProcessStartTimeAuthenticatesCurrentProcess(t *testing.T) {
	t.Parallel()

	process := int32(os.Getpid()) //nolint:gosec // Linux represents the positive native PID as int32.
	if generation, err := processStartTime(process); err != nil || generation == 0 {
		t.Fatalf("processStartTime(current) = %d, %v", generation, err)
	}
	if _, err := processStartTime(-1); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("processStartTime(missing) error = %v", err)
	}
}

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

func TestProcessStatDecoderRejectsMalformedInput(t *testing.T) {
	t.Parallel()

	valid := []byte("1 (process) S" + strings.Repeat(" 0", processStartTimeIndex-1) + " 123")
	if generation, err := decodeProcessStartTime(valid); err != nil || generation != 123 {
		t.Fatalf("decodeProcessStartTime(valid) = %d, %v", generation, err)
	}
	for _, value := range [][]byte{
		nil,
		[]byte("1 (process)"),
		[]byte("1 (process) S 0"),
		[]byte("1 (process) S" + strings.Repeat(" 0", processStartTimeIndex+1)),
	} {
		if _, err := decodeProcessStartTime(value); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("decodeProcessStartTime(%q) = %v", value, err)
		}
	}
}

func TestReadProcessStatRejectsIOFailures(t *testing.T) {
	t.Parallel()

	readFailure := failingPodmanReadCloser{Reader: failingPodmanReader{}, closeErr: nil}
	closeFailure := failingPodmanReadCloser{Reader: strings.NewReader("1 (x)"), closeErr: errPodmanPeerTest}
	oversized := failingPodmanReadCloser{
		Reader: bytes.NewReader(make([]byte, maximumProcessStatBytes+1)), closeErr: nil,
	}
	for _, reader := range []io.ReadCloser{readFailure, closeFailure, oversized} {
		if _, err := readProcessStartTime(reader); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("readProcessStartTime() = %v", err)
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

type failingPodmanReadCloser struct {
	io.Reader

	closeErr error
}

func (reader failingPodmanReadCloser) Close() error { return reader.closeErr }

type failingPodmanReader struct{}

func (failingPodmanReader) Read([]byte) (int, error) { return 0, errPodmanPeerTest }
