//go:build darwin

package backup

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestDarwinFilesystemCapacityConversion(t *testing.T) {
	t.Parallel()

	value := unix.Statfs_t{
		Bsize:  4096,
		Blocks: 10,
		Bavail: 4,
		Ffree:  7,
		Fsid:   unix.Fsid{Val: [2]int32{3, 5}},
	}
	got, err := darwinFilesystemCapacity(value)
	if err != nil || got.totalBytes != 40960 || got.availableBytes != 16384 ||
		got.availableInodes != 7 || got.identity.filesystemID != value.Fsid.Val {
		t.Fatalf("darwinFilesystemCapacity() = %#v, %v", got, err)
	}

	value.Bsize = 0
	if _, err = darwinFilesystemCapacity(value); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("darwinFilesystemCapacity(zero fragment) error = %v", err)
	}
}

func TestReadDarwinFilesystemCapacityRejectsInvalidDescriptor(t *testing.T) {
	t.Parallel()

	if _, err := readFilesystemCapacity(-1); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("readFilesystemCapacity() error = %v", err)
	}
}
