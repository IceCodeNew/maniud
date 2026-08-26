package cli

import (
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strconv"
	"strings"

	dockerruntime "github.com/IceCodeNew/maniud/internal/runtime/docker"
	podmanruntime "github.com/IceCodeNew/maniud/internal/runtime/podman"
)

const (
	maximumComposeSourceBytes  = 1 << 20
	containerHostEnvironment   = "CONTAINER_HOST"
	defaultDockerHost          = "unix:///var/run/docker.sock"
	defaultRootfulPodmanSocket = "/run/podman/podman.sock"
	defaultStateDirectory      = ".local/state"
	podmanSocketDirectory      = "podman"
	podmanSocketName           = "podman.sock"
	stateDatabaseName          = "state.db"
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

func dockerEndpoint(environment map[string]string, stderr io.Writer) (dockerruntime.Endpoint, error) {
	host := environment["DOCKER_HOST"]
	if host == "" {
		host = defaultDockerHost
	}

	if socketPath, found := strings.CutPrefix(host, "unix://"); found {
		return configuredDockerEndpoint(dockerruntime.UnixEndpoint(socketPath))
	}

	if strings.HasPrefix(host, "ssh://") {
		return configuredDockerEndpoint(dockerruntime.SSHEndpoint(host, dockerruntime.SSHOptions{
			Auth: dockerruntime.SSHAuth{
				AgentSocket:     environment["SSH_AUTH_SOCK"],
				PrivateKeyFiles: nil,
				Passphrase:      nil,
			},
			HostKeys:         dockerruntime.SSHHostKeys{Files: nil},
			RemoteDockerPath: "",
		}))
	}

	if strings.HasPrefix(host, "tcp://") && environment["DOCKER_TLS_VERIFY"] == "" {
		if stderr == nil {
			return dockerruntime.Endpoint{}, dockerruntime.ErrWarningDelivery
		}

		return configuredDockerEndpoint(dockerruntime.VPNEndpoint(host, func(warning dockerruntime.Warning) error {
			err := json.NewEncoder(stderr).Encode(struct {
				Code    dockerruntime.WarningCode `json:"code"`
				Message string                    `json:"message"`
			}{Code: warning.Code, Message: warning.Message})
			if err != nil {
				return fmt.Errorf("emit Docker endpoint warning: %w", err)
			}

			return nil
		}))
	}

	return dockerruntime.Endpoint{}, dockerruntime.ErrInvalidEndpoint
}

func podmanSocketPath(environment map[string]string, effectiveUserID int) (string, error) {
	if effectiveUserID < 0 {
		return "", podmanruntime.ErrInvalidEndpoint
	}
	host := environment[containerHostEnvironment]
	if host != "" {
		path, found := strings.CutPrefix(host, "unix://")
		if !found || !validAbsolutePath(path) {
			return "", podmanruntime.ErrInvalidEndpoint
		}

		return path, nil
	}

	runtimeDirectory := environment["XDG_RUNTIME_DIR"]
	if runtimeDirectory != "" {
		if !validAbsolutePath(runtimeDirectory) {
			return "", podmanruntime.ErrInvalidEndpoint
		}

		return filepath.Join(runtimeDirectory, podmanSocketDirectory, podmanSocketName), nil
	}
	if effectiveUserID == 0 {
		return defaultRootfulPodmanSocket, nil
	}

	return filepath.Join(
		"/run/user",
		strconv.Itoa(effectiveUserID),
		podmanSocketDirectory,
		podmanSocketName,
	), nil
}

func validAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func configuredDockerEndpoint(
	endpoint dockerruntime.Endpoint,
	err error,
) (dockerruntime.Endpoint, error) {
	if err != nil {
		return dockerruntime.Endpoint{}, fmt.Errorf("configure Docker endpoint: %w", err)
	}

	return endpoint, nil
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
