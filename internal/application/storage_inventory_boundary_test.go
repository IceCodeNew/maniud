package application

import (
	"context"
	"errors"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestStorageInventoryRejectsInvalidRequestsAndActions(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	var inventories []backup.Inventory
	var archives [][]byte
	_, err := settleStorageInventory(
		context.Background(), nil, fixture.runtime, store.Action{}, 1,
		[]backedStorageSource{fixture.source}, &inventories, &archives,
	)
	if !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("settleStorageInventory(nil mutation) error = %v", err)
	}

	intent := storageInventoryIntent(1, fixture.mutation.preparation.Applied.TransactionID.String(),
		[]backedStorageSource{fixture.source})
	conflicting := storageCompletedAction(fixture.mutation, intent, domain.Hash([]byte("postcondition")))
	conflicting.Kind = testOtherValue
	_, err = settleStorageInventory(
		context.Background(), fixture.mutation, fixture.runtime, conflicting, 1,
		[]backedStorageSource{fixture.source}, &inventories, &archives,
	)
	if !errors.Is(err, ErrConflictingState) {
		t.Fatalf("settleStorageInventory(conflicting action) error = %v", err)
	}
}

func TestStorageInventoryEffectFailureBoundaries(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	effect := newStorageInventoryEffect(
		fixture.runtime,
		fixture.mutation.preparation.Workload,
		fixture.mutation.preparation.Applied.TransactionID.String(),
		[]backedStorageSource{fixture.source},
	)
	fixture.runtime.getErr = errTestBoundary
	if err := effect.Apply(context.Background()); !errors.Is(err, errTestBoundary) {
		t.Fatalf("storageInventoryEffect.Apply(get failure) error = %v", err)
	}
	fixture.runtime.getErr = nil
	fixture.runtime.archive = []byte("not a tar archive")
	if _, err := effect.Probe(context.Background()); !errors.Is(err, backup.ErrInvalidArchive) {
		t.Fatalf("storageInventoryEffect.Probe(invalid archive) error = %v", err)
	}
	fixture.runtime.archive = fixture.archive
	postcondition, err := effect.Probe(context.Background())
	if err != nil || !postcondition.Satisfied || len(effect.inventories) != 1 || len(effect.archives) != 1 {
		t.Fatalf("storageInventoryEffect.Probe() = %#v, %v", postcondition, err)
	}
}

func TestCompletedStorageInventoryRequiresExactEvidence(t *testing.T) {
	t.Parallel()

	fixture := newStorageTestFixture(t, false)
	newEffect := func() *storageInventoryEffect {
		return newStorageInventoryEffect(
			fixture.runtime,
			fixture.mutation.preparation.Workload,
			fixture.mutation.preparation.Applied.TransactionID.String(),
			[]backedStorageSource{fixture.source},
		)
	}
	var inventories []backup.Inventory
	var archives [][]byte
	if _, err := completedStorageInventory(
		context.Background(), store.Action{}, newEffect(), &inventories, &archives,
	); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completedStorageInventory(missing digest) error = %v", err)
	}

	intent := storageInventoryIntent(
		1,
		fixture.mutation.preparation.Applied.TransactionID.String(),
		[]backedStorageSource{fixture.source},
	)
	bad := domain.Hash([]byte("wrong inventory"))
	action := storageCompletedAction(fixture.mutation, intent, bad)
	if _, err := completedStorageInventory(
		context.Background(), action, newEffect(), &inventories, &archives,
	); !errors.Is(err, ErrConflictingState) {
		t.Fatalf("completedStorageInventory(wrong digest) error = %v", err)
	}

	fixture.runtime.getErr = errTestBoundary
	if _, err := completedStorageInventory(
		context.Background(), action, newEffect(), &inventories, &archives,
	); !errors.Is(err, errTestBoundary) {
		t.Fatalf("completedStorageInventory(probe failure) error = %v", err)
	}
	fixture.runtime.getErr = nil
	effect := newEffect()
	postcondition, err := effect.Probe(context.Background())
	if err != nil {
		t.Fatalf("storageInventoryEffect.Probe() error = %v", err)
	}
	action = storageCompletedAction(fixture.mutation, intent, postcondition.Digest)
	postcondition, err = completedStorageInventory(
		context.Background(), action, newEffect(), &inventories, &archives,
	)
	if err != nil || !postcondition.Satisfied || len(inventories) != 1 || len(archives) != 1 {
		t.Fatalf("completedStorageInventory() = %#v, %v", postcondition, err)
	}
}

func storageCompletedAction(
	mutation *boundMutation,
	intent store.ActionIntent,
	postcondition domain.Digest,
) store.Action {
	return store.Action{
		TransactionID:       mutation.preparation.Transaction.ID,
		Sequence:            intent.Sequence,
		Kind:                intent.Kind,
		State:               store.ActionStateCompleted,
		IntentDigest:        intent.IntentDigest,
		PostconditionDigest: &postcondition,
	}
}
