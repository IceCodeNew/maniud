//go:build linux || darwin

package store

import (
	"context"
	"database/sql"
	"errors"
)

// Fence verifies the filesystem capability and current SQLite owner immediately
// before an external effect.
func (lock *ServiceLock) Fence(ctx context.Context) error {
	return lock.fenceWith(ctx, checkWriterLease)
}

func (lock *ServiceLock) fenceWith(
	ctx context.Context,
	check func(context.Context, *sql.DB, writerLease) error,
) error {
	if lock == nil || lock.store == nil || lock.descriptor < 0 || check == nil {
		return ErrOwnershipLost
	}

	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	if !lock.Valid() {
		return ErrInvalidState
	}

	err := check(ctx, lock.store.database, lock.lease)
	if err != nil {
		return err
	}

	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	if !lock.Valid() {
		return ErrInvalidState
	}

	return nil
}

// withFencedWrite keeps the fence proof and typed durable mutations in one
// BEGIN IMMEDIATE transaction. The operation must condition each DML on lease.
func (lock *ServiceLock) withFencedWrite(
	ctx context.Context,
	operation func(context.Context, *sql.Tx, writerLease) error,
) error {
	if !validFencedWrite(lock, operation) {
		return ErrInvalidState
	}

	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	if !lock.Valid() {
		return ErrInvalidState
	}

	transaction, err := lock.store.database.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	err = proveWriterLease(ctx, transaction, lock.lease)
	if err != nil {
		return err
	}

	err = operation(ctx, transaction, lock.lease)
	if err != nil {
		return classifyWriterLeaseOperation(ctx, err)
	}

	err = proveWriterLease(ctx, transaction, lock.lease)
	if err != nil {
		return err
	}

	if !lock.Valid() {
		return ErrInvalidState
	}

	err = transaction.Commit()

	return classifyWriterLeaseResult(ctx, err)
}

func validFencedWrite(
	lock *ServiceLock,
	operation func(context.Context, *sql.Tx, writerLease) error,
) bool {
	return operation != nil && lock != nil && lock.store != nil && lock.descriptor >= 0
}

func proveWriterLease(ctx context.Context, transaction *sql.Tx, lease writerLease) error {
	if transaction == nil || lease.epoch <= 0 {
		return ErrOwnershipLost
	}

	result, err := transaction.ExecContext(
		ctx,
		"UPDATE writer_leases SET owner = owner "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?",
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	return requireWriterLeaseResult(result)
}

func requireWriterLeaseResult(result sql.Result) error {
	if result == nil {
		return ErrInvalidState
	}

	rows, err := result.RowsAffected()
	if err != nil {
		return ErrInvalidState
	}

	if rows != 1 {
		return ErrOwnershipLost
	}

	return nil
}

func classifyWriterLeaseResult(ctx context.Context, err error) error {
	if err == nil {
		return nil
	}

	return classifySQLiteProbe(ctx, err)
}

func classifyWriterLeaseOperation(ctx context.Context, err error) error {
	if ctx.Err() != nil {
		return classifyContext(ctx)
	}

	if errors.Is(err, ErrInvalidState) || errors.Is(err, ErrOwnershipLost) ||
		errors.Is(err, ErrUnavailable) {
		return err
	}

	return ErrInvalidState
}
