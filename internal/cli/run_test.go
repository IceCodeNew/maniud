package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testArchitectureAMD64 = "amd64"
	testDockerRuntime     = "docker"
	testImageSource       = "image:1"
	testInvalidValue      = "invalid"
	testPlatformAMD64     = "linux/amd64"
	testRegistrySource    = "example.invalid/api:1"
	testServiceName       = "service"
	testWorkingDirectory  = "/workspace"
	invalidInputJSON      = "{\"code\":\"invalid_input\",\"message\":" +
		"\"command arguments are invalid; run 'maniud --help' for supported syntax\",\"retryable\":false}\n"
	internalErrorJSON = "{\"code\":\"internal_error\",\"message\":" +
		"\"command is unavailable in this build\",\"retryable\":false}\n"
	cancelledJSON = "{\"code\":\"operation_cancelled\",\"message\":" +
		"\"operation interrupted; rerun the same command to resume\",\"retryable\":false}\n"
)

var errClosedOutput = errors.New("closed output")

func TestRunPublicTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name       string
		args       []string
		wantStatus int
		wantOutput string
	}{
		{
			name:       "root help",
			args:       []string{helpOption},
			wantStatus: 0,
			wantOutput: rootHelp,
		},
		{
			name:       "short root help",
			args:       []string{shortHelpOption},
			wantStatus: 0,
			wantOutput: rootHelp,
		},
		{
			name:       "version",
			args:       []string{versionOption},
			wantStatus: 0,
			wantOutput: "maniud dev\n",
		},
		{
			name:       testInvalidValue,
			args:       []string{"prepare"},
			wantStatus: 1,
			wantOutput: invalidInputJSON,
		},
		{
			name:       "missing apply source",
			args:       []string{string(commandApply), composeFileValue},
			wantStatus: 1,
			wantOutput: "{\"code\":\"apply_failed\",\"message\":" +
				"\"apply validation failed\",\"retryable\":false}\n",
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			var output bytes.Buffer

			status := Run(context.Background(), test.args, strings.NewReader(""), &output, io.Discard)
			if status != test.wantStatus {
				t.Fatalf("Run() status = %d, want %d", status, test.wantStatus)
			}

			if output.String() != test.wantOutput {
				t.Fatalf("Run() output = %q, want %q", output.String(), test.wantOutput)
			}
		})
	}
}

func TestRunCancellation(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	var output bytes.Buffer

	status := Run(cancelled, []string{string(commandApply), composeFileValue}, nil, &output, io.Discard)
	if status != 130 {
		t.Fatalf("Run() status = %d, want 130", status)
	}

	if output.String() != cancelledJSON {
		t.Fatalf("Run() output = %q, want %q", output.String(), cancelledJSON)
	}
}

func TestRunHelpPages(t *testing.T) {
	t.Parallel()

	tests := []struct {
		args []string
		want string
	}{
		{args: []string{string(commandGen), imageValue, helpOption}, want: genHelp},
		{args: []string{debugOption, string(commandApply), shortHelpOption}, want: applyHelp},
		{args: []string{string(commandDaemon), helpOption}, want: daemonHelp},
		{args: []string{string(commandDoctor), helpOption}, want: doctorHelp},
		{args: []string{gitOpsCommand, helpOption}, want: gitopsHelp},
		{args: []string{gitOpsCommand, initCommand, repositoryValue, helpOption}, want: gitopsInitHelp},
	}

	for _, test := range tests {
		var output bytes.Buffer

		status := Run(context.Background(), test.args, strings.NewReader(""), &output, io.Discard)
		if status != 0 {
			t.Fatalf("Run(%q) status = %d, want 0", test.args, status)
		}

		if output.String() != test.want {
			t.Fatalf("Run(%q) output = %q, want %q", test.args, output.String(), test.want)
		}
	}
}

func TestRunProductionBuildsGenerationDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		args  []string
		getwd func() (string, error)
	}{
		{
			name: "working directory failure",
			args: []string{string(commandGen), imageValue},
			getwd: func() (string, error) {
				return "", errClosedOutput
			},
		},
		{
			name:  "generation failure",
			args:  []string{string(commandGen), "\x00"},
			getwd: func() (string, error) { return testWorkingDirectory, nil },
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output := new(bytes.Buffer)
			status := runProduction(
				context.Background(), test.args, output, io.Discard, map[string]string{}, test.getwd,
			)
			if status != 1 || !strings.Contains(output.String(), `"code":"generation_failed"`) {
				t.Fatalf("runProduction() = %d, %q", status, output.String())
			}
		})
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errClosedOutput
}

func TestRunContainsOutputFailures(t *testing.T) {
	t.Parallel()

	if status := Run(context.Background(), []string{helpOption}, nil, failingWriter{}, io.Discard); status != 1 {
		t.Fatalf("Run(help) status = %d, want 1", status)
	}

	if status := Run(context.Background(), nil, nil, failingWriter{}, io.Discard); status != 1 {
		t.Fatalf("Run(error) status = %d, want 1", status)
	}
}

func TestPublicFailureUsesDomainContract(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer

	status := emitFailure(&output, domain.InvalidInput())
	if status != 1 {
		t.Fatalf("emitFailure() status = %d, want 1", status)
	}

	if !strings.Contains(output.String(), `"code":"invalid_input"`) {
		t.Fatalf("emitFailure() output = %q", output.String())
	}
}
