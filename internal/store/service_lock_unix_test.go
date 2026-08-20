//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"

	"golang.org/x/sys/unix"
)

func TestServiceLockScopesProjectAndService(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")
	state := openServiceLockTestStore(t, path)

	owner := requireTryServiceLock(t, state, "project-a", "api")
	t.Cleanup(func() {
		requireNoError(t, owner.Close())
	})

	if !owner.Valid() {
		t.Fatal("service lock is not valid after acquisition")
	}

	for _, identity := range [][2]string{{"project-a", "worker"}, {"project-b", "api"}} {
		other := requireTryServiceLock(t, state, identity[0], identity[1])
		requireNoError(t, other.Close())
	}

	contender, err := state.TryLockService("project-a", "api")
	if contender != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("TryLockService(contended) = %#v, %v", contender, err)
	}

	assertPrivateRegular(t, filepath.Join(filepath.Dir(path), owner.name))
}

func TestServiceLockRejectsUnsafeEntries(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{name: "symlink", setup: makeServiceLockSymlink},
		{name: "hard link", setup: makeServiceLockHardLink},
		{name: "broad permissions", setup: makeServiceLockBroad},
		{name: "directory", setup: makeServiceLockDirectory},
	} {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := privateTempDir(t)
			path := filepath.Join(directory, "state.db")
			state := openServiceLockTestStore(t, path)

			name, valid := serviceLockName(filepath.Base(path), "project", "api")
			if !valid {
				t.Fatal("serviceLockName() rejected valid identity")
			}

			test.setup(t, directory, name)

			serviceLock, lockErr := state.TryLockService("project", "api")
			if serviceLock != nil || !errors.Is(lockErr, ErrInvalidState) {
				t.Fatalf("TryLockService(unsafe) = %#v, %v", serviceLock, lockErr)
			}
		})
	}
}

func makeServiceLockSymlink(t *testing.T, directory, name string) {
	t.Helper()
	requireNoError(t, os.Symlink("target", filepath.Join(directory, name)))
}

func makeServiceLockHardLink(t *testing.T, directory, name string) {
	t.Helper()

	target := filepath.Join(directory, "target")
	requireNoError(t, os.WriteFile(target, nil, privateFileMode))
	requireNoError(t, os.Link(target, filepath.Join(directory, name)))
}

func makeServiceLockBroad(t *testing.T, directory, name string) {
	t.Helper()

	path := filepath.Join(directory, name)
	requireNoError(t, os.WriteFile(path, nil, privateFileMode))
	requireNoError(t, unix.Chmod(path, 0o644))
}

func makeServiceLockDirectory(t *testing.T, directory, name string) {
	t.Helper()
	requireNoError(t, os.Mkdir(filepath.Join(directory, name), 0o700))
}

func TestServiceLockDetectsReplacement(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))

	serviceLock := requireTryServiceLock(t, state, "project", "api")

	entry := filepath.Join(directory, serviceLock.name)
	requireNoError(t, os.Rename(entry, entry+".replaced"))
	requireNoError(t, os.WriteFile(entry, nil, privateFileMode))

	if serviceLock.Valid() {
		t.Fatal("service lock remained valid after entry replacement")
	}

	if !errors.Is(serviceLock.Close(), ErrInvalidState) {
		t.Fatal("ServiceLock.Close() accepted a replaced entry")
	}
}

func TestServiceLockRejectsReplacementDuringAcquisition(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))

	name, valid := serviceLockName(state.anchor.databaseName, "project", "api")
	if !valid {
		t.Fatal("serviceLockName() rejected valid identity")
	}

	serviceLock := requireOpenServiceLockFile(t, state.anchor, name)

	entry := filepath.Join(directory, serviceLock.name)
	requireNoError(t, os.Rename(entry, entry+".replaced"))
	requireNoError(t, os.WriteFile(entry, nil, privateFileMode))

	err := serviceLock.acquire()
	if !errors.Is(err, ErrInvalidState) || serviceLock.descriptor >= 0 {
		t.Fatalf("ServiceLock.acquire(replaced) = %v, descriptor %d", err, serviceLock.descriptor)
	}
}

func TestServiceLockRejectsInvalidIdentityAndClosedDescriptor(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))

	for _, identity := range [][2]string{{"", "api"}, {"project", ""}} {
		serviceLock, lockErr := state.TryLockService(identity[0], identity[1])
		if serviceLock != nil || !errors.Is(lockErr, ErrInvalidState) {
			t.Fatalf("TryLockService(invalid) = %#v, %v", serviceLock, lockErr)
		}
	}

	serviceLock := requireTryServiceLock(t, state, "project", "api")
	requireNoError(t, unix.Close(serviceLock.descriptor))
	serviceLock.descriptor = int(^uint(0) >> 1)

	if serviceLock.Valid() {
		t.Fatal("service lock remained valid after descriptor close")
	}

	if !errors.Is(serviceLock.Close(), ErrUnavailable) {
		t.Fatal("ServiceLock.Close() accepted a closed descriptor")
	}
}

func TestServiceLockNilReceiver(t *testing.T) {
	t.Parallel()

	if (*ServiceLock)(nil).Valid() || !errors.Is((*ServiceLock)(nil).Close(), ErrUnavailable) {
		t.Fatal("nil ServiceLock is valid or closed successfully")
	}

	nilStoreLock, nilStoreErr := (*Store)(nil).TryLockService("project", "api")
	if nilStoreLock != nil || !errors.Is(nilStoreErr, ErrInvalidState) {
		t.Fatalf("nil Store.TryLockService() = %#v, %v", nilStoreLock, nilStoreErr)
	}

	if !errors.Is((*ServiceLock)(nil).acquire(), ErrUnavailable) {
		t.Fatal("nil ServiceLock acquired successfully")
	}
}

func TestServiceLockNameSeparatesTupleBoundaries(t *testing.T) {
	t.Parallel()

	left, leftValid := serviceLockName("state.db", "a", "bc")

	right, rightValid := serviceLockName("state.db", "ab", "c")
	if !leftValid || !rightValid || left == right || filepath.Base(left) != left || filepath.Base(right) != right {
		t.Fatalf("service lock names = %q, %q", left, right)
	}

	if name, valid := serviceLockName("", "project", "api"); valid || name != "" {
		t.Fatalf("serviceLockName(empty database) = %q, %t", name, valid)
	}
}

func openServiceLockTestStore(t *testing.T, path string) *Store {
	t.Helper()

	state, err := Open(context.Background(), path)
	if err != nil || state == nil {
		t.Fatalf("Open() = %#v, %v", state, err)
	}

	t.Cleanup(func() {
		requireNoError(t, state.Close())
	})

	return state
}

func requireServiceLock(t *testing.T, lock *ServiceLock, err error) *ServiceLock {
	t.Helper()

	if err != nil || lock == nil {
		t.Fatalf("service lock = %#v, %v", lock, err)
	}

	return lock
}

func requireTryServiceLock(t *testing.T, state *Store, projectName, serviceName string) *ServiceLock {
	t.Helper()

	lock, err := state.TryLockService(projectName, serviceName)

	return requireServiceLock(t, lock, err)
}

func requireOpenServiceLockFile(t *testing.T, anchor *stateAnchor, name string) *ServiceLock {
	t.Helper()

	lock, err := openServiceLockFile(anchor, name)

	return requireServiceLock(t, lock, err)
}
