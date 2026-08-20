package application

import (
	"context"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func (execution *upgradeExecution) backupManifest(ctx context.Context) (backup.Manifest, error) {
	publication, found, err := execution.openPublishedBackup(ctx)
	if err != nil {
		return backup.Manifest{}, err
	}
	if found {
		if err = matchingBackupArtifacts(publication.Manifest, execution.sources, execution.inventories); err != nil {
			return backup.Manifest{}, err
		}
		execution.publication = publication

		return publication.Manifest, nil
	}

	return newUpgradeBackupManifest(execution.mutation.preparation, execution.sources, execution.inventories)
}

func (execution *upgradeExecution) publishedBackup(ctx context.Context) (backup.Publication, error) {
	if execution.publication.ManifestDigest != (domain.Digest{}) {
		return execution.publication, nil
	}

	publication, found, err := execution.openPublishedBackup(ctx)
	if err != nil {
		return backup.Publication{}, err
	}
	if !found {
		return backup.Publication{}, ErrConflictingState
	}
	if err = matchingBackupArtifacts(publication.Manifest, execution.sources, execution.inventories); err != nil {
		return backup.Publication{}, err
	}

	execution.publication = publication

	return publication, nil
}

func (execution *upgradeExecution) openPublishedBackup(ctx context.Context) (backup.Publication, bool, error) {
	var empty backup.Publication
	if execution == nil || execution.mutation == nil || execution.mutation.backupRoot == "" {
		return empty, false, ErrInvalidRequest
	}

	publication, found, err := backup.Open(
		ctx,
		execution.mutation.backupRoot,
		backup.Identifier(execution.mutation.preparation.Transaction.ID),
	)
	if err != nil {
		return empty, false, fmt.Errorf("open published workload backup: %w", err)
	}

	return publication, found, nil
}

func matchingBackupPublication(expected backup.Manifest, publication backup.Publication) error {
	if publication.Manifest.TransactionID != expected.TransactionID ||
		publication.Manifest.BaseTransactionID != expected.BaseTransactionID ||
		len(publication.Manifest.Artifacts) != len(expected.Artifacts) {
		return ErrConflictingState
	}

	return nil
}

func matchingBackupArtifacts(
	manifest backup.Manifest,
	sources []backedStorageSource,
	inventories []backup.Inventory,
) error {
	if len(manifest.Artifacts) != len(sources) || len(manifest.Artifacts) != len(inventories) {
		return ErrConflictingState
	}

	for index, artifact := range manifest.Artifacts {
		if artifact.Mount != sources[index].Mount || !backup.SameContent(artifact.Inventory, inventories[index]) {
			return ErrConflictingState
		}
	}

	return nil
}
