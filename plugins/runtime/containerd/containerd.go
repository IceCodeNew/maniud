// Package containerd provides the statically linked native containerd capability.
package containerd

import (
	"context"
	"errors"
	"fmt"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
	containerdruntime "github.com/IceCodeNew/maniud/internal/runtime/containerd"
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
		Unavailable:       func(err error) bool { return errors.Is(err, containerdruntime.ErrUnavailable) },
	}
}

//nolint:ireturn // The plugin opener implements the runtime-neutral application boundary.
func open(
	ctx context.Context,
	environment runtimeplugin.Environment,
	_ runtimeplugin.WarningSink,
) (application.OperationRuntime, error) {
	client, err := containerdruntime.Connect(
		ctx,
		environmentValue(environment, addressEnvironment),
		environmentValue(environment, namespaceEnvironment),
	)
	if err != nil {
		return nil, fmt.Errorf("connect containerd runtime: %w", err)
	}

	return client, nil
}

func resolveLocalImage(
	ctx context.Context,
	environment runtimeplugin.Environment,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	identity, err := containerdruntime.ResolveLocalImage(
		ctx,
		environmentValue(environment, addressEnvironment),
		environmentValue(environment, namespaceEnvironment),
		source,
		platform,
	)
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("resolve local containerd image: %w", err)
	}

	return identity, nil
}

func environmentValue(environment runtimeplugin.Environment, name string) string {
	if environment == nil {
		return ""
	}

	return environment(name)
}
