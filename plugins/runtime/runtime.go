// Package runtime composes the container runtime capabilities included in a
// maniud binary.
package runtime

import (
	"context"
	"errors"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

var (
	// ErrInvalidPlugin reports a malformed or duplicate build-time plugin.
	ErrInvalidPlugin = errors.New("runtime plugin is invalid")
	// ErrNotBuilt reports a valid runtime omitted from the current binary.
	ErrNotBuilt = errors.New("runtime is not included in this build")
	// ErrUnavailable marks a concrete runtime availability failure.
	ErrUnavailable = errors.New("container runtime is unavailable")
)

// Environment provides read-only access to process configuration.
type Environment func(string) string

// Warning is a runtime-neutral endpoint warning.
type Warning struct {
	Code    string
	Message string
}

// WarningSink publishes one endpoint warning before a connection opens.
type WarningSink func(Warning) error

// OperationOpener opens one configured runtime connection.
type OperationOpener func(
	context.Context,
	Environment,
	WarningSink,
) (application.OperationRuntime, error)

// LocalImageResolver resolves an image already stored in a local runtime.
type LocalImageResolver func(
	context.Context,
	Environment,
	imageref.Source,
	domain.Platform,
) (domain.ImageIdentity, error)

// UnavailableMatcher recognizes availability errors owned by one plugin.
type UnavailableMatcher func(error) bool

// Plugin describes one statically linked runtime capability. Runtime packages
// return values of this type without registering process-global state.
type Plugin struct {
	Kind              domain.RuntimeKind
	Open              OperationOpener
	ResolveLocalImage LocalImageResolver
	Unavailable       UnavailableMatcher
}

// Set is an immutable set of the runtime plugins linked into one binary.
type Set struct {
	plugins map[domain.RuntimeKind]Plugin
}

// NewSet validates and copies the linked runtime plugins.
func NewSet(plugins ...Plugin) (Set, error) {
	selected := make(map[domain.RuntimeKind]Plugin, len(plugins))
	for _, plugin := range plugins {
		kind, valid := domain.ParseRuntimeKind(plugin.Kind.String())
		if !valid || kind != plugin.Kind || plugin.Open == nil || plugin.Unavailable == nil {
			return Set{}, ErrInvalidPlugin
		}
		if _, duplicate := selected[plugin.Kind]; duplicate {
			return Set{}, ErrInvalidPlugin
		}

		selected[plugin.Kind] = plugin
	}

	return Set{plugins: selected}, nil
}

// Select returns a side-effect-free factory for one compiled runtime. The
// caller can reject an omitted runtime before opening sockets or durable state.
func (set Set) Select(
	kind domain.RuntimeKind,
	environment Environment,
	warnings WarningSink,
) (application.OperationRuntimeFactory, error) {
	plugin, err := set.plugin(kind)
	if err != nil {
		return nil, err
	}

	return func(ctx context.Context) (application.OperationRuntime, error) {
		return plugin.Open(ctx, environment, warnings)
	}, nil
}

// ResolveLocalImage uses the selected runtime's local image resolver.
func (set Set) ResolveLocalImage(
	ctx context.Context,
	kind domain.RuntimeKind,
	environment Environment,
	source imageref.Source,
	platform domain.Platform,
) (domain.ImageIdentity, error) {
	plugin, err := set.plugin(kind)
	if err != nil {
		return domain.ImageIdentity{}, err
	}
	if plugin.ResolveLocalImage == nil {
		return domain.ImageIdentity{}, ErrInvalidPlugin
	}

	return plugin.ResolveLocalImage(ctx, environment, source, platform)
}

// Classify marks concrete runtime availability errors without exposing a
// runtime-specific package to CLI orchestration.
func (set Set) Classify(err error) error {
	if err == nil || errors.Is(err, ErrUnavailable) {
		return err
	}
	for _, plugin := range set.plugins {
		if plugin.Unavailable(err) {
			return errors.Join(err, ErrUnavailable)
		}
	}

	return err
}

func (set Set) plugin(kind domain.RuntimeKind) (Plugin, error) {
	parsed, valid := domain.ParseRuntimeKind(kind.String())
	if !valid || parsed != kind {
		return Plugin{}, application.ErrInvalidRequest
	}
	plugin, built := set.plugins[kind]
	if !built {
		return Plugin{}, ErrNotBuilt
	}

	return plugin, nil
}
