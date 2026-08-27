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
	retryableApplyFailureJSON = "{\"code\":\"apply_failed\",\"message\":\"apply validation failed\",\"retryable\":true}\n"
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

			status := Run(
				context.Background(), test.args, strings.NewReader(""), &output, io.Discard, testRuntimePlugins(t),
			)
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
	}, nil, &output, io.Discard, testRuntimePlugins(t))
	if status != 1 || !strings.Contains(output.String(), `"code":"generation_failed"`) {
		t.Fatalf("Run(runtime help) = %d, %q", status, output.String())
	}
}

func TestRunCancellation(t *testing.T) {
	t.Parallel()

	cancelled, cancel := context.WithCancel(context.Background())
	cancel()

	var output bytes.Buffer

	status := Run(
		cancelled, []string{string(commandApply), composeFileValue}, nil, &output, io.Discard, testRuntimePlugins(t),
	)
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

	status := Run(
		context.Background(), args, strings.NewReader(""), &output, io.Discard, testRuntimePlugins(t),
	)
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
				context.Background(), test.args, nil, output, io.Discard, map[string]string{}, test.getwd,
				testRuntimePlugins(t),
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
			t.Context(), arguments, nil, &output, io.Discard, map[string]string{}, os.Getwd,
			testRuntimePlugins(t),
		)
		if status != 1 {
			t.Fatalf("runProduction(%v) = %d, %q", arguments, status, output.String())
		}
	}
}

func TestRunProductionValidatesNotificationsBeforeApplyDependencies(t *testing.T) {
	t.Parallel()

	workingDirectoryRead := false
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := runProduction(
		t.Context(),
		[]string{string(commandApply), composeFileValue},
		nil,
		&stdout,
		&stderr,
		map[string]string{telegramBotTokenEnvironment: testTelegramBotToken},
		func() (string, error) {
			workingDirectoryRead = true

			return testWorkingDirectory, nil
		},
		testRuntimePlugins(t),
	)
	if status != 1 || stdout.String() != invalidInputJSON ||
		stderr.String() != incompleteTelegramConfigurationMessage+"\n" || workingDirectoryRead {
		t.Fatalf(
			"runProduction(partial Telegram) = %d, %q, %q, dependency read %t",
			status, stdout.String(), stderr.String(), workingDirectoryRead,
		)
	}
}

func TestRunProductionValidatesBarkEncryptionBeforeApplyDependencies(t *testing.T) {
	t.Parallel()

	workingDirectoryRead := false
	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := runProduction(
		t.Context(),
		[]string{string(commandApply), composeFileValue},
		nil,
		&stdout,
		&stderr,
		map[string]string{barkEncryptionKeyEnvironment: testBarkEncryptionKey},
		func() (string, error) {
			workingDirectoryRead = true

			return testWorkingDirectory, nil
		},
		testRuntimePlugins(t),
	)
	if status != 1 || stdout.String() != invalidInputJSON ||
		stderr.String() != incompleteBarkConfigurationMessage+"\n" || workingDirectoryRead {
		t.Fatalf(
			"runProduction(partial Bark) = %d, %q, %q, dependency read %t",
			status, stdout.String(), stderr.String(), workingDirectoryRead,
		)
	}
}

func TestRunProductionValidatesNotificationsBeforeDaemonStart(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := runProduction(
		t.Context(),
		[]string{daemonCommand, startCommand},
		nil,
		&stdout,
		&stderr,
		map[string]string{telegramChatIDEnvironment: testTelegramChatID},
		os.Getwd,
		testRuntimePlugins(t),
	)
	if status != 1 || stdout.String() != invalidInputJSON ||
		stderr.String() != incompleteTelegramConfigurationMessage+"\n" {
		t.Fatalf(
			"runProduction(partial Telegram daemon) = %d, %q, %q",
			status, stdout.String(), stderr.String(),
		)
	}
}

func TestRunProductionLimitsNotificationConfigurationToEventProducingCommands(t *testing.T) {
	t.Parallel()

	environment := map[string]string{
		homeKey:                     t.TempDir(),
		telegramBotTokenEnvironment: testTelegramBotToken,
	}
	tests := []struct {
		name       string
		arguments  []string
		wantStatus int
	}{
		{name: "help", arguments: []string{helpOption}, wantStatus: 0},
		{name: "daemon stop", arguments: []string{daemonCommand, stopCommand}, wantStatus: 0},
	}
	for _, test := range tests {
		var stdout bytes.Buffer
		var stderr bytes.Buffer
		status := runProduction(
			t.Context(), test.arguments, nil, &stdout, &stderr, environment, os.Getwd,
			testRuntimePlugins(t),
		)
		if status != test.wantStatus || stderr.Len() != 0 {
			t.Fatalf("runProduction(%s) = %d, stderr %q", test.name, status, stderr.String())
		}
	}
}

func TestRunProductionOwnsEnabledNotificationLifecycle(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	var stderr bytes.Buffer
	status := runProduction(
		t.Context(),
		[]string{string(commandApply), composeFileValue},
		nil,
		&stdout,
		&stderr,
		map[string]string{homeKey: t.TempDir(), barkDeviceKeyEnvironment: testBarkDeviceKey},
		func() (string, error) { return t.TempDir(), nil },
		testRuntimePlugins(t),
	)
	if status != 1 || !strings.Contains(stdout.String(), `"code":"apply_failed"`) || stderr.Len() != 0 {
		t.Fatalf("runProduction(Bark lifecycle) = %d, %q, %q", status, stdout.String(), stderr.String())
	}
}

func TestRunProductionWiresInteractiveApply(t *testing.T) {
	t.Parallel()

	var stdout bytes.Buffer
	status := runProduction(
		t.Context(),
		[]string{string(commandApply), tuiOption, composeFileValue},
		strings.NewReader("q"),
		&stdout,
		io.Discard,
		map[string]string{homeKey: t.TempDir()},
		func() (string, error) { return t.TempDir(), nil },
		testRuntimePlugins(t),
	)
	if status != 1 || !strings.Contains(stdout.String(), `"code":"apply_failed"`) {
		t.Fatalf("runProduction(TUI) = %d, %q", status, stdout.String())
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

	if status := Run(
		context.Background(), []string{helpOption}, nil, failingWriter{}, io.Discard, testRuntimePlugins(t),
	); status != 1 {
		t.Fatalf("Run(help) status = %d, want 1", status)
	}

	if status := Run(
		context.Background(), nil, nil, failingWriter{}, io.Discard, testRuntimePlugins(t),
	); status != 1 {
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
