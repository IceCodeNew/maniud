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

func TestStateOperationLockExcludesServiceWriters(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))

	exclusive, err := lockExclusiveStateOperation(context.Background(), state.anchor)
	requireNoError(t, err)

	service, err := state.TryLockService("project", "api")
	if service != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("TryLockService(exclusive state operation) = %#v, %v", service, err)
	}

	requireNoError(t, exclusive.close())

	service = requireTryServiceLock(t, state, "project", "api")
	exclusive, err = lockExclusiveStateOperation(context.Background(), state.anchor)
	if exclusive != nil || !errors.Is(err, ErrUnavailable) {
		t.Fatalf("lockExclusiveStateOperation(service writer) = %#v, %v", exclusive, err)
	}

	requireNoError(t, service.Close())

	exclusive, err = lockExclusiveStateOperation(context.Background(), state.anchor)
	requireNoError(t, err)
	requireNoError(t, exclusive.close())
}

func TestStateOperationLockRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))

	operation, err := openStateOperation(context.Background(), nil, false)
	if operation != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("openStateOperation(nil) = %#v, %v", operation, err)
	}
	operation, err = openStateOperationWith(context.Background(), state.anchor, false, nil)
	if operation != nil || !errors.Is(err, ErrInvalidState) {
		t.Fatalf("openStateOperationWith(nil validator) = %#v, %v", operation, err)
	}

	if (*stateOperationLock)(nil).valid() ||
		!errors.Is((*stateOperationLock)(nil).close(), ErrUnavailable) {
		t.Fatal("nil state operation lock is valid or closed successfully")
	}

	operation, err = trySharedStateOperation(context.Background(), state.anchor)
	requireNoError(t, err)
	requireNoError(t, operation.close())

	if operation.valid() || !errors.Is(operation.close(), ErrUnavailable) {
		t.Fatal("closed state operation lock is valid or closed twice")
	}
}

func TestStateOperationLockRejectsUnsafeAndChangedEntries(t *testing.T) {
	t.Parallel()

	t.Run("open failure", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		name := state.anchor.databaseName + stateOperationLockSuffix
		requireNoError(t, os.Mkdir(filepath.Join(state.anchor.directoryPath, name), 0o700))

		operation, err := trySharedStateOperation(context.Background(), state.anchor)
		if operation != nil || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("trySharedStateOperation(directory) = %#v, %v", operation, err)
		}
	})

	t.Run("unsafe file", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		path := filepath.Join(state.anchor.directoryPath, state.anchor.databaseName+stateOperationLockSuffix)
		requireNoError(t, os.WriteFile(path, nil, 0o600))
		//nolint:gosec // The deliberately unsafe mode is the fixture under test.
		requireNoError(t, os.Chmod(path, 0o644))

		operation, err := trySharedStateOperation(context.Background(), state.anchor)
		if operation != nil || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("trySharedStateOperation(broad file) = %#v, %v", operation, err)
		}
	})

	t.Run("post-acquisition validation", func(t *testing.T) {
		t.Parallel()

		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		operation, err := openStateOperationWith(
			context.Background(),
			state.anchor,
			false,
			func(*stateOperationLock) bool { return false },
		)
		if operation != nil || !errors.Is(err, ErrInvalidState) {
			t.Fatalf("openStateOperationWith(rejected) = %#v, %v", operation, err)
		}
	})
}

// TestStateOperationLockContainsDescriptorAndEntryReplacement mutates the
// process-wide descriptor table.
//
//nolint:paralleltest // A parallel open could reuse the deliberately closed descriptor.
func TestStateOperationLockContainsDescriptorAndEntryReplacement(t *testing.T) {
	// This test deliberately closes a live process descriptor. Keep both cases
	// serial so another test cannot reuse the descriptor number before close()
	// observes the failure.
	t.Run("close failure", func(t *testing.T) {
		state := openServiceLockTestStore(t, filepath.Join(privateTempDir(t), "state.db"))
		operation, err := trySharedStateOperation(context.Background(), state.anchor)
		requireNoError(t, err)
		requireNoError(t, unix.Close(operation.descriptor))

		if !errors.Is(operation.close(), ErrUnavailable) {
			t.Fatal("state operation lock hid descriptor close failure")
		}
	})

	t.Run("service writer gate replacement", func(t *testing.T) {
		directory := privateTempDir(t)
		state := openServiceLockTestStore(t, filepath.Join(directory, "state.db"))
		service := requireTryServiceLock(t, state, "project", "api")
		entry := filepath.Join(directory, service.operation.name)
		requireNoError(t, os.Rename(entry, entry+".replaced"))
		requireNoError(t, os.WriteFile(entry, nil, privateFileMode))

		if service.Valid() || !errors.Is(service.Close(), ErrInvalidState) {
			t.Fatal("service writer accepted a replaced state operation gate")
		}
	})
}
