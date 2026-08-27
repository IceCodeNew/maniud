package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/imagearchive"
	"github.com/IceCodeNew/maniud/internal/imageref"
	"github.com/IceCodeNew/maniud/internal/registry"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
	runtimeplugin "github.com/IceCodeNew/maniud/plugins/runtime"
)

const nerdctlRuntimeCommand = "nerdctl"

type genImageResolver interface {
	Resolve(ctx context.Context, source imageref.Source, platform domain.Platform) (domain.ImageIdentity, error)
}

type genImageUserResolver interface {
	ResolveImageUser(
		ctx context.Context,
		expected domain.ImageIdentity,
		specification string,
	) (string, error)
}

type genDependencies struct {
	workingDirectory  string
	images            genImageResolver
	runtimeImage      func(context.Context, imageref.Source, domain.Platform) (domain.ImageIdentity, error)
	probeRuntimeImage func(context.Context, domain.RuntimeKind, domain.ImageIdentity) (application.ImageProbe, error)
	resolveImageUser  func(context.Context, domain.RuntimeKind, domain.ImageIdentity, string) (string, error)
	analyzeArchive    func(context.Context, imagearchive.Source) (imagearchive.Analysis, error)
	recommendations   imageRecommendationOptions
	write             func(generatedCompose) error
	instructions      io.Writer
}

type generatedImageMissingError struct {
	runtime domain.RuntimeKind
	source  imageref.Source
}

func (err *generatedImageMissingError) Error() string {
	return "generated image is unavailable in the selected local runtime"
}

func (err *generatedImageMissingError) Unwrap() error {
	return registry.ErrNotFound
}

type generatedCompose struct {
	content             []byte
	path                string
	absolutePath        string
	preparation         []byte
	preparationPath     string
	preparationAbsolute string
	importCommand       string
	warnings            []runtimeargv.Warning
}

func defaultGenDependencies(
	environment map[string]string,
	stderr io.Writer,
	getWorkingDirectory func() (string, error),
	runtimes runtimeplugin.Set,
) (genDependencies, error) {
	workingDirectory, err := getWorkingDirectory()
	if err != nil {
		return genDependencies{}, fmt.Errorf("resolve generation working directory: %w", err)
	}
	if !filepath.IsAbs(workingDirectory) {
		return genDependencies{}, fmt.Errorf("resolve generation working directory: %w", runtimeargv.ErrInvalid)
	}
	selectRuntime := func(runtimeKind domain.RuntimeKind) (application.OperationRuntimeFactory, error) {
		return runtimes.Select(
			runtimeKind,
			runtimeEnvironment(environment),
			runtimeWarningSink(stderr),
		)
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
			return runtimes.ResolveLocalImage(
				ctx,
				domain.RuntimeContainerd,
				runtimeEnvironment(environment),
				source,
				platform,
			)
		},
		probeRuntimeImage: func(
			ctx context.Context,
			runtimeKind domain.RuntimeKind,
			expected domain.ImageIdentity,
		) (application.ImageProbe, error) {
			return probeGeneratedRuntimeImage(ctx, runtimeKind, expected, selectRuntime)
		},
		resolveImageUser: func(
			ctx context.Context,
			runtimeKind domain.RuntimeKind,
			expected domain.ImageIdentity,
			specification string,
		) (string, error) {
			return resolveGeneratedImageUser(ctx, runtimeKind, expected, specification, selectRuntime)
		},
		analyzeArchive:  imagearchive.Analyze,
		recommendations: defaultImageRecommendationOptions(environment),
		write:           writeGeneratedFiles,
		instructions:    stderr,
	}, nil
}

func probeGeneratedRuntimeImage(
	ctx context.Context,
	runtimeKind domain.RuntimeKind,
	expected domain.ImageIdentity,
	selectRuntime func(domain.RuntimeKind) (application.OperationRuntimeFactory, error),
) (application.ImageProbe, error) {
	openRuntime, err := selectRuntime(runtimeKind)
	if err != nil {
		return application.ImageProbe{}, fmt.Errorf("select generated runtime: %w", err)
	}
	runtime, err := openRuntime(ctx)
	if err != nil {
		return application.ImageProbe{}, fmt.Errorf("open generated runtime: %w", err)
	}
	defer runtime.CloseIdleConnections()

	probe, err := runtime.ProbeImage(ctx, expected)
	if err != nil {
		return application.ImageProbe{}, fmt.Errorf("probe generated runtime: %w", err)
	}

	return probe, nil
}

