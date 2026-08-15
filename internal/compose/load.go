// Package compose owns strict Compose source loading and keeps vendor models private.
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

	"github.com/IceCodeNew/maniud/internal/domain"
)

const maxSourceBytes = 1 << 20

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
}

// Project is a validated Compose project whose vendor representation stays private.
type Project struct {
	value        *composetypes.Project
	sourceDigest domain.Digest
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
func Load(ctx context.Context, source Source) (Project, error) {
	ctxErr := ctx.Err()
	if ctxErr != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, fmt.Errorf("load compose: %w", ctxErr)
	}

	if len(source.Content) == 0 || len(source.Content) > maxSourceBytes || !filepath.IsAbs(source.WorkingDir) {
		return Project{value: nil, sourceDigest: domain.Digest{}}, ErrInvalidSource
	}

	err := validateSource(source.Content)
	if err != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, err
	}

	environment := make(composetypes.Mapping, len(source.Environment))
	maps.Copy(environment, source.Environment)

	details := composetypes.ConfigDetails{
		Version:    "",
		WorkingDir: source.WorkingDir,
		ConfigFiles: []composetypes.ConfigFile{
			{
				Filename: filepath.Join(source.WorkingDir, "compose.yaml"),
				Content:  source.Content,
				Config:   nil,
			},
		},
		Environment: environment,
	}

	project, err := loader.LoadWithContext(
		ctx,
		details,
		loader.WithProfiles(source.Profiles),
		withoutSecondaryReads,
	)
	if err != nil {
		return Project{value: nil, sourceDigest: domain.Digest{}}, classifyLoadError(ctx)
	}

	return Project{value: project, sourceDigest: domain.Hash(source.Content)}, nil
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

func validateSource(content []byte) error {
	var document yaml.Node

	err := yaml.Load(content, &document, yaml.WithUniqueKeys())
	if err != nil {
		return ErrInvalidSource
	}

	if hasUnsupportedYAML(&document) {
		return ErrInvalidSource
	}

	raw := make(map[string]any)

	err = document.Load(&raw, yaml.WithUniqueKeys())
	if err != nil {
		return ErrInvalidSource
	}

	if referencesExternalSource(raw) {
		return ErrExternalSource
	}

	return nil
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
