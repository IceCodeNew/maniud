// Package domain defines runtime-neutral values and operator failures.
package domain

// ErrorCode identifies an operator-visible failure without exposing its cause.
type ErrorCode string

const (
	// ErrorInvalidInput identifies argv outside the public grammar.
	ErrorInvalidInput ErrorCode = "invalid_input"
	// ErrorOperationCancelled identifies an operator cancellation.
	ErrorOperationCancelled ErrorCode = "operation_cancelled"
	// ErrorApplyFailed identifies an apply whose validation could not complete.
	ErrorApplyFailed ErrorCode = "apply_failed"
	// ErrorGenerationFailed identifies generation that could not complete.
	ErrorGenerationFailed ErrorCode = "generation_failed"
	// ErrorRuntimeNotBuilt identifies a selected runtime omitted from the binary.
	ErrorRuntimeNotBuilt ErrorCode = "runtime_not_built"
	// ErrorTUIUnavailable identifies a missing interactive terminal capability.
	ErrorTUIUnavailable ErrorCode = "tui_unavailable"
	// ErrorTUIExportFailed identifies a session export that stdout could not accept.
	ErrorTUIExportFailed ErrorCode = "export_failed"
	// ErrorInternal identifies a build or programming failure.
	ErrorInternal ErrorCode = "internal_error"

	cancelledExitStatus     = 130
	applyFailedMessage      = "apply validation failed"
	generationFailedMessage = "Compose generation failed"
	tuiFallbackMessage      = "use 'maniud apply --dry-run <compose>' or " +
		"'maniud apply --dry-run --json <compose>'"
)

// FailureError is an expected, privacy-safe error at the application boundary.
type FailureError struct {
	code       ErrorCode
	message    string
	retryable  bool
	exitStatus int
}

// InvalidInput reports argv that does not match the public grammar.
func InvalidInput() *FailureError {
	return &FailureError{
		code:       ErrorInvalidInput,
		message:    "command arguments are invalid; run 'maniud --help' for supported syntax",
		retryable:  false,
		exitStatus: 1,
	}
}

// OperationCancelled reports an operator cancellation after durable cleanup.
func OperationCancelled() *FailureError {
	return &FailureError{
		code:       ErrorOperationCancelled,
		message:    "operation interrupted; rerun the same command to resume",
		retryable:  false,
		exitStatus: cancelledExitStatus,
	}
}

// ApplyFailed reports a privacy-safe validation failure. Retryable distinguishes
// temporary runtime, registry, and state availability from rejected evidence.
func ApplyFailed(retryable bool) *FailureError {
	return &FailureError{
		code:       ErrorApplyFailed,
		message:    applyFailedMessage,
		retryable:  retryable,
		exitStatus: 1,
	}
}

// GenerationFailed reports a privacy-safe generation failure. Retryable
// distinguishes temporary registry availability from rejected input.
func GenerationFailed(retryable bool) *FailureError {
	return &FailureError{
		code:       ErrorGenerationFailed,
		message:    generationFailedMessage,
		retryable:  retryable,
		exitStatus: 1,
	}
}

// RuntimeNotBuilt reports a selected runtime capability omitted at build time.
func RuntimeNotBuilt() *FailureError {
	return &FailureError{
		code:       ErrorRuntimeNotBuilt,
		message:    "selected container runtime is not included in this build",
		retryable:  false,
		exitStatus: 1,
	}
}

// TUIInputUnavailable reports that stdin cannot support an interactive session.
func TUIInputUnavailable() *FailureError {
	return tuiUnavailable("interactive TUI requires terminal input")
}

// TUIOutputUnavailable reports that stdout cannot support an interactive session.
func TUIOutputUnavailable() *FailureError {
	return tuiUnavailable("interactive TUI requires terminal output")
}

// TUITermUnavailable reports that TERM explicitly disables terminal interaction.
func TUITermUnavailable() *FailureError {
	return tuiUnavailable("interactive TUI requires an interactive TERM")
}

// TUIExportFailed reports that a terminal-restored session export could not be written.
func TUIExportFailed() *FailureError {
	return &FailureError{
		code:       ErrorTUIExportFailed,
		message:    "TUI session export could not be written; rerun 'maniud tui' to export again",
		retryable:  true,
		exitStatus: 1,
	}
}

func tuiUnavailable(requirement string) *FailureError {
	return &FailureError{
		code:       ErrorTUIUnavailable,
		message:    requirement + "; " + tuiFallbackMessage,
		retryable:  false,
		exitStatus: 1,
	}
}

// CommandUnavailable reports a command whose application service is not in this build.
func CommandUnavailable() *FailureError {
	return &FailureError{
		code:       ErrorInternal,
		message:    "command is unavailable in this build",
		retryable:  false,
		exitStatus: 1,
	}
}

// Error implements error without exposing internal causes.
func (failure *FailureError) Error() string {
	return failure.message
}

// Code returns the stable operator-facing code.
func (failure *FailureError) Code() ErrorCode {
	return failure.code
}

// Retryable reports whether retrying unchanged input may succeed.
func (failure *FailureError) Retryable() bool {
	return failure.retryable
}

// ExitStatus returns the process status for the failure.
func (failure *FailureError) ExitStatus() int {
	return failure.exitStatus
}
