package application

import (
	"github.com/IceCodeNew/maniud/internal/backup"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	storageInventoryActionKind = "storage.inventory"
	storageBackupActionKind    = "storage.backup"
	storageRestoreActionKind   = "storage.restore"
	storageEffectDigestFormat  = 1
	storageEffectIntent        = 0
	storageEffectObserved      = 1
	storageEffectMissing       = 2
)

type storageInventoryEffect struct {
	runtime     WorkloadArchiveRuntime
	workload    domain.DesiredWorkload
	selector    string
	sources     []backedStorageSource
	inventories []backup.Inventory
	archives    [][]byte
}

type storageBackupEffect struct {
	root        string
	manifest    backup.Manifest
	archives    [][]byte
	capacity    backup.PublicationCapacity
	publication backup.Publication
}

type storageRestoreEffect struct {
	runtime     WorkloadArchiveRuntime
	workload    domain.DesiredWorkload
	selector    string
	publication backup.Publication
	archives    [][]byte
}
