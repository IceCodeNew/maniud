package cli

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
)

type diagnosticCycleError struct{}

func (cycle *diagnosticCycleError) Error() string {
	return "diagnostic cycle"
}

func (cycle *diagnosticCycleError) Unwrap() error {
	return cycle
}

type diagnosticJoinError struct {
	children []error
}

func (joined diagnosticJoinError) Error() string {
	return "diagnostic join"
}

func (joined diagnosticJoinError) Unwrap() []error {
	return joined.children
}

func TestDebugFailureIncludesRedactedCauseAndCallSites(t *testing.T) {
	t.Parallel()

	privatePath := "/private/worktree/compose.yaml"
	environmentValue := "ambient-environment-value"
	privateKey := strings.Join([]string{
		"-----BEGIN OPENSSH ", "PRIVATE KEY-----\n", "key-material\n", "-----END OPENSSH ", "PRIVATE KEY-----",
	}, "")
	cause := fmt.Errorf(
		"load %s with %s: %w",
		privatePath,
		environmentValue,
		errors.Join(
			internalDiagnosticError("Authorization: Bearer registry-token"),
			internalDiagnosticError("password=hunter2 https://user:password@example.test/image"),
			internalDiagnosticError(privateKey),
		),
	)
	output := new(bytes.Buffer)

	status := runWithEnvironment(
		context.Background(),
		[]string{debugOption, string(commandApply), composeFileValue},
		output,
		map[string]string{"PRIVATE_VALUE": environmentValue},
		nil,
		func(applyInvocation) error { return cause },
		nil,
		nil,
		nil,
		nil,
	)
	if status != 1 {
		t.Fatalf("runWithEnvironment() status = %d, want 1", status)
	}

	failure := decodePublicFailure(t, output.Bytes())
	assertDebugFailureShape(t, failure)

	assertDebugRedaction(t, output.String(), privatePath, []string{
		environmentValue, "registry-token", "hunter2", "user:password", "key-material",
	})
}

func assertDebugFailureShape(t *testing.T, failure publicFailure) {
	t.Helper()

	wantFailure := publicFailure{
		Code:      domain.ErrorApplyFailed,
		Message:   "apply validation failed",
		Retryable: false,
		Debug:     nil,
	}
	if diff := cmp.Diff(wantFailure, failure, cmpopts.IgnoreFields(publicFailure{}, "Debug")); diff != "" {
		t.Fatalf("debug failure mismatch (-want +got):\n%s", diff)
	}
	if failure.Debug == nil {
		t.Fatal("debug failure omits diagnostic")
	}
	wantShape := struct {
		HasEnoughCauses bool
		HasCallSites    bool
	}{HasEnoughCauses: true, HasCallSites: true}
	gotShape := struct {
		HasEnoughCauses bool
		HasCallSites    bool
	}{
		HasEnoughCauses: len(failure.Debug.Causes) >= 4,
		HasCallSites:    len(failure.Debug.CallSites) > 0,
	}
	if diff := cmp.Diff(wantShape, gotShape); diff != "" {
		t.Fatalf("debug diagnostic shape mismatch (-want +got):\n%s", diff)
	}
}

func decodePublicFailure(t *testing.T, encoded []byte) publicFailure {
	t.Helper()

	var failure publicFailure
	if err := json.Unmarshal(encoded, &failure); err != nil {
		t.Fatalf("decode debug failure: %v", err)
	}

	return failure
}

func assertDebugRedaction(t *testing.T, encoded, privatePath string, secrets []string) {
	t.Helper()

	for _, secret := range secrets {
		if strings.Contains(encoded, secret) {
			t.Fatalf("debug output contains %q: %s", secret, encoded)
		}
	}
	if !strings.Contains(encoded, privatePath) || !strings.Contains(encoded, diagnosticRedaction) {
		t.Fatalf("debug output omits path or redaction marker: %s", encoded)
	}
}

func TestDebugInvalidInputAndCancellation(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		cancelled  bool
		args       []string
		wantStatus int
		wantCode   domain.ErrorCode
	}{
		{
			name: "invalid input", cancelled: false, args: []string{debugOption},
			wantStatus: 1, wantCode: domain.ErrorInvalidInput,
		},
		{
			name: "cancelled", cancelled: true,
			args:       []string{debugOption, string(commandApply), composeFileValue},
			wantStatus: 130, wantCode: domain.ErrorOperationCancelled,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output := new(bytes.Buffer)
			ctx := context.Background()
			if test.cancelled {
				ctx = cancelledContext()
			}
			status := runWithEnvironment(ctx, test.args, output, nil, nil, nil, nil, nil, nil, nil)
			var failure publicFailure
			err := json.Unmarshal(output.Bytes(), &failure)
			if status != test.wantStatus || err != nil || failure.Code != test.wantCode || failure.Debug == nil {
				t.Fatalf("runWithEnvironment() = %d, %#v, %v", status, failure, err)
			}
		})
	}
}

func TestDebugUnavailableCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		invoke   func(*bytes.Buffer) int
		wantText string
	}{
		{
			name: "parsed command",
			invoke: func(output *bytes.Buffer) int {
				return runDoctor(invocation{arguments: applyInvocation{}, debug: true}, output, nil, nil)
			},
			wantText: "doctor application service is unavailable",
		},
		{
			name: "generation executor",
			invoke: func(output *bytes.Buffer) int {
				return runGen(invocation{arguments: applyInvocation{}, debug: true}, output, nil, nil)
			},
			wantText: "generation application service is unavailable",
		},
		{
			name: "apply executor",
			invoke: func(output *bytes.Buffer) int {
				return runApply(invocation{arguments: genInvocation{}, debug: true}, output, nil, nil)
			},
			wantText: "apply application service is unavailable",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output := new(bytes.Buffer)
			status := test.invoke(output)
			if status != 1 || !strings.Contains(output.String(), test.wantText) {
				t.Fatalf("debug unavailable = %d, %s", status, output.String())
			}
		})
	}
}

func TestDiagnosticCauseBoundsAndUnwrapShapes(t *testing.T) {
	t.Parallel()

	redactor := newDiagnosticRedactor(map[string]string{"SHORT": "", "LONG": "longer", "SMALL": "small"})
	causes, truncated := diagnosticCauses(&diagnosticCycleError{}, redactor)
	if !truncated || len(causes) != maximumDiagnosticCauses {
		t.Fatalf("cycle causes = %d, truncated %t", len(causes), truncated)
	}

	joined := diagnosticJoinError{children: []error{
		nil,
		internalDiagnosticError("first"),
		fmt.Errorf("wrapped: %w", internalDiagnosticError("second")),
	}}
	causes, truncated = diagnosticCauses(joined, redactor)
	if truncated || len(causes) != 4 || causes[1].Message != "first" || causes[3].Message != "second" {
		t.Fatalf("joined causes = %#v, truncated %t", causes, truncated)
	}

	causes, truncated = diagnosticCauses(nil, redactor)
	if truncated || len(causes) != 0 {
		t.Fatalf("nil causes = %#v, truncated %t", causes, truncated)
	}
}

func TestDiagnosticValueTruncatesUTF8(t *testing.T) {
	t.Parallel()

	redactor := newDiagnosticRedactor(nil)
	value, used, truncated := diagnosticValue(strings.Repeat("é", maximumDiagnosticMessageSize), redactor, 17)
	if !truncated || used != 17 || len(value) != 17 || !utf8.ValidString(value) ||
		!strings.HasSuffix(value, diagnosticEllipsis) {
		t.Fatalf("diagnosticValue() = %q, %d, %t", value, used, truncated)
	}
}

func TestDiagnosticValueSmallAndTruncationEdges(t *testing.T) {
	t.Parallel()

	value, used, truncated := diagnosticValue("small", newDiagnosticRedactor(nil), 10)
	if value != "small" || used != len(value) || truncated {
		t.Fatalf("diagnosticValue(small) = %q, %d, %t", value, used, truncated)
	}
	if got := truncateDiagnostic("value", 2); got != ".." {
		t.Fatalf("truncateDiagnostic(short) = %q", got)
	}
	if got := truncateDiagnostic("aéb", 5); got != "a…" {
		t.Fatalf("truncateDiagnostic(UTF-8 boundary) = %q", got)
	}
}

func TestDiagnosticFrameClassification(t *testing.T) {
	t.Parallel()

	if diagnosticFrame("runtime.main") || !diagnosticFrame(projectFunctionPrefix+"internal/cli.test") ||
		diagnosticFrame(projectFunctionPrefix+"internal/cli.buildPublicDiagnostic") ||
		diagnosticFrame(projectFunctionPrefix+"internal/cli.diagnosticCallSites") ||
		diagnosticFrame(projectFunctionPrefix+"internal/cli.emitCommandFailure") {
		t.Fatal("diagnosticFrame() classification is invalid")
	}
}

func TestDiagnosticCallSiteBounds(t *testing.T) {
	t.Parallel()

	callSites, truncated := deepDiagnosticCallSites(maximumDiagnosticCallSites + 4)
	if !truncated || len(callSites) != maximumDiagnosticCallSites {
		t.Fatalf("diagnosticCallSites() = %d, truncated %t", len(callSites), truncated)
	}
}

//go:noinline
func deepDiagnosticCallSites(depth int) ([]string, bool) {
	if depth == 0 {
		return diagnosticCallSites(newDiagnosticRedactor(nil))
	}

	return deepDiagnosticCallSites(depth - 1)
}

func TestEmitDebugFailureContainsOutputError(t *testing.T) {
	t.Parallel()

	status := emitCommandFailure(
		failingWriter{},
		domain.ApplyFailed(false),
		true,
		internalDiagnosticError("failure"),
		nil,
	)
	if status != 1 {
		t.Fatalf("emitCommandFailure() status = %d, want 1", status)
	}
}

func cancelledContext() context.Context {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	return ctx
}
