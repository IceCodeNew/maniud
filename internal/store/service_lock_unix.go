//go:build linux || darwin

package store

import (
	"context"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"golang.org/x/sys/unix"
)

const serviceLockPrefix = ".maniud-service-"

// ServiceLock owns one project and service transaction lock while its Store
// remains open. Callers must not use a ServiceLock concurrently with Close.
type ServiceLock struct {
	anchor     *stateAnchor
	descriptor int
	name       string
	identity   fileIdentity
}

// TryLockService acquires the project and service transaction lock without
// waiting for another owner.
func (store *Store) TryLockService(projectName, serviceName string) (*ServiceLock, error) {
	return store.openServiceLock(context.Background(), projectName, serviceName, false)
}

// LockService waits for the project and service transaction lock until the
// context ends or the bounded store lock timeout expires.
func (store *Store) LockService(
	ctx context.Context,
	projectName string,
	serviceName string,
) (*ServiceLock, error) {
	return store.openServiceLock(ctx, projectName, serviceName, true)
}

func (store *Store) openServiceLock(
	ctx context.Context,
	projectName string,
	serviceName string,
	wait bool,
) (*ServiceLock, error) {
	if store == nil || store.anchor == nil {
		return nil, ErrInvalidState
	}

	name, valid := serviceLockName(store.anchor.databaseName, projectName, serviceName)
	if !valid || !store.anchor.valid() {
		return nil, ErrInvalidState
	}

	serviceLock, err := openServiceLockFile(store.anchor, name)
	if err != nil {
		return nil, err
	}

	err = serviceLock.acquire(ctx, wait)
	if err != nil {
		if serviceLock.descriptor >= 0 {
			_ = unix.Close(serviceLock.descriptor)
			serviceLock.descriptor = -1
		}

		return nil, err
	}

	return serviceLock, nil
}

func openServiceLockFile(anchor *stateAnchor, name string) (*ServiceLock, error) {
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
	serviceLock := &ServiceLock{
		anchor:     anchor,
		descriptor: descriptor,
		name:       name,
		identity:   identity,
	}

	if !valid || !privateRegular(identity) || !serviceLock.Valid() {
		_ = unix.Close(descriptor)
		serviceLock.descriptor = -1

		return nil, ErrInvalidState
	}

	return serviceLock, nil
}

func serviceLockName(databaseName, projectName, serviceName string) (string, bool) {
	if databaseName == "" || projectName == "" || serviceName == "" {
		return "", false
	}

	identity := make([]byte, 0, 3*8+len(databaseName)+len(projectName)+len(serviceName))
	for _, value := range []string{databaseName, projectName, serviceName} {
		identity = binary.BigEndian.AppendUint64(identity, uint64(len(value)))
		identity = append(identity, value...)
	}

	digest := sha256.Sum256(identity)

	return serviceLockPrefix + hex.EncodeToString(digest[:]) + ".lock", true
}

// Valid reports whether the lock descriptor, its directory entry, and the
// Store filesystem anchor still identify the acquired objects.
func (lock *ServiceLock) Valid() bool {
	if lock == nil || lock.descriptor < 0 || !lock.anchor.valid() {
		return false
	}

	identity, valid := descriptorIdentity(lock.descriptor)

	return valid && privateRegular(identity) && identity == lock.identity &&
		lock.anchor.validEntry(lock.name, lock.identity)
}

// Close releases the transaction lock and closes its retained descriptor.
// It never removes the persistent lock entry.
func (lock *ServiceLock) Close() error {
	if lock == nil || lock.descriptor < 0 {
		return ErrUnavailable
	}

	descriptor := lock.descriptor
	lock.descriptor = -1

	unlockErr := unix.Flock(descriptor, unix.LOCK_UN)

	closeErr := unix.Close(descriptor)
	if unlockErr != nil || closeErr != nil {
		return errors.Join(ErrUnavailable, unlockErr, closeErr)
	}

	return nil
}

func (lock *ServiceLock) acquire(ctx context.Context, wait bool) error {
	if lock == nil || lock.descriptor < 0 {
		return ErrUnavailable
	}

	var err error

	if wait {
		err = waitForLock(ctx, lock.descriptor)
	} else {
		var acquired bool

		acquired, err = tryLock(lock.descriptor)
		if err == nil && !acquired {
			err = ErrUnavailable
		}
	}

	if err != nil {
		return err
	}

	if !lock.Valid() {
		_ = lock.Close()

		return ErrInvalidState
	}

	return nil
}
