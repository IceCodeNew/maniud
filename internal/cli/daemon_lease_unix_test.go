//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"io"
	"os"
	"path/filepath"
	"strings"
	"syscall"
	"testing"
)

type daemonPIDWriterFault struct {
	failAt int
}

func (writer daemonPIDWriterFault) Truncate(int64) error {
	if writer.failAt == 1 {
		return errClosedOutput
	}

	return nil
}

func (writer daemonPIDWriterFault) Seek(int64, int) (int64, error) {
	if writer.failAt == 2 {
		return 0, errClosedOutput
	}

	return 0, nil
}

func (writer daemonPIDWriterFault) Write(value []byte) (int, error) {
	if writer.failAt == 3 {
		return 0, errClosedOutput
	}

	return len(value), nil
}

func (writer daemonPIDWriterFault) Sync() error {
	if writer.failAt == 4 {
		return errClosedOutput
	}

	return nil
}

type daemonPIDReaderFault struct {
	reader  *strings.Reader
	seekErr error
	readErr error
}

func (reader *daemonPIDReaderFault) Seek(offset int64, whence int) (int64, error) {
	if reader.seekErr != nil {
		return 0, reader.seekErr
	}

	return reader.reader.Seek(offset, whence) //nolint:wrapcheck // Preserve the injected reader's exact seek result.
}

func (reader *daemonPIDReaderFault) Read(value []byte) (int, error) {
	if reader.readErr != nil {
		return 0, reader.readErr
	}

	return reader.reader.Read(value) //nolint:wrapcheck // Preserve io.EOF so io.ReadAll recognizes stream completion.
}

type daemonSignalerFault struct {
	err error
}

func (signaler daemonSignalerFault) Signal(os.Signal) error {
	return signaler.err
}

func TestDaemonLeaseContainsInjectedFailures(t *testing.T) {
	t.Parallel()

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, _, err := acquireDaemonLease(file); err == nil {
		t.Fatal("acquireDaemonLease(file) succeeded")
	}

	directory := t.TempDir()
	flockFailure := func(int, int) error { return syscall.EIO }
	if _, _, err := acquireDaemonLeaseWith(directory, flockFailure, writeDaemonPID); err == nil {
		t.Fatal("acquireDaemonLeaseWith(flock failure) succeeded")
	}
	writeFailure := func(daemonPIDWriter, int) error { return errClosedOutput }
	if _, _, err := acquireDaemonLeaseWith(directory, syscall.Flock, writeFailure); !errors.Is(err, errClosedOutput) {
		t.Fatalf("acquireDaemonLeaseWith(write failure) = %v", err)
	}
}

func TestDaemonStopContainsInjectedFailures(t *testing.T) {
	t.Parallel()

	ownerFailure := func(string) (int, bool, error) { return 0, false, errClosedOutput }
	if _, err := requestDaemonStopWith(
		t.Context(), "state", ownerFailure, signalDaemon, waitDaemonLeaseRelease,
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("requestDaemonStopWith(owner failure) = %v", err)
	}
	notRunning := func(string) (int, bool, error) { return 0, false, nil }
	if running, err := requestDaemonStopWith(
		t.Context(), "state", notRunning, signalDaemon, waitDaemonLeaseRelease,
	); err != nil || running {
		t.Fatalf("requestDaemonStopWith(not running) = %t, %v", running, err)
	}
	running := func(string) (int, bool, error) { return 42, true, nil }
	signalFailure := func(int) error { return errClosedOutput }
	if _, err := requestDaemonStopWith(
		t.Context(), "state", running, signalFailure, waitDaemonLeaseRelease,
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("requestDaemonStopWith(signal failure) = %v", err)
	}
	waitFailure := func(context.Context, string, int) error { return errClosedOutput }
	if _, err := requestDaemonStopWith(
		t.Context(), "state", running, func(int) error { return nil }, waitFailure,
	); !errors.Is(err, errClosedOutput) {
		t.Fatalf("requestDaemonStopWith(wait failure) = %v", err)
	}
	if running, err := requestDaemonStopWith(
		t.Context(), "state", running, func(int) error { return nil },
		func(context.Context, string, int) error { return nil },
	); err != nil || !running {
		t.Fatalf("requestDaemonStopWith(success) = %t, %v", running, err)
	}
}

func TestSignalDaemonContainsFailures(t *testing.T) {
	t.Parallel()

	findFailure := func(int) (daemonSignaler, error) { return nil, errClosedOutput }
	if err := signalDaemonWith(1, findFailure); !errors.Is(err, errClosedOutput) {
		t.Fatalf("signalDaemonWith(find failure) = %v", err)
	}
	find := func(int) (daemonSignaler, error) { return daemonSignalerFault{err: errClosedOutput}, nil }
	if err := signalDaemonWith(1, find); !errors.Is(err, errClosedOutput) {
		t.Fatalf("signalDaemonWith(signal failure) = %v", err)
	}
	find = func(int) (daemonSignaler, error) { return daemonSignalerFault{err: nil}, nil }
	if err := signalDaemonWith(1, find); err != nil {
		t.Fatalf("signalDaemonWith(success) error = %v", err)
	}
}