func resolveGeneratedImageUser(
	ctx context.Context,
	runtimeKind domain.RuntimeKind,
	expected domain.ImageIdentity,
	specification string,
	selectRuntime func(domain.RuntimeKind) (application.OperationRuntimeFactory, error),
) (string, error) {
	openRuntime, err := selectRuntime(runtimeKind)
	if err != nil {
		return "", fmt.Errorf("select generated runtime: %w", err)
	}
	runtime, err := openRuntime(ctx)
	if err != nil {
		return "", fmt.Errorf("open generated runtime: %w", err)
	}
	defer runtime.CloseIdleConnections()
	resolver, supported := runtime.(genImageUserResolver)
	if !supported {
		return "", registry.ErrUnavailable
	}
	resolved, err := resolver.ResolveImageUser(ctx, expected, specification)
	if err != nil {
		return "", fmt.Errorf("resolve generated image user: %w", err)
	}

	return resolved, nil
}

func executeGen(ctx context.Context, arguments genInvocation, output io.Writer, dependencies genDependencies) error {
	generated, err := renderGen(ctx, arguments, dependencies)
	if err != nil {
		return err
	}

	if err := dependencies.write(generated); err != nil {
		return fmt.Errorf("write generated files: %w", err)
	}
	if generated.preparationPath != "" && dependencies.instructions != nil {
		_, _ = io.WriteString(
			dependencies.instructions,
			"maniud: review and run the generated preparation script before maniud apply.\n",
		)
	}
	if !arguments.json {
		writeGenWarnings(dependencies.instructions, generated.warnings)
	}

	return writeGenResult(output, generated, arguments.json)
}

func writeGenResult(output io.Writer, generated generatedCompose, jsonOutput bool) error {
	result := struct {
		Path          string                `json:"path"`
		Status        string                `json:"status"`
		PrepareScript string                `json:"prepare_script,omitempty"`
		ImportCommand string                `json:"import_command,omitempty"`
		Warnings      []runtimeargv.Warning `json:"warnings,omitempty"`
	}{
		Path: generated.path, Status: "generated",
		PrepareScript: generated.preparationPath,
		ImportCommand: generated.importCommand, Warnings: generated.warnings,
	}

	if jsonOutput {
		if err := json.NewEncoder(output).Encode(result); err != nil {
			return fmt.Errorf("encode generation result: %w", err)
		}

		return nil
	}

	if _, err := fmt.Fprintf(output, "Generated %s.\n", generated.path); err != nil {
		return fmt.Errorf("write generation result: %w", err)
	}
	if generated.preparationPath != "" {
		if _, err := fmt.Fprintf(output, "Preparation script: %s.\n", generated.preparationPath); err != nil {
			return fmt.Errorf("write generation preparation result: %w", err)
		}
	}
	if generated.importCommand != "" {
		if _, err := fmt.Fprintf(output, "Import command: %s\n", generated.importCommand); err != nil {
			return fmt.Errorf("write generation import result: %w", err)
		}
	}

	return nil
}

func writeGenWarnings(output io.Writer, warnings []runtimeargv.Warning) {
	if output == nil {
		return
	}
	for _, warning := range warnings {
		if warning.Option == "" || warning.Option == "image" {
			_, _ = fmt.Fprintf(output, "maniud: warning: %s.\n", warning.Reason)

			continue
		}
		_, _ = fmt.Fprintf(output, "maniud: warning for %s: %s.\n", warning.Option, warning.Reason)
	}
}

