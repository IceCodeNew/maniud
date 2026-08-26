// Package docker provides the statically linked Docker Engine capability.
package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	dockerruntime "github.com/IceCodeNew/maniud/internal/runtime/docker"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const (
	defaultHost                = "unix:///var/run/docker.sock"
	dockerHostEnvironment      = "DOCKER_HOST"
	dockerTLSVerifyEnvironment = "DOCKER_TLS_VERIFY"
	sshAuthSockEnvironment     = "SSH_AUTH_SOCK"
)

// New returns the explicit Docker Engine plugin definition.
func New() runtimeplugin.Plugin {
	return runtimeplugin.Plugin{
		Kind:        domain.RuntimeDocker,
		Open:        open,
		Unavailable: func(err error) bool { return errors.Is(err, dockerruntime.ErrUnavailable) },
	}
}

//nolint:ireturn // The plugin opener implements the runtime-neutral application boundary.
func open(
	ctx context.Context,
	environment runtimeplugin.Environment,
	warnings runtimeplugin.WarningSink,
) (application.OperationRuntime, error) {
	endpoint, err := endpoint(environment, warnings)
	if err != nil {
		return nil, err
	}
	client, _, err := dockerruntime.Connect(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect Docker runtime: %w", err)
	}

	return client, nil
}

func endpoint(
	environment runtimeplugin.Environment,
	warnings runtimeplugin.WarningSink,
) (dockerruntime.Endpoint, error) {
	host := environmentValue(environment, dockerHostEnvironment)
	if host == "" {
		host = defaultHost
	}

	if socketPath, found := strings.CutPrefix(host, "unix://"); found {
		return configuredEndpoint(dockerruntime.UnixEndpoint(socketPath))
	}

	if strings.HasPrefix(host, "ssh://") {
		return configuredEndpoint(dockerruntime.SSHEndpoint(host, dockerruntime.SSHOptions{
			Auth: dockerruntime.SSHAuth{
				AgentSocket:     environmentValue(environment, sshAuthSockEnvironment),
				PrivateKeyFiles: nil,
				Passphrase:      nil,
			},
			HostKeys:         dockerruntime.SSHHostKeys{Files: nil},
			RemoteDockerPath: "",
		}))
	}

	if strings.HasPrefix(host, "tcp://") && environmentValue(environment, dockerTLSVerifyEnvironment) == "" {
		if warnings == nil {
			return dockerruntime.Endpoint{}, dockerruntime.ErrWarningDelivery
		}

		return configuredEndpoint(dockerruntime.VPNEndpoint(host, func(warning dockerruntime.Warning) error {
			err := warnings(runtimeplugin.Warning{Code: string(warning.Code), Message: warning.Message})
			if err != nil {
				return fmt.Errorf("emit Docker endpoint warning: %w", err)
			}

			return nil
		}))
	}

	return dockerruntime.Endpoint{}, dockerruntime.ErrInvalidEndpoint
}

func configuredEndpoint(
	endpoint dockerruntime.Endpoint,
	err error,
) (dockerruntime.Endpoint, error) {
	if err != nil {
		return dockerruntime.Endpoint{}, fmt.Errorf("configure Docker endpoint: %w", err)
	}

	return endpoint, nil
}

func environmentValue(environment runtimeplugin.Environment, name string) string {
	if environment == nil {
		return ""
	}

	return environment(name)
}
