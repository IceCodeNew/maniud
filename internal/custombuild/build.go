// Package custombuild produces a maniud binary from an explicit set of
// first-party container runtime capabilities.
package custombuild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
)

const (
	projectModule       = "github.com/IceCodeNew/maniud"
	buildModule         = projectModule + "/custombuild"
	developmentLinkName = projectModule + "/internal/cli.developmentVersion"
	anyLLMModule        = "github.com/mozilla-ai/any-llm-go"
	anyLLMFork          = "github.com/IceCodeNew/any-llm-go"
	notificationModule  = "github.com/nikoksr/notify"
	notificationFork    = "github.com/IceCodeNew/notify"
	maximumCommandError = 8 << 10
	generatedFileMode   = 0o600
	outputDirectoryMode = 0o750
)

var (
	errInvalidConfiguration = errors.New("custom build configuration is invalid")
	errInvalidSource        = errors.New("custom build source is invalid")
	errDependencyMismatch   = errors.New("custom build dependency set is invalid")
)

// Config contains the complete, intentionally restricted custom build input.
// An empty Runtimes slice selects all first-party runtimes unless
// DisableDefaultRuntimes is true.
type Config struct {
	Root                   string
	Output                 string
	Target                 string
	Runtimes               []string
	DisableDefaultRuntimes bool
}

// Manifest records the inputs needed to identify one completed custom build.
type Manifest struct {
	Output         string   `json:"output"`
	Target         string   `json:"target"`
	Runtimes       []string `json:"runtimes"`
	GoVersion      string   `json:"go_version"`
	SourceRevision string   `json:"source_revision"`
	SourceModified bool     `json:"source_modified"`
	Version        string   `json:"version"`
}

type buildPlan struct {
	config              resolvedConfig
	source              sourceMetadata
	goVersion           string
	goDirective         string
	toolchain           string
	anyLLMVersion       string
	notificationVersion string
}

type buildOperations struct {
	inspectSource       func(context.Context, string) (sourceMetadata, error)
	inspectGoToolchain  func(context.Context, string, moduleSettings) (string, error)
	createWorkspace     func(string, string) (string, error)
	removeWorkspace     func(string) error
	pathWithin          func(string, string) bool
	writeModule         func(string, buildPlan) error
	prepareDependencies func(buildPlan, context.Context, string, []string) error
	buildBinary         func(buildPlan, context.Context, string, []string) (Manifest, error)
}

type outputOperations struct {
	mkdirAll   func(string, os.FileMode) error
	createTemp func(string, string) (*os.File, error)
	close      func(*os.File) error
	remove     func(string) error
	compile    func(buildPlan, context.Context, string, string, []string) error
	rename     func(string, string) error
}

func defaultBuildOperations() buildOperations {
	return buildOperations{
		inspectSource:      inspectSource,
		inspectGoToolchain: inspectGoToolchain,
		createWorkspace:    os.MkdirTemp,
		removeWorkspace:    os.RemoveAll,
		pathWithin:         pathWithin,
		writeModule:        writeBuildModule,
		prepareDependencies: func(plan buildPlan, ctx context.Context, workspace string, environment []string) error {
			return plan.prepareDependencies(ctx, workspace, environment)
		},
		buildBinary: func(
			plan buildPlan,
			ctx context.Context,
			workspace string,
			environment []string,
		) (Manifest, error) {
			return plan.buildBinary(ctx, workspace, environment)
		},
	}
}

func defaultOutputOperations() outputOperations {
	return outputOperations{
		mkdirAll:   os.MkdirAll,
		createTemp: os.CreateTemp,
		close:      (*os.File).Close,
		remove:     removeIfExists,
		compile: func(
			plan buildPlan,
			ctx context.Context,
			workspace, output string,
			environment []string,
		) error {
			return plan.compile(ctx, workspace, output, environment)
		},
		rename: os.Rename,
	}
}

// Build creates one static binary and atomically replaces Config.Output only
// after module, dependency, build, and provenance checks succeed.
func Build(ctx context.Context, config Config) (Manifest, error) {
	return buildWithOperations(ctx, config, defaultBuildOperations())
}

func buildWithOperations(ctx context.Context, config Config, operations buildOperations) (Manifest, error) {
	plan, err := newBuildPlan(ctx, config, operations)
	if err != nil {
		return Manifest{}, err
	}

	return plan.run(ctx, operations)
}

func newBuildPlan(ctx context.Context, config Config, operations buildOperations) (buildPlan, error) {
	resolved, err := resolveConfig(config)
	if err != nil {
		return buildPlan{}, err
	}
	source, err := operations.inspectSource(ctx, resolved.root)
	if err != nil {
		return buildPlan{}, err
	}
	goVersion, err := operations.inspectGoToolchain(ctx, resolved.root, resolved.settings)
	if err != nil {
		return buildPlan{}, err
	}

	return buildPlan{
		config:              resolved,
		source:              source,
		goVersion:           goVersion,
		goDirective:         resolved.settings.goDirective,
		toolchain:           resolved.settings.toolchain,
		anyLLMVersion:       resolved.settings.anyLLMVersion,
		notificationVersion: resolved.settings.notificationVersion,
	}, nil
}

