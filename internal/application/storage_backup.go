package application

import (
	"bytes"
	"context"
	"crypto/rand"
	"fmt"
	"io"
	"time"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/store"
)

func (execution *upgradeExecution) backup(ctx context.Context) error {
	if len(execution.sources) == 0 {
		return nil
	}

	root := execution.mutation.backupRoot
	if root == "" {
		return ErrInvalidRequest
	}

	manifest := execution.manifest
	if manifest.TransactionID == (backup.Identifier{}) {
		return ErrInvalidRequest
	}

	action := execution.nextAction()
	postcondition, publication, err := settleStorageBackup(
		ctx,
		execution.mutation,
		action,
		execution.sequence,
		root,
		manifest,
		execution.archives,
		execution.capacity,
	)
	if err != nil {
		return resolveEffectFailure(ctx, execution.mutation, postcondition, err)
	}
	if !postcondition.Satisfied {
		return resolveEffectFailure(ctx, execution.mutation, postcondition, ErrConflictingState)
	}

	execution.publication = publication
	execution.sequence++

	return nil
}

func settleStorageBackup(
	ctx context.Context,
	mutation *boundMutation,
	action store.Action,
	sequence int64,
	root string,
	manifest backup.Manifest,
	archives [][]byte,
	capacity backup.PublicationCapacity,
) (EffectPostcondition, backup.Publication, error) {
	var empty EffectPostcondition
	var publication backup.Publication
	if mutation == nil || root == "" || len(archives) == 0 || len(archives) != len(manifest.Artifacts) {
		return empty, publication, ErrInvalidRequest
	}

	intent := storageBackupIntent(sequence, manifest)
	if action != (store.Action{}) && !actionMatchesExpected(action, mutation.preparation.Transaction.ID, intent) {
		return empty, publication, ErrConflictingState
	}

	effect := &storageBackupEffect{
		root: root, manifest: manifest, archives: cloneArchives(archives), capacity: capacity,
	}
	if action.State == store.ActionStateCompleted {
		postcondition, err := completedStorageBackup(ctx, action, effect)

		return postcondition, effect.publication, err
	}

	postcondition, err := runRuntimeEffect(
		ctx, mutation.effectJournal(), mutation.preparation.Transaction.ID, intent, effect,
	)
	if err != nil || !postcondition.Satisfied {
		return postcondition, publication, err
	}

	return postcondition, effect.publication, nil
}

func completedStorageBackup(
	ctx context.Context,
	action store.Action,
	effect *storageBackupEffect,
) (EffectPostcondition, error) {
	var empty EffectPostcondition
	if action.PostconditionDigest == nil {
		return empty, ErrConflictingState
	}

	postcondition, err := effect.Probe(ctx)
	if err != nil {
		return empty, err
	}
	if postcondition.Digest != *action.PostconditionDigest {
		return empty, ErrConflictingState
	}

	return postcondition, nil
}

func (effect *storageBackupEffect) Apply(ctx context.Context) error {
	publication, found, err := backup.Open(ctx, effect.root, effect.manifest.TransactionID)
	if err != nil {
		return fmt.Errorf("probe workload backup: %w", err)
	}
	if found {
		if err = matchingBackupPublication(effect.manifest, publication); err != nil {
			return err
		}

		effect.publication = publication
		effect.manifest = publication.Manifest

		return nil
	}

	publication, err = backup.Publish(
		ctx,
		effect.root,
		effect.manifest,
		archiveInputs(effect.manifest, effect.archives),
		effect.capacity,
	)
	if err != nil {
		return fmt.Errorf("publish workload backup: %w", err)
	}

	effect.publication = publication
	effect.manifest = publication.Manifest

	return nil
}

