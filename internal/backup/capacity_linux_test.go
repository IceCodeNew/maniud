//go:build linux

package backup

import (
	"errors"
	"testing"

	"golang.org/x/sys/unix"
)

func TestLinuxFilesystemCapacityConversion(t *testing.T) {
	t.Parallel()

	value := unix.Statfs_t{
		Frsize: 4096,
		Blocks: 10,
		Bavail: 4,
		Ffree:  7,
		Fsid:   unix.Fsid{Val: [2]int32{3, 5}},
	}
	got, err := linuxFilesystemCapacity(value)
	if err != nil || got.totalBytes != 40960 || got.availableBytes != 16384 ||
		got.availableInodes != 7 || got.identity.filesystemID != value.Fsid.Val {
		t.Fatalf("linuxFilesystemCapacity() = %#v, %v", got, err)
	}

	value.Frsize = 0
	if _, err = linuxFilesystemCapacity(value); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("linuxFilesystemCapacity(zero fragment) error = %v", err)
	}
	value.Frsize = -1
	if _, err = linuxFilesystemCapacity(value); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("linuxFilesystemCapacity(negative fragment) error = %v", err)
	}
}

func TestReadLinuxFilesystemCapacityRejectsInvalidDescriptor(t *testing.T) {
	t.Parallel()

	if _, err := readFilesystemCapacity(-1); !errors.Is(err, ErrInvalidBackupRoot) {
		t.Fatalf("readFilesystemCapacity() error = %v", err)
	}
}
