package compose

import (
	"bytes"
	"context"
	"fmt"
	"reflect"
	"slices"
	"strconv"
	"strings"

	"go.yaml.in/yaml/v4"

	"github.com/IceCodeNew/maniud/containerconfig"
)

const composeYAMLIndent = 2

const (
	deploymentFieldCount   = 14
	deploymentHealthcheck  = "healthcheck"
	deploymentMappingWidth = 2
	yamlStringTag          = "!!str"
)

// PatchServiceFields rewrites only the named portable fields in an entry-local
// Compose service. It rejects unsafe YAML nodes and verifies the complete
// normalized service after preserving every unselected YAML semantic value.
//
//nolint:cyclop // The stages are independent fail-closed source proofs.
func (source Source) PatchServiceFields(
	ctx context.Context,
	service string,
	expected containerconfig.Spec,
	fields []string,
) (Source, error) {
	if err := ctx.Err(); err != nil {
		return Source{}, fmt.Errorf("patch Compose service: %w", err)
	}
	if service == "" || expected.ServiceName != service || len(fields) == 0 || len(fields) > deploymentFieldCount {
		return Source{}, ErrInvalidSource
	}

	var document yaml.Node
	if err := yaml.Load(source.Content, &document, yaml.WithUniqueKeys()); err != nil {
		return Source{}, ErrInvalidSource
	}
	serviceNode, err := entryLocalServiceNode(&document, service)
	if err != nil {
		return Source{}, err
	}

	paths := make([][]string, 0, len(fields))
	seen := make(map[string]struct{}, len(fields))
	for _, field := range fields {
		if _, duplicate := seen[field]; duplicate {
			return Source{}, ErrInvalidSource
		}
		seen[field] = struct{}{}
		path, mutationErr := mutateDeploymentNode(serviceNode, service, expected, field)
		if mutationErr != nil {
			return Source{}, mutationErr
		}
		paths = append(paths, path)
	}

	rendered, err := yaml.Dump(&document, yaml.WithIndent(composeYAMLIndent))
	if err != nil || bytes.Equal(rendered, source.Content) ||
		!sameUntouchedYAMLSemantics(source.Content, rendered, paths) {
		return Source{}, ErrInvalidSource
	}
	candidate, err := source.WithEntryContent(rendered)
	if err != nil {
		return Source{}, err
	}
	project, err := Load(ctx, candidate)
	if err != nil {
		if ctxErr := ctx.Err(); ctxErr != nil {
			return Source{}, fmt.Errorf("patch Compose service: %w", ctxErr)
		}

		return Source{}, ErrInvalidSource
	}
	actual, err := project.ServiceSpec(service)
	if err != nil || !containerconfig.Equivalent(expected, actual) {
		return Source{}, ErrInvalidSource
	}

	return candidate, nil
}

// WithSemanticallyEquivalentEntryContent replaces the entry bytes only when
// both YAML documents represent the same values. Git attribute normalization
// may change encoding details after the typed deployment patch is rendered.
func (source Source) WithSemanticallyEquivalentEntryContent(content []byte) (Source, error) {
	if !sameUntouchedYAMLSemantics(source.Content, content, nil) {
		return Source{}, ErrInvalidSource
	}

	return source.WithEntryContent(content)
}

//nolint:cyclop // Each structural check rejects one unsafe entry-document shape.
func entryLocalServiceNode(document *yaml.Node, service string) (*yaml.Node, error) {
	if document == nil || document.Kind != yaml.DocumentNode || len(document.Content) != 1 {
		return nil, ErrInvalidSource
	}
	root := document.Content[0]
	if !safeDeploymentMapping(root) {
		return nil, ErrInvalidSource
	}
	services, found, valid := deploymentMappingValue(root, "services")
	if !valid || !found || !safeDeploymentMapping(services) {
		return nil, ErrInvalidSource
	}
	selected, found, valid := deploymentMappingValue(services, service)
	if !valid || !found || !safeDeploymentMapping(selected) {
		return nil, ErrInvalidSource
	}

	return selected, nil
}

func safeDeploymentMapping(node *yaml.Node) bool {
	return node != nil && node.Kind == yaml.MappingNode && node.Style&yaml.TaggedStyle == 0 && node.Anchor == ""
}

