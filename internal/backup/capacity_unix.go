//go:build linux || darwin

package backup

import (
	"errors"
	"math"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/domain"
	"golang.org/x/sys/unix"
)

const (
	publicationWorkspaceBytes = 1 << 20
	publicationEntryInodes    = 2
	minimumFreeBytes          = 1 << 30
	minimumFreePercent        = 10
	percentageBase            = 100
)

// PublicationCapacity binds a backup publication's exact content to the
// filesystem on which its capacity was checked.
type PublicationCapacity struct {
	manifest       domain.Digest
	filesystem     filesystemIdentity
	requiredBytes  uint64
	requiredInodes uint64
}

type filesystemIdentity struct {
	filesystemID [2]int32
	fragmentSize uint64
}

type filesystemCapacity struct {
	identity        filesystemIdentity
	totalBytes      uint64
	availableBytes  uint64
	availableInodes uint64
}

// PreparePublicationCapacity proves that the backup's actual destination can
// hold one complete publication while retaining the fixed safety margin. It
// does not create the backup root.
func PreparePublicationCapacity(root string, manifest Manifest) (PublicationCapacity, error) {
	return preparePublicationCapacity(root, manifest, readFilesystemCapacity)
}

func preparePublicationCapacity(
	root string,
	manifest Manifest,
	readCapacity func(int) (filesystemCapacity, error),
) (PublicationCapacity, error) {
	var empty PublicationCapacity
	if !validRootPath(root) || readCapacity == nil {
		return empty, ErrInvalidBackupRoot
	}
	raw, digest, err := EncodeManifest(manifest)
	if err != nil {
		return empty, ErrInvalidManifest
	}

	descriptor, rootMissing, err := openCapacityTarget(root)
	if err != nil {
		return empty, err
	}
	defer func() { _ = unix.Close(descriptor) }()

	requiredInodes := uint64(len(manifest.Artifacts) + publicationEntryInodes)
	if rootMissing {
		requiredInodes++
	}
	observed, err := readCapacity(descriptor)
	if err != nil {
		return empty, err
	}
	requiredBytes, valid := publicationBytes(manifest, len(raw), observed.identity.fragmentSize)
	if !valid {
		return empty, ErrInvalidManifest
	}
	prepared := PublicationCapacity{
		manifest:       digest,
		filesystem:     observed.identity,
		requiredBytes:  requiredBytes,
		requiredInodes: requiredInodes,
	}
	if !capacityAvailable(observed, prepared) {
		return empty, ErrInsufficientCapacity
	}

	return prepared, nil
}

func publicationBytes(manifest Manifest, manifestBytes int, fragmentSize uint64) (uint64, bool) {
	required, valid := allocatedBytes(publicationWorkspaceBytes, fragmentSize)
	if !valid {
		return 0, false
	}
	for _, artifact := range manifest.Artifacts {
		if artifact.Inventory.ArchiveBytes < 0 {
			return 0, false
		}
		archiveBytes, archiveValid := allocatedBytes(uint64(artifact.Inventory.ArchiveBytes), fragmentSize)
		if !archiveValid || required > math.MaxUint64-archiveBytes {
			return 0, false
		}
		required += archiveBytes
	}
	if manifestBytes < 0 {
		return 0, false
	}
	manifestAllocation, valid := allocatedBytes(uint64(manifestBytes), fragmentSize)
	if !valid || required > math.MaxUint64-manifestAllocation {
		return 0, false
	}

	return required + manifestAllocation, true
}

func openCapacityTarget(root string) (int, bool, error) {
	parent, err := openDirectory(canonicalBackupParent(filepath.Dir(root)))
	if err != nil {
		return -1, false, ErrInvalidBackupRoot
	}

	descriptor, err := unix.Openat(
		parent,
		filepath.Base(root),
		unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY|unix.O_NOFOLLOW,
		0,
	)
	if err == nil {
		_ = unix.Close(parent)
		identity, valid := descriptorIdentity(descriptor)
		if !valid || !privateDirectory(identity) {
			_ = unix.Close(descriptor)

			return -1, false, ErrInvalidBackupRoot
		}

		return descriptor, false, nil
	}
	if !errors.Is(err, unix.ENOENT) {
		_ = unix.Close(parent)

		return -1, false, ErrInvalidBackupRoot
	}

	return parent, true, nil
}

func validatePublicationCapacity(
	descriptor int,
	manifest Manifest,
	prepared PublicationCapacity,
) error {
	return validatePublicationCapacityWith(
		descriptor,
		manifest,
		prepared,
		readFilesystemCapacity,
	)
}

func validatePublicationCapacityWith(
	descriptor int,
	manifest Manifest,
	prepared PublicationCapacity,
	readCapacity func(int) (filesystemCapacity, error),
) error {
	_, digest, err := EncodeManifest(manifest)
	if err != nil || digest != prepared.manifest || prepared.filesystem == (filesystemIdentity{}) ||
		prepared.requiredBytes == 0 || prepared.requiredInodes == 0 || readCapacity == nil {
		return ErrInvalidManifest
	}

	observed, err := readCapacity(descriptor)
	if err != nil {
		return err
	}
	if observed.identity != prepared.filesystem {
		return ErrInvalidBackupRoot
	}
	if !capacityAvailable(observed, prepared) {
		return ErrInsufficientCapacity
	}

	return nil
}

func capacityAvailable(observed filesystemCapacity, prepared PublicationCapacity) bool {
	if observed.availableBytes < prepared.requiredBytes ||
		observed.availableInodes < prepared.requiredInodes {
		return false
	}

	remaining := observed.availableBytes - prepared.requiredBytes
	reserve := uint64(minimumFreeBytes)
	percentage := percentageCeiling(observed.totalBytes, minimumFreePercent)
	if percentage > reserve {
		reserve = percentage
	}

	return remaining >= reserve
}

func percentageCeiling(value uint64, percent uint64) uint64 {
	quotient, remainder := value/percentageBase, value%percentageBase
	result := quotient * percent
	if remainder != 0 {
		result += (remainder*percent + percentageBase - 1) / percentageBase
	}

	return result
}

func filesystemCapacityFromValues(
	filesystemID [2]int32,
	fragmentSize uint64,
	totalBlocks uint64,
	availableBlocks uint64,
	availableInodes uint64,
) (filesystemCapacity, error) {
	var empty filesystemCapacity
	total, totalValid := capacityProduct(totalBlocks, fragmentSize)
	available, availableValid := capacityProduct(availableBlocks, fragmentSize)
	if fragmentSize == 0 || !totalValid || !availableValid || total == 0 {
		return empty, ErrInvalidBackupRoot
	}

	return filesystemCapacity{
		identity: filesystemIdentity{
			filesystemID: filesystemID,
			fragmentSize: fragmentSize,
		},
		totalBytes:      total,
		availableBytes:  available,
		availableInodes: availableInodes,
	}, nil
}

func capacityProduct(count, size uint64) (uint64, bool) {
	if size != 0 && count > math.MaxUint64/size {
		return 0, false
	}

	return count * size, true
}

func allocatedBytes(size, fragmentSize uint64) (uint64, bool) {
	if fragmentSize == 0 {
		return 0, false
	}
	fragments := size / fragmentSize
	if size%fragmentSize != 0 {
		fragments++
	}

	return capacityProduct(fragments, fragmentSize)
}
