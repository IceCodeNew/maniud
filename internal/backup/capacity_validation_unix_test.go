//go:build linux || darwin

package backup

import (
	"errors"
	"math"
	"os"
	"testing"

	"golang.org/x/sys/unix"
)

func TestValidatePublicationCapacityRejectsDriftAndInsufficientSpace(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	root := privatePublicationRoot(t)
	ample := ampleFilesystemCapacity()
	prepared, err := preparePublicationCapacity(root, manifest, fixedCapacityReader(ample, nil))
	if err != nil {
		t.Fatalf("preparePublicationCapacity() error = %v", err)
	}

	if err = validatePublicationCapacityWith(0, manifest, prepared, fixedCapacityReader(ample, nil)); err != nil {
		t.Fatalf("validatePublicationCapacityWith() error = %v", err)
	}

	invalidManifest := manifest
	invalidManifest.Project = "changed"
	invalidCases := []struct {
		name     string
		manifest Manifest
		prepared PublicationCapacity
		read     func(int) (filesystemCapacity, error)
	}{
		{name: "invalid manifest", manifest: Manifest{}, prepared: prepared, read: fixedCapacityReader(ample, nil)},
		{name: "manifest drift", manifest: invalidManifest, prepared: prepared, read: fixedCapacityReader(ample, nil)},
		{name: "zero filesystem", manifest: manifest, prepared: func() PublicationCapacity {
			value := prepared
			value.filesystem = filesystemIdentity{}

			return value
		}(), read: fixedCapacityReader(ample, nil)},
		{name: "zero bytes", manifest: manifest, prepared: func() PublicationCapacity {
			value := prepared
			value.requiredBytes = 0

			return value
		}(), read: fixedCapacityReader(ample, nil)},
		{name: "zero inodes", manifest: manifest, prepared: func() PublicationCapacity {
			value := prepared
			value.requiredInodes = 0

			return value
		}(), read: fixedCapacityReader(ample, nil)},
		{name: "nil reader", manifest: manifest, prepared: prepared},
	}
	for _, test := range invalidCases {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			err := validatePublicationCapacityWith(0, test.manifest, test.prepared, test.read)
			if !errors.Is(err, ErrInvalidManifest) {
				t.Fatalf("validatePublicationCapacityWith() error = %v", err)
			}
		})
	}
}

func TestValidatePublicationCapacityRejectsProbeAndCapacityDrift(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	ample := ampleFilesystemCapacity()
	prepared, err := preparePublicationCapacity(
		privatePublicationRoot(t), manifest, fixedCapacityReader(ample, nil),
	)
	if err != nil {
		t.Fatalf("preparePublicationCapacity() error = %v", err)
	}
	if err = validatePublicationCapacityWith(
		0,
		manifest,
		prepared,
		fixedCapacityReader(filesystemCapacity{}, errCapacityProbeTest),
	); !errors.Is(err, errCapacityProbeTest) {
		t.Fatalf("probe error = %v", err)
	}

	drifted := ample
	drifted.identity.filesystemID[0]++
	if err = validatePublicationCapacityWith(
		0, manifest, prepared, fixedCapacityReader(drifted, nil),
	); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("filesystem drift error = %v", err)
	}

	insufficient := ample
	insufficient.availableInodes = 0
	if err = validatePublicationCapacityWith(
		0, manifest, prepared, fixedCapacityReader(insufficient, nil),
	); !errors.Is(err, ErrInsufficientCapacity) {
		t.Fatalf("insufficient capacity error = %v", err)
	}
}

