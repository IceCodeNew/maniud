package cli

import (
	"testing"
	"time"
)

const (
	composeFileValue = "compose.yaml"
	imageValue       = "image"
	repositoryValue  = "repo"
	unknownValue     = "unknown"
	unknownOption    = "--unknown"
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
			args: []string{string(commandGen), imageValue, nameOption, "service", "--output=service.yaml"},
			want: invocation{
				arguments: genInvocation{source: imageValue, name: "service", output: "service.yaml"},
			},
		},
		{
			name: "apply one service",
			args: []string{string(commandApply), composeFileValue, "api"},
			want: invocation{
				arguments: applyInvocation{compose: composeFileValue, service: "api"},
			},
		},
		{
			name: "apply inferred service",
			args: []string{string(commandApply), composeFileValue},
			want: invocation{
				arguments: applyInvocation{compose: composeFileValue, service: ""},
			},
		},
		{
			name: "gitops init",
			args: []string{gitOpsCommand, initCommand, repositoryValue, branchOption, "stable"},
			want: invocation{
				arguments: gitOpsInitInvocation{repository: repositoryValue, branch: "stable"},
			},
		},
		{
			name: "gitops default branch",
			args: []string{gitOpsCommand, initCommand, repositoryValue},
			want: invocation{
				arguments: gitOpsInitInvocation{repository: repositoryValue, branch: "main"},
			},
		},
		{
			name: "daemon configured",
			args: []string{string(commandDaemon), "--once", intervalOption, "1.5"},
			want: invocation{
				arguments: daemonInvocation{once: true, interval: 1500 * time.Millisecond},
			},
		},
		{
			name: "daemon defaults",
			args: []string{string(commandDaemon)},
			want: invocation{
				arguments: daemonInvocation{once: false, interval: defaultInterval},
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

			if got != test.want {
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
		{string(commandGen)},
		{string(commandGen), "one", "two"},
		{string(commandGen), imageValue, "--"},
		{string(commandGen), imageValue, nameOption},
		{string(commandGen), imageValue, unknownOption, "value"},
		{string(commandApply)},
		{string(commandApply), "one", "two", "three"},
		{string(commandApply), composeFileValue, "--dry-run"},
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
