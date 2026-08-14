package cli

import (
	"errors"
	"flag"
	"io"
	"math"
	"strconv"
	"strings"
	"time"
)

type command string

const (
	commandGen        command = "gen"
	commandApply      command = "apply"
	commandGitOpsInit command = "gitops init"
	commandDaemon     command = "daemon"
	gitOpsCommand             = "gitops"
	initCommand               = "init"
	nameOption                = "--name"
	outputOption              = "--output"
	branchOption              = "--branch"
	intervalOption            = "--interval"
	defaultInterval           = 5 * time.Minute
	maxApplyArguments         = 2
)

type invocationArguments interface {
	kind() command
}

type invocation struct {
	arguments invocationArguments
}

func (value invocation) kind() command {
	return value.arguments.kind()
}

type genInvocation struct {
	source string
	name   string
	output string
}

func (genInvocation) kind() command {
	return commandGen
}

type applyInvocation struct {
	compose string
	service string
}

func (applyInvocation) kind() command {
	return commandApply
}

type gitOpsInitInvocation struct {
	repository string
	branch     string
}

func (gitOpsInitInvocation) kind() command {
	return commandGitOpsInit
}

type daemonInvocation struct {
	once     bool
	interval time.Duration
}

func (daemonInvocation) kind() command {
	return commandDaemon
}

var errInvalidArguments = errors.New("invalid command arguments")

func parse(args []string) (invocation, error) {
	if len(args) == 0 {
		return invocation{}, errInvalidArguments
	}

	switch args[0] {
	case string(commandGen):
		return parseGen(args[1:])
	case string(commandApply):
		return parseApply(args[1:])
	case gitOpsCommand:
		return parseGitOps(args[1:])
	case string(commandDaemon):
		return parseDaemon(args[1:])
	default:
		return invocation{}, errInvalidArguments
	}
}

func parseGen(args []string) (invocation, error) {
	ordered, err := intersperseOptions(args, map[string]bool{nameOption: true, outputOption: true})
	if err != nil {
		return invocation{}, err
	}

	flags := newFlagSet("gen")
	name := flags.String(nameOption[2:], "", "")
	output := flags.String(outputOption[2:], "", "")

	if flags.Parse(ordered) != nil || flags.NArg() != 1 {
		return invocation{}, errInvalidArguments
	}

	return invocation{
		arguments: genInvocation{
			source: flags.Arg(0),
			name:   *name,
			output: *output,
		},
	}, nil
}

func parseApply(args []string) (invocation, error) {
	ordered, err := intersperseOptions(args, nil)
	if err != nil {
		return invocation{}, err
	}

	flags := newFlagSet("apply")
	if flags.Parse(ordered) != nil || flags.NArg() < 1 || flags.NArg() > maxApplyArguments {
		return invocation{}, errInvalidArguments
	}

	parsed := applyInvocation{compose: flags.Arg(0), service: ""}
	if flags.NArg() == maxApplyArguments {
		parsed.service = flags.Arg(1)
	}

	return invocation{arguments: parsed}, nil
}

func parseGitOps(args []string) (invocation, error) {
	if len(args) == 0 || args[0] != initCommand {
		return invocation{}, errInvalidArguments
	}

	ordered, err := intersperseOptions(args[1:], map[string]bool{branchOption: true})
	if err != nil {
		return invocation{}, err
	}

	flags := newFlagSet("gitops init")
	branch := flags.String(branchOption[2:], "main", "")

	if flags.Parse(ordered) != nil || flags.NArg() != 1 {
		return invocation{}, errInvalidArguments
	}

	return invocation{
		arguments: gitOpsInitInvocation{
			repository: flags.Arg(0),
			branch:     *branch,
		},
	}, nil
}

func parseDaemon(args []string) (invocation, error) {
	flags := newFlagSet("daemon")
	once := flags.Bool("once", false, "")
	interval := defaultInterval

	flags.Func(intervalOption[2:], "", func(value string) error {
		parsed, err := parseInterval(value)
		if err == nil {
			interval = parsed
		}

		return err
	})

	if flags.Parse(args) != nil || flags.NArg() != 0 {
		return invocation{}, errInvalidArguments
	}

	return invocation{
		arguments: daemonInvocation{once: *once, interval: interval},
	}, nil
}

func newFlagSet(name string) *flag.FlagSet {
	flags := flag.NewFlagSet(name, flag.ContinueOnError)
	flags.SetOutput(io.Discard)
	flags.Usage = func() {}

	return flags
}

func intersperseOptions(args []string, valued map[string]bool) ([]string, error) {
	options := make([]string, 0, len(args))
	positionals := make([]string, 0, len(args))

	for index := 0; index < len(args); index++ {
		arg := args[index]
		name, _, joined := strings.Cut(arg, "=")

		if valued[name] {
			if joined {
				options = append(options, arg)

				continue
			}

			if index+1 >= len(args) {
				return nil, errInvalidArguments
			}

			options = append(options, arg, args[index+1])
			index++

			continue
		}

		if strings.HasPrefix(arg, "-") {
			return nil, errInvalidArguments
		}

		positionals = append(positionals, arg)
	}

	return append(options, positionals...), nil
}

func parseInterval(value string) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(value, 64)
	maxSeconds := float64(time.Duration(1<<63-1)) / float64(time.Second)

	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 1 || seconds > maxSeconds {
		return 0, errInvalidArguments
	}

	return time.Duration(seconds * float64(time.Second)), nil
}
