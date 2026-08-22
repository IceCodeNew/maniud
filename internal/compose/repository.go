package compose

import (
	"bytes"
	"encoding/binary"
	"maps"
	"path/filepath"
	"slices"
	"strconv"
	"strings"

	"github.com/compose-spec/compose-go/v2/dotenv"
	composeformat "github.com/compose-spec/compose-go/v2/format"
	"go.yaml.in/yaml/v4"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	maximumRepositoryFiles = 256
	maximumRepositoryBytes = 16 << 20
	composeBindMountType   = "bind"
	composeDefaultEnvFile  = ".env"
	composeDisableEnvFile  = "COMPOSE_DISABLE_ENV_FILE"
	repositoryPathKey      = "path"
	repositoryFormatKey    = "format"
)

// RepositoryFile contains one committed regular file and its Git executable bit.
type RepositoryFile struct {
	Content    []byte
	Executable bool
}

// TrackedFileReader returns one optional committed slash-separated repository
// path. A missing file is distinct from an invalid or unavailable snapshot.
type TrackedFileReader func(string) (RepositoryFile, bool, error)

// RepositoryPathSnapshot contains one tracked regular file or one tracked
// directory tree. Files use slash-separated paths relative to the repository.
type RepositoryPathSnapshot struct {
	Directory bool
	Files     map[string]RepositoryFile
}

// TrackedPathReader returns committed bytes for one runtime-visible path.
type TrackedPathReader func(string) (RepositoryPathSnapshot, error)

// RepositoryPath identifies one runtime-visible tracked source path.
type RepositoryPath struct {
	Path      string
	Directory bool
}

// CaptureRepositorySource builds a closed source bundle from one proven-clean
// repository. The caller owns the Git cleanliness and committed-file proof;
// this function discovers and bounds every local file Compose may read.
//
//nolint:cyclop,funlen,gocognit,gocyclo // The breadth-first walk enforces one global file and byte budget.
func CaptureRepositorySource(
	root string,
	entry string,
	environment map[string]string,
	read TrackedFileReader,
	readPath TrackedPathReader,
) (Source, error) {
	if !filepath.IsAbs(root) || filepath.Clean(root) != root || !validRepositoryPath(entry) ||
		read == nil || readPath == nil {
		return Source{}, ErrInvalidSource
	}

	files := make(map[string]RepositoryFile)
	entryBase := filepath.ToSlash(filepath.Dir(entry))
	queue := []repositoryDocument{{path: entry, base: entryBase, compose: true}}
	defaultEnvironment, defaultEnvironmentValid := repositoryDefaultEnvironment(entryBase, environment)
	if !defaultEnvironmentValid {
		return Source{}, ErrInvalidSource
	}
	if defaultEnvironment != "" {
		queue = append(queue, repositoryDocument{path: defaultEnvironment, optional: true})
	}
	parsed := make(map[repositoryDocument]struct{})
	runtimePathNames := make(map[string]struct{})
	totalBytes := 0
	for len(queue) != 0 {
		item := queue[0]
		queue = queue[1:]
		if _, found := parsed[item]; found {
			continue
		}
		parsed[item] = struct{}{}
		file, found := files[item.path]
		if !found {
			var readFound bool
			var err error
			file, readFound, err = read(item.path)
			if !readFound && item.optional && err == nil {
				continue
			}
			totalBytes += len(file.Content)
			if err != nil || !readFound || len(file.Content) > maxSourceBytes ||
				totalBytes > maximumRepositoryBytes ||
				item.path == entry && len(file.Content) == 0 {
				return Source{}, ErrInvalidSource
			}
			if len(files) >= maximumRepositoryFiles {
				return Source{}, ErrInvalidSource
			}
			file.Content = slices.Clone(file.Content)
			files[item.path] = file
		}
		if !item.compose {
			continue
		}
		references, runtimePaths, valid := repositoryDocumentReferences(file.Content, item.base)
		if !valid {
			return Source{}, ErrInvalidSource
		}
		queue = append(queue, references...)
		for _, path := range runtimePaths {
			runtimePathNames[path] = struct{}{}
		}
	}

	runtimePaths := make([]RepositoryPath, 0, len(runtimePathNames))
	for _, path := range slices.Sorted(maps.Keys(runtimePathNames)) {
		snapshot, err := readPath(path)
		if err != nil || len(snapshot.Files) == 0 {
			return Source{}, ErrInvalidSource
		}
		for name, file := range snapshot.Files {
			if !repositoryPathContains(path, name, snapshot.Directory) || len(file.Content) > maxSourceBytes {
				return Source{}, ErrInvalidSource
			}
			if existing, found := files[name]; found {
				if existing.Executable != file.Executable || !slices.Equal(existing.Content, file.Content) {
					return Source{}, ErrInvalidSource
				}

				continue
			}
			if len(files) >= maximumRepositoryFiles {
				return Source{}, ErrInvalidSource
			}
			totalBytes += len(file.Content)
			if totalBytes > maximumRepositoryBytes {
				return Source{}, ErrInvalidSource
			}
			file.Content = slices.Clone(file.Content)
			files[name] = file
		}
		runtimePaths = append(runtimePaths, RepositoryPath{Path: path, Directory: snapshot.Directory})
	}

	resolvedEnvironment, valid := repositoryEnvironment(environment, files[defaultEnvironment])
	if !valid {
		return Source{}, ErrInvalidSource
	}
	content := files[entry].Content

	return Source{
		Content: slices.Clone(content), WorkingDir: filepath.Join(root, filepath.FromSlash(filepath.Dir(entry))),
		Environment: resolvedEnvironment, Profiles: nil,
		Repository: &RepositorySnapshot{
			Root: root, Entry: entry, Files: files, RuntimePaths: runtimePaths,
			Digest: repositoryDigest(entry, files, runtimePaths),
		},
	}, nil
}

