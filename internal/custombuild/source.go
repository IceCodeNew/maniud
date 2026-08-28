package custombuild

import (
	"context"
	"errors"
	"fmt"
	goversion "go/version"
	"os"
	"path/filepath"
	"runtime"
	"slices"
	"strconv"
	"strings"

	"golang.org/x/mod/modfile"
)

const (
	commitDateLength = 8
)

type resolvedConfig struct {
	root     string
	output   string
	target   string
	goos     string
	goarch   string
	runtimes []string
	modules  []localModule
	settings moduleSettings
}

type sourceMetadata struct {
	revision string
	version  string
	modified bool
}

type localModule struct {
	path      string
	directory string
}

type moduleSettings struct {
	goDirective         string
	toolchain           string
	notificationVersion string
}

type sourceModule struct {
	modules  []localModule
	settings moduleSettings
}

func resolveConfig(config Config) (resolvedConfig, error) {
	if config.Root == "" || config.Output == "" {
		return resolvedConfig{}, errInvalidConfiguration
	}
	root, err := resolveSourceRoot(config.Root)
	if err != nil {
		return resolvedConfig{}, err
	}
	module, err := inspectSourceModule(root)
	if err != nil {
		return resolvedConfig{}, err
	}
	if err = validateSourceModules(root, module.modules); err != nil {
		return resolvedConfig{}, err
	}
	output := config.Output
	if !filepath.IsAbs(output) {
		output = filepath.Join(root, output)
	}
	output = filepath.Clean(output)
	goos, goarch, target, err := resolveTarget(config.Target)
	if err != nil {
		return resolvedConfig{}, err
	}
	runtimes, err := resolveRuntimes(config.Runtimes, config.DisableDefaultRuntimes)
	if err != nil {
		return resolvedConfig{}, err
	}

	return resolvedConfig{
		root: root, output: output, target: target, goos: goos, goarch: goarch,
		runtimes: runtimes, modules: module.modules, settings: module.settings,
	}, nil
}

func resolveSourceRoot(value string) (string, error) {
	return resolveSourceRootWith(value, filepath.Abs, filepath.EvalSymlinks)
}

func resolveSourceRootWith(
	value string,
	absolutePath func(string) (string, error),
	evaluateLinks func(string) (string, error),
) (string, error) {
	root, err := absolutePath(value)
	if err != nil {
		return "", fmt.Errorf("resolve custom build source: %w", err)
	}
	root, err = evaluateLinks(root)
	if err != nil {
		return "", fmt.Errorf("resolve custom build source links: %w", err)
	}

	return root, nil
}

func validateSourceModules(root string, modules []localModule) error {
	for _, module := range modules {
		path := filepath.Join(root, module.directory, "go.mod")
		//nolint:gosec // Each path is a validated repository-local replacement from the root go.mod.
		content, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read custom build module %s: %w", module.path, err)
		}
		if !strings.HasPrefix(string(content), "module "+module.path+"\n") {
			return fmt.Errorf("validate custom build module %s: %w", module.path, errInvalidSource)
		}
	}

	return nil
}

func inspectSourceModule(root string) (sourceModule, error) {
	path := filepath.Join(root, "go.mod")
	//nolint:gosec // The path is the validated source root's module manifest.
	content, err := os.ReadFile(path)
	if err != nil {
		return sourceModule{}, fmt.Errorf("read custom build source module: %w", err)
	}
	file, err := modfile.Parse(path, content, nil)
	if err != nil {
		return sourceModule{}, fmt.Errorf(
			"parse custom build source module: %w",
			errors.Join(errInvalidSource, err),
		)
	}

	return sourceModuleFromFile(root, file)
}

func sourceModuleFromFile(root string, file *modfile.File) (sourceModule, error) {
	if file.Module == nil || file.Module.Mod.Path != projectModule {
		return sourceModule{}, fmt.Errorf("validate custom build root module: %w", errInvalidSource)
	}
	modules, err := localModulesFromFile(root, file)
	if err != nil {
		return sourceModule{}, err
	}
	settings, err := moduleSettingsFromFile(file)
	if err != nil {
		return sourceModule{}, err
	}

	return sourceModule{modules: modules, settings: settings}, nil
}

