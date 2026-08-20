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
	"slices"

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
	archives     map[string]archiveSource
	runtimes     map[string]domain.RuntimeKind
	maniud       bool
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

	if len(source.Content) == 0 || len(source.Content) > maxSourceBytes || !filepath.IsAbs(source.WorkingDir) {
		return Project{value: nil, sourceDigest: domain.Digest{}}, ErrInvalidSource
	}

	loadedSource, cleanup, err := materializeSource(source)
	if err != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, err
	}
	defer cleanup()

	archives, runtimes, maniud, err := validateSource(source.Content, source.Repository != nil)
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
		value: project, sourceDigest: sourceDigest, archives: archives, runtimes: runtimes, maniud: maniud,
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

	return ErrInvalidSource
}

func validateSource(
	content []byte,
	allowSecondary bool,
) (map[string]archiveSource, map[string]domain.RuntimeKind, bool, error) {
	var document yaml.Node

	err := yaml.Load(content, &document, yaml.WithUniqueKeys())
	if err != nil {
		return nil, nil, false, ErrInvalidSource
	}

	if hasUnsupportedYAML(&document) {
		return nil, nil, false, ErrInvalidSource
	}

	raw := make(map[string]any)

	err = document.Load(&raw, yaml.WithUniqueKeys())
	if err != nil {
		return nil, nil, false, ErrInvalidSource
	}

	if referencesExternalSource(raw) && !allowSecondary {
		return nil, nil, false, ErrExternalSource
	}

	archives, runtimes, maniud, valid := decodeManiudSources(raw)
	if !valid {
		return nil, nil, false, ErrInvalidSource
	}

	return archives, runtimes, maniud, nil
}

func hasUnsupportedYAML(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.ShortTag() == "!!merge" {
		return true
	}

	if node.Style&yaml.TaggedStyle != 0 && !isApprovedYAMLTag(node.ShortTag()) {
		return true
	}

	return slices.ContainsFunc(node.Content, hasUnsupportedYAML)
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
