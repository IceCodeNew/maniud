// Package podman provides the statically linked Podman Libpod capability.
package podman

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	podmanruntime "github.com/IceCodeNew/maniud/internal/runtime/podman"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const (
	containerHostEnvironment = "CONTAINER_HOST"
	defaultRootfulSocket     = "/run/podman/podman.sock"
	socketDirectory          = "podman"
	socketName               = "podman.sock"
	xdgRuntimeEnvironment    = "XDG_RUNTIME_DIR"
)

// New returns the explicit Podman Libpod plugin definition.
func New() runtimeplugin.Plugin {
	return runtimeplugin.Plugin{
		Kind:        domain.RuntimePodman,
		Open:        open,
		Unavailable: func(err error) bool { return errors.Is(err, podmanruntime.ErrUnavailable) },
	}
}

//nolint:ireturn // The plugin opener implements the runtime-neutral application boundary.
func open(
	ctx context.Context,
	environment runtimeplugin.Environment,
	_ runtimeplugin.WarningSink,
) (application.OperationRuntime, error) {
	path, err := socketPath(environment, os.Geteuid())
	if err != nil {
		return nil, err
	}
	client, _, err := podmanruntime.Connect(ctx, path)
	if err != nil {
		return nil, fmt.Errorf("connect Podman runtime: %w", err)
	}

	return client, nil
}

func socketPath(environment runtimeplugin.Environment, effectiveUserID int) (string, error) {
	if effectiveUserID < 0 {
		return "", podmanruntime.ErrInvalidEndpoint
	}
	host := environmentValue(environment, containerHostEnvironment)
	if host != "" {
		path, found := strings.CutPrefix(host, "unix://")
		if !found || !validAbsolutePath(path) {
			return "", podmanruntime.ErrInvalidEndpoint
		}

		return path, nil
	}

	runtimeDirectory := environmentValue(environment, xdgRuntimeEnvironment)
	if runtimeDirectory != "" {
		if !validAbsolutePath(runtimeDirectory) {
			return "", podmanruntime.ErrInvalidEndpoint
		}

		return filepath.Join(runtimeDirectory, socketDirectory, socketName), nil
	}
	if effectiveUserID == 0 {
		return defaultRootfulSocket, nil
	}

	return filepath.Join(
		"/run/user",
		strconv.Itoa(effectiveUserID),
		socketDirectory,
		socketName,
	), nil
}

func validAbsolutePath(path string) bool {
	return filepath.IsAbs(path) && filepath.Clean(path) == path && !strings.ContainsRune(path, 0)
}

func environmentValue(environment runtimeplugin.Environment, name string) string {
	if environment == nil {
		return ""
	}

	return environment(name)
}
