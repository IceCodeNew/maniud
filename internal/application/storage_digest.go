package application

import (
	"bytes"
	"encoding/binary"

	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func storageInventoryDigest(
	state byte,
	selector string,
	sources []backedStorageSource,
	inventories []backup.Inventory,
) domain.Digest {
	value := []byte{storageEffectDigestFormat, state}
	value = appendStorageString(value, storageInventoryActionKind)
	value = appendStorageString(value, selector)
	value = binary.AppendUvarint(value, uint64(len(sources)))
	for index, source := range sources {
		value = appendRuntimeMount(value, source.Mount)
		if index < len(inventories) {
			value = appendInventory(value, inventories[index])
		}
	}

	return domain.Hash(value)
}

func storageBackupDigest(state byte, manifest backup.Manifest, publication backup.Publication) domain.Digest {
	value := []byte{storageEffectDigestFormat, state}
	value = appendStorageString(value, storageBackupActionKind)
	value = append(value, manifest.TransactionID[:]...)
	value = append(value, manifest.BaseTransactionID[:]...)
	value = binary.AppendUvarint(value, uint64(len(manifest.Artifacts)))
	for _, artifact := range manifest.Artifacts {
		value = appendRuntimeMount(value, artifact.Mount)
		value = append(value, artifact.ProvenanceDigest[:]...)
		value = appendInventory(value, artifact.Inventory)
	}
	if publication.ManifestDigest != (domain.Digest{}) {
		value = append(value, publication.ManifestDigest[:]...)
		value = appendStorageString(value, publication.ManifestPath)
	}

	return domain.Hash(value)
}

func storageRestoreDigest(state byte, selector string, manifest backup.Manifest) domain.Digest {
	value := []byte{storageEffectDigestFormat, state}
	value = appendStorageString(value, storageRestoreActionKind)
	value = appendStorageString(value, selector)
	value = append(value, manifest.TransactionID[:]...)
	value = binary.AppendUvarint(value, uint64(len(manifest.Artifacts)))
	for _, artifact := range manifest.Artifacts {
		value = appendRuntimeMount(value, artifact.Mount)
		value = appendInventory(value, artifact.Inventory)
	}

	return domain.Hash(value)
}

func appendRuntimeMount(value []byte, mount domain.RuntimeMount) []byte {
	value = append(value, byte(mount.Kind))
	value = appendStorageString(value, mount.Name)
	value = appendStorageString(value, mount.Source)
	value = appendStorageString(value, mount.Target)
	if mount.ReadOnly {
		return append(value, 1)
	}

	return append(value, 0)
}

func appendInventory(value []byte, inventory backup.Inventory) []byte {
	value = appendUnsignedCount(value, inventory.EntryCount)
	value = appendUnsignedCount(value, inventory.PayloadBytes)
	value = appendUnsignedCount(value, inventory.ArchiveBytes)
	value = append(value, inventory.ArchiveDigest[:]...)
	value = append(value, inventory.SemanticDigest[:]...)

	return value
}

func appendUnsignedCount(value []byte, count int64) []byte {
	if count < 0 {
		count = 0
	}

	return binary.AppendUvarint(value, uint64(count))
}

func appendStorageString(value []byte, text string) []byte {
	value = binary.AppendUvarint(value, uint64(len(text)))

	return append(value, text...)
}

func cloneArchives(archives [][]byte) [][]byte {
	cloned := make([][]byte, len(archives))
	for index, archive := range archives {
		cloned[index] = bytes.Clone(archive)
	}

	return cloned
}
