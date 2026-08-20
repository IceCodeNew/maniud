package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

type doctorReport struct {
	Root     string              `json:"root"`
	Entries  []doctorReportEntry `json:"entries"`
	Replaced bool                `json:"replaced"`
}

type doctorReportEntry struct {
	TransactionID  string `json:"transaction_id"`
	ManifestPath   string `json:"manifest_path"`
	ManifestDigest string `json:"manifest_digest"`
	CreatedUnix    int64  `json:"created_unix"`
}

type doctorBackupIndexLock interface {
	ReplaceBackupIndex(ctx context.Context, candidates []store.BackupIndexCandidate) error
	Close() error
}

type doctorDependencies struct {
	openState       func(context.Context, string) (*store.Store, error)
	lockBackupIndex func(context.Context, *store.Store) (doctorBackupIndexLock, error)
	scanBackupRoot  func(context.Context, string) ([]backup.Publication, error)
}

func defaultDoctorDependencies() doctorDependencies {
	return doctorDependencies{
		openState: store.Open,
		lockBackupIndex: func(ctx context.Context, state *store.Store) (doctorBackupIndexLock, error) {
			return state.LockBackupIndex(ctx)
		},
		scanBackupRoot: scanBackupRoot,
	}
}

func executeDoctor(
	ctx context.Context,
	arguments doctorInvocation,
	output io.Writer,
	environment map[string]string,
) error {
	return executeDoctorWithDependencies(
		ctx,
		arguments,
		output,
		environment,
		defaultDoctorDependencies(),
	)
}

func executeDoctorWithDependencies(
	ctx context.Context,
	arguments doctorInvocation,
	output io.Writer,
	environment map[string]string,
	dependencies doctorDependencies,
) error {
	if !arguments.reindexBackups {
		return errInvalidArguments
	}

	statePath, err := doctorStatePath(arguments.state, environment)
	if err != nil {
		return err
	}

	var (
		root         string
		publications []backup.Publication
	)
	if arguments.confirm {
		root, publications, err = rebuildBackupIndex(ctx, statePath, dependencies)
	} else {
		root = filepath.Join(filepath.Dir(statePath), "backups")
		publications, err = dependencies.scanBackupRoot(ctx, root)
	}
	if err != nil {
		return err
	}

	return writeDoctorReport(
		output,
		root,
		doctorReportEntries(publications),
		arguments.confirm,
	)
}

func doctorStatePath(explicit string, environment map[string]string) (string, error) {
	if explicit != "" {
		if !filepath.IsAbs(explicit) || filepath.Clean(explicit) != explicit {
			return "", errStateHomeInvalid
		}

		return explicit, nil
	}

	return defaultStatePath(environment)
}

func writeDoctorReport(output io.Writer, root string, entries []doctorReportEntry, replaced bool) error {
	err := json.NewEncoder(output).Encode(doctorReport{
		Root:     root,
		Entries:  entries,
		Replaced: replaced,
	})
	if err != nil {
		return fmt.Errorf("encode doctor report: %w", err)
	}

	return nil
}

func classifyDoctorCommandFailure(err error) *domain.FailureError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, context.Canceled):
		return domain.OperationCancelled()
	case errors.Is(err, errInvalidArguments),
		errors.Is(err, errStateHomeUnavailable),
		errors.Is(err, errStateHomeInvalid),
		errors.Is(err, errGitOpsRepositoryInvalid):
		return domain.InvalidInput()
	default:
		return domain.ApplyFailed(false)
	}
}
