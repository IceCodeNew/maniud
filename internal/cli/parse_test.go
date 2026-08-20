package cli

import (
	"reflect"
	"testing"
	"time"
)

const (
	composeFileValue   = "compose.yaml"
	generatedFileValue = "service.yaml"
	imageValue         = "image"
	repositoryValue    = "repo"
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
			args: []string{string(commandGen), imageValue, nameOption, "service", "--output=" + generatedFileValue},
			want: invocation{
				arguments: genInvocation{
					source:      imageValue,
					runtimeArgs: nil,
					name:        "service",
					output:      generatedFileValue,
				},
				debug: false,
			},
		},
		{
			name: "generate from runtime arguments",
			args: []string{string(commandGen), outputOption, generatedFileValue, "--", "podman", runOperation, imageValue},
			want: invocation{
				arguments: genInvocation{
					source:      "",
					runtimeArgs: []string{"podman", runOperation, imageValue},
					name:        "",
					output:      generatedFileValue,
				},
				debug: false,
			},
		},
		{
			name: "apply one service",
			args: []string{string(commandApply), composeFileValue, "api", dryRunOption},
			want: invocation{
				arguments: applyInvocation{compose: composeFileValue, service: "api", dryRun: true},
				debug:     false,
			},
		},
		{
			name: "apply inferred service",
			args: []string{string(commandApply), composeFileValue},
			want: invocation{
				arguments: applyInvocation{compose: composeFileValue, service: "", dryRun: false},
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
				arguments: gitOpsInitInvocation{repository: repositoryValue, branch: "main"},
				debug:     false,
			},
		},
		{
			name: "daemon configured",
			args: []string{string(commandDaemon), "--once", intervalOption, "1.5"},
			want: invocation{
				arguments: daemonInvocation{once: true, interval: 1500 * time.Millisecond},
				debug:     false,
			},
		},
		{
			name: "daemon defaults",
			args: []string{string(commandDaemon)},
			want: invocation{
				arguments: daemonInvocation{once: false, interval: defaultInterval},
				debug:     false,
			},
		},
		{
			name: "doctor confirms reindex",
			args: []string{string(commandDoctor), configOption + "=config.toml", reindexBackupsOption, confirmOption},
			want: invocation{
				arguments: doctorInvocation{
					reindexBackups: true,
					confirm:        true,
					config:         "config.toml",
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
					config:         "",
					state:          "state.db",
				},
				debug: true,
			},
		},
	}

	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			got, err := parse(test.args)
			if err != nil {
				t.Fatalf("parse(%q) error = %v", test.args, err)
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
		{string(commandGen), imageValue, "--", "docker", runOperation, imageValue},
		{string(commandGen), "--"},
		{string(commandGen), imageValue, nameOption},
		{string(commandGen), imageValue, unknownOption, "value"},
		{string(commandApply)},
		{string(commandApply), "one", "two", "three"},
		{string(commandApply), composeFileValue, dryRunOption + "=true"},
		{gitOpsCommand},
		{gitOpsCommand, unknownValue},
		{gitOpsCommand, initCommand},
		{gitOpsCommand, initCommand, repositoryValue, branchOption},
		{gitOpsCommand, initCommand, repositoryValue, unknownOption},
		{string(commandDaemon), "extra"},
		{string(commandDaemon), unknownOption},
		{string(commandDaemon), intervalOption, "invalid"},
		{string(commandDaemon), intervalOption, "NaN"},
		{string(commandDaemon), intervalOption, "+Inf"},
		{string(commandDaemon), intervalOption, "0"},
		{string(commandDaemon), intervalOption, "100000000000"},
		{debugOption},
		{string(commandDoctor)},
		{string(commandDoctor), reindexBackupsOption, "extra"},
		{string(commandDoctor), reindexBackupsOption + "=true"},
		{string(commandDoctor), reindexBackupsOption, configOption},
		{string(commandDoctor), reindexBackupsOption, unknownOption},
	}

	for _, args := range tests {
		_, err := parse(args)
		if err == nil {
			t.Fatalf("parse(%q) succeeded, want error", args)
		}
	}
}

func TestRequestedHelpRejectsUnknownPaths(t *testing.T) {
	t.Parallel()

	tests := [][]string{
		nil,
		{versionOption},
		{unknownValue, helpOption},
		{gitOpsCommand, unknownValue, helpOption},
	}
	for _, args := range tests {
		help, ok := requestedHelp(args)
		if ok || help != "" {
			t.Fatalf("requestedHelp(%q) = %q, %t", args, help, ok)
		}
	}
}

func TestRequestedHelpAcceptsDebugPrefix(t *testing.T) {
	t.Parallel()

	help, ok := requestedHelp([]string{debugOption, string(commandDoctor), helpOption})
	if !ok || help != doctorHelp {
		t.Fatalf("requestedHelp(debug doctor) = %q, %t", help, ok)
	}
}