func (effect *storageBackupEffect) Probe(ctx context.Context) (EffectPostcondition, error) {
	var empty EffectPostcondition
	publication, found, err := backup.Open(ctx, effect.root, effect.manifest.TransactionID)
	if err != nil {
		return empty, fmt.Errorf("probe workload backup: %w", err)
	}
	if !found {
		return storageBackupPostcondition(effect.manifest, backup.Publication{}, false), nil
	}
	if err = matchingBackupPublication(effect.manifest, publication); err != nil {
		return empty, err
	}

	effect.publication = publication
	effect.manifest = publication.Manifest

	return storageBackupPostcondition(publication.Manifest, publication, true), nil
}

func storageBackupIntent(sequence int64, manifest backup.Manifest) store.ActionIntent {
	return store.ActionIntent{
		Sequence:     sequence,
		Kind:         storageBackupActionKind,
		IntentDigest: storageBackupDigest(storageEffectIntent, manifest, backup.Publication{}),
	}
}

func storageBackupPostcondition(
	manifest backup.Manifest,
	publication backup.Publication,
	satisfied bool,
) EffectPostcondition {
	state := byte(storageEffectMissing)
	if satisfied {
		state = storageEffectObserved
	}

	return EffectPostcondition{
		Digest:    storageBackupDigest(state, manifest, publication),
		Satisfied: satisfied,
	}
}

func newUpgradeBackupManifest(
	preparation Preparation,
	sources []backedStorageSource,
	inventories []backup.Inventory,
) (backup.Manifest, error) {
	var empty backup.Manifest
	if len(sources) == 0 || len(sources) != len(inventories) {
		return empty, ErrConflictingState
	}

	token, err := newBackupIdentifier()
	if err != nil {
		return empty, err
	}

	artifacts := make([]backup.Artifact, len(sources))
	for index, source := range sources {
		artifacts[index] = backup.Artifact{
			Mount:     source.Mount,
			FileName:  backupArtifactName(index),
			Inventory: inventories[index],
		}
		if source.Mount.Kind == domain.MountBind {
			artifacts[index].ProvenanceDigest = preparation.Applied.SourceDigest
		}
	}

	return backup.Manifest{
		Version:               1,
		OperationToken:        token,
		TransactionID:         backup.Identifier(preparation.Transaction.ID),
		BaseTransactionID:     backup.Identifier(preparation.Applied.TransactionID),
		Project:               preparation.Plan.Project,
		Service:               preparation.Plan.Service,
		Runtime:               preparation.Execution.Kind,
		CreatedUnix:           time.Now().Unix(),
		SourceDigest:          preparation.Workload.SourceDigest,
		EffectiveDigest:       preparation.Workload.EffectiveDigest,
		ExecutionDigest:       preparation.Execution.Digest,
		PredecessorWorkloadID: preparation.Applied.WorkloadID,
		Artifacts:             artifacts,
	}, nil
}

func newBackupIdentifier() (backup.Identifier, error) {
	var identifier backup.Identifier
	_, err := io.ReadFull(rand.Reader, identifier[:])
	if err != nil {
		return backup.Identifier{}, fmt.Errorf("generate backup operation token: %w", err)
	}

	return identifier, nil
}

func backupArtifactName(index int) string {
	return fmt.Sprintf("artifact-%06d.tar", index+1)
}

func archiveInputs(manifest backup.Manifest, archives [][]byte) []backup.ArchiveInput {
	inputs := make([]backup.ArchiveInput, len(manifest.Artifacts))
	for index, artifact := range manifest.Artifacts {
		inputs[index] = backup.ArchiveInput{
			Target:       artifact.Mount.Target,
			Reader:       bytes.NewReader(archives[index]), //nolint:gosec // settleStorageBackup proves equal lengths.
			MaximumBytes: archiveTransferLimit,
		}
	}

	return inputs
}

func backupIndexIntent(publication backup.Publication) *store.BackupIndexIntent {
	if publication.ManifestDigest == (domain.Digest{}) {
		return nil
	}

	return &store.BackupIndexIntent{
		ManifestPath:   publication.ManifestPath,
		ManifestDigest: publication.ManifestDigest,
		CreatedUnix:    publication.Manifest.CreatedUnix,
	}
}
