package custombuild

import (
	"bytes"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"slices"
	"strings"
	"testing"
)

//nolint:paralleltest // Real builds intentionally share Go's module and build caches serially.
func TestBuildRuntimeCombinations(t *testing.T) {
	root := testRepositoryRoot(t)
	tests := []struct {
		name            string
		runtimes        []string
		disableDefaults bool
		want            []string
	}{
		{name: "default all", want: []string{dockerRuntime, podmanRuntime, containerdRuntime}},
		{name: "no defaults", disableDefaults: true, want: []string{}},
		{name: dockerRuntime, runtimes: []string{dockerRuntime}, want: []string{dockerRuntime}},
		{name: podmanRuntime, runtimes: []string{podmanRuntime}, want: []string{podmanRuntime}},
		{name: containerdRuntime, runtimes: []string{containerdRuntime}, want: []string{containerdRuntime}},
		{
			name: "docker and podman", runtimes: []string{podmanRuntime, dockerRuntime},
			want: []string{dockerRuntime, podmanRuntime},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			testBuildCombination(t, root, test.runtimes, test.disableDefaults, test.want)
		})
	}
}

//nolint:paralleltest // A real cross-build intentionally shares Go's module and build caches serially.
func TestBuildExplicitTarget(t *testing.T) {
	const target = "darwin/arm64"
	if os.Getenv("MANIUD_BRANCH_COVERAGE") == "1" {
		t.Skip("branch coverage source contains only current-platform files")
	}

	output := filepath.Join(t.TempDir(), "maniud")
	manifest, err := Build(t.Context(), Config{
		Root: testRepositoryRoot(t), Output: output, Target: target, Runtimes: []string{dockerRuntime},
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	if manifest.Target != target || !slices.Equal(manifest.Runtimes, []string{dockerRuntime}) {
		t.Fatalf("Build() target = %q, runtimes = %q", manifest.Target, manifest.Runtimes)
	}
	//nolint:gosec // The Go executable is fixed and output is the test-owned binary path.
	metadata, err := exec.CommandContext(t.Context(), "go", "version", "-m", output).CombinedOutput()
	if err != nil || !bytes.Contains(metadata, []byte("\tbuild\tGOOS=darwin\n")) ||
		!bytes.Contains(metadata, []byte("\tbuild\tGOARCH=arm64\n")) {
		t.Fatalf("go version -m = %q, %v", metadata, err)
	}
}

func testBuildCombination(t *testing.T, root string, runtimes []string, disableDefaults bool, want []string) {
	t.Helper()

	output := filepath.Join(t.TempDir(), "maniud")
	manifest, err := Build(t.Context(), Config{
		Root: root, Output: output, Runtimes: runtimes, DisableDefaultRuntimes: disableDefaults,
	})
	if err != nil {
		t.Fatalf("Build() error = %v", err)
	}
	verifyBuildManifest(t, manifest, output, want)
	verifyBuiltBinary(t, output, manifest.Version)
}

func verifyBuildManifest(t *testing.T, manifest Manifest, output string, want []string) {
	t.Helper()

	if manifest.Output != output {
		t.Errorf("Build() output = %q, want %q", manifest.Output, output)
	}
	if manifest.Target != runtime.GOOS+"/"+runtime.GOARCH {
		t.Errorf("Build() target = %q, want %q", manifest.Target, runtime.GOOS+"/"+runtime.GOARCH)
	}
	if !slices.Equal(manifest.Runtimes, want) {
		t.Errorf("Build() runtimes = %q, want %q", manifest.Runtimes, want)
	}
	if manifest.GoVersion == "" || manifest.SourceRevision == "" || manifest.Version == "" {
		t.Errorf("Build() provenance is incomplete: %#v", manifest)
	}
}

func verifyBuiltBinary(t *testing.T, output, wantVersion string) {
	t.Helper()

	info, err := os.Stat(output)
	if err != nil || info.Mode()&0o111 == 0 {
		t.Fatalf("Build() output mode = %v, %v", info, err)
	}
	versionOutput, err := exec.CommandContext(t.Context(), output, "--version").CombinedOutput()
	if err != nil || string(versionOutput) != "maniud "+wantVersion+"\n" {
		t.Fatalf("custom --version = %q, %v", versionOutput, err)
	}
	//nolint:gosec // The Go executable is fixed and output is the test-owned binary path.
	metadata, err := exec.CommandContext(t.Context(), "go", "version", "-m", output).CombinedOutput()
	if err != nil || !bytes.Contains(metadata, []byte("\tdep\t"+projectModule+"\tv0.0.0")) {
		t.Fatalf("go version -m = %q, %v", metadata, err)
	}
}

func TestBuildRejectsInvalidInputWithoutReplacingOutput(t *testing.T) {
	t.Parallel()

	output := filepath.Join(t.TempDir(), "maniud")
	if err := os.WriteFile(output, []byte("existing"), 0o600); err != nil {
		t.Fatal(err)
	}
	_, err := Build(t.Context(), Config{
		Root: testRepositoryRoot(t), Output: output, Runtimes: []string{testUnknownRuntime},
	})
	if !errors.Is(err, errInvalidConfiguration) {
		t.Fatalf("Build(invalid runtime) error = %v", err)
	}
	//nolint:gosec // The path belongs to this test's private temporary directory.
	content, readErr := os.ReadFile(output)
	if readErr != nil || string(content) != "existing" {
		t.Fatalf("Build(invalid runtime) output = %q, %v", content, readErr)
	}
}

func TestResolveRuntimesHandlesDefaultsAndValidatesExplicitSelections(t *testing.T) {
	t.Parallel()

	got, err := resolveRuntimes(nil, true)
	if err != nil || len(got) != 0 || got == nil {
		t.Fatalf("resolveRuntimes(no defaults) = %q, %v", got, err)
	}
	got, err = resolveRuntimes([]string{containerdRuntime, dockerRuntime}, false)
	if err != nil || !slices.Equal(got, []string{dockerRuntime, containerdRuntime}) {
		t.Fatalf("resolveRuntimes() = %q, %v", got, err)
	}
	if _, err = resolveRuntimes(
		[]string{dockerRuntime, dockerRuntime}, false,
	); !errors.Is(err, errInvalidConfiguration) {
		t.Fatalf("resolveRuntimes(duplicate) error = %v", err)
	} else if !strings.Contains(err.Error(), "remove the duplicate --runtime flag") {
		t.Fatalf("resolveRuntimes(duplicate) guidance = %v", err)
	}
	if _, err = resolveRuntimes(
		[]string{testUnknownRuntime}, false,
	); !errors.Is(err, errInvalidConfiguration) {
		t.Fatalf("resolveRuntimes(unknown) error = %v", err)
	} else if !strings.Contains(err.Error(), "choose docker, podman, or containerd") {
		t.Fatalf("resolveRuntimes(unknown) guidance = %v", err)
	}
}

func TestRenderMainReportsRuntimePluginErrors(t *testing.T) {
	t.Parallel()

	mainSource := string(renderMain(nil))
	if !strings.Contains(mainSource, `"fmt"`) ||
		!strings.Contains(mainSource, `_, _ = fmt.Fprintln(os.Stderr, "maniud:", err)`) {
		t.Fatalf("renderMain() omitted runtime plugin error output:\n%s", mainSource)
	}
}

func TestVerifyRuntimeDependenciesRequiresExactSelection(t *testing.T) {
	t.Parallel()

	core := projectModule + "/plugins/runtime\n"
	docker := core + projectModule + "/plugins/runtime/docker\n"
	if err := verifyRuntimeDependencies("", []string{dockerRuntime}); !errors.Is(err, errDependencyMismatch) {
		t.Fatalf("verifyRuntimeDependencies(missing core) error = %v", err)
	}
	if err := verifyRuntimeDependencies(docker, []string{dockerRuntime}); err != nil {
		t.Fatalf("verifyRuntimeDependencies(docker) error = %v", err)
	}
	dependencies := docker + projectModule + "/plugins/runtime/podman\n"
	if err := verifyRuntimeDependencies(dependencies, []string{dockerRuntime}); !errors.Is(err, errDependencyMismatch) {
		t.Fatalf("verifyRuntimeDependencies(extra) error = %v", err)
	}
}

func TestResolveTarget(t *testing.T) {
	t.Parallel()

	goos, goarch, target, err := resolveTarget(testLinuxARM64)
	if err != nil || goos != "linux" || goarch != "arm64" || target != testLinuxARM64 {
		t.Fatalf("resolveTarget() = %q, %q, %q, %v", goos, goarch, target, err)
	}
	for _, value := range []string{"linux", "linux/", "/amd64", "linux/amd64/extra", "linux/$ARCH"} {
		if _, _, _, err = resolveTarget(value); !errors.Is(err, errInvalidConfiguration) {
			t.Fatalf("resolveTarget(%q) error = %v", value, err)
		}
	}
}

func TestReplacedEnvironmentOverridesNamedValues(t *testing.T) {
	t.Parallel()

	got := replacedEnvironment(
		[]string{"GOOS=old", "PATH=/bin", "MALFORMED"},
		[]string{"GOOS=linux", "GOARCH=arm64"},
	)
	if slices.Contains(got, "GOOS=old") || !slices.Contains(got, "GOOS=linux") ||
		!slices.Contains(got, "GOARCH=arm64") || !slices.Contains(got, "PATH=/bin") ||
		!slices.Contains(got, "MALFORMED") {
		t.Fatalf("replacedEnvironment() = %q", got)
	}
}

func testRepositoryRoot(t *testing.T) string {
	t.Helper()

	directory, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	root := filepath.Clean(filepath.Join(directory, "../.."))
	if os.Getenv("MANIUD_BRANCH_COVERAGE") == "1" {
		root = branchCoverageRepositoryRoot(t, root)
	}
	content, err := os.ReadFile(filepath.Join(root, "go.mod"))
	if err != nil || !strings.HasPrefix(string(content), "module "+projectModule+"\n") {
		t.Fatalf("repository root %q is invalid: %v", root, err)
	}

	return root
}

func branchCoverageRepositoryRoot(t *testing.T, instrumentedRoot string) string {
	t.Helper()

	//nolint:gosec // Gobco supplies the test-owned instrumented module root.
	content, err := os.ReadFile(filepath.Join(instrumentedRoot, "go.mod"))
	if err != nil {
		t.Fatal(err)
	}
	for line := range strings.Lines(string(content)) {
		fields := strings.Fields(line)
		if len(fields) == 3 && fields[0] == projectModule+"/argv" && fields[1] == "=>" &&
			filepath.IsAbs(fields[2]) {
			return filepath.Dir(fields[2])
		}
	}
	t.Fatalf("branch coverage module %q has no absolute argv replacement", instrumentedRoot)

	return ""
}
