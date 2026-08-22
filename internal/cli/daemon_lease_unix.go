//go:build linux || darwin

package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"os"
	"os/signal"
	"strconv"
	"strings"
	"syscall"
	"time"
)

const (
	daemonLockName       = "daemon.lock"
	daemonLockMode       = os.FileMode(0o600)
	maximumDaemonPIDSize = 32
	daemonStopPoll       = 10 * time.Millisecond
)

var (
	errDaemonAlreadyRunning     = errors.New("daemon is already running")
	errDaemonControlUnavailable = errors.New("daemon control is unavailable")
)

type daemonLease struct {
	root *os.Root
	lock *os.File
}

func acquireDaemonLease(directory string) (*daemonLease, bool, error) {
	return acquireDaemonLeaseWith(directory, syscall.Flock, writeDaemonPID)
}

func acquireDaemonLeaseWith(
	directory string,
	flock func(int, int) error,
	writePID func(daemonPIDWriter, int) error,
) (*daemonLease, bool, error) {
	root, err := os.OpenRoot(directory)
	if err != nil {
		return nil, false, fmt.Errorf("open daemon state directory: %w", err)
	}
	lock, err := openDaemonLock(root, os.O_RDWR|os.O_CREATE)
	if err != nil {
		return nil, false, errors.Join(err, root.Close())
	}
	if err = flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err != nil {
		closeErr := errors.Join(lock.Close(), root.Close())
		if errors.Is(err, syscall.EWOULDBLOCK) || errors.Is(err, syscall.EAGAIN) {
			return nil, true, closeErr
		}

		return nil, false, errors.Join(fmt.Errorf("lock daemon state: %w", err), closeErr)
	}
	if err = writePID(lock, os.Getpid()); err != nil {
		return nil, false, errors.Join(err, lock.Close(), root.Close())
	}

	return &daemonLease{root: root, lock: lock}, false, nil
}

func requestDaemonStop(ctx context.Context, directory string) (bool, error) {
	return requestDaemonStopWith(ctx, directory, daemonLeaseOwner, signalDaemon, waitDaemonLeaseRelease)
}

func requestDaemonStopWith(
	ctx context.Context,
	directory string,
	owner func(string) (int, bool, error),
	signalProcess func(int) error,
	wait func(context.Context, string, int) error,
) (bool, error) {
	pid, running, err := owner(directory)
	if err != nil || !running {
		return running, err
	}
	if err = signalProcess(pid); err != nil {
		return true, err
	}
	if err = wait(ctx, directory, pid); err != nil {
		return true, err
	}

	return true, nil
}

type daemonSignaler interface {
	Signal(signal os.Signal) error
}

func signalDaemon(pid int) error {
	return signalDaemonWith(pid, func(pid int) (daemonSignaler, error) {
		return os.FindProcess(pid)
	})
}

func signalDaemonWith(pid int, find func(int) (daemonSignaler, error)) error {
	process, err := find(pid)
	if err != nil {
		return fmt.Errorf("find daemon process: %w", err)
	}
	if err = process.Signal(syscall.SIGUSR1); err != nil {
		return fmt.Errorf("signal daemon process: %w", err)
	}

	return nil
}

func daemonLeaseOwner(directory string) (int, bool, error) {
	return daemonLeaseOwnerWith(directory, syscall.Flock, readDaemonPID)
}

func daemonLeaseOwnerWith(
	directory string,
	flock func(int, int) error,
	readPID func(io.ReadSeeker) (int, error),
) (int, bool, error) {
	root, err := os.OpenRoot(directory)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, nil
	}
	if err != nil {
		return 0, false, fmt.Errorf("open daemon state directory: %w", err)
	}
	lock, err := openDaemonLock(root, os.O_RDWR)
	if errors.Is(err, fs.ErrNotExist) {
		return 0, false, errors.Join(root.Close())
	}
	if err != nil {
		return 0, false, errors.Join(err, root.Close())
	}
	if err = flock(int(lock.Fd()), syscall.LOCK_EX|syscall.LOCK_NB); err == nil {
		lease := &daemonLease{root: root, lock: lock}

		return 0, false, lease.Close()
	}
	if !errors.Is(err, syscall.EWOULDBLOCK) && !errors.Is(err, syscall.EAGAIN) {
		return 0, false, errors.Join(fmt.Errorf("inspect daemon state lock: %w", err), lock.Close(), root.Close())
	}
	pid, readErr := readPID(lock)

	return pid, true, errors.Join(readErr, lock.Close(), root.Close())
}

