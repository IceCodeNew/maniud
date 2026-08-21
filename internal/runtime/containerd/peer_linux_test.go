//go:build linux

package containerd

import (
	"bytes"
	"errors"
	"io"
	"os"
	"strings"
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

	process := int32(os.Getpid()) //nolint:gosec // Linux represents a positive native PID as int32.
	if generation, err := processStartTime(process); err != nil || generation == 0 {
		t.Fatalf("processStartTime(current) = %d, %v", generation, err)
	}
	if _, err := processStartTime(-1); !errors.Is(err, ErrInvalidEndpoint) {
		t.Fatalf("processStartTime(missing) = %v", err)
	}
	for _, credentials := range []*syscall.Ucred{nil, {Pid: -1}, {Pid: 1<<30 + 1}} {
		if _, err := peerFromCredentials(credentials); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("peerFromCredentials(%#v) = %v", credentials, err)
		}
	}

	valid := []byte("1 (containerd test) S" + strings.Repeat(" 0", processStartTimeIndex-1) + " 123")
	if generation, err := decodeProcessStartTime(valid); err != nil || generation != 123 {
		t.Fatalf("decodeProcessStartTime(valid) = %d, %v", generation, err)
	}
	for _, value := range [][]byte{
		nil,
		[]byte("1 (containerd test)"),
		[]byte("1 (containerd test) S 0"),
		[]byte("1 (containerd test) S" + strings.Repeat(" 0", processStartTimeIndex+1)),
	} {
		if _, err := decodeProcessStartTime(value); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("decodeProcessStartTime(%q) = %v", value, err)
		}
	}
}

func TestLinuxPeerProcessStatIOFailures(t *testing.T) {
	t.Parallel()

	for _, reader := range []io.ReadCloser{
		containerdReadCloser{Reader: containerdFailingReader{}},
		containerdReadCloser{Reader: strings.NewReader("1 (x)"), closeErr: errContainerdTest},
		containerdReadCloser{Reader: bytes.NewReader(make([]byte, maximumProcessStatBytes+1))},
	} {
		if _, err := readProcessStartTime(reader); !errors.Is(err, ErrInvalidEndpoint) {
			t.Fatalf("readProcessStartTime() = %v", err)
		}
	}
}

type containerdReadCloser struct {
	io.Reader

	closeErr error
}

func (reader containerdReadCloser) Close() error { return reader.closeErr }

type containerdFailingReader struct{}

func (containerdFailingReader) Read([]byte) (int, error) { return 0, errContainerdTest }

type containerdStatFileInfo struct {
	fakeFileInfo

	stat *syscall.Stat_t
}

func (info containerdStatFileInfo) Sys() any { return info.stat }
