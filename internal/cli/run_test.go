package cli

import (
	"bytes"
	"context"
	"errors"
	"io"
	"os"
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
	testRelativePath      = "relative"
	testServiceName       = "service"
	testNumericUser       = "1000:1001"
	testWorkingDirectory  = "/workspace"
	invalidInputJSON      = "{\"code\":\"invalid_input\",\"message\":" +
		"\"command arguments are invalid; run 'maniud --help' for supported syntax\",\"retryable\":false}\n"
	internalErrorJSON = "{\"code\":\"internal_error\",\"message\":" +
		"\"command is unavailable in this build\",\"retryable\":false}\n"
	cancelledJSON = "{\"code\":\"operation_cancelled\",\"message\":" +
		"\"operation interrupted; rerun the same command to resume\",\"retryable\":false}\n"
)

var errClosedOutput = errors.New("closed output")

type unavailableInvocation struct{}

func (unavailableInvocation) kind() command {
	return "unavailable"
}

//nolint:funlen // One table keeps the public help, version, and failure transport contract visible.
func TestRunPublicTransport(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name         string
		args         []string
		wantStatus   int
		wantOutput   string
		wantContains []string
	}{
		{
			name:         "root help",
			args:         []string{helpOption},
			wantStatus:   0,
			wantContains: []string{"Usage: maniud <command> [flags]", "gitops", "daemon", versionOption},
		},
		{
			name:         "short root help",
			args:         []string{shortHelpOption},
			wantStatus:   0,
			wantContains: []string{"Usage: maniud <command> [flags]", "gen", "apply"},
		},
		{
			name:       "version",
			args:       []string{versionOption},
			wantStatus: 0,
			wantOutput: "maniud " + currentVersion() + "\n",
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

			if test.wantOutput != "" && output.String() != test.wantOutput {
				t.Fatalf("Run() output = %q, want %q", output.String(), test.wantOutput)
			}
			for _, part := range test.wantContains {
				if !strings.Contains(output.String(), part) {
					t.Fatalf("Run() output = %q, want substring %q", output.String(), part)
				}
			}
		})
	}
}

func TestRunTreatsRuntimeHelpAsInput(t *testing.T) {
	t.Parallel()

	var output bytes.Buffer
	status := Run(context.Background(), []string{
		string(commandGen), runtimeArgumentsSeparator, testDockerRuntime, runOperation, helpOption,
	}, nil, &output, io.Discard)
	if status != 1 || !strings.Contains(output.String(), `"code":"generation_failed"`) {
		t.Fatalf("Run(runtime help) = %d, %q", status, output.String())
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
		args         []string
		wantContains []string
		wantAbsent   []string
	}{
		{
			args:         []string{string(commandGen), imageValue, helpOption},
			wantContains: []string{"Usage: maniud gen", ".prepare.sh"},
			wantAbsent:   []string{recommendedDefaultsOption},
		},
		{
			args:         []string{debugOption, string(commandApply), shortHelpOption},
			wantContains: []string{"Usage: maniud apply", "Dry run passed", jsonOption},
		},
		{
			args:         []string{daemonCommand, helpOption},
			wantContains: []string{"Usage: maniud daemon <command>", startCommand, stopCommand},
		},
		{
			args:         []string{daemonCommand, startCommand, helpOption},
			wantContains: []string{"Usage: maniud daemon start", intervalOption},
		},
		{
			args:         []string{daemonCommand, stopCommand, helpOption},
			wantContains: []string{"Usage: maniud daemon stop", "safe boundary"},
		},
		{
			args:         []string{string(commandDoctor), helpOption},
			wantContains: []string{"Usage: maniud doctor", reindexBackupsOption},
		},
		{
			args:         []string{gitOpsCommand, helpOption},
			wantContains: []string{"Usage: maniud gitops <command>", initCommand},
		},
		{
			args:         []string{gitOpsCommand, initCommand, repositoryValue, helpOption},
			wantContains: []string{"Usage: maniud gitops init", branchOption},
		},
	}

	for _, test := range tests {
		assertRunHelpPage(t, test.args, test.wantContains, test.wantAbsent)
	}
}

func assertRunHelpPage(t *testing.T, args, wantContains, wantAbsent []string) {
	t.Helper()

	var output bytes.Buffer

	status := Run(context.Background(), args, strings.NewReader(""), &output, io.Discard)
	if status != 0 {
		t.Fatalf("Run(%q) status = %d, want 0", args, status)
	}
	for _, part := range wantContains {
		if !strings.Contains(output.String(), part) {
			t.Fatalf("Run(%q) output = %q, want substring %q", args, output.String(), part)
		}
	}
	for _, part := range wantAbsent {
		if strings.Contains(output.String(), part) {
			t.Fatalf("Run(%q) output = %q, unwanted substring %q", args, output.String(), part)
		}
	}
}

func TestRunProductionBuildsGenerationDependencies(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		args     []string
		getwd    func() (string, error)
		wantCode string
	}{
		{
			name: "working directory failure",
			args: []string{string(commandGen), imageValue},
			getwd: func() (string, error) {
				return "", errClosedOutput
			},
			wantCode: `"code":"generation_failed"`,
		},
		{
			name:     "invalid untrusted argument",
			args:     []string{string(commandGen), "\x00"},
			getwd:    func() (string, error) { return testWorkingDirectory, nil },
			wantCode: `"code":"invalid_input"`,
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			output := new(bytes.Buffer)
			status := runProduction(
				context.Background(), test.args, output, io.Discard, map[string]string{}, test.getwd,
			)
			if status != 1 || !strings.Contains(output.String(), test.wantCode) {
				t.Fatalf("runProduction() = %d, %q", status, output.String())
			}
		})
	}
}

func TestRunProductionWiresLongRunningCommands(t *testing.T) {
	t.Parallel()

	commands := [][]string{
		{gitOpsCommand, initCommand, testRelativePath},
		{daemonCommand, startCommand},
		{daemonCommand, stopCommand},
		{string(commandDoctor), reindexBackupsOption},
	}
	for _, arguments := range commands {
		var output bytes.Buffer
		status := runProduction(
			t.Context(), arguments, &output, io.Discard, map[string]string{}, os.Getwd,
		)
		if status != 1 {
			t.Fatalf("runProduction(%v) = %d, %q", arguments, status, output.String())
		}
	}
}

func TestDispatchParsedCommandRoutesEveryApplicationService(t *testing.T) {
	t.Parallel()

	tests := []invocation{
		{arguments: gitOpsInitInvocation{}},
		{arguments: daemonInvocation{operation: commandDaemonStart}},
		{arguments: daemonInvocation{operation: commandDaemonStop}},
		{arguments: doctorInvocation{}},
	}
	for _, parsed := range tests {
		var output bytes.Buffer
		status := dispatchParsedCommand(
			parsed, &output, nil,
			func(genInvocation) error { return nil },
			func(applyInvocation) error { return nil },
			func(gitOpsInitInvocation) error { return nil },
			func(daemonInvocation) error { return nil },
			func(doctorInvocation) error { return nil },
		)
		if status != 0 || output.Len() != 0 {
			t.Fatalf("dispatchParsedCommand(%T) = %d, %q", parsed.arguments, status, output.String())
		}
	}

	var output bytes.Buffer
	status := dispatchParsedCommand(
		invocation{arguments: unavailableInvocation{}}, &output, nil, nil, nil, nil, nil, nil,
	)
	if status != 1 || output.String() != internalErrorJSON {
		t.Fatalf("dispatchParsedCommand(unavailable) = %d, %q", status, output.String())
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