func renderGen(
	ctx context.Context,
	arguments genInvocation,
	dependencies genDependencies,
) (generatedCompose, error) {
	if archiveInvocation(arguments) {
		rendered, name, importCommand, warnings, err := renderArchiveGen(ctx, arguments, dependencies)
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
			importCommand: importCommand, warnings: warnings,
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
	workload, warnings, err := prepareGeneratedWorkload(arguments, projection, image, dependencies.recommendations)
	if err != nil {
		return generatedCompose{}, err
	}
	preparation, err := renderGeneratedBindPreparation(ctx, projection, image, workload, dependencies)
	if err != nil {
		return generatedCompose{}, fmt.Errorf("prepare runtime bind mounts: %w", err)
	}
	preparationPath, preparationAbsolute := generatedPreparationPath(selectedPath, absolutePath, preparation)
	rendered, err := compose.RenderResolvedRuntime(ctx, projection, image, workload, filepath.Dir(absolutePath))
	if err != nil {
		return generatedCompose{}, fmt.Errorf("render runtime arguments: %w", err)
	}

	return generatedCompose{
		content: rendered, path: selectedPath, absolutePath: absolutePath,
		preparation: preparation, preparationPath: preparationPath, preparationAbsolute: preparationAbsolute,
		importCommand: "", warnings: warnings,
	}, nil
}

func prepareGeneratedWorkload(
	arguments genInvocation,
	projection runtimeargv.Projection,
	image domain.ImageIdentity,
	recommendations imageRecommendationOptions,
) (domain.WorkloadSpec, []runtimeargv.Warning, error) {
	workload, err := projection.Workload(image)
	if err != nil {
		return domain.WorkloadSpec{}, nil, fmt.Errorf("prepare runtime bind mounts: %w", err)
	}
	warnings := projection.Warnings()
	if arguments.runtimeArgs == nil {
		warnings = append(warnings, imageSettingsReviewWarning())
	}
	if !arguments.recommendedDefaults {
		return workload, warnings, nil
	}
	recommended, recommendedWarnings, err := recommendImageWorkload(workload, recommendations)
	if err != nil {
		return domain.WorkloadSpec{}, nil, fmt.Errorf("prepare recommended image settings: %w", err)
	}

	return recommended, append(warnings, recommendedWarnings...), nil
}

func renderGeneratedBindPreparation(
	ctx context.Context,
	projection runtimeargv.Projection,
	image domain.ImageIdentity,
	workload domain.WorkloadSpec,
	dependencies genDependencies,
) ([]byte, error) {
	preparation, err := renderBindPreparation(workload)
	if !errors.Is(err, errBindPreparationOwner) {
		return preparation, err
	}
	if dependencies.resolveImageUser == nil {
		return nil, registry.ErrUnavailable
	}
	resolved, err := dependencies.resolveImageUser(
		ctx,
		projection.Runtime(),
		image,
		workload.User,
	)
	if err != nil {
		return nil, err
	}
	workload.User = resolved

	return renderBindPreparation(workload)
}

func resolveGeneratedImage(
	ctx context.Context,
	projection runtimeargv.Projection,
	dependencies genDependencies,
) (domain.ImageIdentity, error) {
	if projection.Runtime() == domain.RuntimeContainerd {
		return resolveGeneratedContainerdImage(ctx, projection, dependencies.runtimeImage)
	}

	return resolveGeneratedClientImage(ctx, projection, dependencies)
}

func resolveGeneratedContainerdImage(
	ctx context.Context,
	projection runtimeargv.Projection,
	resolve func(context.Context, imageref.Source, domain.Platform) (domain.ImageIdentity, error),
) (domain.ImageIdentity, error) {
	if resolve == nil {
		return domain.ImageIdentity{}, registry.ErrUnavailable
	}
	image, err := resolve(ctx, projection.Source(), projection.Platform())
	if errors.Is(err, registry.ErrNotFound) {
		return domain.ImageIdentity{}, &generatedImageMissingError{
			runtime: projection.Runtime(), source: projection.Source(),
		}
	}
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("resolve local runtime image: %w", err)
	}

	return image, nil
}

