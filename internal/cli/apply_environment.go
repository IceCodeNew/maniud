package cli

import (
	"errors"
	"path/filepath"
	"strings"
)

const (
	maximumComposeSourceBytes = 1 << 20
	defaultStateDirectory     = ".local/state"
	stateDatabaseName         = "state.db"
)

var (
	errStateHomeUnavailable = errors.New("state home is unavailable")
	errStateHomeInvalid     = errors.New("state home is invalid")
)

func defaultStatePath(environment map[string]string) (string, error) {
	root := environment["XDG_STATE_HOME"]
	if root == "" {
		home := environment["HOME"]
		if home == "" {
			return "", errStateHomeUnavailable
		}

		root = filepath.Join(home, defaultStateDirectory)
	}

	if !filepath.IsAbs(root) || filepath.Clean(root) != root {
		return "", errStateHomeInvalid
	}

	return filepath.Join(root, "maniud", stateDatabaseName), nil
}

func dockerConfigPath(environment map[string]string) string {
	directory := environment["DOCKER_CONFIG"]
	if directory == "" {
		home := environment["HOME"]
		if home == "" {
			return ""
		}

		directory = filepath.Join(home, ".docker")
	}

	return filepath.Join(directory, "config.json")
}

func normalizedComposeSourcePath(path, workingDirectory string) (string, bool) {
	if path == "" || strings.IndexByte(path, 0) >= 0 || !filepath.IsAbs(workingDirectory) {
		return "", false
	}

	absolutePath := path
	if !filepath.IsAbs(absolutePath) {
		absolutePath = filepath.Join(workingDirectory, absolutePath)
	}

	return filepath.Clean(absolutePath), true
}

func environmentMap(values []string) map[string]string {
	environment := make(map[string]string, len(values))
	for _, value := range values {
		name, content, found := strings.Cut(value, "=")
		if found {
			environment[name] = content
		}
	}

	return environment
}