func TestCapacityAvailableRetainsByteAndInodeMargins(t *testing.T) {
	t.Parallel()

	prepared := PublicationCapacity{requiredBytes: 1 << 20, requiredInodes: 3}
	tests := []struct {
		name     string
		observed filesystemCapacity
		want     bool
	}{
		{
			name: "required bytes missing",
			observed: filesystemCapacity{
				totalBytes: 20 << 30, availableBytes: prepared.requiredBytes - 1, availableInodes: 3,
			},
		},
		{
			name: "required inodes missing",
			observed: filesystemCapacity{
				totalBytes: 20 << 30, availableBytes: 10 << 30, availableInodes: 2,
			},
		},
		{
			name: "fixed reserve missing",
			observed: filesystemCapacity{
				totalBytes: 10 << 30, availableBytes: prepared.requiredBytes + minimumFreeBytes - 1,
				availableInodes: 3,
			},
		},
		{
			name: "percentage reserve missing",
			observed: filesystemCapacity{
				totalBytes: 20 << 30, availableBytes: prepared.requiredBytes + 2<<30 - 1,
				availableInodes: 3,
			},
		},
		{
			name: "fixed reserve satisfied",
			observed: filesystemCapacity{
				totalBytes: 10 << 30, availableBytes: prepared.requiredBytes + minimumFreeBytes,
				availableInodes: 3,
			},
			want: true,
		},
		{
			name: "percentage reserve satisfied",
			observed: filesystemCapacity{
				totalBytes: 20 << 30, availableBytes: prepared.requiredBytes + 2<<30,
				availableInodes: 3,
			},
			want: true,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if got := capacityAvailable(test.observed, prepared); got != test.want {
				t.Fatalf("capacityAvailable() = %t, want %t", got, test.want)
			}
		})
	}
}

func TestCapacityArithmetic(t *testing.T) {
	t.Parallel()

	if got := percentageCeiling(200, 10); got != 20 {
		t.Fatalf("percentageCeiling(exact) = %d", got)
	}
	if got := percentageCeiling(201, 10); got != 21 {
		t.Fatalf("percentageCeiling(remainder) = %d", got)
	}
	if got, valid := capacityProduct(3, 7); !valid || got != 21 {
		t.Fatalf("capacityProduct(valid) = %d, %t", got, valid)
	}
	if got, valid := capacityProduct(math.MaxUint64, 0); !valid || got != 0 {
		t.Fatalf("capacityProduct(zero) = %d, %t", got, valid)
	}
	if _, valid := capacityProduct(math.MaxUint64, 2); valid {
		t.Fatal("capacityProduct(overflow) succeeded")
	}
}

func TestFilesystemCapacityConversionRejectsInvalidValues(t *testing.T) {
	t.Parallel()

	filesystemID := [2]int32{1, 2}
	got, err := filesystemCapacityFromValues(filesystemID, 4096, 10, 4, 7)
	if err != nil || got.totalBytes != 40960 || got.availableBytes != 16384 ||
		got.availableInodes != 7 || got.identity.filesystemID != filesystemID {
		t.Fatalf("filesystemCapacityFromValues() = %#v, %v", got, err)
	}

	invalid := []struct {
		name            string
		fragmentSize    uint64
		totalBlocks     uint64
		availableBlocks uint64
	}{
		{name: "zero fragment", totalBlocks: 1, availableBlocks: 1},
		{name: "total overflow", fragmentSize: 2, totalBlocks: math.MaxUint64, availableBlocks: 1},
		{name: "available overflow", fragmentSize: 2, totalBlocks: 1, availableBlocks: math.MaxUint64},
		{name: "zero total", fragmentSize: 1, availableBlocks: 1},
	}
	for _, test := range invalid {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()
			if _, err := filesystemCapacityFromValues(
				filesystemID,
				test.fragmentSize,
				test.totalBlocks,
				test.availableBlocks,
				1,
			); !errors.Is(err, ErrInvalidBackupRoot) {
				t.Fatalf("filesystemCapacityFromValues() error = %v", err)
			}
		})
	}
}

func TestValidatePublicationCapacityUsesDestinationDescriptor(t *testing.T) {
	t.Parallel()

	manifest := validManifestForTest(t)
	root := privatePublicationRoot(t)
	prepared, err := PreparePublicationCapacity(root, manifest)
	if err != nil {
		t.Fatalf("PreparePublicationCapacity() error = %v", err)
	}
	if err = os.Mkdir(root, privateDirectoryMode); err != nil {
		t.Fatalf("Mkdir(backup root): %v", err)
	}
	descriptor, err := unix.Open(root, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_DIRECTORY, 0)
	if err != nil {
		t.Fatalf("Open(backup root): %v", err)
	}
	defer func() { _ = unix.Close(descriptor) }()

	if err = validatePublicationCapacity(descriptor, manifest, prepared); err != nil {
		t.Fatalf("validatePublicationCapacity() error = %v", err)
	}
	if err = validatePublicationCapacity(-1, manifest, prepared); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("validatePublicationCapacity(invalid descriptor) error = %v", err)
	}
}
