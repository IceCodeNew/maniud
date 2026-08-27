package containerd

import (
	"context"
	"errors"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const (
	addressEnvironment   = "CONTAINERD_ADDRESS"
	namespaceEnvironment = "CONTAINERD_NAMESPACE"
)

// New returns the explicit native containerd plugin definition.
func New() runtimeplugin.Plugin {
	return runtimeplugin.Plugin{
		Kind:              domain.RuntimeContainerd,
		Open:              open,
		ResolveLocalImage: resolveLocalImage,
		Unavailable:       func(err error) bool { return errors.Is(err, ErrUnavailable) },
	}
}

//nolint:ireturn // The plugin opener implements the runtime-neutral application boundary.
func open(
	ctx context.Context,
	environment runtimeplugin.Environment,
	_ runtimeplugin.WarningSink,
) (application.OperationRuntime, error) {
	return Connect(
		ctx,
		environmentValue(environment, addressEnvironment),
		environmentValue(environment, namespaceEnvironment),
	)
}

func resolveLocalImage(
	ctx context.Context,
	environment runtimeplugin.Environment,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	return ResolveLocalImage(
		ctx,
		environmentValue(environment, addressEnvironment),
		environmentValue(environment, namespaceEnvironment),
		source,
		platform,
	)
}

func environmentValue(environment runtimeplugin.Environment, name string) string {
	if environment == nil {
		return ""
	}

	return environment(name)
}
