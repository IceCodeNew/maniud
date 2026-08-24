//go:build linux || darwin

package store

import (
	"context"
	"errors"

	"golang.org/x/sys/unix"
)

const stateOperationLockSuffix = ".operations.lock"

type stateOperationLock struct {
	anchor     *stateAnchor
	descriptor int
	name       string
	identity   fileIdentity
}

func trySharedStateOperation(
	ctx context.Context,
	anchor *stateAnchor,
) (*stateOperationLock, error) {
	return openStateOperation(ctx, anchor, false)
}

func lockExclusiveStateOperation(
	ctx context.Context,
	anchor *stateAnchor,
) (*stateOperationLock, error) {
	return openStateOperation(ctx, anchor, true)
}

func openStateOperation(
	ctx context.Context,
	anchor *stateAnchor,
	wait bool,
) (*stateOperationLock, error) {
	return openStateOperationWith(ctx, anchor, wait, (*stateOperationLock).valid)
}

func openStateOperationWith(
	ctx context.Context,
	anchor *stateAnchor,
	wait bool,
	validate func(*stateOperationLock) bool,
) (*stateOperationLock, error) {
	if anchor == nil || !anchor.valid() || validate == nil {
		return nil, ErrInvalidState
	}

	name := anchor.databaseName + stateOperationLockSuffix
	descriptor, err := unix.Openat(
		anchor.directory,
		name,
		unix.O_RDWR|unix.O_CREAT|unix.O_CLOEXEC|unix.O_NOFOLLOW,
		privateFileMode,
	)
	if err != nil {
		return nil, ErrInvalidState
	}

	identity, valid := descriptorIdentity(descriptor)
	operation := &stateOperationLock{
		anchor:     anchor,
		descriptor: descriptor,
		name:       name,
		identity:   identity,
	}
	if !valid || !privateRegular(identity) {
		_ = operation.close()

		return nil, ErrInvalidState
	}

	err = acquireStateOperation(ctx, descriptor, wait)
	if err != nil {
		_ = operation.close()

		return nil, err
	}

	if !validate(operation) {
		_ = operation.close()

		return nil, ErrInvalidState
	}

	return operation, nil
}

func acquireStateOperation(ctx context.Context, descriptor int, wait bool) error {
	if wait {
		return waitForLock(ctx, descriptor)
	}

	acquired, err := trySharedLock(descriptor)
	if err == nil && !acquired {
		return ErrUnavailable
	}

	return err
}

func (operation *stateOperationLock) valid() bool {
	if operation == nil || operation.descriptor < 0 || operation.anchor == nil ||
		!operation.anchor.valid() {
		return false
	}

	identity, valid := descriptorIdentity(operation.descriptor)

	return valid && privateRegular(identity) && identity == operation.identity &&
		operation.anchor.validEntry(operation.name, operation.identity)
}

func (operation *stateOperationLock) close() error {
	if operation == nil || operation.descriptor < 0 {
		return ErrUnavailable
	}

	descriptor := operation.descriptor
	operation.descriptor = -1

	unlockErr := unix.Flock(descriptor, unix.LOCK_UN)
	closeErr := unix.Close(descriptor)
	if unlockErr != nil || closeErr != nil {
		return errors.Join(ErrUnavailable, unlockErr, closeErr)
	}

	return nil
}