func deploymentMappingValue(mapping *yaml.Node, key string) (*yaml.Node, bool, bool) {
	if mapping == nil || mapping.Kind != yaml.MappingNode || len(mapping.Content)%deploymentMappingWidth != 0 {
		return nil, false, false
	}
	for index := 0; index < len(mapping.Content); index += deploymentMappingWidth {
		name := mapping.Content[index]
		if name.Kind != yaml.ScalarNode {
			return nil, false, false
		}
		if name.Value == key {
			return mapping.Content[index+1], true, true
		}
	}

	return nil, false, true
}

//nolint:cyclop // Direct and nested fields share one auditable node mutation boundary.
func mutateDeploymentNode(
	serviceNode *yaml.Node,
	service string,
	expected containerconfig.Spec,
	field string,
) ([]string, error) {
	key, nested, valid := deploymentYAMLPath(field)
	value, present, valueValid := deploymentYAMLValue(expected, field)
	if !valid || !valueValid {
		return nil, ErrInvalidSource
	}
	path := []string{"services", service, key}
	owner := serviceNode
	if nested != "" {
		path = append(path, nested)
		healthcheck, found, mappingValid := deploymentMappingValue(serviceNode, key)
		if !mappingValid || !found || !safeDeploymentMapping(healthcheck) {
			return nil, ErrInvalidSource
		}
		owner = healthcheck
		key = nested
	}

	current, found, mappingValid := deploymentMappingValue(owner, key)
	if !mappingValid || found && !safeDeploymentTarget(current) {
		return nil, ErrInvalidSource
	}
	if !present {
		if found {
			removeDeploymentMappingValue(owner, key)
		}

		return path, nil
	}
	if found {
		copyDeploymentComments(value, current)
	}
	setDeploymentMappingValue(owner, key, value)

	return path, nil
}

func safeDeploymentTarget(node *yaml.Node) bool {
	if node == nil || node.Kind == yaml.AliasNode || node.Style&yaml.TaggedStyle != 0 || node.Anchor != "" {
		return false
	}
	if node.Kind == yaml.ScalarNode && strings.ContainsRune(node.Value, '$') {
		return false
	}
	for _, child := range node.Content {
		if !safeDeploymentTarget(child) {
			return false
		}
	}

	return true
}

func deploymentYAMLPath(field string) (string, string, bool) {
	switch field {
	case "cpus", "mem_limit", "pids_limit", "restart", "shm_size", "stop_grace_period", "init", "read_only":
		return field, "", true
	case "no_new_privileges":
		return "security_opt", "", true
	case "healthcheck.interval":
		return deploymentHealthcheck, "interval", true
	case "healthcheck.timeout":
		return deploymentHealthcheck, "timeout", true
	case "healthcheck.retries":
		return deploymentHealthcheck, "retries", true
	case "healthcheck.start_period":
		return deploymentHealthcheck, "start_period", true
	case "healthcheck.start_interval":
		return deploymentHealthcheck, "start_interval", true
	default:
		return "", "", false
	}
}

//nolint:cyclop // Every closed field has a distinct Spec representation.
func deploymentYAMLValue(spec containerconfig.Spec, field string) (*yaml.Node, bool, bool) {
	switch field {
	case "cpus":
		return deploymentScalar("!!float", spec.CPUs), spec.CPUs != "", true
	case "mem_limit":
		return deploymentInteger(spec.MemoryBytes), spec.MemoryBytes != 0, true
	case "pids_limit":
		return deploymentOptionalInteger(spec.PidsLimit)
	case "restart":
		return deploymentScalar(yamlStringTag, spec.Restart), spec.Restart != "", true
	case "shm_size":
		return deploymentInteger(spec.SharedMemoryBytes), spec.SharedMemoryBytes != 0, true
	case "stop_grace_period":
		if spec.StopTimeout == nil {
			return nil, false, true
		}

		return deploymentScalar(yamlStringTag, strconv.FormatInt(*spec.StopTimeout, 10)+"s"), true, true
	case "init":
		return deploymentOptionalBoolean(spec.Init)
	case "read_only":
		return deploymentOptionalBoolean(spec.ReadOnly)
	case "no_new_privileges":
		if !spec.NoNewPrivileges {
			return nil, false, false
		}

		return &yaml.Node{Kind: yaml.SequenceNode, Tag: "!!seq", Content: []*yaml.Node{{
			Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: "no-new-privileges:true",
		}}}, true, true
	case "healthcheck.interval":
		return deploymentHealthValue(spec.Healthcheck, func(value *containerconfig.Healthcheck) string {
			return value.Interval
		})
	case "healthcheck.timeout":
		return deploymentHealthValue(spec.Healthcheck, func(value *containerconfig.Healthcheck) string {
			return value.Timeout
		})
	case "healthcheck.retries":
		if spec.Healthcheck == nil || spec.Healthcheck.Disabled {
			return nil, false, false
		}

		return deploymentOptionalInteger(spec.Healthcheck.Retries)
	case "healthcheck.start_period":
		return deploymentHealthValue(spec.Healthcheck, func(value *containerconfig.Healthcheck) string {
			return value.StartPeriod
		})
	case "healthcheck.start_interval":
		return deploymentHealthValue(spec.Healthcheck, func(value *containerconfig.Healthcheck) string {
			return value.StartInterval
		})
	default:
		return nil, false, false
	}
}

