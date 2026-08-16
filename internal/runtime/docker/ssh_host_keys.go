package docker

import (
	"errors"
	"os"
	"path/filepath"

	"golang.org/x/crypto/ssh"
	"golang.org/x/crypto/ssh/knownhosts"
)

func loadHostKeyCallback(configured []string) (ssh.HostKeyCallback, error) {
	files, err := knownHostsFiles(configured, os.UserHomeDir, os.Stat)
	if err != nil || len(files) == 0 {
		return nil, ErrInvalidEndpoint
	}

	callback, err := knownhosts.New(files...)
	if err != nil {
		return nil, ErrInvalidEndpoint
	}

	return callback, nil
}

func knownHostsFiles(
	configured []string,
	userHomeDir func() (string, error),
	stat func(string) (os.FileInfo, error),
) ([]string, error) {
	if len(configured) > maximumSSHFiles {
		return nil, ErrInvalidEndpoint
	}

	if len(configured) > 0 {
		return validateKnownHostsFiles(configured)
	}

	home, err := userHomeDir()
	if err != nil {
		return nil, ErrInvalidEndpoint
	}

	candidates := []string{filepath.Join(home, ".ssh", "known_hosts"), "/etc/ssh/ssh_known_hosts"}

	files := make([]string, 0, len(candidates))
	for _, candidate := range candidates {
		info, statErr := stat(candidate)
		if errors.Is(statErr, os.ErrNotExist) {
			continue
		}

		if statErr != nil || !info.Mode().IsRegular() {
			return nil, ErrInvalidEndpoint
		}

		files = append(files, candidate)
	}

	return files, nil
}

func validateKnownHostsFiles(configured []string) ([]string, error) {
	files := make([]string, 0, len(configured))

	seen := make(map[string]struct{}, len(configured))
	for _, filename := range configured {
		if !validAbsolutePath(filename) {
			return nil, ErrInvalidEndpoint
		}

		if _, duplicate := seen[filename]; duplicate {
			return nil, ErrInvalidEndpoint
		}

		info, err := os.Stat(filename)
		if err != nil || !info.Mode().IsRegular() {
			return nil, ErrInvalidEndpoint
		}

		seen[filename] = struct{}{}
		files = append(files, filename)
	}

	return files, nil
}
