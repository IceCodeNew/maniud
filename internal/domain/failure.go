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
	// ErrorInternal identifies a build or programming failure.
	ErrorInternal ErrorCode = "internal_error"

	cancelledExitStatus = 130
	applyFailedMessage  = "apply validation failed"
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
