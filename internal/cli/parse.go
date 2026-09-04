package cli

import (
	"errors"
	"io"
	"math"
	"slices"
	"strconv"
	"time"

	commandargv "github.com/IceCodeNew/maniud/argv"
	"github.com/alecthomas/kong"
)

type command string

const (
	commandGen                command = "gen"
	commandApply              command = "apply"
	commandTUI                command = "tui"
	commandGitOpsInit         command = "gitops init"
	commandDaemonStart        command = "daemon start"
	commandDaemonStop         command = "daemon stop"
	commandDoctor             command = "doctor"
	daemonCommand                     = "daemon"
	gitOpsCommand                     = "gitops"
	initCommand                       = "init"
	startCommand                      = "start"
	stopCommand                       = "stop"
	nameOption                        = "--name"
	outputOption                      = "--output"
	branchOption                      = "--branch"
	defaultGitOpsBranch               = "master"
	intervalOption                    = "--interval"
	dryRunOption                      = "--dry-run"
	jsonOption                        = "--json"
	debugOption                       = "--debug"
	helpOption                        = "--help"
	shortHelpOption                   = "-h"
	versionOption                     = "--version"
	reindexBackupsOption              = "--reindex-backups"
	confirmOption                     = "--confirm"
	stateOption                       = "--state"
	recommendedDefaultsOption         = "--recommended-defaults"
	runtimeArgumentsSeparator         = "--"
	defaultInterval                   = 5 * time.Minute
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
	source              string
	runtimeArgs         []string
	name                string
	output              string
	json                bool
	recommendedDefaults bool
}

func (genInvocation) kind() command {
	return commandGen
}

type applyInvocation struct {
	compose string
	service string
	dryRun  bool
	json    bool
}

func (applyInvocation) kind() command {
	return commandApply
}

type tuiInvocation struct{}

func (tuiInvocation) kind() command {
	return commandTUI
}

type gitOpsInitInvocation struct {
	repository string
	branch     string
}

func (gitOpsInitInvocation) kind() command {
	return commandGitOpsInit
}

type daemonInvocation struct {
	operation command
	interval  time.Duration
}

func (value daemonInvocation) kind() command {
	return value.operation
}

type doctorInvocation struct {
	reindexBackups bool
	confirm        bool
	state          string
}

func (doctorInvocation) kind() command {
	return commandDoctor
}

//nolint:lll // tagalign keeps this declarative command grammar readable as one field per line.
type commandLine struct {
	Debug   bool              `help:"Include internal diagnostic context in command failures."`
	Version kong.VersionFlag  `help:"Show the release version or source revision."`
	Gen     genCommandLine    `cmd:""                                                          help:"Create a deployable Compose file."`
	Apply   applyCommandLine  `cmd:""                                                          help:"Deploy one Compose service."`
	TUI     tuiCommandLine    `cmd:""                                                          help:"Open the interactive service workspace."`
	GitOps  gitOpsCommandLine `cmd:""                                                          help:"Register a desired-state repository."                name:"gitops"`
	Daemon  daemonCommandLine `cmd:""                                                          help:"Start or stop registered-repository reconciliation."`
	Doctor  doctorCommandLine `cmd:""                                                          help:"Inspect or rebuild maniud's internal backup index."`
}

//nolint:lll // tagalign keeps this declarative command grammar readable as one field per line.
type genCommandLine struct {
	Name                string   `help:"Set the generated service name."                             placeholder:"SERVICE"`
	Output              string   `help:"Write the generated Compose file to PATH."                   placeholder:"PATH"`
	JSON                bool     `help:"Print one JSON object instead of the short success summary."`
	RecommendedDefaults bool     `hidden:""                                                          name:"recommended-defaults"`
	Inputs              []string `arg:""                                                             help:"Use an image source, or put a copied runtime command after --." name:"input" optional:""`
}