func resolveGeneratedClientImage(
	ctx context.Context,
	projection runtimeargv.Projection,
	dependencies genDependencies,
) (domain.ImageIdentity, error) {
	if dependencies.images == nil {
		return domain.ImageIdentity{}, registry.ErrUnavailable
	}

	image, err := dependencies.images.Resolve(ctx, projection.Source(), projection.Platform())
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("resolve registry image: %w", err)
	}
	if dependencies.probeRuntimeImage == nil {
		return domain.ImageIdentity{}, registry.ErrUnavailable
	}
	probe, err := dependencies.probeRuntimeImage(ctx, projection.Runtime(), image)
	if err != nil {
		return domain.ImageIdentity{}, fmt.Errorf("probe local runtime image: %w", err)
	}
	switch probe.State {
	case application.ImageProbeMissing:
		return domain.ImageIdentity{}, &generatedImageMissingError{
			runtime: projection.Runtime(), source: projection.Source(),
		}
	case application.ImageProbeObserved:
		if !probe.Matches(image) {
			return domain.ImageIdentity{}, registry.ErrProtocol
		}
	case application.ImageProbeUnknown:
		return domain.ImageIdentity{}, registry.ErrUnavailable
	default:
		return domain.ImageIdentity{}, registry.ErrProtocol
	}

	return image, nil
}

func writeGenFailureHint(output io.Writer, err error) {
	if output == nil {
		return
	}
	if missing, ok := errors.AsType[*generatedImageMissingError](err); ok {
		command := string(missing.runtime)
		if missing.runtime == domain.RuntimeContainerd {
			command = nerdctlRuntimeCommand
		}
		_, _ = fmt.Fprintf(
			output,
			"maniud: pull the image first with '%s pull %s', then rerun maniud gen.\n",
			command,
			missing.source.String(),
		)

		return
	}
	if _, ok := errors.AsType[*bindPreparationOwnerError](err); ok {
		_, _ = io.WriteString(
			output,
			"maniud: the pulled image does not contain a resolvable account for its configured user.\n",
		)
	}
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

func generatedPreparationPath(selected, absolute string, content []byte) (string, string) {
	if len(content) == 0 {
		return "", ""
	}
	prepare := func(path string) string {
		extension := filepath.Ext(path)

		return strings.TrimSuffix(path, extension) + ".prepare.sh"
	}

	return prepare(selected), prepare(absolute)
}

func archiveInvocation(arguments genInvocation) bool {
	return arguments.runtimeArgs == nil && strings.HasPrefix(arguments.source, "docker-archive:")
}

func renderArchiveGen(
	ctx context.Context,
	arguments genInvocation,
	dependencies genDependencies,
) ([]byte, string, string, []runtimeargv.Warning, error) {
	source, err := imagearchive.ParseSource(arguments.source)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("parse Docker archive source: %w", err)
	}
	if dependencies.analyzeArchive == nil {
		return nil, "", "", nil, runtimeargv.ErrInvalid
	}
	analysis, err := dependencies.analyzeArchive(ctx, source)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("analyze Docker archive for gen command: %w", err)
	}
	name, err := analysis.ServiceName(arguments.name)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("select Docker archive service: %w", err)
	}
	workload := domain.ResolveWorkloadSpec(domain.WorkloadSpec{
		ServiceName: name, ContainerName: name, Platform: analysis.Identity.Platform, NetworkMode: "bridge",
	}, analysis.Identity)
	warnings := []runtimeargv.Warning{imageSettingsReviewWarning()}
	if arguments.recommendedDefaults {
		var recommendedWarnings []runtimeargv.Warning
		workload, recommendedWarnings, err = recommendImageWorkload(workload, dependencies.recommendations)
		if err != nil {
			return nil, "", "", nil, fmt.Errorf("prepare recommended archive settings: %w", err)
		}
		warnings = append(warnings, recommendedWarnings...)
	}
	rendered, err := compose.RenderResolvedArchive(ctx, analysis, workload, dependencies.workingDirectory)
	if err != nil {
		return nil, "", "", nil, fmt.Errorf("render Docker archive: %w", err)
	}

	return rendered, name, analysis.ImportCommand(), warnings, nil
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
	if errors.Is(err, runtimeplugin.ErrNotBuilt) {
		return domain.RuntimeNotBuilt()
	}

	retryable := errors.Is(err, runtimeplugin.ErrUnavailable) || errors.Is(err, registry.ErrUnavailable) ||
		errors.Is(err, registry.ErrRateLimited)

	return domain.GenerationFailed(retryable)
}
