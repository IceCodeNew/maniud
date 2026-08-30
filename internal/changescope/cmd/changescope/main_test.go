//nolint:paralleltest // Tests share replaceable process seams and process arguments.
package main

import (
	"bytes"
	"errors"
	"io"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/changescope"
)

const (
	testAffectedMode = "affected"
	testBase         = "base"
	testBaseFlag     = "--base"
	testManifest     = "manifest"
	testOutputFlag   = "--output"
	testPaths        = "paths"
	testPathsFlag    = "--paths-file"
	testRepository   = "repository"
)

var (
	errTestMain  = errors.New("main test failure")
	errTestClose = errors.New("close failure")
)

type testWriteCloser struct {
	io.Writer

	closeErr error
	closed   bool
}

func (writer *testWriteCloser) Close() error {
	writer.closed = true

	return writer.closeErr
}

func TestMainReportsExecuteStatus(t *testing.T) {
	originalExit := exitMain
	originalRun := runMain
	t.Cleanup(func() {
		exitMain = originalExit
		runMain = originalRun
	})
	want := 7
	got := 0
	runMain = func() int { return want }
	exitMain = func(status int) { got = status }

	main()
	if got != want {
		t.Fatalf("main() status = %d, want %d", got, want)
	}
}

//nolint:paralleltest // This test owns process arguments and stderr.
func TestRunMainUsesProcessArguments(t *testing.T) {
	originalArgs := os.Args
	originalStderr := os.Stderr
	t.Cleanup(func() {
		os.Args = originalArgs
		os.Stderr = originalStderr
	})
	stderr, err := os.CreateTemp(t.TempDir(), "stderr-")
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = stderr.Close() })
	os.Args = []string{"changescope"}
	os.Stderr = stderr
	if status := runMain(); status != statusFailure {
		t.Fatalf("runMain() = %d, want %d", status, statusFailure)
	}
}

func TestDefaultDependenciesUseProcessImplementations(t *testing.T) {
	deps := defaultDependencies()
	if deps.getwd == nil || deps.readPaths == nil || deps.selectGate == nil || deps.create == nil {
		t.Fatalf("defaultDependencies() = %#v", deps)
	}
	path := filepath.Join(t.TempDir(), "created")
	file, err := deps.create(path)
	if err != nil {
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func TestExecuteWritesAffectedManifest(t *testing.T) {
	paths := []string{"one.go", "line\nbreak.go"}
	var output bytes.Buffer
	writer := &testWriteCloser{Writer: &output}
	deps := successfulDependencies(writer)
	deps.readPaths = func(string) ([]string, error) { return paths, nil }
	deps.selectGate = func(repository, base, head string, gotPaths []string) (changescope.Manifest, error) {
		if repository != testRepository || base != testBase || head != "HEAD" || !slices.Equal(gotPaths, paths) {
			t.Fatalf("selectGate(%q, %q, %q, %q)", repository, base, head, gotPaths)
		}

		return changescope.Manifest{Mode: testAffectedMode, Packages: map[string][]string{}}, nil
	}
	args := []string{testBaseFlag, testBase, testPathsFlag, testPaths, testOutputFlag, testManifest}
	if status := execute(args, io.Discard, deps); status != statusOK || !writer.closed ||
		!strings.HasPrefix(output.String(), "mode\t"+testAffectedMode+"\n") {
		t.Fatalf("execute() = %d, closed %t, output %q", status, writer.closed, output.String())
	}
}

func TestExecuteReportsFailures(t *testing.T) {
	affectedArgs := []string{testBaseFlag, testBase, testPathsFlag, testPaths, testOutputFlag, testManifest}
	tests := []struct {
		name string
		args []string
		edit func(*dependencies)
		want int
	}{
		{name: "flags", args: []string{"--unknown"}, want: statusUsage},
		{name: "missing output", want: statusFailure},
		{
			name: "working directory",
			args: affectedArgs,
			edit: func(deps *dependencies) {
				deps.getwd = func() (string, error) { return "", errTestMain }
			}, want: statusFailure,
		},
		{name: "missing affected input", args: []string{testOutputFlag, testManifest}, want: statusFailure},
		{
			name: "read paths",
			args: []string{testBaseFlag, testBase, testPathsFlag, testPaths, testOutputFlag, testManifest},
			edit: func(deps *dependencies) {
				deps.readPaths = func(string) ([]string, error) { return nil, errTestMain }
			}, want: statusFailure,
		},
		{
			name: "selection",
			args: []string{testBaseFlag, testBase, testPathsFlag, testPaths, testOutputFlag, testManifest},
			edit: func(deps *dependencies) {
				deps.selectGate = func(string, string, string, []string) (changescope.Manifest, error) {
					return changescope.Manifest{}, errTestMain
				}
			}, want: statusFailure,
		},
		{name: "create", args: affectedArgs, edit: func(deps *dependencies) {
			deps.create = func(string) (io.WriteCloser, error) { return nil, errTestMain }
		}, want: statusFailure},
		{name: "write", args: affectedArgs, edit: func(deps *dependencies) {
			deps.create = func(string) (io.WriteCloser, error) {
				return &testWriteCloser{Writer: failingWriter{}}, nil
			}
		}, want: statusFailure},
		{name: "close", args: affectedArgs, edit: func(deps *dependencies) {
			deps.create = func(string) (io.WriteCloser, error) {
				return &testWriteCloser{Writer: io.Discard, closeErr: errTestClose}, nil
			}
		}, want: statusFailure},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			deps := successfulDependencies(&testWriteCloser{Writer: io.Discard})
			if test.edit != nil {
				test.edit(&deps)
			}
			var stderr bytes.Buffer
			if got := execute(test.args, &stderr, deps); got != test.want || stderr.Len() == 0 {
				t.Fatalf("execute() = %d, stderr %q", got, stderr.String())
			}
		})
	}
}

func TestReadPaths(t *testing.T) {
	path := filepath.Join(t.TempDir(), "paths")
	if err := os.WriteFile(path, []byte("one\x00line\nbreak\x00"), 0o600); err != nil {
		t.Fatal(err)
	}
	paths, err := readPaths(path)
	if err != nil || !slices.Equal(paths, []string{"one", "line\nbreak"}) {
		t.Fatalf("readPaths() = %q, %v", paths, err)
	}
	if _, err := readPaths(filepath.Join(t.TempDir(), "missing")); err == nil {
		t.Fatal("readPaths() accepted a missing file")
	}
	invalid := filepath.Join(t.TempDir(), "invalid")
	if err := os.WriteFile(invalid, []byte("not terminated"), 0o600); err != nil {
		t.Fatal(err)
	}
	if _, err := readPaths(invalid); err == nil {
		t.Fatal("readPaths() accepted a non-NUL-terminated file")
	}
}

func successfulDependencies(writer io.WriteCloser) dependencies {
	return dependencies{
		getwd:     func() (string, error) { return testRepository, nil },
		readPaths: func(string) ([]string, error) { return []string{"changed.go"}, nil },
		selectGate: func(string, string, string, []string) (changescope.Manifest, error) {
			return changescope.Manifest{Mode: testAffectedMode, Packages: map[string][]string{}}, nil
		},
		create: func(string) (io.WriteCloser, error) { return writer, nil },
	}
}

type failingWriter struct{}

func (failingWriter) Write([]byte) (int, error) {
	return 0, errTestMain
}