func waitDaemonLeaseRelease(ctx context.Context, directory string, pid int) error {
	return waitDaemonLeaseReleaseWith(ctx, directory, pid, daemonLeaseOwner)
}

func waitDaemonLeaseReleaseWith(
	ctx context.Context,
	directory string,
	pid int,
	owner func(string) (int, bool, error),
) error {
	ticker := time.NewTicker(daemonStopPoll)
	defer ticker.Stop()

	for {
		select {
		case <-ctx.Done():
			return fmt.Errorf("wait for daemon to stop: %w", ctx.Err())
		case <-ticker.C:
			currentOwner, running, err := owner(directory)
			if err != nil {
				return err
			}
			if !running {
				return nil
			}
			if currentOwner != pid {
				return errDaemonControlUnavailable
			}
		}
	}
}

func daemonStopRequests(parent context.Context) (<-chan struct{}, func()) {
	requested := make(chan struct{})
	signals := make(chan os.Signal, 1)
	finished := make(chan struct{})
	signal.Notify(signals, syscall.SIGUSR1)
	go func() {
		defer close(finished)
		select {
		case <-signals:
			close(requested)
		case <-parent.Done():
		}
	}()
	closeSignals := func() {
		signal.Stop(signals)
		select {
		case <-finished:
		case signals <- syscall.SIGUSR1:
			<-finished
		}
	}

	return requested, closeSignals
}

func openDaemonLock(root *os.Root, flags int) (*os.File, error) {
	if info, err := root.Lstat(daemonLockName); err == nil &&
		(!info.Mode().IsRegular() || info.Mode().Perm() != daemonLockMode) {
		return nil, errDaemonControlUnavailable
	} else if err != nil && !errors.Is(err, fs.ErrNotExist) {
		return nil, fmt.Errorf("inspect daemon lock: %w", err)
	}
	lock, err := root.OpenFile(daemonLockName, flags, daemonLockMode)
	if err != nil {
		return nil, fmt.Errorf("open daemon lock: %w", err)
	}

	return validateDaemonLock(lock)
}

func validateDaemonLock(lock *os.File) (*os.File, error) {
	info, err := lock.Stat()
	if err != nil || !info.Mode().IsRegular() || info.Mode().Perm() != daemonLockMode {
		return nil, errors.Join(errDaemonControlUnavailable, err, lock.Close())
	}

	return lock, nil
}

type daemonPIDWriter interface {
	Truncate(size int64) error
	Seek(offset int64, whence int) (int64, error)
	Write(value []byte) (int, error)
	Sync() error
}

func writeDaemonPID(lock daemonPIDWriter, pid int) error {
	if err := lock.Truncate(0); err != nil {
		return fmt.Errorf("truncate daemon lock: %w", err)
	}
	if _, err := lock.Seek(0, io.SeekStart); err != nil {
		return fmt.Errorf("rewind daemon lock: %w", err)
	}
	if _, err := fmt.Fprintf(lock, "%d\n", pid); err != nil {
		return fmt.Errorf("write daemon process ID: %w", err)
	}
	if err := lock.Sync(); err != nil {
		return fmt.Errorf("persist daemon process ID: %w", err)
	}

	return nil
}

func readDaemonPID(lock io.ReadSeeker) (int, error) {
	if _, err := lock.Seek(0, io.SeekStart); err != nil {
		return 0, fmt.Errorf("rewind daemon lock: %w", err)
	}
	encoded, err := io.ReadAll(io.LimitReader(lock, maximumDaemonPIDSize))
	if err != nil {
		return 0, fmt.Errorf("read daemon process ID: %w", err)
	}
	pid, err := strconv.Atoi(strings.TrimSpace(string(encoded)))
	if err != nil || pid <= 0 {
		return 0, errDaemonControlUnavailable
	}

	return pid, nil
}

func (lease *daemonLease) Close() error {
	if lease == nil {
		return nil
	}

	return errors.Join(
		syscall.Flock(int(lease.lock.Fd()), syscall.LOCK_UN),
		lease.lock.Close(),
		lease.root.Close(),
	)
}
