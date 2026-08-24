// Package nerdctl translates the supported nerdctl create and run argv
// contract to and from containerconfig.Spec without executing nerdctl.
package nerdctl

import (
	"slices"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/containerconfig/runtimeargv"
	"github.com/IceCodeNew/maniud/imageref"
)

// Command is one normalized nerdctl create or run command.
type Command struct {
	Operation        string
	Image            imageref.Source
	Spec             containerconfig.Spec
	EnvironmentFiles []string
	Warnings         []runtimeargv.Warning
}

// Clone returns an owned copy of the command.
func (command Command) Clone() Command {
	clone := command
	clone.Spec = command.Spec.Clone()
	clone.EnvironmentFiles = slices.Clone(command.EnvironmentFiles)
	clone.Warnings = slices.Clone(command.Warnings)

	return clone
}

// Parse validates a complete nerdctl create or run argv and returns its
// portable configuration. workingDirectory must be absolute.
func Parse(arguments []string, explicitName, workingDirectory string) (Command, error) {
	projection, err := runtimeargv.Parse(arguments, explicitName, workingDirectory)
	if err != nil || projection.Runtime() != runtimeargv.RuntimeNerdctl {
		return Command{}, validationError(containerconfig.ValidationInvalidDocument, "")
	}

	return Command{
		Operation: projection.Operation(), Image: projection.Source(), Spec: projection.Spec(),
		EnvironmentFiles: projection.EnvironmentFiles(), Warnings: projection.Warnings(),
	}, nil
}

// Validate checks whether command can round-trip through the supported
// nerdctl argv contract without loss.
func Validate(command Command) error {
	_, err := encode(command)

	return err
}

// Encode returns deterministic complete nerdctl argv. Execution-only options
// represented by Warnings are intentionally not recreated.
func Encode(command Command) ([]string, error) {
	arguments, err := encode(command)
	if err != nil {
		return nil, err
	}

	return slices.Clone(arguments), nil
}

func encode(command Command) ([]string, error) {
	canonical := canonicalCommand(command)
	if canonical.Spec.Healthcheck != nil && canonical.Spec.Healthcheck.StartInterval != "" {
		return nil, validationError(containerconfig.ValidationUnsupportedCapability, "/healthcheck/start_interval")
	}
	arguments := encodeCommand(canonical)
	parsed, err := Parse(arguments, canonical.Spec.ServiceName, "/")
	if err != nil || parsed.Operation != canonical.Operation || parsed.Image != canonical.Image ||
		!containerconfig.Equivalent(parsed.Spec, canonical.Spec) ||
		!slices.Equal(parsed.EnvironmentFiles, canonical.EnvironmentFiles) {
		return nil, validationError(containerconfig.ValidationInvalidValue, "")
	}

	return arguments, nil
}

func canonicalCommand(command Command) Command {
	canonical := command.Clone()
	canonical.Warnings = nil
	canonical.Spec = containerconfig.Canonical(canonical.Spec)

	return canonical
}

func validationError(code containerconfig.ValidationCode, path string) error {
	return containerconfig.ValidationError{Code: code, Path: path}
}