func repositoryDefaultEnvironment(base string, environment map[string]string) (string, bool) {
	disabled, configured := environment[composeDisableEnvFile]
	if configured {
		value, err := strconv.ParseBool(disabled)
		if err != nil {
			return "", false
		}
		if value {
			return "", true
		}
	}

	return resolveRepositoryDefaultEnv(base), true
}

func repositoryEnvironment(
	environment map[string]string,
	file RepositoryFile,
) (map[string]string, bool) {
	result := make(map[string]string, len(environment))
	maps.Copy(result, environment)
	if file.Content == nil {
		return result, true
	}
	fromFile, err := dotenv.ParseWithLookup(bytes.NewReader(file.Content), func(key string) (string, bool) {
		value, found := result[key]

		return value, found
	})
	if err != nil {
		return nil, false
	}
	for key, value := range fromFile {
		if _, found := result[key]; !found {
			result[key] = value
		}
	}

	return result, true
}

type repositoryDocument struct {
	path     string
	base     string
	compose  bool
	optional bool
}

//nolint:cyclop // One Compose document may reference each supported local source family.
func repositoryDocumentReferences(content []byte, base string) ([]repositoryDocument, []string, bool) {
	var node yaml.Node
	if err := yaml.Load(content, &node, yaml.WithUniqueKeys()); err != nil || hasUnsupportedYAML(&node) {
		return nil, nil, false
	}
	var document map[string]any
	if err := node.Load(&document, yaml.WithUniqueKeys()); err != nil {
		return nil, nil, false
	}

	var references []repositoryDocument
	var runtimePaths []string
	valid := collectResourceFiles(document["configs"], base, &references) &&
		collectResourceFiles(document["secrets"], base, &references) &&
		collectIncludes(document["include"], base, &references)
	services, servicesValid := document["services"].(map[string]any)
	if !servicesValid && document["services"] != nil {
		return nil, nil, false
	}
	for _, raw := range services {
		service, mapping := raw.(map[string]any)
		if !mapping || service["develop"] != nil ||
			!collectPathList(service["env_file"], base, &references) ||
			!collectPathList(service["label_file"], base, &references) ||
			!collectExtends(service["extends"], base, &references) ||
			!collectBindMounts(service["volumes"], base, &runtimePaths) {
			return nil, nil, false
		}
	}

	return references, runtimePaths, valid
}

