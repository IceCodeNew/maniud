// Package compose owns strict Compose source loading and keeps vendor models private.
package compose

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"maps"
	"os"
	"path/filepath"

	"github.com/compose-spec/compose-go/v2/loader"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v4"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	maxSourceBytes             = 1 << 20
	materializedFileMode       = os.FileMode(0o600)
	materializedExecutableMode = os.FileMode(0o700)
	materializedDirectoryMode  = os.FileMode(0o700)
)

var (
	// ErrInvalidSource reports malformed or semantically invalid Compose input.
	ErrInvalidSource = errors.New("compose source is invalid")
	// ErrExternalSource reports source that would read unverified secondary files.
	ErrExternalSource = errors.New("compose source references unverified external content")
)

// DiagnosticReason is a stable, content-free Compose validation category.
type DiagnosticReason string

const (
	// DiagnosticYAMLSyntax identifies input that the YAML parser cannot read.
	DiagnosticYAMLSyntax DiagnosticReason = "yaml_syntax_invalid"
	// DiagnosticYAMLStructure identifies an invalid YAML mapping or value shape.
	DiagnosticYAMLStructure DiagnosticReason = "yaml_structure_invalid"
	// DiagnosticYAMLUnsupported identifies YAML aliases, merge keys, or unapproved tags.
	DiagnosticYAMLUnsupported DiagnosticReason = "yaml_feature_unsupported"
	// DiagnosticComposeValidation identifies input rejected by Compose validation.
	DiagnosticComposeValidation DiagnosticReason = "compose_validation_failed"
)

// SourceDiagnosticError reports a stable reason and an optional safe source position.
// It intentionally excludes parser messages and source content.
type SourceDiagnosticError struct {
	File   string
	Reason DiagnosticReason
	Line   int
	Column int
}

// Error implements error without exposing parser or source content.
func (*SourceDiagnosticError) Error() string {
	return ErrInvalidSource.Error()
}

// Unwrap preserves the existing invalid-source contract.
func (*SourceDiagnosticError) Unwrap() error {
	return ErrInvalidSource
}

// Source is one immutable Compose document and its explicit interpolation context.
type Source struct {
	Content     []byte
	WorkingDir  string
	Environment map[string]string
	Profiles    []string
	Repository  *RepositorySnapshot
	runtimeBase string
}

// RepositorySnapshot is a bounded committed source bundle. Entry and Files
// use slash-separated paths relative to Root.
type RepositorySnapshot struct {
	Root         string
	Entry        string
	Files        map[string]RepositoryFile
	RuntimePaths []RepositoryPath
	Digest       domain.Digest
}

// Project is a validated Compose project whose vendor representation stays private.
type Project struct {
	value        *composetypes.Project
	sourceDigest domain.Digest
	extension    maniudExtension
	pathFrom     string
	pathTo       string
}

// Name returns the normalized project name.
func (project Project) Name() string {
	return project.value.Name
}

// ServiceNames returns active service names in lexical order.
func (project Project) ServiceNames() []string {
	return project.value.ServiceNames()
}

// Load validates and normalizes one in-memory Compose document without secondary file reads.
//
//nolint:cyclop // Repository and in-memory sources share one loader and distinct I/O policies.
func Load(ctx context.Context, source Source) (Project, error) {
	ctxErr := ctx.Err()
	if ctxErr != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, fmt.Errorf("load compose: %w", ctxErr)
	}

	if !filepath.IsAbs(source.WorkingDir) {
		return Project{value: nil, sourceDigest: domain.Digest{}}, ErrInvalidSource
	}
	if len(source.Content) == 0 || len(source.Content) > maxSourceBytes {
		return Project{value: nil, sourceDigest: domain.Digest{}}, newSourceDiagnostic(
			DiagnosticComposeValidation,
			0,
			0,
		)
	}

	loadedSource, cleanup, err := materializeSource(source)
	if err != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, err
	}
	defer cleanup()

	extension, err := validateSource(source.Content, source.Repository != nil)
	if err != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, err
	}

	environment := make(composetypes.Mapping, len(source.Environment))
	maps.Copy(environment, source.Environment)

	details := composetypes.ConfigDetails{
		Version:    "",
		WorkingDir: loadedSource.workingDirectory,
		ConfigFiles: []composetypes.ConfigFile{
			{
				Filename: loadedSource.filename,
				Content:  source.Content,
				Config:   nil,
			},
		},
		Environment: environment,
	}

	options := []func(*loader.Options){loader.WithProfiles(source.Profiles)}
	if source.Repository == nil {
		options = append(options, withoutSecondaryReads)
	} else {
		options = append(options, loader.WithDiscardEnvFiles)
	}
	project, err := loader.LoadWithContext(ctx, details, options...)
	if err != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, classifyLoadError(ctx)
	}
	if source.Repository != nil {
		discardResolvedSecondaryReferences(project)
	}

	sourceDigest := domain.Hash(source.Content)
	if source.Repository != nil {
		sourceDigest = source.Repository.Digest
	}

	return Project{
		value: project, sourceDigest: sourceDigest, extension: extension,
		pathFrom: loadedSource.repositoryRoot, pathTo: source.repositoryRoot(),
	}, nil
}

