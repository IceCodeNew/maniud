package docker

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
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
		Unavailable: func(err error) bool { return errors.Is(err, ErrUnavailable) },
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
	client, _, err := Connect(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect Docker runtime: %w", err)
	}

	return client, nil
}

func endpoint(
	environment runtimeplugin.Environment,
	warnings runtimeplugin.WarningSink,
) (Endpoint, error) {
	host := environmentValue(environment, dockerHostEnvironment)
	if host == "" {
		host = defaultHost
	}

	if socketPath, found := strings.CutPrefix(host, "unix://"); found {
		return configuredEndpoint(UnixEndpoint(socketPath))
	}

	if strings.HasPrefix(host, "ssh://") {
		return configuredEndpoint(SSHEndpoint(host, SSHOptions{
			Auth: SSHAuth{
				AgentSocket:     environmentValue(environment, sshAuthSockEnvironment),
				PrivateKeyFiles: nil,
				Passphrase:      nil,
			},
			HostKeys:         SSHHostKeys{Files: nil},
			RemoteDockerPath: "",
		}))
	}

	if strings.HasPrefix(host, "tcp://") && environmentValue(environment, dockerTLSVerifyEnvironment) == "" {
		if warnings == nil {
			return Endpoint{}, ErrWarningDelivery
		}

		return configuredEndpoint(VPNEndpoint(host, func(warning Warning) error {
			err := warnings(runtimeplugin.Warning{Code: string(warning.Code), Message: warning.Message})
			if err != nil {
				return fmt.Errorf("emit Docker endpoint warning: %w", err)
			}

			return nil
		}))
	}

	return Endpoint{}, ErrInvalidEndpoint
}

func configuredEndpoint(
	endpoint Endpoint,
	err error,
) (Endpoint, error) {
	if err != nil {
		return Endpoint{}, fmt.Errorf("configure Docker endpoint: %w", err)
	}

	return endpoint, nil
}

func environmentValue(environment runtimeplugin.Environment, name string) string {
	if environment == nil {
		return ""
	}

	return environment(name)
}