func moduleSettingsFromFile(file *modfile.File) (moduleSettings, error) {
	settings := moduleSettings{}
	if file.Go != nil {
		settings.goDirective = file.Go.Version
	}
	if file.Toolchain != nil {
		settings.toolchain = file.Toolchain.Name
	}
	version, err := notificationVersionFromFile(file.Replace)
	if err != nil {
		return moduleSettings{}, err
	}
	settings.notificationVersion = version
	if settings.goDirective == "" || settings.notificationVersion == "" {
		return moduleSettings{}, fmt.Errorf(
			"validate custom build source module versions: %w",
			errInvalidSource,
		)
	}

	return settings, nil
}

func notificationVersionFromFile(replacements []*modfile.Replace) (string, error) {
	version := ""
	for _, replacement := range replacements {
		if replacement.Old.Path != notificationModule || replacement.Old.Version != "" {
			continue
		}
		if version != "" || replacement.New.Path != notificationFork || replacement.New.Version == "" {
			return "", fmt.Errorf("validate custom build notification module: %w", errInvalidSource)
		}
		version = replacement.New.Version
	}

	return version, nil
}

func localModulesFromFile(root string, file *modfile.File) ([]localModule, error) {
	modules := []localModule{{path: projectModule, directory: "."}}
	for _, replacement := range file.Replace {
		module, found, err := localModuleFromReplacement(root, replacement)
		if err != nil {
			return nil, err
		}
		if !found {
			continue
		}
		modules = append(modules, module)
	}

	return modules, nil
}

func localModuleFromReplacement(root string, replacement *modfile.Replace) (localModule, bool, error) {
	if !strings.HasPrefix(replacement.Old.Path, projectModule+"/") {
		return localModule{}, false, nil
	}
	if replacement.Old.Version != "" || replacement.New.Version != "" {
		return localModule{}, false, fmt.Errorf(
			"validate custom build local module %s: %w", replacement.Old.Path, errInvalidSource,
		)
	}
	directory, err := localReplacementDirectory(root, replacement.New.Path)
	if err != nil {
		return localModule{}, false, fmt.Errorf(
			"validate custom build local module %s: %w",
			replacement.Old.Path,
			errors.Join(errInvalidSource, err),
		)
	}

	return localModule{path: replacement.Old.Path, directory: directory}, true, nil
}

func localReplacementDirectory(root, target string) (string, error) {
	target = filepath.FromSlash(target)
	absolute := target
	if !filepath.IsAbs(absolute) {
		absolute = filepath.Join(root, target)
	}
	resolved, err := filepath.EvalSymlinks(absolute)
	if err != nil {
		return "", fmt.Errorf("resolve local module links: %w", err)
	}
	// Both paths use the current platform and resolveConfig supplies an absolute root.
	directory, _ := filepath.Rel(root, resolved)
	if !filepath.IsLocal(directory) || directory == "." {
		return "", errInvalidSource
	}

	return directory, nil
}

func resolveTarget(value string) (string, string, string, error) {
	supportedTargets := []string{"darwin/amd64", "darwin/arm64", "linux/amd64", "linux/arm64"}
	if value == "" {
		value = runtime.GOOS + "/" + runtime.GOARCH
	}
	goos, goarch, found := strings.Cut(value, "/")
	if !found || goos == "" || goarch == "" || strings.Contains(goarch, "/") ||
		!validTargetToken(goos) || !validTargetToken(goarch) {
		return "", "", "", fmt.Errorf("validate custom build target: %w", errInvalidConfiguration)
	}
	if !slices.Contains(supportedTargets, value) {
		return "", "", "", fmt.Errorf(
			"target %q is unsupported; choose one of %s: %w",
			value,
			strings.Join(supportedTargets, ", "),
			errInvalidConfiguration,
		)
	}

	return goos, goarch, value, nil
}

func validTargetToken(value string) bool {
	for _, character := range []byte(value) {
		if character < 'a' || character > 'z' {
			if character < '0' || character > '9' {
				return false
			}
		}
	}

	return true
}

func inspectSource(ctx context.Context, root string) (sourceMetadata, error) {
	return inspectSourceWithRunner(ctx, root, runCommand)
}

