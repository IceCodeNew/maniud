package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/registry"
	"github.com/IceCodeNew/maniud/internal/store"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

type applyRuntime = application.OperationRuntime

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
	runtimes runtimeplugin.Set,
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
			return openApplyRuntime(ctx, runtimeKind, environment, stderr, runtimes)
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
	runtimes runtimeplugin.Set,
) (applyRuntime, error) {
	factory, err := runtimes.Select(
		runtimeKind,
		runtimeEnvironment(environment),
		runtimeWarningSink(stderr),
	)
	if err != nil {
		return nil, fmt.Errorf("select apply runtime: %w", err)
	}
	runtime, err := factory(ctx)
	if err != nil {
		return nil, fmt.Errorf("open apply runtime: %w", err)
	}

	return runtime, nil
}

func runtimeEnvironment(environment map[string]string) runtimeplugin.Environment {
	return func(name string) string { return environment[name] }
}

func runtimeWarningSink(stderr io.Writer) runtimeplugin.WarningSink {
	if stderr == nil {
		return nil
	}

	return func(warning runtimeplugin.Warning) error {
		err := json.NewEncoder(stderr).Encode(struct {
			Code    string `json:"code"`
			Message string `json:"message"`
		}{Code: warning.Code, Message: warning.Message})
		if err != nil {
			return fmt.Errorf("emit runtime endpoint warning: %w", err)
		}

		return nil
	}
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
	if errors.Is(err, runtimeplugin.ErrNotBuilt) {
		return domain.RuntimeNotBuilt()
	}

	retryable := errors.Is(err, runtimeplugin.ErrUnavailable) || errors.Is(err, registry.ErrUnavailable) ||
		errors.Is(err, registry.ErrRateLimited) || errors.Is(err, store.ErrUnavailable)

	return domain.ApplyFailed(retryable)
}
