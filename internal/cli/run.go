// Package cli owns maniud's public command grammar and output transport.
package cli

import (
	"context"
	"encoding/json"
	"io"
	"os"

	"github.com/IceCodeNew/maniud/internal/domain"
)

var version = "dev"

// Run parses and executes one command without terminating the process.
func Run(ctx context.Context, args []string, _ io.Reader, stdout, stderr io.Writer) int {
	return run(ctx, args, stdout, func(arguments applyInvocation) error {
		dependencies, err := defaultApplyDependencies(environmentMap(os.Environ()), stderr, os.Getwd)
		if err != nil {
			return err
		}

		return executeDryRun(ctx, arguments, stdout, dependencies)
	})
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	executeApply func(applyInvocation) error,
) int {
	if ctx.Err() != nil {
		return emitFailure(stdout, domain.OperationCancelled())
	}

	if help, requested := requestedHelp(args); requested {
		return writeText(stdout, help)
	}

	if len(args) == 1 && args[0] == versionOption {
		return writeText(stdout, "maniud "+version+"\n")
	}

	parsed, err := parse(args)
	if err != nil {
		return emitFailure(stdout, domain.InvalidInput())
	}

	if parsed.kind() != commandApply {
		return emitFailure(stdout, domain.CommandUnavailable())
	}

	return runApply(parsed, stdout, executeApply)
}

func runApply(parsed invocation, stdout io.Writer, execute func(applyInvocation) error) int {
	arguments, valid := parsed.arguments.(applyInvocation)
	if !valid || !arguments.dryRun || execute == nil {
		return emitFailure(stdout, domain.CommandUnavailable())
	}

	err := execute(arguments)
	if err != nil {
		return emitFailure(stdout, classifyApplyFailure(err))
	}

	return 0
}

type publicFailure struct {
	Code      domain.ErrorCode `json:"code"`
	Message   string           `json:"message"`
	Retryable bool             `json:"retryable"`
}

func emitFailure(output io.Writer, failure *domain.FailureError) int {
	encoded := publicFailure{
		Code:      failure.Code(),
		Message:   failure.Error(),
		Retryable: failure.Retryable(),
	}

	encodeErr := json.NewEncoder(output).Encode(encoded)
	if encodeErr != nil {
		return 1
	}

	return failure.ExitStatus()
}

func writeText(output io.Writer, value string) int {
	_, err := io.WriteString(output, value)
	if err != nil {
		return 1
	}

	return 0
}