func inspectSourceWithRunner(ctx context.Context, root string, run commandRunner) (sourceMetadata, error) {
	revisionOutput, err := run(
		ctx, root, nil, "read custom build revision",
		"git", "-C", root, "rev-parse", "--verify", "HEAD",
	)
	if err != nil {
		return sourceMetadata{}, err
	}
	revision := strings.TrimSpace(string(revisionOutput))
	if len(revision) < 12 || !validHex(revision) {
		return sourceMetadata{}, fmt.Errorf("validate custom build revision: %w", errInvalidSource)
	}
	dateOutput, err := run(
		ctx, root, []string{"TZ=UTC"}, "read custom build commit date",
		"git", "-C", root, "show", "-s", "--format=%cd", "--date=format-local:%Y%m%d", "HEAD",
	)
	if err != nil {
		return sourceMetadata{}, err
	}
	date := strings.TrimSpace(string(dateOutput))
	if len(date) != commitDateLength {
		return sourceMetadata{}, fmt.Errorf("validate custom build commit date: %w", errInvalidSource)
	}
	statusOutput, err := run(
		ctx, root, nil, "read custom build source state",
		"git", "-C", root, "status", "--porcelain=v1", "--untracked-files=normal",
	)
	if err != nil {
		return sourceMetadata{}, err
	}
	modified := len(statusOutput) != 0
	modifiedSuffix := ""
	if modified {
		modifiedSuffix = "-dirty"
	}

	return sourceMetadata{
		revision: revision,
		version:  date + modifiedSuffix + "-g" + revision[:12],
		modified: modified,
	}, nil
}

func validHex(value string) bool {
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			if character < 'a' || character > 'f' {
				return false
			}
		}
	}

	return true
}

func inspectGoToolchain(ctx context.Context, root string, settings moduleSettings) (string, error) {
	return inspectGoToolchainWithRunner(ctx, root, settings, runCommand)
}

func inspectGoToolchainWithRunner(
	ctx context.Context,
	root string,
	settings moduleSettings,
	run commandRunner,
) (string, error) {
	required := goversion.Lang("go" + settings.goDirective)
	if settings.toolchain != "" {
		required = goversion.Lang(settings.toolchain)
	}
	if required == "" {
		return "", fmt.Errorf("validate custom build source Go version: %w", errInvalidSource)
	}
	versionOutput, err := run(ctx, root, nil, "read Go toolchain version", "go", "env", "GOVERSION")
	if err != nil {
		return "", err
	}
	version := strings.TrimSpace(string(versionOutput))
	if goversion.Lang(version) != required {
		return "", fmt.Errorf(
			"found Go version %q; install %s and rerun the builder: %w",
			version,
			required,
			errInvalidConfiguration,
		)
	}

	return version, nil
}

func writeBuildModule(directory string, plan buildPlan) error {
	return writeBuildModuleWithWriter(directory, plan, os.WriteFile)
}

func writeBuildModuleWithWriter(
	directory string,
	plan buildPlan,
	writeFile func(string, []byte, os.FileMode) error,
) error {
	var module strings.Builder
	module.WriteString("module " + buildModule + "\n\n")
	module.WriteString("go " + plan.goDirective + "\n\n")
	if plan.toolchain != "" {
		module.WriteString("toolchain " + plan.toolchain + "\n\n")
	}
	module.WriteString("require " + projectModule + " v0.0.0\n\n")
	for _, local := range plan.config.modules {
		path := filepath.Join(plan.config.root, local.directory)
		module.WriteString("replace " + local.path + " => " + strconv.Quote(filepath.ToSlash(path)) + "\n")
	}
	module.WriteString(
		"replace " + notificationModule + " => " + notificationFork + " " +
			plan.notificationVersion + "\n",
	)
	if err := writeFile(filepath.Join(directory, "go.mod"), []byte(module.String()), generatedFileMode); err != nil {
		return fmt.Errorf("write custom build module: %w", err)
	}
	entrypoint := filepath.Join(directory, "main.go")
	if err := writeFile(entrypoint, renderMain(plan.config.runtimes), generatedFileMode); err != nil {
		return fmt.Errorf("write custom build entrypoint: %w", err)
	}

	return nil
}