func TestDaemonLeaseOwnerHandlesMissingState(t *testing.T) {
	t.Parallel()

	if _, running, err := daemonLeaseOwner(filepath.Join(t.TempDir(), "missing")); err != nil || running {
		t.Fatalf("daemonLeaseOwner(missing directory) = %t, %v", running, err)
	}
	directory := t.TempDir()
	if _, running, err := daemonLeaseOwner(directory); err != nil || running {
		t.Fatalf("daemonLeaseOwner(missing lock) = %t, %v", running, err)
	}

	file := filepath.Join(t.TempDir(), "file")
	if err := os.WriteFile(file, []byte("x"), 0o600); err != nil {
		t.Fatalf("WriteFile() error = %v", err)
	}
	if _, _, err := daemonLeaseOwner(file); err == nil {
		t.Fatal("daemonLeaseOwner(file) succeeded")
	}
}

func TestDaemonLeaseOwnerContainsLockFailures(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	lock := filepath.Join(directory, daemonLockName)
	if err := os.WriteFile(lock, []byte("42\n"), 0o600); err != nil {
		t.Fatalf("WriteFile(lock) error = %v", err)
	}
	if _, _, err := daemonLeaseOwnerWith(
		directory, func(int, int) error { return syscall.EIO }, readDaemonPID,
	); err == nil {
		t.Fatal("daemonLeaseOwnerWith(flock failure) succeeded")
	}
	if _, running, err := daemonLeaseOwnerWith(
		directory,
		func(int, int) error { return syscall.EWOULDBLOCK },
		func(io.ReadSeeker) (int, error) { return 0, errClosedOutput },
	); !running || !errors.Is(err, errClosedOutput) {
		t.Fatalf("daemonLeaseOwnerWith(read failure) = %t, %v", running, err)
	}
}

func TestWaitDaemonLeaseReleaseBoundaries(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if err := waitDaemonLeaseReleaseWith(cancelled, "state", 1, daemonLeaseOwner); !errors.Is(err, context.Canceled) {
		t.Fatalf("waitDaemonLeaseReleaseWith(cancelled) = %v", err)
	}
	ownerFailure := func(string) (int, bool, error) { return 0, false, errClosedOutput }
	if err := waitDaemonLeaseReleaseWith(t.Context(), "state", 1, ownerFailure); !errors.Is(err, errClosedOutput) {
		t.Fatalf("waitDaemonLeaseReleaseWith(owner failure) = %v", err)
	}
	notRunning := func(string) (int, bool, error) { return 0, false, nil }
	if err := waitDaemonLeaseReleaseWith(t.Context(), "state", 1, notRunning); err != nil {
		t.Fatalf("waitDaemonLeaseReleaseWith(released) = %v", err)
	}
	differentOwner := func(string) (int, bool, error) { return 2, true, nil }
	if err := waitDaemonLeaseReleaseWith(
		t.Context(), "state", 1, differentOwner,
	); !errors.Is(err, errDaemonControlUnavailable) {
		t.Fatalf("waitDaemonLeaseReleaseWith(different owner) = %v", err)
	}
}

func TestDaemonLockBoundaries(t *testing.T) {
	t.Parallel()

	directory := t.TempDir()
	root, err := os.OpenRoot(directory)
	if err != nil {
		t.Fatalf("OpenRoot() error = %v", err)
	}
	if err = root.Close(); err != nil {
		t.Fatalf("Root.Close() error = %v", err)
	}
	if _, err = openDaemonLock(root, os.O_RDWR); err == nil {
		t.Fatal("openDaemonLock(closed root) succeeded")
	}

	file, err := os.CreateTemp(t.TempDir(), "lock")
	if err != nil {
		t.Fatalf("CreateTemp() error = %v", err)
	}
	if err = file.Close(); err != nil {
		t.Fatalf("File.Close() error = %v", err)
	}
	if _, err = validateDaemonLock(file); err == nil {
		t.Fatal("validateDaemonLock(closed file) succeeded")
	}
}

func TestWriteDaemonPIDContainsFailures(t *testing.T) {
	t.Parallel()

	for failAt := 1; failAt <= 4; failAt++ {
		if err := writeDaemonPID(daemonPIDWriterFault{failAt: failAt}, 42); !errors.Is(err, errClosedOutput) {
			t.Fatalf("writeDaemonPID(failure %d) = %v", failAt, err)
		}
	}
}

func TestReadDaemonPIDContainsFailures(t *testing.T) {
	t.Parallel()

	reader := &daemonPIDReaderFault{reader: strings.NewReader("42\n"), seekErr: errClosedOutput, readErr: nil}
	if _, err := readDaemonPID(reader); !errors.Is(err, errClosedOutput) {
		t.Fatalf("readDaemonPID(seek failure) = %v", err)
	}
	reader = &daemonPIDReaderFault{reader: strings.NewReader("42\n"), seekErr: nil, readErr: errClosedOutput}
	if _, err := readDaemonPID(reader); !errors.Is(err, errClosedOutput) {
		t.Fatalf("readDaemonPID(read failure) = %v", err)
	}
}

func TestNilDaemonLeaseClosesCleanly(t *testing.T) {
	t.Parallel()

	if err := (*daemonLease)(nil).Close(); err != nil {
		t.Fatalf("daemonLease(nil).Close() = %v", err)
	}
}
