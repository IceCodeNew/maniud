//go:build linux || darwin

package store

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"testing"
	"time"

	"golang.org/x/sys/unix"
)

func TestOpenRejectsInvalidPaths(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	dirtyPath := directory + string(filepath.Separator) + ".." + string(filepath.Separator) + "state.db"

	for _, path := range []string{"", "state.db", dirtyPath, string(filepath.Separator)} {
		state, err := Open(context.Background(), path)
		if state != nil || !errors.Is(err, ErrInvalidPath) {
			t.Fatalf("Open(%q) = %#v, %v", path, state, err)
		}

		anchor, anchorErr := openStateAnchor(context.Background(), path)
		if anchor != nil || !errors.Is(anchorErr, ErrInvalidPath) {
			t.Fatalf("openStateAnchor(%q) = %#v, %v", path, anchor, anchorErr)
		}
	}
}

func TestOpenRejectsUnsafeFilesystemObjects(t *testing.T) {
	t.Parallel()

	for _, test := range unsafeFilesystemCases() {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			directory := privateTempDir(t)
			path := filepath.Join(directory, "state.db")
			test.setup(t, directory, path)

			state, err := Open(context.Background(), path)
			if state != nil || !errors.Is(err, ErrInvalidState) {
				t.Fatalf("Open() = %#v, %v", state, err)
			}
		})
	}
}

func unsafeFilesystemCases() []struct {
	name  string
	setup func(*testing.T, string, string)
} {
	return []struct {
		name  string
		setup func(*testing.T, string, string)
	}{
		{name: "broad directory", setup: makeBroadDirectory},
		{name: "symlink directory", setup: makeSymlinkDirectory},
		{name: "symlink database", setup: makeSymlinkDatabase},
		{name: "hard-linked database", setup: makeHardLinkedDatabase},
		{name: "broad database", setup: makeBroadDatabase},
		{name: "broad write ahead log", setup: makeBroadWriteAheadLog},
		{name: "symlink shared memory", setup: makeSymlinkSharedMemory},
		{name: "database directory", setup: makeDatabaseDirectory},
		{name: "symlink lock", setup: makeSymlinkLock},
		{name: "hard-linked lock", setup: makeHardLinkedLock},
		{name: "broad lock", setup: makeBroadLock},
		{name: "lock directory", setup: makeLockDirectory},
	}
}

func makeBroadDirectory(t *testing.T, directory, _ string) {
	t.Helper()
	requireNoError(t, unix.Chmod(directory, 0o755))
}

func makeSymlinkDirectory(t *testing.T, directory, _ string) {
	t.Helper()

	target := directory + "-target"
	requireNoError(t, os.Rename(directory, target))
	requireNoError(t, os.Symlink(target, directory))
}

func makeSymlinkDatabase(t *testing.T, _, path string) {
	t.Helper()
	requireNoError(t, os.Symlink("target", path))
}

func makeHardLinkedDatabase(t *testing.T, directory, path string) {
	t.Helper()

	target := filepath.Join(directory, "database-target")
	requireNoError(t, os.WriteFile(target, nil, 0o600))
	requireNoError(t, os.Link(target, path))
}

func makeBroadDatabase(t *testing.T, _, path string) {
	t.Helper()
	requireNoError(t, os.WriteFile(path, nil, 0o600))
	requireNoError(t, unix.Chmod(path, 0o644))
}

func makeBroadWriteAheadLog(t *testing.T, _, path string) {
	t.Helper()

	writeAheadLog := path + "-wal"
	requireNoError(t, os.WriteFile(writeAheadLog, nil, 0o600))
	requireNoError(t, unix.Chmod(writeAheadLog, 0o644))
}

func makeSymlinkSharedMemory(t *testing.T, _, path string) {
	t.Helper()
	requireNoError(t, os.Symlink("target", path+"-shm"))
}

func makeDatabaseDirectory(t *testing.T, _, path string) {
	t.Helper()
	requireNoError(t, os.Mkdir(path, 0o700))
}

func makeSymlinkLock(t *testing.T, _, path string) {
	t.Helper()
	requireNoError(t, os.Symlink("target", path+".lock"))
}

