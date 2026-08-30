// Package main writes the Go gate manifest selected for a revision range.
package main

import (
	"bytes"
	"errors"
	"flag"
	"fmt"
	"io"
	"os"

	"github.com/IceCodeNew/maniud/internal/changescope"
)

const (
	statusOK      = 0
	statusFailure = 1
	statusUsage   = 2
)

var errMissingNULTerminator = errors.New("read changed paths: missing NUL terminator")

type dependencies struct {
	getwd      func() (string, error)
	readPaths  func(string) ([]string, error)
	selectGate func(string, string, string, []string) (changescope.Manifest, error)
	create     func(string) (io.WriteCloser, error)
}

var exitMain = os.Exit //nolint:gochecknoglobals // Tests replace the process exit seam.

var runMain = func() int { //nolint:gochecknoglobals // Tests replace the top-level execution seam.
	return execute(os.Args[1:], os.Stderr, defaultDependencies())
}

func main() {
	exitMain(runMain())
}

func defaultDependencies() dependencies {
	return dependencies{
		getwd:      os.Getwd,
		readPaths:  readPaths,
		selectGate: changescope.Select,
		create: func(path string) (io.WriteCloser, error) {
			return os.Create(path) //nolint:gosec // The caller explicitly supplies the manifest output path.
		},
	}
}

//nolint:cyclop // The command reports each process-boundary failure at one exit seam.
func execute(args []string, stderr io.Writer, deps dependencies) int {
	flags := flag.NewFlagSet("changescope", flag.ContinueOnError)
	flags.SetOutput(stderr)
	base := flags.String("base", "", "base Git revision")
	head := flags.String("head", "HEAD", "head Git revision")
	output := flags.String("output", "", "manifest output path")
	pathsFile := flags.String("paths-file", "", "NUL-separated changed-path file")
	if err := flags.Parse(args); err != nil {
		return statusUsage
	}
	if *output == "" {
		return report(stderr, "--output is required")
	}
	repository, err := deps.getwd()
	if err != nil {
		return report(stderr, err.Error())
	}
	if *base == "" || *pathsFile == "" {
		return report(stderr, "--base and --paths-file are required")
	}
	paths, err := deps.readPaths(*pathsFile)
	if err != nil {
		return report(stderr, err.Error())
	}
	manifest, err := deps.selectGate(repository, *base, *head, paths)
	if err != nil {
		return report(stderr, err.Error())
	}
	file, err := deps.create(*output)
	if err != nil {
		return report(stderr, err.Error())
	}
	if err := changescope.Write(file, manifest); err != nil {
		_ = file.Close()

		return report(stderr, err.Error())
	}
	if err := file.Close(); err != nil {
		return report(stderr, err.Error())
	}

	return statusOK
}

func readPaths(path string) ([]string, error) {
	raw, err := os.ReadFile(path) //nolint:gosec // The caller explicitly supplies the changed-path manifest.
	if err != nil {
		return nil, fmt.Errorf("read changed paths: %w", err)
	}
	if len(raw) == 0 || raw[len(raw)-1] != 0 {
		return nil, errMissingNULTerminator
	}
	parts := bytes.Split(raw[:len(raw)-1], []byte{0})
	paths := make([]string, len(parts))
	for index, part := range parts {
		paths[index] = string(part)
	}

	return paths, nil
}

func report(output io.Writer, message string) int {
	_, _ = fmt.Fprintln(output, message)

	return statusFailure
}
