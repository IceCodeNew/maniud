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
	return runProduction(
		ctx,
		args,
		stdout,
		stderr,
		environmentMap(os.Environ()),
		os.Getwd,
	)
}

func runProduction(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	stderr io.Writer,
	environment map[string]string,
	getWorkingDirectory func() (string, error),
) int {
	return runWithEnvironment(ctx, args, stdout, environment, func(arguments genInvocation) error {
		dependencies, err := defaultGenDependencies(environment, getWorkingDirectory)
		if err != nil {
			return err
		}

		return executeGen(ctx, arguments, stdout, dependencies)
	}, func(arguments applyInvocation) error {
		dependencies, err := defaultApplyDependencies(environment, stderr, getWorkingDirectory)
		if err != nil {
			return err
		}

		return executeApply(ctx, arguments, stdout, dependencies)
	}, func(arguments gitOpsInitInvocation) error {
		return executeGitOpsInit(ctx, arguments, environment)
	}, func(arguments daemonInvocation) error {
		return executeDaemon(ctx, arguments, stdout, environment, stderr, getWorkingDirectory)
	}, func(arguments doctorInvocation) error {
		return executeDoctor(ctx, arguments, stdout, environment)
	})
}

func run(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	executeGen func(genInvocation) error,
	executeApply func(applyInvocation) error,
) int {
	return runWithEnvironment(ctx, args, stdout, nil, executeGen, executeApply, nil, nil, nil)
}

func runWithEnvironment(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	environment map[string]string,
	executeGen func(genInvocation) error,
	executeApply func(applyInvocation) error,
	executeGitOps func(gitOpsInitInvocation) error,
	executeDaemonOnce func(daemonInvocation) error,
	executeDoctorOnce func(doctorInvocation) error,
) int {
	debug := len(args) > 0 && args[0] == debugOption
	if status, handled := handleCommandPrelude(ctx, args, stdout, environment, debug); handled {
		return status
	}

	parsed, err := parse(args)
	if err != nil {
		return emitCommandFailure(stdout, domain.InvalidInput(), debug, err, environment)
	}

	return dispatchParsedCommand(
		parsed,
		stdout,
		environment,
		executeGen,
		executeApply,
		executeGitOps,
		executeDaemonOnce,
		executeDoctorOnce,
	)
}

func handleCommandPrelude(
	ctx context.Context,
	args []string,
	stdout io.Writer,
	environment map[string]string,
	debug bool,
) (int, bool) {
	if ctx.Err() != nil {
		return emitCommandFailure(stdout, domain.OperationCancelled(), debug, ctx.Err(), environment), true
	}
	if help, requested := requestedHelp(args); requested {
		return writeText(stdout, help), true
	}
	if len(args) == 1 && args[0] == versionOption {
		return writeText(stdout, "maniud "+version+"\n"), true
	}

	return 0, false
}

func dispatchParsedCommand(
	parsed invocation,
	stdout io.Writer,
	environment map[string]string,
	executeGen func(genInvocation) error,
	executeApply func(applyInvocation) error,
	executeGitOps func(gitOpsInitInvocation) error,
	executeDaemonOnce func(daemonInvocation) error,
	executeDoctorOnce func(doctorInvocation) error,
) int {
	switch parsed.kind() {
	case commandGen:
		return runGen(parsed, stdout, environment, executeGen)
	case commandApply:
		return runApply(parsed, stdout, environment, executeApply)
	case commandGitOpsInit:
		return runGitOpsInit(parsed, stdout, environment, executeGitOps)
	case commandDaemon:
		return runDaemon(parsed, stdout, environment, executeDaemonOnce)
	case commandDoctor:
		return runDoctor(parsed, stdout, environment, executeDoctorOnce)
	default:
		return emitUnavailableCommand(stdout, parsed, environment)
	}
}

func emitUnavailableCommand(stdout io.Writer, parsed invocation, environment map[string]string) int {
	return emitCommandFailure(
		stdout,
		domain.CommandUnavailable(),
		parsed.debug,
		internalDiagnosticError("parsed command has no application service"),
		environment,
	)
}

func runGen(
	parsed invocation,
	stdout io.Writer,
	environment map[string]string,
	execute func(genInvocation) error,
) int {
	return runApplicationCommand(
		parsed, stdout, environment, execute, classifyGenFailure,
		"generation application service is unavailable",
	)
}

func runApply(
	parsed invocation,
	stdout io.Writer,
	environment map[string]string,
	execute func(applyInvocation) error,
) int {
	return runApplicationCommand(
		parsed, stdout, environment, execute, classifyApplyFailure,
		"apply application service is unavailable",
	)
}

func runGitOpsInit(
	parsed invocation,
	stdout io.Writer,
	environment map[string]string,
	execute func(gitOpsInitInvocation) error,
) int {
	return runApplicationCommand(
		parsed, stdout, environment, execute, classifyGitOpsCommandFailure,
		"gitops application service is unavailable",
	)
}

func runDaemon(
	parsed invocation,
	stdout io.Writer,
	environment map[string]string,
	execute func(daemonInvocation) error,
) int {
	return runApplicationCommand(
		parsed, stdout, environment, execute, classifyDaemonCommandFailure,
		"daemon application service is unavailable",
	)
}

func runDoctor(
	parsed invocation,
	stdout io.Writer,
	environment map[string]string,
	execute func(doctorInvocation) error,
) int {
	return runApplicationCommand(
		parsed, stdout, environment, execute, classifyDoctorCommandFailure,
		"doctor application service is unavailable",
	)
}

func runApplicationCommand[T any](
	parsed invocation,
	stdout io.Writer,
	environment map[string]string,
	execute func(T) error,
	classify func(error) *domain.FailureError,
	unavailable string,
) int {
	arguments, valid := parsed.arguments.(T)
	if !valid || execute == nil {
		return emitCommandFailure(
			stdout,
			domain.CommandUnavailable(),
			parsed.debug,
			internalDiagnosticError(unavailable),
			environment,
		)
	}

	err := execute(arguments)
	if err != nil {
		return emitCommandFailure(stdout, classify(err), parsed.debug, err, environment)
	}

	return 0
}

type publicFailure struct {
	Code      domain.ErrorCode  `json:"code"`
	Message   string            `json:"message"`
	Retryable bool              `json:"retryable"`
	Debug     *publicDiagnostic `json:"debug,omitempty"`
}

func emitFailure(output io.Writer, failure *domain.FailureError) int {
	return emitCommandFailure(output, failure, false, nil, nil)
}

func emitCommandFailure(
	output io.Writer,
	failure *domain.FailureError,
	debug bool,
	cause error,
	environment map[string]string,
) int {
	encoded := publicFailure{
		Code:      failure.Code(),
		Message:   failure.Error(),
		Retryable: failure.Retryable(),
		Debug:     nil,
	}
	if debug {
		encoded.Debug = buildPublicDiagnostic(cause, environment)
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
