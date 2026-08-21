package compose

import (
	"context"
	"errors"
	"fmt"
	"maps"
	"path/filepath"
	"slices"

	"github.com/compose-spec/compose-go/v2/loader"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v4"

	"github.com/IceCodeNew/maniud/containerconfig"
)

var (
	errDocumentIdentity = errors.New("invalid Compose document identity")
	errServiceIdentity  = errors.New("invalid Compose service identity")
	errExtensionValue   = errors.New("unsupported Compose extension value")
)

// EncodeOptions supplies Compose document fields that sit outside Spec.
type EncodeOptions struct {
	Image            string
	WorkingDirectory string
	ProjectName      string
	EnvironmentFiles []string
	PullPolicy       string
	Extensions       map[string]any
}

// Encode serializes a portable Spec as one deterministic Compose document.
// It validates the portable projection before adding caller-owned extensions.
//
//nolint:cyclop // Encoding keeps each fail-closed validation boundary next to the projection it checks.
func Encode(ctx context.Context, spec containerconfig.Spec, options EncodeOptions) ([]byte, error) {
	if err := ctx.Err(); err != nil {
		return nil, fmt.Errorf("encode Compose: %w", err)
	}
	if options.Image == "" || !filepath.IsAbs(options.WorkingDirectory) {
		return nil, validationError(containerconfig.ValidationInvalidValue, "")
	}
	projectName := options.ProjectName
	if projectName == "" {
		projectName = spec.ServiceName
	}
	base := documentFromSpec(spec, options.Image, projectName)
	baseRendered, err := marshalDocument(base)
	if err != nil {
		return nil, validationError(containerconfig.ValidationInvalidValue, "")
	}
	decoded, err := Decode(ctx, baseRendered, DecodeOptions{
		WorkingDirectory: options.WorkingDirectory,
		ProjectName:      projectName,
		Service:          spec.ServiceName,
		Platform:         spec.Platform,
	})
	if err != nil || !containerconfig.Equivalent(decoded, spec) {
		if err != nil {
			return nil, err
		}

		return nil, validationError(containerconfig.ValidationInvalidValue, "/services/"+pointerToken(spec.ServiceName))
	}

	document := base
	service := document.Services[spec.ServiceName]
	service.EnvFile = slices.Clone(options.EnvironmentFiles)
	service.PullPolicy = options.PullPolicy
	document.Services[spec.ServiceName] = service
	document.Extensions = maps.Clone(options.Extensions)
	rendered, err := marshalDocument(document)
	if err != nil || len(rendered) > maximumDocumentBytes {
		return nil, validationError(containerconfig.ValidationInvalidValue, "")
	}
	if err := validateEncoded(ctx, rendered, options.WorkingDirectory, projectName); err != nil {
		return nil, err
	}

	return rendered, nil
}

func marshalDocument(document encodedDocument) ([]byte, error) {
	if document.Name == "" || len(document.Services) != 1 {
		return nil, errDocumentIdentity
	}
	for name := range document.Services {
		if name == "" {
			return nil, errServiceIdentity
		}
	}
	extensions, ok := normalizeExtensions(document.Extensions)
	if !ok {
		return nil, errExtensionValue
	}
	document.Extensions = extensions

	// The normalized document is a closed acyclic scalar, slice, map, and
	// struct tree. Preserve the YAML package error for forward compatibility.
	return yaml.Marshal(document) //nolint:wrapcheck // The public caller classifies this value-free error.
}

func validateEncoded(ctx context.Context, rendered []byte, workingDirectory, projectName string) error {
	_, err := loader.LoadWithContext(ctx, composetypes.ConfigDetails{
		WorkingDir: workingDirectory,
		ConfigFiles: []composetypes.ConfigFile{{
			Filename: filepath.Join(workingDirectory, "compose.yaml"),
			Content:  rendered,
		}},
		Environment: composetypes.Mapping{},
	}, func(options *loader.Options) {
		options.SetProjectName(projectName, true)
		options.SkipInclude = true
		options.SkipResolveEnvironment = true
		options.SkipResolveLabels = true
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return fmt.Errorf("encode Compose: %w", ctxErr)
		}

		return validationError(containerconfig.ValidationInvalidValue, "")
	}

	return nil
}
