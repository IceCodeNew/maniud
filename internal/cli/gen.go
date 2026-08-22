package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
	containerdruntime "github.com/IceCodeNew/maniud/internal/runtime/containerd"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const (
	nerdctlRuntimeCommand          = "nerdctl"
	containerdAddressEnvironment   = "CONTAINERD_ADDRESS"
	containerdNamespaceEnvironment = "CONTAINERD_NAMESPACE"
)

type genImageResolver interface {
	Resolve(ctx context.Context, source imageref.Source, platform domain.Platform) (domain.ImageIdentity, error)
}

type genDependencies struct {
	workingDirectory string
	images           genImageResolver
	runtimeImage     func(context.Context, imageref.Source, domain.Platform) (domain.ImageIdentity, error)
	analyzeArchive   func(context.Context, imagearchive.Source) (imagearchive.Analysis, error)
	write            func(string, []byte) error
}

type generatedCompose struct {
	content       []byte
	path          string
	absolutePath  string
	importCommand string
	warnings      []runtimeargv.Warning
}

func defaultGenDependencies(
	environment map[string]string,
	getWorkingDirectory func() (string, error),
) (genDependencies, error) {
	workingDirectory, err := getWorkingDirectory()
	if err != nil {
		return genDependencies{}, fmt.Errorf("resolve generation working directory: %w", err)
	}
	if !filepath.IsAbs(workingDirectory) {
		return genDependencies{}, fmt.Errorf("resolve generation working directory: %w", runtimeargv.ErrInvalid)
	}

	return genDependencies{
		workingDirectory: workingDirectory,
		images: registry.NewResolver(registry.Options{
			Credentials:      nil,
			DockerConfigPath: dockerConfigPath(environment),
		}),
		runtimeImage: func(
			ctx context.Context,
			source imageref.Source,
			platform domain.Platform,
		) (domain.ImageIdentity, error) {
			return containerdruntime.ResolveLocalImage(
				ctx,
				environment[containerdAddressEnvironment],
				environment[containerdNamespaceEnvironment],
				source,
				platform,
			)
		},
		analyzeArchive: imagearchive.Analyze,
		write:          writeGeneratedCompose,
	}, nil
}

func executeGen(ctx context.Context, arguments genInvocation, output io.Writer, dependencies genDependencies) error {
	generated, err := renderGen(ctx, arguments, dependencies)
	if err != nil {
		return err
	}

	if err := dependencies.write(generated.absolutePath, generated.content); err != nil {
		return fmt.Errorf("write generated Compose: %w", err)
	}

	result := struct {
		Path          string                `json:"path"`
		Status        string                `json:"status"`
		ImportCommand string                `json:"import_command,omitempty"`
		Warnings      []runtimeargv.Warning `json:"warnings,omitempty"`
	}{
		Path: generated.path, Status: "generated",
		ImportCommand: generated.importCommand, Warnings: generated.warnings,
	}

	if err := json.NewEncoder(output).Encode(result); err != nil {
		return fmt.Errorf("encode generation result: %w", err)
	}

	return nil
}

func renderGen(
	ctx context.Context,
	arguments genInvocation,
	dependencies genDependencies,
) (generatedCompose, error) {
	if archiveInvocation(arguments) {
		rendered, name, importCommand, err := renderArchiveGen(ctx, arguments, dependencies)
		if err != nil {
			return generatedCompose{}, err
		}
		selectedPath, absolutePath, err := generatedComposePath(
			arguments.output,
			name,
			dependencies.workingDirectory,
		)
		if err != nil {
			return generatedCompose{}, err
		}

		return generatedCompose{
			content: rendered, path: selectedPath, absolutePath: absolutePath,
			importCommand: importCommand, warnings: nil,
		}, nil
	}

	projection, err := parseGenProjection(arguments, dependencies.workingDirectory)
	if err != nil {
		return generatedCompose{}, fmt.Errorf("parse generation source: %w", err)
	}

	selectedPath, absolutePath, err := generatedComposePath(
		arguments.output,
		projection.Name(),
		dependencies.workingDirectory,
	)
	if err != nil {
		return generatedCompose{}, err
	}
	image, err := resolveGeneratedImage(ctx, projection, dependencies)
	if err != nil {
		return generatedCompose{}, fmt.Errorf("resolve generated image: %w", err)
	}

	rendered, err := compose.RenderRuntime(ctx, projection, image, filepath.Dir(absolutePath))
	if err != nil {
		return generatedCompose{}, fmt.Errorf("render runtime arguments: %w", err)
	}

	return generatedCompose{
		content: rendered, path: selectedPath, absolutePath: absolutePath,
		importCommand: "", warnings: projection.Warnings(),
	}, nil
}