func collectResourceFiles(raw any, base string, result *[]repositoryDocument) bool {
	if raw == nil {
		return true
	}
	resources, valid := raw.(map[string]any)
	if !valid {
		return false
	}
	for _, rawResource := range resources {
		resource, mapping := rawResource.(map[string]any)
		if !mapping {
			continue
		}
		if file, found := resource["file"]; found && !collectPath(file, base, false, false, result) {
			return false
		}
	}

	return true
}

//nolint:cyclop,gocognit // Compose include variants retain their distinct path bases in one audit surface.
func collectIncludes(raw any, base string, result *[]repositoryDocument) bool {
	if raw == nil {
		return true
	}
	values, valid := raw.([]any)
	if !valid || len(values) == 0 {
		return false
	}
	for _, value := range values {
		if path, valid := value.(string); valid {
			resolved, pathValid := resolveRepositoryPath(path, base)
			if !pathValid {
				return false
			}
			projectBase := filepath.ToSlash(filepath.Dir(resolved))
			*result = append(*result, repositoryDocument{
				path: resolved, base: projectBase, compose: true,
			}, repositoryDocument{
				path: resolveRepositoryDefaultEnv(projectBase), optional: true,
			})

			continue
		}
		include, valid := value.(map[string]any)
		if !valid || len(include) == 0 {
			return false
		}
		for key := range include {
			if key != repositoryPathKey && key != "env_file" && key != "project_directory" {
				return false
			}
		}
		paths, pathsValid := repositoryPaths(include[repositoryPathKey], base)
		rawEnv, hasEnv := include["env_file"]
		if !pathsValid || len(paths) == 0 || hasEnv && rawEnv == nil ||
			hasEnv && !collectPathList(rawEnv, base, result) {
			return false
		}
		projectBase := filepath.ToSlash(filepath.Dir(paths[0]))
		if rawProjectBase, found := include["project_directory"]; found {
			var projectBaseValid bool
			projectBase, projectBaseValid = resolveRepositoryPath(rawProjectBase, base)
			if !projectBaseValid {
				return false
			}
		}
		for _, path := range paths {
			*result = append(*result, repositoryDocument{path: path, base: projectBase, compose: true})
		}
		if !hasEnv {
			*result = append(*result, repositoryDocument{
				path: resolveRepositoryDefaultEnv(projectBase), optional: true,
			})
		}
	}

	return true
}

func resolveRepositoryDefaultEnv(base string) string {
	return filepath.ToSlash(filepath.Join(filepath.FromSlash(base), composeDefaultEnvFile))
}

func repositoryPaths(raw any, base string) ([]string, bool) {
	values := []any{raw}
	if list, valid := raw.([]any); valid {
		values = list
	}
	if raw == nil || len(values) == 0 {
		return nil, false
	}
	paths := make([]string, len(values))
	for index, value := range values {
		path, valid := resolveRepositoryPath(value, base)
		if !valid {
			return nil, false
		}
		paths[index] = path
	}

	return paths, true
}

func collectExtends(raw any, base string, result *[]repositoryDocument) bool {
	if raw == nil {
		return true
	}
	extends, valid := raw.(map[string]any)
	if !valid {
		return false
	}
	file, found := extends["file"]
	if !found {
		return true
	}

	return collectPath(file, base, true, false, result)
}

