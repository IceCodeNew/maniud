package custombuild

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"testing"
)

const (
	testGoVersion           = "go1.27.0"
	testGoDirective         = "1.27.0"
	testAnyLLMVersion       = "v0.0.0-20260830222028-1c5364355b59"
	testNotificationVersion = "v1.5.0"
	testSuccess             = "success"
	testLinuxARM64          = "linux/arm64"
	testOutputFilename      = "maniud"
	testUnknownRuntime      = "unknown"
)

var errCustomBuildTest = errors.New("custom build test failure")

type buildFailureCase struct {
	name      string
	configure func(*buildOperations)
	want      error
}

func TestBuildWithOperationsReportsEachLifecycleFailure(t *testing.T) {
	t.Parallel()

	config, success := successfulBuildOperations(t)
	tests := []buildFailureCase{
		{name: "source", configure: failSourceInspection, want: errCustomBuildTest},
		{name: "toolchain", configure: failToolchainInspection, want: errCustomBuildTest},
		{name: "workspace", configure: failWorkspaceCreation, want: errCustomBuildTest},
		{name: "workspace inside source", configure: placeWorkspaceInsideSource, want: errInvalidConfiguration},
		{name: "module write", configure: failBuildModuleWrite, want: errCustomBuildTest},
		{name: "dependencies", configure: failDependencyPreparation, want: errCustomBuildTest},
		{name: "binary", configure: failBinaryBuild, want: errCustomBuildTest},
		{name: "workspace cleanup", configure: failWorkspaceCleanup, want: errCustomBuildTest},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			operations := success
			test.configure(&operations)
			_, err := buildWithOperations(t.Context(), config, operations)
			if !errors.Is(err, test.want) {
				t.Fatalf("buildWithOperations() error = %v, want %v", err, test.want)
			}
		})
	}
}

func successfulBuildOperations(t *testing.T) (Config, buildOperations) {
	t.Helper()

	root := testRepositoryRoot(t)
	workspace := t.TempDir()

	return Config{Root: root, Output: filepath.Join(t.TempDir(), testOutputFilename)}, buildOperations{
		inspectSource: func(context.Context, string) (sourceMetadata, error) {
			return sourceMetadata{revision: "0123456789abcdef", version: "test-version"}, nil
		},
		inspectGoToolchain: func(context.Context, string, moduleSettings) (string, error) {
			return testGoVersion, nil
		},
		createWorkspace: func(string, string) (string, error) { return workspace, nil },
		removeWorkspace: func(string) error { return nil },
		pathWithin:      func(string, string) bool { return false },
		writeModule:     func(string, buildPlan) error { return nil },
		prepareDependencies: func(buildPlan, context.Context, string, []string) error {
			return nil
		},
		buildBinary: func(plan buildPlan, _ context.Context, _ string, _ []string) (Manifest, error) {
			return Manifest{Output: plan.config.output}, nil
		},
	}
}

func failSourceInspection(operations *buildOperations) {
	operations.inspectSource = func(context.Context, string) (sourceMetadata, error) {
		return sourceMetadata{}, errCustomBuildTest
	}
}

func failToolchainInspection(operations *buildOperations) {
	operations.inspectGoToolchain = func(context.Context, string, moduleSettings) (string, error) {
		return "", errCustomBuildTest
	}
}

func failWorkspaceCreation(operations *buildOperations) {
	operations.createWorkspace = func(string, string) (string, error) { return "", errCustomBuildTest }
}

func placeWorkspaceInsideSource(operations *buildOperations) {
	operations.pathWithin = func(string, string) bool { return true }
}

func failBuildModuleWrite(operations *buildOperations) {
	operations.writeModule = func(string, buildPlan) error { return errCustomBuildTest }
}

func failDependencyPreparation(operations *buildOperations) {
	operations.prepareDependencies = func(buildPlan, context.Context, string, []string) error {
		return errCustomBuildTest
	}
}

func failBinaryBuild(operations *buildOperations) {
	operations.buildBinary = func(buildPlan, context.Context, string, []string) (Manifest, error) {
		return Manifest{}, errCustomBuildTest
	}
}

func failWorkspaceCleanup(operations *buildOperations) {
	operations.removeWorkspace = func(string) error { return errCustomBuildTest }
}

type outputFailureCase struct {
	name      string
	configure func(*outputOperations)
}