func (plan buildPlan) run(ctx context.Context, operations buildOperations) (manifest Manifest, err error) {
	workspace, err := operations.createWorkspace("", "maniud-custom-build-")
	if err != nil {
		return Manifest{}, fmt.Errorf("create custom build workspace: %w", err)
	}
	defer func() {
		err = errors.Join(err, operations.removeWorkspace(workspace))
	}()
	if operations.pathWithin(plan.config.root, workspace) {
		return Manifest{}, fmt.Errorf("create custom build workspace outside source: %w", errInvalidConfiguration)
	}
	if err = operations.writeModule(workspace, plan); err != nil {
		return Manifest{}, err
	}
	environment := buildEnvironment(plan.config.goos, plan.config.goarch)
	if err = operations.prepareDependencies(plan, ctx, workspace, environment); err != nil {
		return Manifest{}, err
	}

	return operations.buildBinary(plan, ctx, workspace, environment)
}

func (plan buildPlan) prepareDependencies(ctx context.Context, workspace string, environment []string) error {
	return plan.prepareDependenciesWithRunner(ctx, workspace, environment, runCommand)
}

func (plan buildPlan) prepareDependenciesWithRunner(
	ctx context.Context,
	workspace string,
	environment []string,
	run commandRunner,
) error {
	if _, err := run(
		ctx, workspace, environment, "prepare custom build module",
		"go", "mod", "tidy", "-go="+plan.goDirective,
	); err != nil {
		return err
	}
	if _, err := run(
		ctx, workspace, environment, "verify custom build modules",
		"go", "mod", "verify",
	); err != nil {
		return err
	}
	dependencies, err := run(
		ctx, workspace, environment, "list custom build dependencies",
		"go", "list", "-deps", "-mod=readonly", ".",
	)
	if err != nil {
		return err
	}

	return verifyRuntimeDependencies(string(dependencies), plan.config.runtimes)
}

func (plan buildPlan) buildBinary(
	ctx context.Context,
	workspace string,
	environment []string,
) (manifest Manifest, err error) {
	return plan.buildBinaryWithOperations(ctx, workspace, environment, defaultOutputOperations())
}

func (plan buildPlan) buildBinaryWithOperations(
	ctx context.Context,
	workspace string,
	environment []string,
	operations outputOperations,
) (manifest Manifest, err error) {
	if err = operations.mkdirAll(filepath.Dir(plan.config.output), outputDirectoryMode); err != nil {
		return Manifest{}, fmt.Errorf("create custom build output directory: %w", err)
	}
	temporaryOutput, err := operations.createTemp(filepath.Dir(plan.config.output), ".maniud-build-*")
	if err != nil {
		return Manifest{}, fmt.Errorf("create atomic custom build output: %w", err)
	}
	temporaryOutputPath := temporaryOutput.Name()
	if err = operations.close(temporaryOutput); err != nil {
		return Manifest{}, errors.Join(
			fmt.Errorf("close atomic custom build output: %w", err),
			operations.remove(temporaryOutputPath),
		)
	}
	defer func() {
		err = errors.Join(err, operations.remove(temporaryOutputPath))
	}()

	if err = operations.compile(plan, ctx, workspace, temporaryOutputPath, environment); err != nil {
		return Manifest{}, err
	}
	if err = operations.rename(temporaryOutputPath, plan.config.output); err != nil {
		return Manifest{}, fmt.Errorf("publish custom maniud binary: %w", err)
	}

	return Manifest{
		Output:         plan.config.output,
		Target:         plan.config.target,
		Runtimes:       slices.Clone(plan.config.runtimes),
		GoVersion:      plan.goVersion,
		SourceRevision: plan.source.revision,
		SourceModified: plan.source.modified,
		Version:        plan.source.version,
	}, nil
}

func (plan buildPlan) compile(
	ctx context.Context,
	workspace, output string,
	environment []string,
) error {
	return plan.compileWithRunner(ctx, workspace, output, environment, runCommand)
}

func (plan buildPlan) compileWithRunner(
	ctx context.Context,
	workspace, output string,
	environment []string,
	run commandRunner,
) error {
	ldflags := "-X " + developmentLinkName + "=" + plan.source.version
	if _, err := run(
		ctx, workspace, environment, "build custom maniud binary",
		"go", "build", "-mod=readonly", "-buildvcs=true", "-trimpath", "-ldflags", ldflags,
		"-o", output, ".",
	); err != nil {
		return err
	}
	buildMetadata, err := run(
		ctx, workspace, environment, "read custom build provenance",
		"go", "version", "-m", output,
	)
	if err != nil {
		return err
	}

	return verifyBuildMetadata(string(buildMetadata), plan.goVersion)
}