func deploymentScalar(tag, value string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: tag, Value: value}
}

func deploymentInteger[T ~int | ~int64](value T) *yaml.Node {
	return deploymentScalar("!!int", strconv.FormatInt(int64(value), 10))
}

func deploymentOptionalInteger[T ~int | ~int64](value *T) (*yaml.Node, bool, bool) {
	if value == nil {
		return nil, false, true
	}

	return deploymentInteger(*value), true, true
}

func deploymentOptionalBoolean(value *bool) (*yaml.Node, bool, bool) {
	if value == nil {
		return nil, false, true
	}

	return deploymentScalar("!!bool", strconv.FormatBool(*value)), true, true
}

func deploymentHealthValue(
	healthcheck *containerconfig.Healthcheck,
	selected func(*containerconfig.Healthcheck) string,
) (*yaml.Node, bool, bool) {
	if healthcheck == nil || healthcheck.Disabled {
		return nil, false, false
	}
	value := selected(healthcheck)

	return deploymentScalar(yamlStringTag, value), value != "", true
}

func setDeploymentMappingValue(mapping *yaml.Node, key string, value *yaml.Node) {
	for index := 0; index < len(mapping.Content); index += deploymentMappingWidth {
		if mapping.Content[index].Value == key {
			mapping.Content[index+1] = value

			return
		}
	}
	mapping.Content = append(mapping.Content,
		&yaml.Node{Kind: yaml.ScalarNode, Tag: yamlStringTag, Value: key},
		value,
	)
}

func removeDeploymentMappingValue(mapping *yaml.Node, key string) {
	for index := 0; index < len(mapping.Content); index += deploymentMappingWidth {
		if mapping.Content[index].Value == key {
			mapping.Content = slices.Delete(mapping.Content, index, index+deploymentMappingWidth)

			return
		}
	}
}

func copyDeploymentComments(destination, source *yaml.Node) {
	destination.HeadComment = source.HeadComment
	destination.LineComment = source.LineComment
	destination.FootComment = source.FootComment
}

func sameUntouchedYAMLSemantics(before, after []byte, paths [][]string) bool {
	left, valid := deploymentSemanticTree(before)
	if !valid {
		return false
	}
	right, valid := deploymentSemanticTree(after)
	if !valid {
		return false
	}
	for _, path := range paths {
		removeDeploymentSemanticPath(left, path)
		removeDeploymentSemanticPath(right, path)
	}

	return reflect.DeepEqual(left, right)
}

func deploymentSemanticTree(content []byte) (map[string]any, bool) {
	var document yaml.Node
	if err := yaml.Load(content, &document, yaml.WithUniqueKeys()); err != nil {
		return nil, false
	}
	result := make(map[string]any)
	if err := document.Load(&result, yaml.WithUniqueKeys()); err != nil {
		return nil, false
	}

	return result, true
}

func removeDeploymentSemanticPath(tree map[string]any, path []string) {
	if len(path) == 0 {
		return
	}
	if len(path) == 1 {
		delete(tree, path[0])

		return
	}
	child, valid := tree[path[0]].(map[string]any)
	if !valid {
		return
	}
	removeDeploymentSemanticPath(child, path[1:])
	if len(child) == 0 {
		delete(tree, path[0])
	}
}
