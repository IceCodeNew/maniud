package application

import (
	"context"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func (execution *upgradeExecution) prepareBackupCapacity(ctx context.Context) error {
	if len(execution.sources) == 0 {
		return nil
	}
	if execution.mutation == nil || execution.mutation.backupRoot == "" {
		return ErrInvalidRequest
	}

	manifest, err := execution.backupManifest(ctx)
	if err != nil {
		return execution.failCapacity(ctx, err)
	}
	execution.manifest = manifest
	if execution.publication.ManifestDigest != (domain.Digest{}) {
		return nil
	}

	capacity, err := backup.PreparePublicationCapacity(execution.mutation.backupRoot, manifest)
	if err != nil {
		return execution.failCapacity(ctx, err)
	}
	execution.capacity = capacity

	return nil
}

func (execution *upgradeExecution) failCapacity(ctx context.Context, cause error) error {
	return resolveUpgradeFailure(
		ctx,
		execution.mutation,
		store.TransactionFailed,
		fmt.Errorf("prepare workload backup capacity: %w", cause),
	)
}
