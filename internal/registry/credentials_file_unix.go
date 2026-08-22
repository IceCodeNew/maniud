//go:build linux || darwin

package registry

import (
	"errors"
	"io/fs"
	"os"

	"golang.org/x/sys/unix"

	"github.com/IceCodeNew/maniud/internal/jsonstrict"
)

func readDockerCredentialConfig(path string, maximumBytes int64) ([]byte, bool, error) {
	descriptor, err := unix.Open(path, unix.O_RDONLY|unix.O_CLOEXEC|unix.O_NOFOLLOW|unix.O_NONBLOCK, 0)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return nil, false, nil
		}

		return nil, false, errDockerCredentials
	}

	file := os.NewFile(uintptr(descriptor), path)
	defer func() {
		_ = file.Close()
	}()

	var metadata unix.Stat_t
	if unix.Fstat(descriptor, &metadata) != nil || metadata.Mode&unix.S_IFMT != unix.S_IFREG ||
		metadata.Uid != uint32(os.Geteuid()) || //nolint:gosec // Effective UIDs are non-negative on supported Unix targets.
		metadata.Mode&0o077 != 0 {
		return nil, false, errDockerCredentials
	}

	value, valid := jsonstrict.Read(file, maximumBytes)
	if !valid {
		return nil, false, errDockerCredentials
	}

	return value, true, nil
}
