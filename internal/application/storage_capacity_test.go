package application

import (
	"bytes"
	"context"
	"errors"
	"math"
	"testing"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func TestPrepareBackupCapacityRejectsMissingMutation(t *testing.T) {
	t.Parallel()

	execution := &upgradeExecution{sources: []backedStorageSource{{}}}
	if err := execution.prepareBackupCapacity(context.Background()); !errors.Is(err, ErrInvalidRequest) {
		t.Fatalf("prepareBackupCapacity() error = %v", err)
	}
}

func TestPrepareBackupCapacityFailsTransactionBeforeStop(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name        string
		inventories func(*testing.T) []backup.Inventory
	}{
		{name: "manifest mismatch"},
		{name: "capacity overflow", inventories: overflowingInventories},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			state, mutation, runtime := newUpgradeMutation(t)
			defer closeBootstrapMutation(t, state, mutation)
			execution := &upgradeExecution{
				mutation: mutation,
				runtime:  runtime,
				sources:  capacityTestSources(),
			}
			if test.inventories != nil {
				execution.inventories = test.inventories(t)
			}

			err := execution.prepareBackupCapacity(context.Background())
			if err == nil || mutation.preparation.Transaction.State != store.TransactionFailed {
				t.Fatalf("prepareBackupCapacity() = %v, transaction %#v", err, mutation.preparation.Transaction)
			}
			if runtime.transitionApplies[WorkloadTransitionStop] != 0 {
				t.Fatalf("capacity failure stopped workload: %#v", runtime.transitionApplies)
			}
		})
	}
}

func capacityTestSources() []backedStorageSource {
	return []backedStorageSource{
		{Mount: domain.RuntimeMount{
			Kind: domain.MountVolume, Name: testDataName, Source: "/runtime/data", Target: "/data",
		}},
		{Mount: domain.RuntimeMount{
			Kind: domain.MountVolume, Name: "state", Source: "/runtime/state", Target: "/state",
		}},
	}
}

func overflowingInventories(t *testing.T) []backup.Inventory {
	t.Helper()

	archive := upgradeTestArchive(t, "payload")
	inventory, err := backup.Analyze(context.Background(), bytes.NewReader(archive), int64(len(archive)))
	if err != nil {
		t.Fatalf("Analyze() error = %v", err)
	}
	maximumArchiveBytes := int64(math.MaxInt64 / 512 * 512)
	inventory.ArchiveBytes = maximumArchiveBytes

	return []backup.Inventory{inventory, inventory}
}
