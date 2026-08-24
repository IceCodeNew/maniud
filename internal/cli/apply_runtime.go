package cli

import (
	"context"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry"
	containerdruntime "github.com/IceCodeNew/maniud/internal/runtime/containerd"
	dockerruntime "github.com/IceCodeNew/maniud/internal/runtime/docker"
	podmanruntime "github.com/IceCodeNew/maniud/internal/runtime/podman"
	"github.com/IceCodeNew/maniud/internal/store"
)

type applyRuntime interface {
	application.Runtime
	ProbeImage(ctx context.Context, expected domain.ImageIdentity) (application.ImageProbe, error)
	CloseIdleConnections()
}

type applyTransactionReader interface {
	application.TransactionReader
	Close() error
}

type applyDependencies struct {
	loadSource  func(context.Context, string) (compose.Source, error)
	openRuntime func(context.Context, domain.RuntimeKind) (applyRuntime, error)
	openReader  func(context.Context) (applyTransactionReader, error)
	openState   func(context.Context) (*store.Store, error)
	mutate      func(context.Context, application.Request, *store.Store, applyRuntime) (application.Plan, error)
	images      application.ImageResolver
}

func defaultApplyDependencies(
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
) (applyDependencies, error) {
	workingDirectory, err := getWorkingDirectory()
	if err != nil {
		return applyDependencies{}, fmt.Errorf("resolve working directory: %w", err)
	}

	statePath, err := defaultStatePath(environment)
	if err != nil {
		return applyDependencies{}, err
	}

	resolver := registry.NewResolver(registry.Options{
		Credentials: nil, DockerConfigPath: dockerConfigPath(environment),
	})

	return applyDependencies{
		loadSource: func(ctx context.Context, path string) (compose.Source, error) {
			return loadComposeSource(ctx, path, workingDirectory, environment, filepath.Dir(statePath))
		},
		openRuntime: func(ctx context.Context, runtimeKind domain.RuntimeKind) (applyRuntime, error) {
			return openApplyRuntime(ctx, runtimeKind, environment, stderr)
		},
		openReader: func(ctx context.Context) (applyTransactionReader, error) {
			return store.OpenReader(ctx, statePath)
		},
		openState: func(ctx context.Context) (*store.Store, error) {
			return store.Open(ctx, statePath)
		},
		mutate: func(
			ctx context.Context,
			request application.Request,
			state *store.Store,
			runtime applyRuntime,
		) (application.Plan, error) {
			return application.NewService(resolver, runtime, state).Apply(ctx, request, state, resolver)
		},
		images: resolver,
	}, nil
}

//nolint:ireturn // Runtime selection intentionally returns the application boundary.
func openApplyRuntime(
	ctx context.Context,
	runtimeKind domain.RuntimeKind,
	environment map[string]string,
	stderr io.Writer,
) (applyRuntime, error) {
	switch runtimeKind {
	case domain.RuntimeDocker:
		client, err := openDockerApplyRuntime(ctx, environment, stderr)
		if err != nil {
			return nil, err
		}

		return client, nil
	case domain.RuntimePodman:
		client, err := openPodmanApplyRuntime(ctx, environment)
		if err != nil {
			return nil, err
		}

		return client, nil
	case domain.RuntimeContainerd:
		client, err := openContainerdApplyRuntime(ctx, environment)
		if err != nil {
			return nil, err
		}

		return client, nil
	default:
		return nil, application.ErrInvalidRequest
	}
}

func openDockerApplyRuntime(
	ctx context.Context,
	environment map[string]string,
	stderr io.Writer,
) (*dockerruntime.Client, error) {
	endpoint, err := dockerEndpoint(environment, stderr)
	if err != nil {
		return nil, err
	}
	client, _, err := dockerruntime.Connect(ctx, endpoint)
	if err != nil {
		return nil, fmt.Errorf("connect Docker runtime: %w", err)
	}

	return client, nil
}

func openPodmanApplyRuntime(
	ctx context.Context,
	environment map[string]string,
) (*podmanruntime.Client, error) {
	socketPath, err := podmanSocketPath(environment, os.Geteuid())
	if err != nil {
		return nil, err
	}
	client, _, err := podmanruntime.Connect(ctx, socketPath)
	if err != nil {
		return nil, fmt.Errorf("connect Podman runtime: %w", err)
	}

	return client, nil
}

func openContainerdApplyRuntime(
	ctx context.Context,
	environment map[string]string,
) (*containerdruntime.Client, error) {
	client, err := containerdruntime.Connect(
		ctx,
		environment[containerdAddressEnvironment],
		environment[containerdNamespaceEnvironment],
	)
	if err != nil {
		return nil, fmt.Errorf("connect containerd runtime: %w", err)
	}

	return client, nil
}

func applyRuntimeKind(
	ctx context.Context,
	source compose.Source,
	service string,
) (domain.RuntimeKind, error) {
	project, err := compose.Load(ctx, source)
	if err != nil {
		return "", fmt.Errorf("load Compose runtime metadata: %w", err)
	}
	runtimeKind, err := project.Runtime(service)
	if err != nil {
		return "", fmt.Errorf("select Compose service runtime: %w", err)
	}

	return runtimeKind, nil
}

func classifyApplyFailure(err error) *domain.FailureError {
	if errors.Is(err, context.Canceled) || errors.Is(err, registry.ErrCancelled) {
		return domain.OperationCancelled()
	}

	retryable := errors.Is(err, containerdruntime.ErrUnavailable) || errors.Is(err, dockerruntime.ErrUnavailable) ||
		errors.Is(err, podmanruntime.ErrUnavailable) || errors.Is(err, registry.ErrUnavailable) ||
		errors.Is(err, registry.ErrRateLimited) || errors.Is(err, store.ErrUnavailable)

	return domain.ApplyFailed(retryable)
}