type materializedSource struct {
	filename         string
	workingDirectory string
	repositoryRoot   string
}

func materializeSource(source Source) (materializedSource, func(), error) {
	if source.Repository == nil {
		return materializedSource{
			filename: filepath.Join(source.WorkingDir, "compose.yaml"), workingDirectory: source.WorkingDir,
		}, func() {}, nil
	}
	snapshot := source.Repository
	if !validRepositorySnapshot(source) {
		return materializedSource{}, func() {}, ErrInvalidSource
	}
	root, err := os.MkdirTemp("", "maniud-compose-")
	if err != nil {
		return materializedSource{}, func() {}, ErrInvalidSource
	}
	cleanup := func() { _ = os.RemoveAll(root) }
	for name, file := range snapshot.Files {
		path := filepath.Join(root, filepath.FromSlash(name))
		mode := materializedFileMode
		if file.Executable {
			mode = materializedExecutableMode
		}
		if err := os.MkdirAll(filepath.Dir(path), materializedDirectoryMode); err != nil ||
			os.WriteFile(path, file.Content, mode) != nil {
			cleanup()

			return materializedSource{}, func() {}, ErrInvalidSource
		}
	}
	filename := filepath.Join(root, filepath.FromSlash(snapshot.Entry))

	return materializedSource{
		filename: filename, workingDirectory: filepath.Dir(filename), repositoryRoot: root,
	}, cleanup, nil
}

//nolint:cyclop // Every bundle bound is rechecked before any temporary file is materialized.
func validRepositorySnapshot(source Source) bool {
	snapshot := source.Repository
	if snapshot == nil || !filepath.IsAbs(snapshot.Root) || snapshot.Digest == (domain.Digest{}) ||
		!validRepositoryPath(snapshot.Entry) || !bytes.Equal(source.Content, snapshot.Files[snapshot.Entry].Content) ||
		len(snapshot.Files) > maximumRepositoryFiles || snapshot.Digest != repositoryDigest(
		snapshot.Entry,
		snapshot.Files,
		snapshot.RuntimePaths,
		source.Environment,
	) {
		return false
	}
	totalBytes := 0
	for name, file := range snapshot.Files {
		if !validRepositoryPath(name) || len(file.Content) > maxSourceBytes {
			return false
		}
		totalBytes += len(file.Content)
		if totalBytes > maximumRepositoryBytes {
			return false
		}
	}
	for index, path := range snapshot.RuntimePaths {
		if !validRepositoryPath(path.Path) || index > 0 &&
			snapshot.RuntimePaths[index-1].Path >= path.Path {
			return false
		}
	}

	return true
}

func validRepositoryPath(value string) bool {
	return value != "" && filepath.IsLocal(filepath.FromSlash(value)) &&
		filepath.ToSlash(filepath.Clean(filepath.FromSlash(value))) == value
}

func (source Source) repositoryRoot() string {
	if source.Repository == nil {
		return ""
	}
	if source.runtimeBase != "" {
		return repositoryRuntimeRoot(source.runtimeBase, source.Repository.Digest)
	}

	return source.Repository.Root
}

