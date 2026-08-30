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

type applyOperations interface {
	DryRun(ctx context.Context, request application.Request) (application.Plan, error)
	Apply(ctx context.Context, request application.Request) (application.Plan, error)
	RepositoryInventory(
		ctx context.Context,
		scope compose.RepositoryScope,
	) ([]application.RepositoryTransaction, error)
	Snapshot(ctx context.Context, request application.Request) (application.OperationSnapshot, error)
	Evidence(snapshot application.OperationSnapshot) (application.EvidenceBundle, error)
}

type applyDependencies struct {
	loadSource     func(context.Context, string) (compose.Source, error)
	operations     applyOperations
	events         application.EventSink
	repositoryRoot string
	repository     compose.RepositoryScope
}

func defaultApplyDependencies(
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
	events application.EventSink,
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
	operations := application.NewApplyFacade(
		resolver,
		resolver,
		func(runtimeKind domain.RuntimeKind) (application.OperationRuntimeFactory, error) {
			return runtimes.Select(
				runtimeKind,
				runtimeEnvironment(environment),
				runtimeWarningSink(stderr),
			)
		},
		func(ctx context.Context) (application.OperationReader, error) {
			return store.OpenReader(ctx, statePath)
		},
		func(ctx context.Context) (*store.Store, error) {
			return store.Open(ctx, statePath)
		},
		events,
	)

	return applyDependencies{
		loadSource: func(ctx context.Context, path string) (compose.Source, error) {
			return loadComposeSource(ctx, path, workingDirectory, environment, filepath.Dir(statePath))
		},
		operations: operations,
		events:     events,
	}, nil
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

func classifyApplyFailure(err error) *domain.FailureError {
	if _, ok := errors.AsType[notificationConfigurationError](err); ok {
		return domain.InvalidInput()
	}
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
