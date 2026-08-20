package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"path/filepath"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

var (
	errDoctorTest     = errors.New("doctor test failure")
	errDoctorLockTest = errors.New("backup index lock is not held")
)

func TestExecuteDoctorConfirmsScanUnderLock(t *testing.T) {
	t.Parallel()

	statePath := privateDoctorStatePath(t)
	publication := testDoctorPublication()
	var (
		locked      bool
		scannedRoot string
		captured    []store.BackupIndexCandidate
	)
	lock := &doctorTestLock{
		locked:   &locked,
		captured: &captured,
	}
	dependencies := defaultDoctorDependencies()
	dependencies.lockBackupIndex = func(context.Context, *store.Store) (doctorBackupIndexLock, error) {
		locked = true

		return lock, nil
	}
	dependencies.scanBackupRoot = func(_ context.Context, root string) ([]backup.Publication, error) {
		if !locked {
			return nil, errDoctorLockTest
		}
		scannedRoot = root

		return []backup.Publication{publication}, nil
	}

	output := new(bytes.Buffer)
	err := executeDoctorWithDependencies(context.Background(), doctorInvocation{
		reindexBackups: true,
		confirm:        true,
		state:          statePath,
	}, output, nil, dependencies)
	if err != nil {
		t.Fatalf("executeDoctorWithDependencies() error = %v", err)
	}
	var report doctorReport
	if err = json.Unmarshal(output.Bytes(), &report); err != nil {
		t.Fatalf("Unmarshal() error = %v", err)
	}
	assertDoctorConfirmResult(t, statePath, scannedRoot, locked, captured, publication, report)
}

func TestExecuteDoctorDoesNotReportFailedReplacement(t *testing.T) {
	t.Parallel()

	statePath := privateDoctorStatePath(t)
	dependencies := defaultDoctorDependencies()
	dependencies.lockBackupIndex = func(context.Context, *store.Store) (doctorBackupIndexLock, error) {
		return &doctorTestLock{replaceErr: store.ErrInvalidState}, nil
	}
	dependencies.scanBackupRoot = func(context.Context, string) ([]backup.Publication, error) {
		return nil, nil
	}

	output := new(bytes.Buffer)
	err := executeDoctorWithDependencies(context.Background(), doctorInvocation{
		reindexBackups: true,
		confirm:        true,
		state:          statePath,
	}, output, nil, dependencies)
	if !errors.Is(err, store.ErrInvalidState) {
		t.Fatalf("executeDoctorWithDependencies() error = %v", err)
	}
	if output.Len() != 0 {
		t.Fatalf("output = %q", output.String())
	}
}

func TestRebuildBackupIndexReportsDependencyFailures(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name   string
		mutate func(*doctorDependencies)
	}{
		{
			name: "open state",
			mutate: func(dependencies *doctorDependencies) {
				dependencies.openState = func(context.Context, string) (*store.Store, error) {
					return nil, errDoctorTest
				}
			},
		},
		{
			name: "lock index",
			mutate: func(dependencies *doctorDependencies) {
				dependencies.lockBackupIndex = func(context.Context, *store.Store) (doctorBackupIndexLock, error) {
					return nil, errDoctorTest
				}
			},
		},
		{
			name: "scan backups",
			mutate: func(dependencies *doctorDependencies) {
				dependencies.lockBackupIndex = func(context.Context, *store.Store) (doctorBackupIndexLock, error) {
					return &doctorTestLock{}, nil
				}
				dependencies.scanBackupRoot = func(context.Context, string) ([]backup.Publication, error) {
					return nil, errDoctorTest
				}
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			dependencies := defaultDoctorDependencies()
			test.mutate(&dependencies)
			_, _, err := rebuildBackupIndex(
				context.Background(),
				privateDoctorStatePath(t),
				dependencies,
			)
			if !errors.Is(err, errDoctorTest) {
				t.Fatalf("rebuildBackupIndex() error = %v", err)
			}
		})
	}
}

type doctorTestLock struct {
	locked     *bool
	captured   *[]store.BackupIndexCandidate
	replaceErr error
}

func (lock *doctorTestLock) ReplaceBackupIndex(
	_ context.Context,
	candidates []store.BackupIndexCandidate,
) error {
	if lock.locked != nil && !*lock.locked {
		return errDoctorLockTest
	}
	if lock.captured != nil {
		*lock.captured = append([]store.BackupIndexCandidate(nil), candidates...)
	}

	return lock.replaceErr
}

func (lock *doctorTestLock) Close() error {
	if lock.locked != nil {
		*lock.locked = false
	}

	return nil
}

func testDoctorPublication() backup.Publication {
	transaction := backup.Identifier{1}

	return backup.Publication{
		Manifest: backup.Manifest{
			Version:               1,
			TransactionID:         transaction,
			BaseTransactionID:     backup.Identifier{2},
			Project:               "project",
			Service:               "service",
			Runtime:               domain.RuntimeDocker,
			CreatedUnix:           42,
			SourceDigest:          domain.Digest{3},
			EffectiveDigest:       domain.Digest{4},
			ExecutionDigest:       domain.Digest{5},
			PredecessorWorkloadID: "old-workload",
		},
		ManifestPath:   transaction.String() + "/manifest.json",
		ManifestDigest: domain.Digest{6},
	}
}

func testDoctorCandidate(publication backup.Publication) store.BackupIndexCandidate {
	manifest := publication.Manifest

	return store.BackupIndexCandidate{
		TransactionID:         store.TransactionID(manifest.TransactionID),
		Project:               manifest.Project,
		Service:               manifest.Service,
		Runtime:               manifest.Runtime,
		SourceDigest:          manifest.SourceDigest,
		EffectiveDigest:       manifest.EffectiveDigest,
		ExecutionDigest:       manifest.ExecutionDigest,
		BaseTransactionID:     store.TransactionID(manifest.BaseTransactionID),
		PredecessorWorkloadID: manifest.PredecessorWorkloadID,
		ManifestPath:          publication.ManifestPath,
		ManifestDigest:        publication.ManifestDigest,
		CreatedUnix:           manifest.CreatedUnix,
	}
}

func assertDoctorConfirmResult(
	t *testing.T,
	statePath string,
	scannedRoot string,
	locked bool,
	captured []store.BackupIndexCandidate,
	publication backup.Publication,
	report doctorReport,
) {
	t.Helper()

	if locked {
		t.Fatal("backup index lock remains held")
	}
	if filepath.Base(scannedRoot) != "backups" {
		t.Fatalf("backup root = %q", scannedRoot)
	}
	if len(captured) != 1 || captured[0] != testDoctorCandidate(publication) {
		t.Fatalf("captured = %#v", captured)
	}
	if !report.Replaced || len(report.Entries) != 1 {
		t.Fatalf("report = %#v", report)
	}
	if report.Root != filepath.Join(filepath.Dir(statePath), "backups") {
		t.Fatalf("report root = %q", report.Root)
	}
}
