package domain

import "testing"

//nolint:funlen // Keeping every stable public failure in one table makes contract drift visible.
func TestFailuresExposeStableOperatorContracts(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		failure    *FailureError
		code       ErrorCode
		message    string
		retryable  bool
		exitStatus int
	}{
		{
			name:       "invalid input",
			failure:    InvalidInput(),
			code:       ErrorInvalidInput,
			message:    "command arguments are invalid; run 'maniud --help' for supported syntax",
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "cancelled",
			failure:    OperationCancelled(),
			code:       ErrorOperationCancelled,
			message:    "operation interrupted; rerun the same command to resume",
			retryable:  false,
			exitStatus: 130,
		},
		{
			name:       "apply failed",
			failure:    ApplyFailed(false),
			code:       ErrorApplyFailed,
			message:    applyFailedMessage,
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "apply unavailable",
			failure:    ApplyFailed(true),
			code:       ErrorApplyFailed,
			message:    applyFailedMessage,
			retryable:  true,
			exitStatus: 1,
		},
		{
			name:       "health pending",
			failure:    HealthPending(),
			code:       ErrorHealthPending,
			message:    "workload health is pending; retry the same command to resume",
			retryable:  true,
			exitStatus: 1,
		},
		{
			name:       "health degraded",
			failure:    HealthDegraded(),
			code:       ErrorHealthDegraded,
			message:    "workload health requires a decision; run 'maniud tui' to review it",
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "generation failed",
			failure:    GenerationFailed(false),
			code:       ErrorGenerationFailed,
			message:    generationFailedMessage,
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "generation unavailable",
			failure:    GenerationFailed(true),
			code:       ErrorGenerationFailed,
			message:    generationFailedMessage,
			retryable:  true,
			exitStatus: 1,
		},
		{
			name:       "runtime not built",
			failure:    RuntimeNotBuilt(),
			code:       ErrorRuntimeNotBuilt,
			message:    "selected container runtime is not included in this build",
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "terminal input unavailable",
			failure:    TUIInputUnavailable(),
			code:       ErrorTUIUnavailable,
			message:    "interactive TUI requires terminal input; " + tuiFallbackMessage,
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "terminal output unavailable",
			failure:    TUIOutputUnavailable(),
			code:       ErrorTUIUnavailable,
			message:    "interactive TUI requires terminal output; " + tuiFallbackMessage,
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "interactive TERM unavailable",
			failure:    TUITermUnavailable(),
			code:       ErrorTUIUnavailable,
			message:    "interactive TUI requires an interactive TERM; " + tuiFallbackMessage,
			retryable:  false,
			exitStatus: 1,
		},
		{
			name:       "TUI export failed",
			failure:    TUIExportFailed(),
			code:       ErrorTUIExportFailed,
			message:    "TUI session export could not be written; rerun 'maniud tui' to export again",
			retryable:  true,
			exitStatus: 1,
		},
		{
			name:       "unavailable",
			failure:    CommandUnavailable(),
			code:       ErrorInternal,
			message:    "command is unavailable in this build",
			retryable:  false,
			exitStatus: 1,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			if test.failure.Code() != test.code {
				t.Fatalf("Code() = %q, want %q", test.failure.Code(), test.code)
			}

			if test.failure.Error() != test.message {
				t.Fatalf("Error() = %q, want %q", test.failure.Error(), test.message)
			}

			if test.failure.Retryable() != test.retryable {
				t.Fatalf("Retryable() = %t, want %t", test.failure.Retryable(), test.retryable)
			}

			if test.failure.ExitStatus() != test.exitStatus {
				t.Fatalf("ExitStatus() = %d, want %d", test.failure.ExitStatus(), test.exitStatus)
			}
		})
	}
}
