// maniud-builder creates a static maniud binary containing an explicit subset
// of first-party container runtime capabilities.
package main

import (
	"context"
	"encoding/json/v2"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"syscall"

	"github.com/IceCodeNew/maniud/internal/custombuild"
)

const (
	defaultOutput  = "bin/maniud"
	exitUsageError = 2
)

type buildFunc func(context.Context, custombuild.Config) (custombuild.Manifest, error)

type builderOptions struct {
	output                 string
	target                 string
	runtimes               []string
	disableDefaultRuntimes bool
}

func main() {
	os.Exit(realMain())
}

func realMain() int {
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	return run(ctx, os.Args[1:], os.Stdout, os.Stderr, os.Getwd, custombuild.Build)
}

func run(
	ctx context.Context,
	args []string,
	stdout, stderr io.Writer,
	getWorkingDirectory func() (string, error),
	build buildFunc,
) int {
	options := builderOptions{}
	flags := newBuilderFlagSet(stderr, &options)
	if err := flags.Parse(args); err != nil {
		if errors.Is(err, flag.ErrHelp) {
			return 0
		}

		return exitUsageError
	}
	if flags.NArg() != 0 || build == nil || getWorkingDirectory == nil {
		flags.Usage()

		return exitUsageError
	}
	root, err := getWorkingDirectory()
	if err != nil {
		writeError(stderr, fmt.Errorf("resolve source directory: %w", err))

		return 1
	}
	manifest, err := build(ctx, custombuild.Config{
		Root: root, Output: options.output, Target: options.target, Runtimes: options.runtimes,
		DisableDefaultRuntimes: options.disableDefaultRuntimes,
	})
	if err != nil {
		writeError(stderr, err)

		return 1
	}
	if err = json.MarshalWrite(stdout, &manifest, json.Deterministic(true)); err != nil {
		writeError(stderr, fmt.Errorf("write build manifest: %w", err))

		return 1
	}
	if _, err = io.WriteString(stdout, "\n"); err != nil {
		writeError(stderr, fmt.Errorf("finish build manifest: %w", err))

		return 1
	}

	return 0
}

func newBuilderFlagSet(stderr io.Writer, options *builderOptions) *flag.FlagSet {
	flags := flag.NewFlagSet("maniud-builder", flag.ContinueOnError)
	flags.SetOutput(stderr)
	flags.Usage = func() {
		_, _ = fmt.Fprintln(
			stderr,
			"Usage: maniud-builder [--runtime RUNTIME]... [--no-default-runtimes] [--output PATH] [--target GOOS/GOARCH]",
		)
		_, _ = fmt.Fprintln(
			stderr,
			"Runtimes: docker, podman, containerd. Omitting --runtime builds all three unless defaults are disabled.",
		)
		flags.PrintDefaults()
	}
	flags.StringVar(&options.output, "output", defaultOutput, "output binary path")
	flags.StringVar(&options.target, "target", "", "GOOS/GOARCH cross-compilation target")
	flags.BoolVar(
		&options.disableDefaultRuntimes,
		"no-default-runtimes",
		false,
		"omit all runtime capabilities unless --runtime is set",
	)
	flags.Func("runtime", "runtime capability to include; repeat for multiple runtimes", func(value string) error {
		options.runtimes = append(options.runtimes, value)

		return nil
	})

	return flags
}

func writeError(output io.Writer, err error) {
	_, _ = fmt.Fprintf(output, "maniud-builder: %v\n", err)
}
