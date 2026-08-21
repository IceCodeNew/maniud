package compose

import (
	"context"
	"fmt"
	"maps"
	"path/filepath"
	"slices"
	"strings"

	"github.com/compose-spec/compose-go/v2/loader"
	composetypes "github.com/compose-spec/compose-go/v2/types"
	"go.yaml.in/yaml/v4"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const maximumDocumentBytes = 1 << 20

const platformPartCountWithVariant = 3

// DecodeOptions supplies the external context required to normalize one
// in-memory Compose document. Decode never reads secondary files.
type DecodeOptions struct {
	WorkingDirectory string
	ProjectName      string
	Environment      map[string]string
	Profiles         []string
	Service          string
	Platform         containerconfig.Platform
	Paths            PathMapping
}

// Validate checks whether Decode can represent the selected service without
// loss for the supplied platform and path mapping.
func Validate(ctx context.Context, content []byte, options DecodeOptions) error {
	_, err := Decode(ctx, content, options)

	return err
}

// Decode strictly parses one Compose document and returns the selected
// service as a portable Spec.
//
//nolint:cyclop // Parsing keeps each fail-closed source boundary adjacent to its error classification.
func Decode(ctx context.Context, content []byte, options DecodeOptions) (containerconfig.Spec, error) {
	if err := ctx.Err(); err != nil {
		return containerconfig.Spec{}, fmt.Errorf("decode Compose: %w", err)
	}
	if len(content) == 0 || len(content) > maximumDocumentBytes || !filepath.IsAbs(options.WorkingDirectory) {
		return containerconfig.Spec{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}
	if err := validateDocument(content); err != nil {
		return containerconfig.Spec{}, err
	}

	environment := make(composetypes.Mapping, len(options.Environment))
	maps.Copy(environment, options.Environment)
	project, err := loader.LoadWithContext(ctx, composetypes.ConfigDetails{
		WorkingDir: options.WorkingDirectory,
		ConfigFiles: []composetypes.ConfigFile{{
			Filename: filepath.Join(options.WorkingDirectory, "compose.yaml"),
			Content:  content,
		}},
		Environment: environment,
	}, func(loaderOptions *loader.Options) {
		loaderOptions.SkipInclude = true
		loaderOptions.SkipResolveEnvironment = true
		loaderOptions.SkipResolveLabels = true
		loader.WithProfiles(options.Profiles)(loaderOptions)
		projectName := options.ProjectName
		imperative := projectName != ""
		if !imperative {
			projectName = loader.NormalizeProjectName(filepath.Base(options.WorkingDirectory))
		}
		loaderOptions.SetProjectName(projectName, imperative)
	})
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return containerconfig.Spec{}, fmt.Errorf("decode Compose: %w", ctxErr)
		}

		return containerconfig.Spec{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}
	if err := validateProject(project); err != nil {
		return containerconfig.Spec{}, err
	}
	service, err := selectService(project, options.Service)
	if err != nil {
		return containerconfig.Spec{}, err
	}
	platform, err := selectedPlatform(service, options.Platform)
	if err != nil {
		return containerconfig.Spec{}, err
	}

	return FromService(service, platform, options.Paths, ServiceOptions{})
}

func validateDocument(content []byte) error {
	var document yaml.Node
	if err := yaml.Load(content, &document, yaml.WithUniqueKeys()); err != nil || hasUnsupportedYAML(&document) {
		return validationError(containerconfig.ValidationInvalidDocument, "")
	}
	raw := make(map[string]any)
	if err := document.Load(&raw, yaml.WithUniqueKeys()); err != nil {
		return validationError(containerconfig.ValidationInvalidDocument, "")
	}
	if path := externalSourcePath(raw); path != "" {
		return validationError(containerconfig.ValidationUnsupportedField, path)
	}

	return nil
}

func hasUnsupportedYAML(node *yaml.Node) bool {
	if node.Kind == yaml.AliasNode || node.ShortTag() == "!!merge" {
		return true
	}
	if node.Style&yaml.TaggedStyle != 0 && !approvedYAMLTag(node.ShortTag()) {
		return true
	}

	return slices.ContainsFunc(node.Content, hasUnsupportedYAML)
}

func approvedYAMLTag(tag string) bool {
	switch tag {
	case "!override", "!reset", "!!binary", "!!bool", "!!float", "!!int",
		"!!map", "!!null", "!!seq", "!!str", "!!timestamp":
		return true
	default:
		return false
	}
}

//nolint:cyclop // Each supported external Compose source has one explicit field path.
func externalSourcePath(raw map[string]any) string {
	if _, exists := raw["include"]; exists {
		return "/include"
	}
	for _, resource := range []string{"configs", "secrets"} {
		if name := resourceUsingFile(raw[resource]); name != "" {
			return "/" + resource + "/" + pointerToken(name) + "/file"
		}
	}
	services, ok := raw["services"].(map[string]any)
	if !ok {
		return ""
	}
	for name, rawService := range services {
		service, isMapping := rawService.(map[string]any)
		if !isMapping {
			continue
		}
		for _, field := range []string{"env_file", "label_file"} {
			if _, exists := service[field]; exists {
				return servicePath(name, field)
			}
		}
		extends, isMapping := service["extends"].(map[string]any)
		if isMapping {
			if _, exists := extends["file"]; exists {
				return servicePath(name, "extends") + "/file"
			}
		}
	}

	return ""
}

func resourceUsingFile(raw any) string {
	resources, ok := raw.(map[string]any)
	if !ok {
		return ""
	}
	for name, rawResource := range resources {
		resource, isMapping := rawResource.(map[string]any)
		if isMapping {
			if _, exists := resource["file"]; exists {
				return name
			}
		}
	}

	return ""
}

func validateProject(project *composetypes.Project) error {
	fields := []struct {
		path    string
		present bool
	}{
		{"/networks", len(project.Networks) != 0},
		{"/volumes", len(project.Volumes) != 0},
		{"/secrets", len(project.Secrets) != 0},
		{"/configs", len(project.Configs) != 0},
		{"/models", len(project.Models) != 0},
		{"", len(project.Extensions) != 0},
	}
	for _, field := range fields {
		if field.present {
			return validationError(containerconfig.ValidationUnsupportedField, field.path)
		}
	}

	return nil
}

func selectService(project *composetypes.Project, requested string) (composetypes.ServiceConfig, error) {
	if requested == "" {
		names := project.ServiceNames()
		if len(names) != 1 {
			return composetypes.ServiceConfig{}, validationError(
				containerconfig.ValidationInvalidValue,
				"/services",
			)
		}
		requested = names[0]
	}
	service, err := project.GetService(requested)
	if err != nil {
		return composetypes.ServiceConfig{}, validationError(
			containerconfig.ValidationInvalidValue,
			"/services/"+pointerToken(requested),
		)
	}

	return service, nil
}

func selectedPlatform(
	service composetypes.ServiceConfig,
	requested containerconfig.Platform,
) (containerconfig.Platform, error) {
	declared, err := parsePlatform(service.Platform)
	if err != nil {
		return containerconfig.Platform{}, validationError(
			containerconfig.ValidationInvalidValue,
			servicePath(service.Name, "platform"),
		)
	}
	if requested == (containerconfig.Platform{}) {
		requested = declared
	} else if declared != (containerconfig.Platform{}) && requested != declared {
		return containerconfig.Platform{}, validationError(
			containerconfig.ValidationInvalidValue,
			servicePath(service.Name, "platform"),
		)
	}
	if requested.OS == "" || requested.Architecture == "" {
		return containerconfig.Platform{}, validationError(
			containerconfig.ValidationInvalidValue,
			servicePath(service.Name, "platform"),
		)
	}

	return requested, nil
}

func parsePlatform(value string) (containerconfig.Platform, error) {
	if value == "" {
		return containerconfig.Platform{}, nil
	}
	parts := strings.Split(value, "/")
	if len(parts) < 2 || len(parts) > 3 || slices.Contains(parts, "") {
		return containerconfig.Platform{}, validationError(containerconfig.ValidationInvalidValue, "")
	}
	platform := containerconfig.Platform{OS: parts[0], Architecture: parts[1]}
	if len(parts) == platformPartCountWithVariant {
		platform.Variant = parts[2]
	}

	return platform, nil
}
