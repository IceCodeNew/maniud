//go:build linux || darwin

package store

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"database/sql"
	"io"
	"math"
)

const writerOwnerBytes = 16

type writerLease struct {
	serviceID [sha256.Size]byte
	epoch     int64
	owner     [writerOwnerBytes]byte
}

func serviceIdentity(projectName, serviceName string) ([sha256.Size]byte, bool) {
	return identityDigest(projectName, serviceName)
}

func newWriterLease(
	ctx context.Context,
	database *sql.DB,
	serviceID [sha256.Size]byte,
) (writerLease, error) {
	return acquireWriterLease(ctx, database, serviceID, rand.Reader)
}

func acquireWriterLease(
	ctx context.Context,
	database *sql.DB,
	serviceID [sha256.Size]byte,
	random io.Reader,
) (writerLease, error) {
	lease := writerLease{serviceID: serviceID, epoch: 0, owner: [writerOwnerBytes]byte{}}
	if !validWriterLeaseAcquisition(database, random) {
		return lease, ErrInvalidState
	}

	_, err := io.ReadFull(random, lease.owner[:])
	if err != nil {
		return writerLease{}, ErrUnavailable
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return writerLease{}, classifySQLiteProbe(ctx, err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	err = advanceWriterLease(ctx, transaction, &lease)
	if err != nil {
		return writerLease{}, err
	}

	err = transaction.Commit()
	if err != nil {
		return writerLease{}, classifySQLiteProbe(ctx, err)
	}

	return lease, nil
}

func advanceWriterLease(ctx context.Context, transaction *sql.Tx, lease *writerLease) error {
	result, err := transaction.ExecContext(
		ctx,
		"INSERT INTO writer_leases (service_id, epoch, owner) VALUES (?, 1, ?) "+
			"ON CONFLICT(service_id) DO UPDATE SET "+
			"epoch = writer_leases.epoch + 1, owner = excluded.owner "+
			"WHERE writer_leases.epoch < ?",
		lease.serviceID[:],
		lease.owner[:],
		int64(math.MaxInt64),
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	rows, err := result.RowsAffected()
	if err != nil || rows != 1 {
		return ErrInvalidState
	}

	err = transaction.QueryRowContext(
		ctx,
		"SELECT epoch FROM writer_leases WHERE service_id = ? AND owner = ?",
		lease.serviceID[:],
		lease.owner[:],
	).Scan(&lease.epoch)
	if err != nil {
		return classifyWriterLeaseResult(ctx, err)
	}

	if lease.epoch <= 0 {
		return ErrInvalidState
	}

	return nil
}

func validWriterLeaseAcquisition(database *sql.DB, random io.Reader) bool {
	return database != nil && random != nil
}

func checkWriterLease(ctx context.Context, database *sql.DB, lease writerLease) error {
	if database == nil || lease.epoch <= 0 {
		return ErrOwnershipLost
	}

	var matches int

	err := database.QueryRowContext(
		ctx,
		"SELECT count(*) FROM writer_leases WHERE service_id = ? AND epoch = ? AND owner = ?",
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	).Scan(&matches)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	if matches != 1 {
		return ErrOwnershipLost
	}

	return nil
}

func releaseWriterLease(ctx context.Context, database *sql.DB, lease writerLease) error {
	if database == nil || lease.epoch <= 0 {
		return ErrOwnershipLost
	}

	transaction, err := database.BeginTx(ctx, nil)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}
	defer func() {
		_ = transaction.Rollback()
	}()

	result, err := transaction.ExecContext(
		ctx,
		"UPDATE writer_leases SET owner = NULL "+
			"WHERE service_id = ? AND epoch = ? AND owner = ?",
		lease.serviceID[:],
		lease.epoch,
		lease.owner[:],
	)
	if err != nil {
		return classifySQLiteProbe(ctx, err)
	}

	err = requireWriterLeaseResult(result)
	if err != nil {
		return err
	}

	err = transaction.Commit()

	return classifyWriterLeaseResult(ctx, err)
}