func resolveGeneratedImage(
	ctx context.Context,
	projection runtimeargv.Projection,
	dependencies genDependencies,
) (domain.ImageIdentity, error) {
	if projection.Runtime() == domain.RuntimeContainerd {
		if dependencies.runtimeImage == nil {
			return domain.ImageIdentity{}, registry.ErrUnavailable
		}

		image, err := dependencies.runtimeImage(ctx, projection.Source(), projection.Platform())
		if err != nil {
			return domain.ImageIdentity{}, fmt.Errorf("resolve local runtime image: %w", err)
		}

		return image, nil
	}
	if dependencies.images == nil {
		return domain.ImageIdentity{}, registry.ErrUnavailable
	}

	image, err := dependencies.images.Resolve(ctx, projection.Source(), projection.Platform())
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("resolve registry image: %w", err)
	}

	return image, nil
}

func generatedComposePath(output, name, workingDirectory string) (string, string, error) {
	selected := output
	if selected == "" {
		selected = name + ".yaml"
	}
	if selected == "" || strings.IndexByte(selected, 0) >= 0 || !filepath.IsAbs(workingDirectory) {
		return "", "", runtimeargv.ErrInvalid
	}
	absolute := selected
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(workingDirectory, absolute)
	}

	return selected, filepath.Clean(absolute), nil
}

func archiveInvocation(arguments genInvocation) bool {
	return arguments.runtimeArgs == nil && strings.HasPrefix(arguments.source, "docker-archive:")
}

func renderArchiveGen(
	ctx context.Context,
	arguments genInvocation,
	dependencies genDependencies,
) ([]byte, string, string, error) {
	source, err := imagearchive.ParseSource(arguments.source)
	if err != nil {
		return nil, "", "", fmt.Errorf("parse Docker archive source: %w", err)
	}
	if dependencies.analyzeArchive == nil {
		return nil, "", "", runtimeargv.ErrInvalid
	}
	analysis, err := dependencies.analyzeArchive(ctx, source)
	if err != nil {
		return nil, "", "", fmt.Errorf("analyze Docker archive for gen command: %w", err)
	}
	rendered, name, err := compose.RenderArchive(
		ctx,
		analysis,
		arguments.name,
		dependencies.workingDirectory,
	)
	if err != nil {
		return nil, "", "", fmt.Errorf("render Docker archive: %w", err)
	}

	return rendered, name, analysis.ImportCommand(), nil
}

func parseGenProjection(arguments genInvocation, workingDirectory string) (runtimeargv.Projection, error) {
	if arguments.source != "" && arguments.runtimeArgs == nil {
		projection, err := runtimeargv.ParseSource(arguments.source, arguments.name)
		if err != nil {
			return runtimeargv.Projection{}, fmt.Errorf("parse registry source: %w", err)
		}

		return projection, nil
	}
	if arguments.source == "" && len(arguments.runtimeArgs) > 0 {
		projection, err := runtimeargv.Parse(arguments.runtimeArgs, arguments.name, workingDirectory)
		if err != nil {
			return runtimeargv.Projection{}, fmt.Errorf("parse runtime arguments: %w", err)
		}

		return projection, nil
	}

	return runtimeargv.Projection{}, runtimeargv.ErrInvalid
}

func classifyGenFailure(err error) *domain.FailureError {
	if errors.Is(err, context.Canceled) || errors.Is(err, registry.ErrCancelled) {
		return domain.OperationCancelled()
	}

	retryable := errors.Is(err, registry.ErrUnavailable) || errors.Is(err, registry.ErrRateLimited)

	return domain.GenerationFailed(retryable)
}