func TestBuildBinaryWithOperationsReportsEachOutputFailure(t *testing.T) {
	t.Parallel()

	tests := []outputFailureCase{
		{name: "directory", configure: failOutputDirectory},
		{name: "temporary output", configure: failTemporaryOutput},
		{name: "close", configure: failTemporaryOutputClose},
		{name: "compile", configure: failOutputCompilation},
		{name: "rename", configure: failOutputRename},
		{name: "cleanup", configure: failOutputCleanup},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			plan := outputBuildPlan(filepath.Join(t.TempDir(), testOutputFilename))
			operations := defaultOutputOperations()
			operations.compile = func(buildPlan, context.Context, string, string, []string) error {
				return nil
			}
			test.configure(&operations)
			_, err := plan.buildBinaryWithOperations(t.Context(), t.TempDir(), nil, operations)
			if !errors.Is(err, errCustomBuildTest) {
				t.Fatalf("buildBinaryWithOperations() error = %v", err)
			}
		})
	}
}

func outputBuildPlan(output string) buildPlan {
	return buildPlan{
		config:    resolvedConfig{output: output, target: "linux/amd64", runtimes: []string{dockerRuntime}},
		source:    sourceMetadata{revision: "0123456789abcdef", version: "test-version"},
		goVersion: testGoVersion,
	}
}

func failOutputDirectory(operations *outputOperations) {
	operations.mkdirAll = func(string, os.FileMode) error { return errCustomBuildTest }
}

func failTemporaryOutput(operations *outputOperations) {
	operations.createTemp = func(string, string) (*os.File, error) { return nil, errCustomBuildTest }
}

func failTemporaryOutputClose(operations *outputOperations) {
	closeFile := operations.close
	operations.close = func(file *os.File) error {
		return errors.Join(closeFile(file), errCustomBuildTest)
	}
}

func failOutputCompilation(operations *outputOperations) {
	operations.compile = func(buildPlan, context.Context, string, string, []string) error {
		return errCustomBuildTest
	}
}

func failOutputRename(operations *outputOperations) {
	operations.rename = func(string, string) error { return errCustomBuildTest }
}

func failOutputCleanup(operations *outputOperations) {
	operations.remove = func(string) error { return errCustomBuildTest }
}

func TestPrepareDependenciesWithRunnerReportsEachFailure(t *testing.T) {
	t.Parallel()

	core := projectModule + "/plugins/runtime\n"
	docker := projectModule + "/plugins/runtime/docker\n"
	tests := []struct {
		name    string
		outputs [][]byte
		errors  []error
		want    error
	}{
		{name: "tidy", errors: []error{errCustomBuildTest}, want: errCustomBuildTest},
		{name: "verify", outputs: [][]byte{nil}, errors: []error{nil, errCustomBuildTest}, want: errCustomBuildTest},
		{
			name: "list", outputs: [][]byte{nil, nil},
			errors: []error{nil, nil, errCustomBuildTest}, want: errCustomBuildTest,
		},
		{name: "dependency mismatch", outputs: [][]byte{nil, nil, []byte(core)}, want: errDependencyMismatch},
		{name: testSuccess, outputs: [][]byte{nil, nil, []byte(core + docker)}},
	}
	plan := buildPlan{goDirective: testGoDirective, config: resolvedConfig{runtimes: []string{dockerRuntime}}}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := plan.prepareDependenciesWithRunner(
				t.Context(), t.TempDir(), nil, sequenceCommandRunner(test.outputs, test.errors),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("prepareDependenciesWithRunner() error = %v, want %v", err, test.want)
			}
		})
	}
}

func TestCompileWithRunnerReportsEachFailure(t *testing.T) {
	t.Parallel()

	validMetadata := []byte("maniud: " + testGoVersion + "\n\tdep\t" + projectModule + "\tv0.0.0\n")
	tests := []struct {
		name    string
		outputs [][]byte
		errors  []error
		want    error
	}{
		{name: "build", errors: []error{errCustomBuildTest}, want: errCustomBuildTest},
		{
			name: "metadata command", outputs: [][]byte{nil},
			errors: []error{nil, errCustomBuildTest}, want: errCustomBuildTest,
		},
		{name: "metadata", outputs: [][]byte{nil, []byte("invalid")}, want: errDependencyMismatch},
		{name: testSuccess, outputs: [][]byte{nil, validMetadata}},
	}
	plan := outputBuildPlan(filepath.Join(t.TempDir(), testOutputFilename))
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			t.Parallel()

			err := plan.compileWithRunner(
				t.Context(), t.TempDir(), plan.config.output, nil,
				sequenceCommandRunner(test.outputs, test.errors),
			)
			if !errors.Is(err, test.want) {
				t.Fatalf("compileWithRunner() error = %v, want %v", err, test.want)
			}
		})
	}
}

func sequenceCommandRunner(outputs [][]byte, failures []error) commandRunner {
	call := 0

	return func(context.Context, string, []string, string, string, ...string) ([]byte, error) {
		index := call
		call++
		var output []byte
		if index < len(outputs) {
			output = slices.Clone(outputs[index])
		}
		if index < len(failures) {
			return output, failures[index]
		}

		return output, nil
	}
}
