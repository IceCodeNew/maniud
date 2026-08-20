package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestStorageRestoreRejectsInvalidRequestsAndActions(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, true)
	_, err := settleStorageRestore(
		context.Background(), nil, fixture.runtime, store.Action{}, 1, fixture.publication, nil,
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("settleStorageRestore(nil mutation) error = %v", err)
	}

	intent := storageRestoreIntent(
		1,
		fixture.mutation.preparation.Transaction.ID.String(),
		fixture.manifest,
	)
	conflicting := storageCompletedAction(fixture.mutation, intent, domain.Hash([]byte("postcondition")))
	conflicting.Kind = testOtherValue
	_, err = settleStorageRestore(
		context.Background(), fixture.mutation, fixture.runtime, conflicting, 1,
		fixture.publication, [][]byte{fixture.archive},
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleStorageRestore(conflicting action) error = %v", err)
	}
}

func TestStorageRestoreEffectFailureBoundaries(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, true)
	effect := &storageRestoreEffect{
		runtime: fixture.runtime, workload: fixture.mutation.preparation.Workload,
		selector: fixture.mutation.preparation.Transaction.ID.String(), publication: fixture.publication,
	}
	if err := effect.Apply(context.Background()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("storageRestoreEffect.Apply(missing archive) error = %v", err)
	}
	effect.archives = [][]byte{fixture.archive}
	fixture.runtime.putErr = errTestBoundary
	if err := effect.Apply(context.Background()); !errors.Is(err, errTestBoundary) {
		t.Fatalf("storageRestoreEffect.Apply(put failure) error = %v", err)
	}
	fixture.runtime.putErr = nil
	if err := effect.Apply(context.Background()); err != nil {
		t.Fatalf("storageRestoreEffect.Apply() error = %v", err)
	}

	fixture.runtime.getErr = errTestBoundary
	if _, err := effect.Probe(context.Background()); !errors.Is(err, errTestBoundary) {
		t.Fatalf("storageRestoreEffect.Probe(get failure) error = %v", err)
	}
	fixture.runtime.getErr = nil
	fixture.runtime.archive = []byte("not a tar archive")
	if _, err := effect.Probe(context.Background()); !errors.Is(err, backup.ErrInvalidArchive) {
		t.Fatalf("storageRestoreEffect.Probe(invalid archive) error = %v", err)
	}
	fixture.runtime.archive = upgradeTestArchive(t, "different")
	postcondition, err := effect.Probe(context.Background())
	if err != nil || postcondition.Satisfied {
		t.Fatalf("storageRestoreEffect.Probe(mismatch) = %#v, %v", postcondition, err)
	}
	fixture.runtime.archive = fixture.archive
	postcondition, err = effect.Probe(context.Background())
	if err != nil || !postcondition.Satisfied {
		t.Fatalf("storageRestoreEffect.Probe() = %#v, %v", postcondition, err)
	}
}

func TestCompletedStorageRestoreRequiresExactEvidence(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, true)
	newEffect := func() *storageRestoreEffect {
		return &storageRestoreEffect{
			runtime: fixture.runtime, workload: fixture.mutation.preparation.Workload,
			selector: fixture.mutation.preparation.Transaction.ID.String(), publication: fixture.publication,
			archives: [][]byte{fixture.archive},
		}
	}
	if _, err := completedStorageRestore(context.Background(), store.Action{}, newEffect()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completedStorageRestore(missing digest) error = %v", err)
	}

	intent := storageRestoreIntent(
		1,
		fixture.mutation.preparation.Transaction.ID.String(),
		fixture.manifest,
	)
	action := storageCompletedAction(fixture.mutation, intent, domain.Hash([]byte("wrong restore")))
	if _, err := completedStorageRestore(context.Background(), action, newEffect()); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completedStorageRestore(wrong digest) error = %v", err)
	}
	fixture.runtime.getErr = errTestBoundary
	if _, err := completedStorageRestore(context.Background(), action, newEffect()); !errors.Is(err, errTestBoundary) {
		t.Fatalf("completedStorageRestore(probe failure) error = %v", err)
	}
	fixture.runtime.getErr = nil
	postcondition, err := newEffect().Probe(context.Background())
	if err != nil {
		t.Fatalf("storageRestoreEffect.Probe() error = %v", err)
	}
	action = storageCompletedAction(fixture.mutation, intent, postcondition.Digest)
	postcondition, err = completedStorageRestore(context.Background(), action, newEffect())
	if err != nil || !postcondition.Satisfied {
		t.Fatalf("completedStorageRestore() = %#v, %v", postcondition, err)
	}
}