func discardResolvedSecondaryReferences(project *composetypes.Project) {
	for name, service := range project.Services {
		service.EnvFiles = nil
		service.LabelFiles = nil
		service.Extends = nil
		project.Services[name] = service
	}
}

func withoutSecondaryReads(options *loader.Options) {
	options.SkipInclude = true
	options.SkipResolveEnvironment = true
	options.SkipResolveLabels = true
}

func classifyLoadError(ctx context.Context) error {
	ctxErr := ctx.Err()
	if ctxErr != nil {
		return fmt.Errorf("load compose: %w", ctxErr)
	}

	return newSourceDiagnostic(DiagnosticComposeValidation, 0, 0)
}

func validateSource(
	content []byte,
	allowSecondary bool,
) (maniudExtension, error) {
	var document yaml.Node

	err := yaml.Load(content, &document, yaml.WithUniqueKeys())
	if err != nil {
		return maniudExtension{}, sourceYAMLError(DiagnosticYAMLSyntax, err)
	}

	if unsupported := unsupportedYAMLNode(&document); unsupported != nil {
		return maniudExtension{}, newSourceDiagnostic(
			DiagnosticYAMLUnsupported,
			unsupported.Line,
			unsupported.Column,
		)
	}

	raw := make(map[string]any)

	err = document.Load(&raw, yaml.WithUniqueKeys())
	if err != nil {
		return maniudExtension{}, sourceYAMLError(DiagnosticYAMLStructure, err)
	}

	if referencesExternalSource(raw) && !allowSecondary {
		return maniudExtension{}, ErrExternalSource
	}

	extension, valid := decodeComposeExtensions(raw)
	if !valid {
		return maniudExtension{}, newSourceDiagnostic(DiagnosticComposeValidation, 0, 0)
	}

	return extension, nil
}

func unsupportedYAMLNode(node *yaml.Node) *yaml.Node {
	if node.Kind == yaml.AliasNode || node.ShortTag() == "!!merge" {
		return node
	}

	if node.Style&yaml.TaggedStyle != 0 && !isApprovedYAMLTag(node.ShortTag()) {
		return node
	}

	for _, child := range node.Content {
		if unsupported := unsupportedYAMLNode(child); unsupported != nil {
			return unsupported
		}
	}

	return nil
}

func sourceYAMLError(reason DiagnosticReason, err error) *SourceDiagnosticError {
	loadError, ok := errors.AsType[*yaml.LoadError](err)
	if !ok {
		return newSourceDiagnostic(reason, 0, 0)
	}

	return newSourceDiagnostic(reason, loadError.Mark.Line, loadError.Mark.Column)
}

func newSourceDiagnostic(reason DiagnosticReason, line, column int) *SourceDiagnosticError {
	return &SourceDiagnosticError{Reason: reason, Line: max(line, 0), Column: max(column, 0)}
}

func isApprovedYAMLTag(tag string) bool {
	switch tag {
	case "!override", "!reset", "!!binary", "!!bool", "!!float", "!!int",
		"!!map", "!!null", "!!seq", "!!str", "!!timestamp":
		return true
	default:
		return false
	}
}

func referencesExternalSource(raw map[string]any) bool {
	if _, exists := raw["include"]; exists {
		return true
	}

	if resourceUsesFile(raw["configs"]) || resourceUsesFile(raw["secrets"]) {
		return true
	}

	services, ok := raw["services"].(map[string]any)
	if !ok {
		return false
	}

	for _, rawService := range services {
		service, isMapping := rawService.(map[string]any)
		if !isMapping {
			continue
		}

		if serviceReferencesExternalSource(service) {
			return true
		}
	}

	return false
}

func serviceReferencesExternalSource(service map[string]any) bool {
	if _, exists := service["env_file"]; exists {
		return true
	}

	if _, exists := service["label_file"]; exists {
		return true
	}

	extends, ok := service["extends"].(map[string]any)
	if !ok {
		return false
	}

	_, exists := extends["file"]

	return exists
}

func resourceUsesFile(raw any) bool {
	resources, ok := raw.(map[string]any)
	if !ok {
		return false
	}

	for _, rawResource := range resources {
		resource, isMapping := rawResource.(map[string]any)
		if !isMapping {
			continue
		}

		if _, exists := resource["file"]; exists {
			return true
		}
	}

	return false
}
