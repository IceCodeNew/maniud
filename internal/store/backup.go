package store

import (
	"context"
	"database/sql"
	"errors"
	"time"

	modernsqlite "modernc.org/sqlite"
)

const (
	backupStepPages    = 128
	backupRetryLimit   = 25
	backupRetryDelay   = 10 * time.Millisecond
	sqliteResultMask   = 0xff
	sqliteResultBusy   = 5
	sqliteResultLocked = 6
)

type onlineBackuper interface {
	NewBackup(destination string) (*modernsqlite.Backup, error)
}

type onlineRestorer interface {
	NewRestore(source string) (*modernsqlite.Backup, error)
}

type backupHandle interface {
	Step(pages int32) (bool, error)
	Finish() error
}

type backupWaiter func(context.Context) bool

func backupSQLite(ctx context.Context, database *sql.DB, destination string) error {
	connection, err := database.Conn(ctx)
	if err != nil {
		return classifyContext(ctx)
	}

	err = connection.Raw(func(raw any) error {
		return runOnlineBackup(ctx, raw, destination)
	})
	closeErr := connection.Close()

	return classifyBackupResult(ctx, err, closeErr)
}

func runOnlineBackup(ctx context.Context, raw any, destination string) error {
	maker, valid := raw.(onlineBackuper)
	if !valid {
		return ErrInvalidState
	}

	backup, err := maker.NewBackup(destination)
	if err != nil {
		return ErrUnavailable
	}

	return performOnlineBackup(ctx, backup)
}

func restoreSQLite(ctx context.Context, database *sql.DB, source string) error {
	database.SetMaxIdleConns(0)
	database.SetMaxOpenConns(1)

	defer func() {
		database.SetMaxOpenConns(maximumConnections)
		database.SetMaxIdleConns(maximumConnections)
	}()

	connection, err := database.Conn(ctx)
	if err != nil {
		return classifyContext(ctx)
	}

	err = connection.Raw(func(raw any) error {
		return runOnlineRestore(ctx, raw, source)
	})
	closeErr := connection.Close()

	return classifyBackupResult(ctx, err, closeErr)
}

func runOnlineRestore(ctx context.Context, raw any, source string) error {
	maker, valid := raw.(onlineRestorer)
	if !valid {
		return ErrInvalidState
	}

	restore, err := maker.NewRestore(source)
	if err != nil {
		return ErrUnavailable
	}

	return performOnlineBackup(ctx, restore)
}

func performOnlineBackup(ctx context.Context, backup backupHandle) error {
	return performOnlineBackupWithWait(ctx, backup, waitForBackupRetry)
}

func performOnlineBackupWithWait(ctx context.Context, backup backupHandle, wait backupWaiter) error {
	finished := false
	defer func() {
		if !finished {
			_ = backup.Finish()
		}
	}()

	retries := 0

	for {
		if ctx.Err() != nil {
			return errors.Join(ErrUnavailable, ctx.Err())
		}

		more, stepErr := backup.Step(backupStepPages)
		if stepErr != nil {
			if retryableSQLiteError(stepErr) && retries < backupRetryLimit {
				retries++

				if wait(ctx) {
					continue
				}

				return classifyContext(ctx)
			}

			return ErrUnavailable
		}

		if !more {
			break
		}
	}

	finishErr := backup.Finish()
	finished = true

	if finishErr != nil {
		return ErrUnavailable
	}

	return nil
}

func retryableSQLiteError(err error) bool {
	var result interface{ Code() int }
	if !errors.As(err, &result) {
		return false
	}

	code := result.Code() & sqliteResultMask

	return code == sqliteResultBusy || code == sqliteResultLocked
}

func waitForBackupRetry(ctx context.Context) bool {
	timer := time.NewTimer(backupRetryDelay)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return false
	case <-timer.C:
		return true
	}
}

func classifyBackupResult(ctx context.Context, operationErr, closeErr error) error {
	if operationErr != nil {
		if ctx.Err() != nil {
			return classifyContext(ctx)
		}

		if errors.Is(operationErr, ErrInvalidState) {
			return ErrInvalidState
		}

		return ErrUnavailable
	}

	if closeErr != nil {
		return ErrUnavailable
	}

	return nil
}