func (*genCommandLine) Help() string {
	return "Create a Compose file from a docker://, podman://, or containerd:// image, a Docker archive " +
		"member, or a supported docker, podman, or nerdctl create/run command.\n\n" +
		"Runtime arguments are parsed as input and are never executed. The command refuses to replace an existing " +
		"output file. When the service uses host bind mounts, gen also writes a reviewable .prepare.sh file for " +
		"creating paths and setting their permissions on the runtime host before apply.\n\n" +
		"For an image source without a copied runtime command, gen warns about application settings that cannot be " +
		"inferred from the image."
}

//nolint:lll // tagalign keeps this declarative command grammar readable as one field per line.
type applyCommandLine struct {
	DryRun  bool   `help:"Validate and show the planned action without changing anything." name:"dry-run"`
	JSON    bool   `help:"Print one detailed JSON object instead of the short summary."`
	Compose string `arg:""                                                                 help:"Compose file to apply."                                  name:"compose"`
	Service string `arg:""                                                                 help:"Service to select when the file contains more than one." name:"service" optional:""`
}

type tuiCommandLine struct{}

func (*tuiCommandLine) Help() string {
	return "Open an interactive workspace for registered services or a committed Compose file.\n\n" +
		"The command requires terminal input and output. For non-interactive validation, use " +
		"'maniud apply --dry-run <compose>' or 'maniud apply --dry-run --json <compose>'."
}

func (*applyCommandLine) Help() string {
	return "Validate and apply one selected Compose service through the journaled transaction.\n\n" +
		"A successful --dry-run prints \"Dry run passed\", the planned action, and confirmation that no changes were " +
		"made. A failed dry run exits with a non-zero status and prints a failure object.\n\n" +
		"With --json, the result contains status, project, service, runtime, platform, image, source_digest, " +
		"desired_digest, and warnings. Status is bootstrap, adopt, unchanged, upgrade, resume, probe-unknown-effect, " +
		"or restore; these mean create, adopt, retain, replace, continue, verify an uncertain effect, or recover the " +
		"previous workload."
}

type gitOpsCommandLine struct {
	Init gitOpsInitCommandLine `cmd:"" help:"Create or register a tracked desired-state repository."`
}

type gitOpsInitCommandLine struct {
	Branch     string `default:"master" help:"Track BRANCH instead of master."             placeholder:"BRANCH"`
	Repository string `arg:""           help:"Repository directory to create or register." name:"repository"`
}

func (*gitOpsInitCommandLine) Help() string {
	return "Create a Git repository with an initial commit and an empty services directory, or register an existing " +
		"clean checkout after proving its branch can fast-forward from origin."
}

type daemonCommandLine struct {
	Start daemonStartCommandLine `cmd:"" help:"Start registered-repository reconciliation."`
	Stop  daemonStopCommandLine  `cmd:"" help:"Stop registered-repository reconciliation."`
}

type daemonStartCommandLine struct {
	Interval string `default:"300" help:"Check the registered repository every SECONDS." placeholder:"SECONDS"`
}

func (*daemonStartCommandLine) Help() string {
	return "Reconcile immediately, then check the registered repository after each interval."
}

type daemonStopCommandLine struct{}

func (*daemonStopCommandLine) Help() string {
	return "Stop the daemon for the current state directory after its active operation reaches a safe boundary."
}

//nolint:lll // tagalign keeps this declarative command grammar readable as one field per line.
type doctorCommandLine struct {
	ReindexBackups bool   `help:"Validate manifests and report index candidates."                    name:"reindex-backups" required:""`
	Confirm        bool   `help:"Replace maniud's internal backup index with the validated entries."`
	State          string `help:"Use the state database at PATH."                                    placeholder:"PATH"`
}

func (*doctorCommandLine) Help() string {
	return "Validate complete maniud backup manifests and report candidate entries for its internal index. The " +
		"command is read-only unless --confirm is supplied; confirmation only rebuilds maniud's internal backup " +
		"index and does not infer applied commits or transactions."
}

var errInvalidArguments = errors.New("invalid command arguments")

