package custombuild

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
)

type commandRunner func(context.Context, string, []string, string, string, ...string) ([]byte, error)

func buildEnvironment(goos, goarch string) []string {
	return replacedEnvironment(os.Environ(), []string{
		"CGO_ENABLED=0",
		"GOFLAGS=",
		"GOOS=" + goos,
		"GOARCH=" + goarch,
		"GOWORK=off",
	})
}

func replacedEnvironment(base, replacements []string) []string {
	names := make(map[string]struct{}, len(replacements))
	for _, replacement := range replacements {
		name, _, _ := strings.Cut(replacement, "=")
		names[name] = struct{}{}
	}
	result := make([]string, 0, len(base)+len(replacements))
	for _, value := range base {
		name, _, _ := strings.Cut(value, "=")
		if _, replaced := names[name]; !replaced {
			result = append(result, value)
		}
	}

	return append(result, replacements...)
}

func runCommand(
	ctx context.Context,
	directory string,
	environment []string,
	action string,
	name string,
	arguments ...string,
) ([]byte, error) {
	//nolint:gosec // Callers choose from fixed git and Go commands; no shell is involved.
	command := exec.CommandContext(ctx, name, arguments...)
	command.Dir = directory
	if environment != nil {
		command.Env = replacedEnvironment(os.Environ(), environment)
	}
	output, err := command.CombinedOutput()
	if err == nil {
		return output, nil
	}
	message := strings.TrimSpace(string(output))
	if len(message) > maximumCommandError {
		message = message[len(message)-maximumCommandError:]
	}
	if message == "" {
		return nil, fmt.Errorf("%s: %w", action, err)
	}

	return nil, fmt.Errorf("%s: %s: %w", action, message, err)
}

func verifyBuildMetadata(output, goVersion string) error {
	firstLine, _, found := strings.Cut(output, "\n")
	if !found || !strings.HasSuffix(firstLine, ": "+goVersion) ||
		!strings.Contains(output, "\tdep\t"+projectModule+"\tv0.0.0") {
		return fmt.Errorf("verify custom build provenance: %w", errDependencyMismatch)
	}

	return nil
}

func pathWithin(parent, child string) bool {
	return pathWithinWith(parent, child, filepath.Rel)
}

func pathWithinWith(parent, child string, relativePath func(string, string) (string, error)) bool {
	relative, err := relativePath(parent, child)
	if err != nil {
		return false
	}

	return relative != ".." && !strings.HasPrefix(relative, ".."+string(filepath.Separator))
}

func removeIfExists(path string) error {
	err := os.Remove(path)
	if errors.Is(err, os.ErrNotExist) {
		return nil
	}
	if err != nil {
		return fmt.Errorf("remove temporary custom build output: %w", err)
	}

	return nil
}
