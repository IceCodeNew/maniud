package cli

import (
	"errors"
	"flag"
	"io"
	"math"
	"slices"
	"strconv"
	"strings"
	"time"
)

type command string

const (
	commandGen           command = "gen"
	commandApply         command = "apply"
	commandGitOpsInit    command = "gitops init"
	commandDaemon        command = "daemon"
	commandDoctor        command = "doctor"
	gitOpsCommand                = "gitops"
	initCommand                  = "init"
	nameOption                   = "--name"
	outputOption                 = "--output"
	branchOption                 = "--branch"
	intervalOption               = "--interval"
	dryRunOption                 = "--dry-run"
	debugOption                  = "--debug"
	reindexBackupsOption         = "--reindex-backups"
	confirmOption                = "--confirm"
	configOption                 = "--config"
	stateOption                  = "--state"
	defaultInterval              = 5 * time.Minute
	maxApplyArguments            = 2
)

type invocationArguments interface {
	kind() command
}

type invocation struct {
	arguments invocationArguments
	debug     bool
}

func (value invocation) kind() command {
	return value.arguments.kind()
}

type genInvocation struct {
	source      string
	runtimeArgs []string
	name        string
	output      string
}

func (genInvocation) kind() command {
	return commandGen
}

type applyInvocation struct {
	compose string
	service string
	dryRun  bool
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

type doctorInvocation struct {
	reindexBackups bool
	confirm        bool
	config         string
	state          string
}

func (doctorInvocation) kind() command {
	return commandDoctor
}

var errInvalidArguments = errors.New("invalid command arguments")

func parse(args []string) (invocation, error) {
	debug, commandArgs, err := parseGlobalOptions(args)
	if err != nil {
		return invocation{}, err
	}

	parsed, err := parseCommand(commandArgs)
	if err != nil {
		return invocation{}, err
	}

	parsed.debug = debug

	return parsed, nil
}

func parseGlobalOptions(args []string) (bool, []string, error) {
	if len(args) == 0 {
		return false, nil, errInvalidArguments
	}

	debug := args[0] == debugOption
	if debug {
		args = args[1:]
		if len(args) == 0 {
			return false, nil, errInvalidArguments
		}
	}

	return debug, args, nil
}

func parseCommand(args []string) (invocation, error) {
	var (
		parsed invocation
		err    error
	)

	switch args[0] {
	case string(commandGen):
		parsed, err = parseGen(args[1:])
	case string(commandApply):
		parsed, err = parseApply(args[1:])
	case gitOpsCommand:
		parsed, err = parseGitOps(args[1:])
	case string(commandDaemon):
		parsed, err = parseDaemon(args[1:])
	case string(commandDoctor):
		parsed, err = parseDoctor(args[1:])
	default:
		return invocation{}, errInvalidArguments
	}

	if err != nil {
		return invocation{}, err
	}

	return parsed, nil
}

func parseGen(args []string) (invocation, error) {
	runtimeArgs := []string(nil)
	if separator := slices.Index(args, "--"); separator >= 0 {
		runtimeArgs = args[separator+1:]
		args = args[:separator]
	}

	ordered, err := intersperseOptions(args, map[string]bool{nameOption: true, outputOption: true})
	if err != nil {
		return invocation{}, err
	}

	flags := newFlagSet("gen")
	name := flags.String(nameOption[2:], "", "")
	output := flags.String(outputOption[2:], "", "")

	if flags.Parse(ordered) != nil || !validGenInputs(flags.NArg(), runtimeArgs) {
		return invocation{}, errInvalidArguments
	}

	source := ""
	if flags.NArg() == 1 {
		source = flags.Arg(0)
	}

	return invocation{
		arguments: genInvocation{
			source:      source,
			runtimeArgs: runtimeArgs,
			name:        *name,
			output:      *output,
		},
		debug: false,
	}, nil
}

func validGenInputs(sourceCount int, runtimeArgs []string) bool {
	if runtimeArgs == nil {
		return sourceCount == 1
	}

	return sourceCount == 0 && len(runtimeArgs) > 0
}

func parseApply(args []string) (invocation, error) {
	ordered, err := intersperseOptions(args, map[string]bool{dryRunOption: false})
	if err != nil {
		return invocation{}, err
	}

	flags := newFlagSet("apply")
	dryRun := flags.Bool(dryRunOption[2:], false, "")

	if flags.Parse(ordered) != nil || flags.NArg() < 1 || flags.NArg() > maxApplyArguments {
		return invocation{}, errInvalidArguments
	}

	parsed := applyInvocation{compose: flags.Arg(0), service: "", dryRun: *dryRun}
	if flags.NArg() == maxApplyArguments {
		parsed.service = flags.Arg(1)
	}

	return invocation{arguments: parsed, debug: false}, nil
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
		debug: false,
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
		debug:     false,
	}, nil
}

func parseDoctor(args []string) (invocation, error) {
	accepted := map[string]bool{
		reindexBackupsOption: false,
		confirmOption:        false,
		configOption:         true,
		stateOption:          true,
	}

	ordered, err := intersperseOptions(args, accepted)
	if err != nil {
		return invocation{}, err
	}

	flags := newFlagSet("doctor")
	reindexBackups := flags.Bool(reindexBackupsOption[2:], false, "")
	confirm := flags.Bool(confirmOption[2:], false, "")
	config := flags.String(configOption[2:], "", "")
	state := flags.String(stateOption[2:], "", "")

	if flags.Parse(ordered) != nil || flags.NArg() != 0 || !*reindexBackups {
		return invocation{}, errInvalidArguments
	}

	return invocation{
		arguments: doctorInvocation{
			reindexBackups: *reindexBackups,
			confirm:        *confirm,
			config:         *config,
			state:          *state,
		},
		debug: false,
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

		takesValue, accepted := valued[name]
		if !accepted {
			if strings.HasPrefix(arg, "-") {
				return nil, errInvalidArguments
			}

			positionals = append(positionals, arg)

			continue
		}

		if !takesValue {
			if joined {
				return nil, errInvalidArguments
			}

			options = append(options, arg)

			continue
		}

		if joined {
			options = append(options, arg)

			continue
		}

		if index+1 >= len(args) {
			return nil, errInvalidArguments
		}

		options = append(options, arg, args[index+1])
		index++
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
