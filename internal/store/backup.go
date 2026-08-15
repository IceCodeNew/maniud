package store

import (
	"context"
	"database/sql"
	"errors"

	modernsqlite "modernc.org/sqlite"
)

const backupStepPages = 128

type onlineBackuper interface {
	NewBackup(destination string) (*modernsqlite.Backup, error)
}

type backupHandle interface {
	Step(pages int32) (bool, error)
	Finish() error
}

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

func performOnlineBackup(ctx context.Context, backup backupHandle) error {
	finished := false
	defer func() {
		if !finished {
			_ = backup.Finish()
		}
	}()

	for {
		if ctx.Err() != nil {
			return errors.Join(ErrUnavailable, ctx.Err())
		}

		more, stepErr := backup.Step(backupStepPages)
		if stepErr != nil {
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
