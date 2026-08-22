package cli

import (
	"errors"
	"io"
	"reflect"
	"slices"
	"testing"
	"time"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	applyServiceValue  = "api"
	composeFileValue   = "compose.yaml"
	extraArgumentValue = "extra"
	generatedFileValue = "service.yaml"
	imageValue         = "image"
	repositoryValue    = "repo"
	createOperation    = "create"
	runOperation       = "run"
	unknownValue       = "unknown"
	unknownOption      = "--unknown"
)

//nolint:funlen // Keeping the accepted grammar in one table makes omissions visible.
func TestParseAcceptedCommands(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		args []string
		want invocation
	}{
		{
			name: "generate",
			args: []string{
				string(commandGen), imageValue, nameOption, testServiceName, "--output=" + generatedFileValue,
				jsonOption, recommendedDefaultsOption,
			},
			want: invocation{
				arguments: genInvocation{
					source:              imageValue,
					runtimeArgs:         nil,
					name:                testServiceName,
					output:              generatedFileValue,
					json:                true,
					recommendedDefaults: true,
				},
				debug: false,
			},
		},
		{
			name: "generate from runtime arguments",
			args: []string{
				string(commandGen), outputOption, generatedFileValue, recommendedDefaultsOption,
				"--", "podman", runOperation, imageValue,
			},
			want: invocation{
				arguments: genInvocation{
					source:              "",
					runtimeArgs:         []string{"podman", runOperation, imageValue},
					name:                "",
					output:              generatedFileValue,
					recommendedDefaults: true,
				},
				debug: false,
			},
		},
		{
			name: "apply one service",
			args: []string{string(commandApply), composeFileValue, applyServiceValue, dryRunOption, jsonOption},
			want: invocation{
				arguments: applyInvocation{
					compose: composeFileValue, service: applyServiceValue, dryRun: true, json: true,
				},
				debug: false,
			},
		},
		{
			name: "apply inferred service",
			args: []string{string(commandApply), composeFileValue},
			want: invocation{
				arguments: applyInvocation{compose: composeFileValue, service: "", dryRun: false, json: false},
				debug:     false,
			},
		},
		{
			name: "gitops init",
			args: []string{gitOpsCommand, initCommand, repositoryValue, branchOption, "stable"},
			want: invocation{
				arguments: gitOpsInitInvocation{repository: repositoryValue, branch: "stable"},
				debug:     false,
			},
		},
		{
			name: "gitops default branch",
			args: []string{gitOpsCommand, initCommand, repositoryValue},
			want: invocation{
				arguments: gitOpsInitInvocation{repository: repositoryValue, branch: "master"},
				debug:     false,
			},
		},
		{
			name: "daemon start configured",
			args: []string{daemonCommand, startCommand, intervalOption, "1.5"},
			want: invocation{
				arguments: daemonInvocation{operation: commandDaemonStart, interval: 1500 * time.Millisecond},
				debug:     false,
			},
		},
		{
			name: "daemon start defaults",
			args: []string{daemonCommand, startCommand},
			want: invocation{
				arguments: daemonInvocation{operation: commandDaemonStart, interval: defaultInterval},
				debug:     false,
			},
		},
		{
			name: "daemon stop",
			args: []string{daemonCommand, stopCommand},
			want: invocation{
				arguments: daemonInvocation{operation: commandDaemonStop, interval: 0},
				debug:     false,
			},
		},
		{
			name: "doctor confirms reindex",
			args: []string{string(commandDoctor), reindexBackupsOption, confirmOption},
			want: invocation{
				arguments: doctorInvocation{
					reindexBackups: true,
					confirm:        true,
					state:          "",
				},
				debug: false,
			},
		},
		{
			name: "debug doctor report",
			args: []string{debugOption, string(commandDoctor), reindexBackupsOption, stateOption, "state.db"},
			want: invocation{
				arguments: doctorInvocation{
					reindexBackups: true,
					confirm:        false,
					state:          "state.db",
				},
				debug: true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, handled, err := parse(test.args, io.Discard)
			if err != nil {
				t.Fatalf("parse(%q) error = %v", test.args, err)
			}
			if handled {
				t.Fatalf("parse(%q) handled help or version", test.args)
			}

			if !reflect.DeepEqual(got, test.want) {
				t.Fatalf("parse(%q) = %#v, want %#v", test.args, got, test.want)
			}

			if got.kind() != test.want.kind() {
				t.Fatalf("parse(%q) kind = %q, want %q", test.args, got.kind(), test.want.kind())
			}
		})
	}
}

