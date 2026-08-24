// Package runtimeargv binds the public runtime argv projection to maniud's
// immutable image identity.
package runtimeargv

import (
	publicargv "github.com/IceCodeNew/maniud/containerconfig/runtimeargv"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imageref"
)

// ErrInvalid reports argv or image proof that cannot produce executable
// desired state without loss.
var ErrInvalid = publicargv.ErrInvalid

// Warning describes an accepted runtime option that was ignored or normalized.
type Warning = publicargv.Warning

// Projection is one validated runtime command awaiting immutable image proof.
type Projection struct {
	spec             domain.WorkloadSpec
	source           imageref.Source
	platform         domain.Platform
	warnings         []Warning
	environmentFiles []string
	runtime          domain.RuntimeKind
}

// Parse validates and projects one complete runtime create/run argv.
func Parse(arguments []string, explicitName, workingDirectory string) (Projection, error) {
	parsed, err := publicargv.Parse(arguments, explicitName, workingDirectory)
	if err != nil {
		return Projection{}, ErrInvalid
	}

	return newProjection(
		parsed.Spec(), parsed.Source().String(), parsed.Platform(), parsed.Warnings(),
		parsed.EnvironmentFiles(), parsed.Runtime(),
	)
}

// ParseSource validates one registry source and derives a minimal service.
func ParseSource(value, explicitName string) (Projection, error) {
	parsed, err := publicargv.ParseSource(value, explicitName)
	if err != nil {
		return Projection{}, ErrInvalid
	}

	return newProjection(
		parsed.Spec(), parsed.Source().String(), parsed.Platform(), parsed.Warnings(),
		parsed.EnvironmentFiles(), parsed.Runtime(),
	)
}

func newProjection(
	spec domain.WorkloadSpec,
	sourceValue string,
	platform domain.Platform,
	warnings []Warning,
	environmentFiles []string,
	runtime string,
) (Projection, error) {
	source, err := imageref.Normalize(sourceValue)
	if err != nil {
		return Projection{}, ErrInvalid
	}
	runtimeKind, valid := targetRuntime(runtime)
	if !valid {
		return Projection{}, ErrInvalid
	}

	return Projection{
		spec: spec.Clone(), source: source, platform: platform,
		warnings:         append([]Warning(nil), warnings...),
		environmentFiles: append([]string(nil), environmentFiles...), runtime: runtimeKind,
	}, nil
}

func targetRuntime(sourceClient string) (domain.RuntimeKind, bool) {
	switch sourceClient {
	case "", publicargv.RuntimeDocker:
		return domain.RuntimeDocker, true
	case publicargv.RuntimePodman:
		return domain.RuntimePodman, true
	case publicargv.RuntimeNerdctl:
		return domain.RuntimeContainerd, true
	default:
		return "", false
	}
}

// Source returns the normalized registry source named by the command.
func (projection Projection) Source() imageref.Source {
	return projection.source
}

// Platform returns the exact image platform selected by the command.
func (projection Projection) Platform() domain.Platform {
	return projection.platform
}

// Name returns the generated Compose service name.
func (projection Projection) Name() string {
	return projection.spec.ContainerName
}

// Warnings returns privacy-safe notices for ignored or normalized options.
func (projection Projection) Warnings() []Warning {
	return append([]Warning(nil), projection.warnings...)
}

// EnvironmentFiles returns files Compose must resolve under its source boundary.
func (projection Projection) EnvironmentFiles() []string {
	return append([]string(nil), projection.environmentFiles...)
}

// Runtime returns the execution runtime selected by the source client.
func (projection Projection) Runtime() domain.RuntimeKind {
	return projection.runtime
}

// Workload binds verified image identity to the portable configuration.
func (projection Projection) Workload(image domain.ImageIdentity) (domain.WorkloadSpec, error) {
	if image.Origin != domain.ImageOriginRegistry || image.Platform != projection.Platform() ||
		image.Reference == "" || image.ReferenceDigest == (domain.Digest{}) {
		return domain.WorkloadSpec{}, ErrInvalid
	}
	expected, err := projection.source.Pin(image.ReferenceDigest)
	if err != nil || image.Reference != expected.String() || projection.Name() == "" {
		return domain.WorkloadSpec{}, ErrInvalid
	}

	return domain.ResolveWorkloadSpec(projection.spec, image), nil
}
