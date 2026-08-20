package cli

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
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
	generatedFileMode              = os.FileMode(0o600)
	nerdctlRuntimeCommand          = "nerdctl"
	containerdAddressEnvironment   = "CONTAINERD_ADDRESS"
	containerdNamespaceEnvironment = "CONTAINERD_NAMESPACE"
)

type generatedComposeOperations struct {
	openRoot      func(string) (*os.Root, error)
	openDirectory func(*os.Root) (*os.File, error)
	openFile      func(*os.Root, string) (*os.File, error)
	statFile      func(*os.File) (os.FileInfo, error)
	chmodFile     func(*os.File, os.FileMode) error
	writeFile     func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	syncDirectory func(*os.File) error
	lstat         func(*os.Root, string) (os.FileInfo, error)
	remove        func(*os.Root, string) error
	closeRoot     func(*os.Root) error
}

func generatedComposeDefaultOperations() generatedComposeOperations {
	return generatedComposeOperations{
		openRoot: os.OpenRoot,
		openDirectory: func(root *os.Root) (*os.File, error) {
			return root.Open(".")
		},
		openFile: func(root *os.Root, name string) (*os.File, error) {
			return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, generatedFileMode)
		},
		statFile:      (*os.File).Stat,
		chmodFile:     (*os.File).Chmod,
		writeFile:     (*os.File).Write,
		syncFile:      (*os.File).Sync,
		closeFile:     (*os.File).Close,
		syncDirectory: (*os.File).Sync,
		lstat:         (*os.Root).Lstat,
		remove:        (*os.Root).Remove,
		closeRoot:     (*os.Root).Close,
	}
}

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
	if projection.Runtime() == nerdctlRuntimeCommand {
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

func writeGeneratedCompose(path string, content []byte) (returnErr error) {
	return writeGeneratedComposeWithOperations(path, content, generatedComposeDefaultOperations())
}

func writeGeneratedComposeWithOperations(
	path string,
	content []byte,
	operations generatedComposeOperations,
) (returnErr error) {
	owned, err := openGeneratedComposeWithOperations(path, operations)
	if err != nil {
		return err
	}
	published := false
	defer func() {
		if !published {
			returnErr = errors.Join(returnErr, owned.remove())
		}
		returnErr = errors.Join(returnErr, owned.close())
	}()

	if err := owned.write(content); err != nil {
		return err
	}
	published = true

	return nil
}

type generatedComposeFile struct {
	root       *os.Root
	directory  *os.File
	file       *os.File
	name       string
	identity   os.FileInfo
	operations generatedComposeOperations
}

func openGeneratedCompose(path string) (*generatedComposeFile, error) {
	return openGeneratedComposeWithOperations(path, generatedComposeDefaultOperations())
}

func openGeneratedComposeWithOperations(
	path string,
	operations generatedComposeOperations,
) (*generatedComposeFile, error) {
	root, err := operations.openRoot(filepath.Dir(path))
	if err != nil {
		return nil, fmt.Errorf("open generated Compose directory: %w", err)
	}

	directory, err := operations.openDirectory(root)
	if err != nil {
		_ = operations.closeRoot(root)

		return nil, fmt.Errorf("retain generated Compose directory: %w", err)
	}

	file, err := operations.openFile(root, filepath.Base(path))
	if err != nil {
		_ = operations.closeFile(directory)
		_ = operations.closeRoot(root)

		return nil, fmt.Errorf("create generated Compose: %w", err)
	}

	identity, err := operations.statFile(file)
	if err != nil {
		_ = operations.closeFile(file)
		_ = operations.closeFile(directory)
		_ = operations.closeRoot(root)

		return nil, fmt.Errorf("inspect generated Compose: %w", err)
	}

	return &generatedComposeFile{
		root:       root,
		directory:  directory,
		file:       file,
		name:       filepath.Base(path),
		identity:   identity,
		operations: operations,
	}, nil
}

func (owned *generatedComposeFile) write(content []byte) error {
	if err := owned.operations.chmodFile(owned.file, generatedFileMode); err != nil {
		return fmt.Errorf("set generated Compose mode: %w", err)
	}
	written, err := owned.operations.writeFile(owned.file, content)
	if err != nil {
		return fmt.Errorf("write generated Compose: %w", err)
	}
	if written != len(content) {
		return fmt.Errorf("write generated Compose: %w", io.ErrShortWrite)
	}
	if err := owned.operations.syncFile(owned.file); err != nil {
		return fmt.Errorf("sync generated Compose: %w", err)
	}

	if err := owned.revalidate(int64(len(content))); err != nil {
		return err
	}
	if err := owned.operations.closeFile(owned.file); err != nil {
		return fmt.Errorf("close generated Compose: %w", err)
	}
	owned.file = nil
	if err := owned.operations.syncDirectory(owned.directory); err != nil {
		return fmt.Errorf("sync generated Compose directory: %w", err)
	}
	if err := owned.revalidate(int64(len(content))); err != nil {
		return err
	}

	return nil
}

func (owned *generatedComposeFile) revalidate(size int64) error {
	current, err := owned.operations.lstat(owned.root, owned.name)
	if err != nil {
		return errors.Join(fmt.Errorf("revalidate generated Compose: %w", err), runtimeargv.ErrInvalid)
	}
	if !os.SameFile(owned.identity, current) || current.Size() != size {
		return fmt.Errorf("generated Compose identity changed: %w", runtimeargv.ErrInvalid)
	}

	return nil
}

func (owned *generatedComposeFile) remove() error {
	current, err := owned.operations.lstat(owned.root, owned.name)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("inspect failed generated Compose: %w", err)
	}
	if !os.SameFile(owned.identity, current) {
		return fmt.Errorf("failed generated Compose ownership changed: %w", runtimeargv.ErrInvalid)
	}
	if err := owned.operations.remove(owned.root, owned.name); err != nil {
		return fmt.Errorf("remove failed generated Compose: %w", err)
	}
	if err := owned.operations.syncDirectory(owned.directory); err != nil {
		return fmt.Errorf("sync removed generated Compose: %w", err)
	}

	return nil
}

func (owned *generatedComposeFile) close() error {
	var errs []error
	if owned.file != nil {
		errs = append(errs, owned.operations.closeFile(owned.file))
	}
	errs = append(
		errs,
		owned.operations.closeFile(owned.directory),
		owned.operations.closeRoot(owned.root),
	)

	return errors.Join(errs...)
}

func classifyGenFailure(err error) *domain.FailureError {
	if errors.Is(err, context.Canceled) || errors.Is(err, registry.ErrCancelled) {
		return domain.OperationCancelled()
	}

	retryable := errors.Is(err, registry.ErrUnavailable) || errors.Is(err, registry.ErrRateLimited)

	return domain.GenerationFailed(retryable)
}