//nolint:cyclop // The Compose short and long env_file syntaxes share one strict validator.
func collectPathList(raw any, base string, result *[]repositoryDocument) bool {
	if raw == nil {
		return true
	}
	values := []any{raw}
	if list, valid := raw.([]any); valid {
		values = list
	}
	for _, value := range values {
		optional := false
		if mapping, valid := value.(map[string]any); valid {
			for key := range mapping {
				if key != repositoryPathKey && key != "required" && key != repositoryFormatKey {
					return false
				}
			}
			if rawRequired, found := mapping["required"]; found {
				required, valid := rawRequired.(bool)
				if !valid {
					return false
				}
				optional = !required
			}
			value = mapping[repositoryPathKey]
		}
		if !collectPath(value, base, false, optional, result) {
			return false
		}
	}

	return true
}

func collectPath(raw any, base string, compose, optional bool, result *[]repositoryDocument) bool {
	path, valid := resolveRepositoryPath(raw, base)
	if !valid {
		return false
	}
	*result = append(*result, repositoryDocument{
		path: path, base: filepath.ToSlash(filepath.Dir(path)), compose: compose, optional: optional,
	})

	return true
}

func resolveRepositoryPath(raw any, base string) (string, bool) {
	value, valid := raw.(string)
	if !valid || value == "" || strings.ContainsAny(value, "$:") || strings.HasPrefix(value, "~") ||
		filepath.IsAbs(filepath.FromSlash(value)) {
		return "", false
	}
	path := filepath.ToSlash(filepath.Clean(filepath.Join(filepath.FromSlash(base), filepath.FromSlash(value))))

	return path, validRepositoryPath(path)
}

//nolint:cyclop // Bind short and long syntax share one strict local-path collector.
func collectBindMounts(raw any, base string, result *[]string) bool {
	if raw == nil {
		return true
	}
	values, valid := raw.([]any)
	if !valid {
		return false
	}
	for _, rawVolume := range values {
		var kind, source string
		var readOnly bool
		switch value := rawVolume.(type) {
		case string:
			volume, err := composeformat.ParseVolume(value)
			if err != nil {
				return false
			}
			kind, source, readOnly = volume.Type, volume.Source, volume.ReadOnly
		case map[string]any:
			kind, _ = value["type"].(string)
			source, _ = value["source"].(string)
			readOnly, _ = value["read_only"].(bool)
		default:
			return false
		}
		if kind != composeBindMountType {
			continue
		}
		path, pathValid := resolveRepositoryPath(source, base)
		if !pathValid || path == "." || !readOnly {
			return false
		}
		*result = append(*result, path)
	}

	return true
}

func repositoryPathContains(path, name string, directory bool) bool {
	return validRepositoryPath(path) && validRepositoryPath(name) &&
		(!directory && name == path || directory && strings.HasPrefix(name, path+"/"))
}

func repositoryDigest(
	entry string,
	files map[string]RepositoryFile,
	runtimePaths []RepositoryPath,
) domain.Digest {
	encoded := []byte{2}
	encoded = binary.AppendUvarint(encoded, uint64(len(entry)))
	encoded = append(encoded, entry...)
	keys := slices.Sorted(maps.Keys(files))
	encoded = binary.AppendUvarint(encoded, uint64(len(keys)))
	for _, key := range keys {
		file := files[key]
		encoded = binary.AppendUvarint(encoded, uint64(len(key)))
		encoded = append(encoded, key...)
		encoded = append(encoded, byte(0))
		if file.Executable {
			encoded[len(encoded)-1] = 1
		}
		encoded = binary.AppendUvarint(encoded, uint64(len(file.Content)))
		encoded = append(encoded, file.Content...)
	}
	encoded = binary.AppendUvarint(encoded, uint64(len(runtimePaths)))
	for _, path := range runtimePaths {
		encoded = binary.AppendUvarint(encoded, uint64(len(path.Path)))
		encoded = append(encoded, path.Path...)
		encoded = append(encoded, byte(0))
		if path.Directory {
			encoded[len(encoded)-1] = 1
		}
	}

	return domain.Hash(encoded)
}
