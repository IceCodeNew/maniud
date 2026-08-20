//go:build linux || darwin

package backup

import "errors"

var errCapacityProbeTest = errors.New("capacity probe failed")

func ampleFilesystemCapacity() filesystemCapacity {
	return filesystemCapacity{
		identity: filesystemIdentity{
			filesystemID: [2]int32{17, 23},
			fragmentSize: 4096,
		},
		totalBytes:      20 << 30,
		availableBytes:  10 << 30,
		availableInodes: 1000,
	}
}

func fixedCapacityReader(capacity filesystemCapacity, err error) func(int) (filesystemCapacity, error) {
	return func(int) (filesystemCapacity, error) {
		return capacity, err
	}
}