func TestParseRejectsInvalidCommands(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		nil,
		{"prepare"},
		{"recovery", composeFileValue},
		{string(commandGen)},
		{string(commandGen), "one", "two"},
		{string(commandGen), imageValue, "--"},
		{string(commandGen), imageValue, "--", testDockerRuntime, runOperation, imageValue},
		{string(commandGen), "--"},
		{string(commandGen), imageValue, nameOption},
		{string(commandGen), imageValue, nameOption, outputOption, generatedFileValue},
		{string(commandGen), imageValue, unknownOption, "value"},
		{string(commandApply)},
		{string(commandApply), "one", "two", "three"},
		{gitOpsCommand},
		{gitOpsCommand, unknownValue},
		{gitOpsCommand, initCommand},
		{gitOpsCommand, initCommand, repositoryValue, branchOption},
		{gitOpsCommand, initCommand, repositoryValue, unknownOption},
		{daemonCommand},
		{daemonCommand, extraArgumentValue},
		{daemonCommand, startCommand, "--once"},
		{daemonCommand, startCommand, unknownOption},
		{daemonCommand, startCommand, intervalOption, testInvalidValue},
		{daemonCommand, startCommand, intervalOption, "NaN"},
		{daemonCommand, startCommand, intervalOption, "+Inf"},
		{daemonCommand, startCommand, intervalOption, "0"},
		{daemonCommand, startCommand, intervalOption, "9223372036.854776"},
		{daemonCommand, startCommand, intervalOption, "100000000000"},
		{daemonCommand, stopCommand, extraArgumentValue},
		{daemonCommand, stopCommand, intervalOption, "1"},
		{debugOption},
		{string(commandDoctor)},
		{string(commandDoctor), reindexBackupsOption, extraArgumentValue},
		{string(commandDoctor), reindexBackupsOption, "--config", "config.toml"},
		{string(commandDoctor), reindexBackupsOption, unknownOption},
	}

	for _, args := range tests {
		_, handled, err := parse(args, io.Discard)
		if err == nil || handled {
			t.Fatalf("parse(%q) succeeded, want error", args)
		}
	}
}

func TestParseTreatsRuntimeHelpAsInput(t *testing.T) {
	t.Parallel()

	parsed, handled, err := parse(
		[]string{string(commandGen), runtimeArgumentsSeparator, testDockerRuntime, runOperation, helpOption},
		io.Discard,
	)
	if err != nil || handled {
		t.Fatalf("parse(runtime help) = %#v, %t, %v", parsed, handled, err)
	}
	arguments, ok := parsed.arguments.(genInvocation)
	if !ok || !slices.Equal(arguments.runtimeArgs, []string{testDockerRuntime, runOperation, helpOption}) {
		t.Fatalf("parse(runtime help) arguments = %#v", parsed.arguments)
	}
}

func TestParseHandlesContextualHelpAndVersion(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		{helpOption},
		{shortHelpOption},
		{versionOption},
		{debugOption, string(commandDoctor), helpOption},
		{string(commandGen), testImageSource, helpOption},
	}
	for _, args := range tests {
		parsed, handled, err := parse(args, io.Discard)
		if err != nil || !handled || parsed.arguments != nil {
			t.Fatalf("parse(%q) = %#v, %t, %v", args, parsed, handled, err)
		}
	}
}

func TestParsePreservesContainerCommandSeparatorAndHelp(t *testing.T) {
	t.Parallel()

	containerCommand := []string{runtimeArgumentsSeparator, "internal-cmd", helpOption}
	runtimeArguments := []string{
		testDockerRuntime, runOperation, "--restart", "always", "image:latest",
		containerCommand[0], containerCommand[1], containerCommand[2],
	}
	parsed, handled, err := parse(
		append([]string{string(commandGen), recommendedDefaultsOption, runtimeArgumentsSeparator}, runtimeArguments...),
		io.Discard,
	)
	if err != nil || handled {
		t.Fatalf("parse(nested separator) = %#v, %t, %v", parsed, handled, err)
	}
	arguments, ok := parsed.arguments.(genInvocation)
	if !ok {
		t.Fatalf("parse(nested separator) arguments = %T", parsed.arguments)
	}
	if !arguments.recommendedDefaults || !slices.Equal(arguments.runtimeArgs, runtimeArguments) {
		t.Fatalf("parse(nested separator) runtime arguments = %q, want %q", arguments.runtimeArgs, runtimeArguments)
	}

	projection, err := parseGenProjection(arguments, testWorkingDirectory)
	if err != nil {
		t.Fatalf("parseGenProjection() error = %v", err)
	}
	digest := domain.Hash([]byte("container command separator"))
	reference, err := projection.Source().Pin(digest)
	if err != nil {
		t.Fatalf("Projection.Source().Pin() error = %v", err)
	}
	workload, err := projection.Workload(domain.ImageIdentity{
		Origin:          domain.ImageOriginRegistry,
		Reference:       reference.String(),
		ReferenceDigest: digest,
		Platform:        projection.Platform(),
	})
	if err != nil {
		t.Fatalf("Projection.Workload() error = %v", err)
	}
	if !slices.Equal(workload.Command, containerCommand) {
		t.Fatalf("Projection.Workload().Command = %q, want %q", workload.Command, containerCommand)
	}
}

func TestCommandLineRejectsUnknownSelectedPath(t *testing.T) {
	t.Parallel()

	parsed, err := (commandLine{}).invocation("unknown", []string{})
	if !errors.Is(err, errInvalidArguments) || parsed.arguments != nil {
		t.Fatalf("commandLine.invocation(unknown) = %#v, %v", parsed, err)
	}
}
