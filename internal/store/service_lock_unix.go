//go:build linux || darwin

package store

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"encoding/binary"
	"encoding/hex"
	"errors"

	"golang.org/x/sys/unix"
)

const (
	identityLengthBytes = 8
	serviceLockPrefix   = ".maniud-service-"
)

// ServiceLock owns one project and service transaction lock while its Store
// remains open. Callers must not use a ServiceLock concurrently with Close.
type ServiceLock struct {
	anchor     *stateAnchor
	descriptor int
	name       string
	identity   fileIdentity
	operation  *stateOperationLock
	store      *Store
	lease      writerLease
}

// TryLockService acquires the project and service transaction lock without
// waiting for another owner.
func (store *Store) TryLockService(projectName, serviceName string) (*ServiceLock, error) {
	return store.openServiceLock(context.Background(), projectName, serviceName)
}

func (store *Store) openServiceLock(
	ctx context.Context,
	projectName string,
	serviceName string,
) (*ServiceLock, error) {
	return store.openServiceLockWith(ctx, projectName, serviceName, newWriterLease)
}

func (store *Store) openServiceLockWith(
	ctx context.Context,
	projectName string,
	serviceName string,
	acquireLease func(context.Context, *sql.DB, [sha256.Size]byte) (writerLease, error),
) (*ServiceLock, error) {
	if !validServiceLockRequest(store, acquireLease) {
		return nil, ErrInvalidState
	}

	serviceID, valid := serviceIdentity(projectName, serviceName)
	if !valid {
		return nil, ErrInvalidState
	}

	name, valid := serviceLockName(store.anchor.databaseName, projectName, serviceName)
	if !valid || !store.anchor.valid() {
		return nil, ErrInvalidState
	}

	operation, err := trySharedStateOperation(ctx, store.anchor)
	if err != nil {
		return nil, err
	}

	serviceLock, err := openServiceLockFile(store.anchor, name)
	if err != nil {
		_ = operation.close()

		return nil, err
	}

	serviceLock.operation = operation
	serviceLock.store = store

	err = serviceLock.acquire()
	if err != nil {
		_ = serviceLock.closeFilesystem()

		return nil, err
	}

	serviceLock.lease, err = acquireLease(ctx, store.database, serviceID)
	if err != nil {
		_ = serviceLock.closeFilesystem()

		return nil, err
	}

	if !serviceLock.Valid() {
		releaseErr := releaseWriterLease(context.WithoutCancel(ctx), store.database, serviceLock.lease)
		closeErr := serviceLock.closeFilesystem()

		return nil, errors.Join(ErrInvalidState, releaseErr, closeErr)
	}

	return serviceLock, nil
}

func validServiceLockRequest(
	store *Store,
	acquireLease func(context.Context, *sql.DB, [sha256.Size]byte) (writerLease, error),
) bool {
	return store != nil && store.anchor != nil && acquireLease != nil
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
		operation:  nil,
		store:      nil,
		lease: writerLease{
			serviceID: [sha256.Size]byte{},
			epoch:     0,
			owner:     [writerOwnerBytes]byte{},
		},
	}

	if !valid || !privateRegular(identity) || !serviceLock.Valid() {
		_ = unix.Close(descriptor)
		serviceLock.descriptor = -1

		return nil, ErrInvalidState
	}

	return serviceLock, nil
}

func serviceLockName(databaseName, projectName, serviceName string) (string, bool) {
	digest, valid := identityDigest(databaseName, projectName, serviceName)
	if !valid {
		return "", false
	}

	return serviceLockPrefix + hex.EncodeToString(digest[:]) + ".lock", true
}

func identityDigest(values ...string) ([sha256.Size]byte, bool) {
	size := len(values) * identityLengthBytes
	for _, value := range values {
		if value == "" {
			return [sha256.Size]byte{}, false
		}

		size += len(value)
	}

	identity := make([]byte, 0, size)
	for _, value := range values {
		identity = binary.BigEndian.AppendUint64(identity, uint64(len(value)))
		identity = append(identity, value...)
	}

	return sha256.Sum256(identity), true
}

// Valid reports whether the lock descriptor, its directory entry, and the
// Store filesystem anchor still identify the acquired objects.
func (lock *ServiceLock) Valid() bool {
	if lock == nil || lock.descriptor < 0 || !lock.anchor.valid() {
		return false
	}
	if lock.operation != nil && !lock.operation.valid() {
		return false
	}

	identity, valid := descriptorIdentity(lock.descriptor)

	return valid && privateRegular(identity) && identity == lock.identity &&
		lock.anchor.validEntry(lock.name, lock.identity)
}

// Close clears the current SQLite writer owner before releasing the filesystem
// lock. It never removes the lease row or persistent lock entry.
func (lock *ServiceLock) Close() error {
	if lock == nil || lock.descriptor < 0 {
		return ErrUnavailable
	}

	valid := lock.Valid()

	releaseErr := ErrInvalidState
	if lock.store != nil {
		releaseErr = releaseWriterLease(context.Background(), lock.store.database, lock.lease)
	}

	valid = valid && lock.Valid()

	closeErr := lock.closeFilesystem()
	if !valid {
		return errors.Join(ErrInvalidState, releaseErr, closeErr)
	}

	return errors.Join(releaseErr, closeErr)
}

func (lock *ServiceLock) closeFilesystem() error {
	if lock == nil || lock.descriptor < 0 {
		return ErrUnavailable
	}

	descriptor := lock.descriptor
	lock.descriptor = -1

	unlockErr := unix.Flock(descriptor, unix.LOCK_UN)

	closeErr := unix.Close(descriptor)
	operationErr := error(nil)
	if lock.operation != nil {
		operationErr = lock.operation.close()
	}
	if unlockErr != nil || closeErr != nil || operationErr != nil {
		return errors.Join(ErrUnavailable, unlockErr, closeErr, operationErr)
	}

	return nil
}

func (lock *ServiceLock) acquire() error {
	if lock == nil || lock.descriptor < 0 {
		return ErrUnavailable
	}

	acquired, err := tryLock(lock.descriptor)
	if err == nil && !acquired {
		err = ErrUnavailable
	}

	if err != nil {
		return err
	}

	if !lock.Valid() {
		_ = lock.closeFilesystem()

		return ErrInvalidState
	}

	return nil
}