func makeHardLinkedLock(t *testing.T, directory, path string) {
	t.Helper()

	target := filepath.Join(directory, "lock-target")
	requireNoError(t, os.WriteFile(target, nil, 0o600))
	requireNoError(t, os.Link(target, path+".lock"))
}

func makeBroadLock(t *testing.T, _, path string) {
	t.Helper()

	lockPath := path + ".lock"
	requireNoError(t, os.WriteFile(lockPath, nil, 0o600))
	requireNoError(t, unix.Chmod(lockPath, 0o644))
}

func makeLockDirectory(t *testing.T, _, path string) {
	t.Helper()
	requireNoError(t, os.Mkdir(path+".lock", 0o700))
}

func TestOpenBoundsStartupLockWait(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")

	owner, err := openStateAnchor(context.Background(), path)
	if err != nil {
		t.Fatalf("openStateAnchor() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, owner.close())
	})

	ctx, cancel := context.WithTimeout(context.Background(), 40*time.Millisecond)
	defer cancel()

	state, err := Open(ctx, path)
	if state != nil || !errors.Is(err, context.DeadlineExceeded) || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open(contended) = %#v, %v", state, err)
	}
}

func TestOpenTimesOutStartupLockWait(t *testing.T) {
	t.Parallel()

	path := filepath.Join(privateTempDir(t), "state.db")

	owner, err := openStateAnchor(context.Background(), path)
	if err != nil {
		t.Fatalf("openStateAnchor() error = %v", err)
	}

	t.Cleanup(func() {
		requireNoError(t, owner.close())
	})

	state, err := Open(context.Background(), path)
	if state != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Open(contended) = %#v, %v", state, err)
	}
}

func TestAnchorDetectsPublicDirectoryReplacement(t *testing.T) {
	t.Parallel()

	directory := privateTempDir(t)
	path := filepath.Join(directory, "state.db")

	state, err := Open(context.Background(), path)
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	moved := directory + "-moved"
	requireNoError(t, os.Rename(directory, moved))
	requireNoError(t, os.Mkdir(directory, 0o700))

	if state.anchor.valid() {
		t.Fatal("anchor remained valid after public directory replacement")
	}

	requireNoError(t, state.Close())
}

// TestCloseReportsDescriptorFailure changes the process file-descriptor table.
//
//nolint:paralleltest // A parallel open could reuse the deliberately closed descriptor.
func TestCloseReportsDescriptorFailure(t *testing.T) {
	state, err := Open(context.Background(), filepath.Join(privateTempDir(t), "state.db"))
	if err != nil {
		t.Fatalf("Open() error = %v", err)
	}

	requireNoError(t, unix.Close(state.anchor.directory))

	err = state.Close()
	if !errors.Is(err, ErrUnavailable) {
		t.Fatalf("Close() error = %v", err)
	}
}

func TestFilesystemHelpersRejectInvalidDescriptors(t *testing.T) {
	t.Parallel()

	if _, valid := descriptorIdentity(-1); valid {
		t.Fatal("descriptorIdentity(-1) succeeded")
	}

	if _, valid := pathIdentity(filepath.Join(t.TempDir(), "missing")); valid {
		t.Fatal("pathIdentity(missing) succeeded")
	}

	if _, valid := entryIdentity(-1, "missing"); valid {
		t.Fatal("entryIdentity(-1) succeeded")
	}

	descriptor, err := openDirectory(filepath.Join(t.TempDir(), "missing"))
	if err == nil || descriptor >= 0 {
		t.Fatalf("openDirectory(missing) = %d, %v", descriptor, err)
	}

	err = waitForLock(context.Background(), -1)
	if err == nil {
		t.Fatal("waitForLock(-1) succeeded")
	}

	components := splitAbsolutePath(string(filepath.Separator))
	if components != nil {
		t.Fatalf("splitAbsolutePath(root) = %#v", components)
	}
}

func TestWaitForFileLockHonorsCancellationWhileBlocked(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	err := waitForFileLock(cancelled, 1, func(int) (bool, error) {
		return false, nil
	})
	if !errors.Is(err, ErrUnavailable) || !errors.Is(err, context.Canceled) {
		t.Fatalf("waitForFileLock(cancelled) = %v", err)
	}
}
