package custombuild

import (
	"fmt"
	"slices"
	"strings"
)

const (
	dockerRuntime     = "docker"
	podmanRuntime     = "podman"
	containerdRuntime = "containerd"
)

type runtimeDefinition struct {
	name   string
	alias  string
	plugin string
}

func runtimeDefinitions() []runtimeDefinition {
	return []runtimeDefinition{
		{
			name: dockerRuntime, alias: "dockerplugin",
			plugin: projectModule + "/plugins/runtime/docker",
		},
		{
			name: podmanRuntime, alias: "podmanplugin",
			plugin: projectModule + "/plugins/runtime/podman",
		},
		{
			name: containerdRuntime, alias: "containerdplugin",
			plugin: projectModule + "/plugins/runtime/containerd",
		},
	}
}

func resolveRuntimes(values []string, disableDefaults bool) ([]string, error) {
	definitions := runtimeDefinitions()
	if len(values) == 0 {
		if disableDefaults {
			return []string{}, nil
		}
		selected := make([]string, 0, len(definitions))
		for _, definition := range definitions {
			selected = append(selected, definition.name)
		}

		return selected, nil
	}

	requested := make(map[string]struct{}, len(values))
	for _, value := range values {
		_, known := runtimeByName(value)
		if !known {
			return nil, fmt.Errorf(
				"unknown runtime %q; choose docker, podman, or containerd: %w",
				value,
				errInvalidConfiguration,
			)
		}
		if _, duplicate := requested[value]; duplicate {
			return nil, fmt.Errorf(
				"runtime %q was provided more than once; remove the duplicate --runtime flag: %w",
				value,
				errInvalidConfiguration,
			)
		}
		requested[value] = struct{}{}
	}
	selected := make([]string, 0, len(requested))
	for _, definition := range definitions {
		if _, present := requested[definition.name]; present {
			selected = append(selected, definition.name)
		}
	}

	return selected, nil
}

func runtimeByName(name string) (runtimeDefinition, bool) {
	for _, definition := range runtimeDefinitions() {
		if definition.name == name {
			return definition, true
		}
	}

	return runtimeDefinition{}, false
}

func renderMain(runtimes []string) []byte {
	var source strings.Builder
	source.WriteString("package main\n\n")
	source.WriteString("import (\n\t\"context\"\n\t\"fmt\"\n\t\"os\"\n\n")
	source.WriteString("\t\"" + projectModule + "/internal/cli\"\n")
	source.WriteString("\truntimeplugin \"" + projectModule + "/plugins/runtime\"\n")
	for _, name := range runtimes {
		definition, _ := runtimeByName(name)
		source.WriteString("\t" + definition.alias + " \"" + definition.plugin + "\"\n")
	}
	source.WriteString(")\n\n")
	source.WriteString("func main() {\n\tos.Exit(run())\n}\n\n")
	source.WriteString("func run() int {\n")
	source.WriteString("\tctx, stop := cli.CommandContext(context.Background())\n\tdefer stop()\n")
	source.WriteString("\truntimes, err := runtimeplugin.NewSet(\n")
	for _, name := range runtimes {
		definition, _ := runtimeByName(name)
		source.WriteString("\t\t" + definition.alias + ".New(),\n")
	}
	source.WriteString("\t)\n\tif err != nil {\n")
	source.WriteString("\t\t_, _ = fmt.Fprintln(os.Stderr, \"maniud:\", err)\n\n\t\treturn 1\n\t}\n")
	source.WriteString(
		"\treturn cli.Run(ctx, os.Args[1:], os.Stdin, os.Stdout, os.Stderr, runtimes)\n}\n",
	)

	return []byte(source.String())
}

func verifyRuntimeDependencies(output string, selected []string) error {
	dependencies := strings.Fields(output)
	if !slices.Contains(dependencies, projectModule+"/plugins/runtime") {
		return fmt.Errorf("verify runtime plugin core: %w", errDependencyMismatch)
	}
	for _, definition := range runtimeDefinitions() {
		want := slices.Contains(selected, definition.name)
		pluginPresent := slices.Contains(dependencies, definition.plugin)
		if pluginPresent != want {
			return fmt.Errorf("verify %s runtime dependency: %w", definition.name, errDependencyMismatch)
		}
	}

	return nil
}
