package cli

import (
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/store"
)

func rebuildBackupIndex(
	ctx context.Context,
	statePath string,
	dependencies doctorDependencies,
) (root string, publications []backup.Publication, err error) {
	state, err := dependencies.openState(ctx, statePath)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		err = errors.Join(err, state.Close())
	}()

	root = filepath.Join(filepath.Dir(statePath), "backups")
	index, err := dependencies.lockBackupIndex(ctx, state)
	if err != nil {
		return "", nil, err
	}
	defer func() {
		err = errors.Join(err, index.Close())
	}()

	publications, err = dependencies.scanBackupRoot(ctx, root)
	if err != nil {
		return "", nil, err
	}
	if err = index.ReplaceBackupIndex(ctx, backupIndexCandidates(publications)); err != nil {
		return "", nil, fmt.Errorf("replace backup index: %w", err)
	}

	return root, publications, nil
}

func scanBackupRoot(ctx context.Context, root string) ([]backup.Publication, error) {
	entries, err := os.ReadDir(root)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}

		return nil, fmt.Errorf("read backup root: %w", err)
	}

	publications := make([]backup.Publication, 0, len(entries))
	seen := make(map[backup.Identifier]struct{}, len(entries))
	for _, entry := range entries {
		publication, identifier, openErr := openBackupEntry(ctx, root, entry)
		if openErr != nil {
			return nil, openErr
		}
		if _, exists := seen[identifier]; exists {
			return nil, errGitOpsRepositoryInvalid
		}

		publications = append(publications, publication)
		seen[identifier] = struct{}{}
	}

	return publications, nil
}

func openBackupEntry(
	ctx context.Context,
	root string,
	entry os.DirEntry,
) (backup.Publication, backup.Identifier, error) {
	if !entry.IsDir() || entry.Type()&os.ModeSymlink != 0 {
		return backup.Publication{}, backup.Identifier{}, errGitOpsRepositoryInvalid
	}

	identifier, valid := parseBackupDirectoryName(entry.Name())
	if !valid {
		return backup.Publication{}, backup.Identifier{}, errGitOpsRepositoryInvalid
	}

	publication, found, err := backup.Open(ctx, root, identifier)
	if err != nil || !found {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return backup.Publication{}, backup.Identifier{}, fmt.Errorf("scan backups: %w", ctxErr)
		}

		return backup.Publication{}, backup.Identifier{}, errGitOpsRepositoryInvalid
	}

	return publication, identifier, nil
}

func parseBackupDirectoryName(name string) (backup.Identifier, bool) {
	var identifier backup.Identifier
	decoded, err := hex.DecodeString(name)
	if err != nil || len(decoded) != len(identifier) {
		return identifier, false
	}

	copy(identifier[:], decoded)

	return identifier, true
}

func doctorReportEntries(publications []backup.Publication) []doctorReportEntry {
	entries := make([]doctorReportEntry, 0, len(publications))
	for _, publication := range publications {
		entries = append(entries, doctorReportEntry{
			TransactionID:  publication.Manifest.TransactionID.String(),
			ManifestPath:   publication.ManifestPath,
			ManifestDigest: publication.ManifestDigest.String(),
			CreatedUnix:    publication.Manifest.CreatedUnix,
		})
	}

	return entries
}

func backupIndexCandidates(publications []backup.Publication) []store.BackupIndexCandidate {
	candidates := make([]store.BackupIndexCandidate, 0, len(publications))
	for _, publication := range publications {
		manifest := publication.Manifest
		candidates = append(candidates, store.BackupIndexCandidate{
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
		})
	}

	return candidates
}