func parse(args []string, output io.Writer) (invocation, bool, error) {
	boundedArgs, err := commandargv.Validate(args)
	if err != nil {
		return invocation{}, false, errInvalidArguments
	}

	arguments := commandLine{}
	handled := false
	parser := kong.Must(
		&arguments,
		kong.Name("maniud"),
		kong.Description("Create and deploy Compose services, or reconcile them from Git."),
		kong.Vars{"version": "maniud " + currentVersion()},
		kong.Writers(output, io.Discard),
		kong.Exit(func(int) { handled = true }),
		kong.ConfigureHelp(kong.HelpOptions{Summary: false, Tree: true}),
	)

	context, err := parser.Parse(boundedArgs)
	if handled {
		return invocation{}, true, nil
	}
	if err != nil {
		return invocation{}, false, errInvalidArguments
	}

	parsed, err := arguments.invocation(context.Selected().Path(), boundedArgs)
	if err != nil {
		return invocation{}, false, err
	}

	return parsed, false, nil
}

func (value commandLine) invocation(path string, args []string) (invocation, error) {
	switch command(path) {
	case commandGen:
		return value.genInvocation(args)
	case commandApply:
		return invocation{arguments: applyInvocation{
			compose: value.Apply.Compose,
			service: value.Apply.Service,
			dryRun:  value.Apply.DryRun,
			json:    value.Apply.JSON,
		}, debug: value.Debug}, nil
	case commandTUI:
		return invocation{arguments: tuiInvocation{}, debug: value.Debug}, nil
	case commandGitOpsInit:
		return invocation{arguments: gitOpsInitInvocation{
			repository: value.GitOps.Init.Repository,
			branch:     value.GitOps.Init.Branch,
		}, debug: value.Debug}, nil
	case commandDaemonStart:
		interval, err := parseInterval(value.Daemon.Start.Interval)
		if err != nil {
			return invocation{}, err
		}

		return invocation{
			arguments: daemonInvocation{operation: commandDaemonStart, interval: interval},
			debug:     value.Debug,
		}, nil
	case commandDaemonStop:
		return invocation{
			arguments: daemonInvocation{operation: commandDaemonStop, interval: 0},
			debug:     value.Debug,
		}, nil
	case commandDoctor:
		return invocation{arguments: doctorInvocation{
			reindexBackups: value.Doctor.ReindexBackups,
			confirm:        value.Doctor.Confirm,
			state:          value.Doctor.State,
		}, debug: value.Debug}, nil
	default:
		return invocation{}, errInvalidArguments
	}
}

func (value commandLine) genInvocation(args []string) (invocation, error) {
	separator := slices.Index(args, runtimeArgumentsSeparator)
	source := ""
	var runtimeArgs []string

	switch {
	case separator < 0 && len(value.Gen.Inputs) == 1:
		source = value.Gen.Inputs[0]
	case separator >= 0:
		runtimeArgs = args[separator+1:]
		if len(runtimeArgs) == 0 || !slices.Equal(value.Gen.Inputs, runtimeArgs) {
			return invocation{}, errInvalidArguments
		}
	default:
		return invocation{}, errInvalidArguments
	}

	return invocation{arguments: genInvocation{
		source:              source,
		runtimeArgs:         runtimeArgs,
		name:                value.Gen.Name,
		output:              value.Gen.Output,
		json:                value.Gen.JSON,
		recommendedDefaults: value.Gen.RecommendedDefaults,
	}, debug: value.Debug}, nil
}

func parseInterval(value string) (time.Duration, error) {
	seconds, err := strconv.ParseFloat(value, 64)
	nanoseconds := seconds * float64(time.Second)
	if err != nil || math.IsNaN(seconds) || math.IsInf(seconds, 0) || seconds < 1 ||
		nanoseconds >= float64(math.MaxInt64) {
		return 0, errInvalidArguments
	}

	return time.Duration(nanoseconds), nil
}
