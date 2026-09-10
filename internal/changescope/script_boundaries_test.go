package changescope

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestCommandOnlySelectionRunsEstablishedCoverageScope(t *testing.T) {
	t.Parallel()
	repository := bootstrapRealSelectorRepository(t)
	copyFile(t, "../../scripts/check-go-coverage", filepath.Join(repository, "scripts/check-go-coverage"))
	writeExecutable(t, filepath.Join(repository, "scripts/list-go-modules"), "#!/bin/sh\nprintf '.\\n'\n")
	write(t, repository, "cmd/maniud/main.go", "package main\nfunc main() {}\n")
	write(t, repository, "plugins/probe/probe.go", "package probe\n")
	base := commit(t, repository, "command baseline")
	appendFile(t, repository, "cmd/maniud/main.go", "const Changed = true\n")
	head := commit(t, repository, "command-only change")
	manifest := filepath.Join(t.TempDir(), "manifest.tsv")
	run(t, repository, "bash", "scripts/select-affected-gates", "--base", base, "--head", head, "--output", manifest)
	contents, err := os.ReadFile(manifest) //nolint:gosec // Test-owned manifest.
	if err != nil {
		t.Fatal(err)
	}
	for _, expected := range []string{
		testAffectedHeader,
		"package\t.\tgithub.com/IceCodeNew/maniud/cmd/maniud\n",
		"command-e2e\ttrue\n",
	} {
		if !strings.Contains(string(contents), expected) {
			t.Fatalf("command-only selection missing %q:\n%s", expected, contents)
		}
	}
	if strings.Count(string(contents), "package\t") != 1 {
		t.Fatalf("unexpected command-only selection:\n%s", contents)
	}
	// Keep real go list resolution. Stop at the test dispatch boundary and check
	// every argument instead of running this repository's coverage recursively.
	goBinary, err := exec.LookPath("go")
	if err != nil {
		t.Fatal(err)
	}
	bin := t.TempDir()
	writeExecutable(t, filepath.Join(bin, "go"), `#!/bin/sh
if [ "$1" = test ]; then
    printf '%s\n' "$@"
    exit 73
fi
exec "$REAL_GO" "$@"
`)
	//nolint:gosec // Runs the repository-owned script against a test-owned manifest.
	command := exec.CommandContext(t.Context(), "bash", "scripts/check-go-coverage", "--packages-from", manifest)
	command.Dir = repository
	command.Env = append(os.Environ(), "MANIUD_GATE_MANIFEST=", "REAL_GO="+goBinary,
		"PATH="+bin+string(os.PathListSeparator)+os.Getenv("PATH"))
	output, err := command.CombinedOutput()
	exit, ok := errors.AsType[*exec.ExitError](err)
	if !ok || exit.ExitCode() != 73 {
		t.Fatalf("coverage never dispatched tests: %v\n%s", err, output)
	}
	const prefix = "github.com/IceCodeNew/maniud/"
	const packages = prefix + "internal/changescope," + prefix + "internal/changescope/cmd/changescope," +
		prefix + "plugins/probe," + prefix + "plugins/runtime"
	if !strings.Contains(string(output), "-coverpkg="+packages+"\n") ||
		!strings.HasSuffix(string(output), strings.ReplaceAll(packages, ",", "\n")+"\n") {
		t.Fatalf("coverage did not dispatch the established root scope:\n%s", output)
	}
}

func TestCoverageStopsAfterManifestFilterFailure(t *testing.T) {
	t.Parallel()
	repository := t.TempDir()
	copyFile(t, "../../scripts/check-go-coverage", filepath.Join(repository, "scripts/check-go-coverage"))
	write(t, repository, "scripts/list-go-modules", "#!/bin/sh\nprintf '.\\n'\n")
	write(t, repository, "manifest.tsv", "package\t.\texample.test/root\n")
	write(t, repository, "bin/awk", "#!/bin/sh\nprintf 'filter-failed\\n' >&2\nexit 47\n")
	write(t, repository, "bin/go", "#!/bin/sh\nprintf 'unexpected-go-call\\n' >&2\nexit 91\n")
	run(t, repository, "chmod", "700", "scripts/list-go-modules", "bin/awk", "bin/go")
	command := exec.CommandContext(t.Context(), "bash", "scripts/check-go-coverage", "--packages-from", "manifest.tsv")
	command.Dir = repository
	path := filepath.Join(repository, "bin") + string(os.PathListSeparator) + os.Getenv("PATH")
	command.Env = append(os.Environ(), "PATH="+path,
		"MANIUD_GATE_MANIFEST=")
	output, err := command.CombinedOutput()
	if err == nil || !strings.Contains(string(output), "filter-failed") ||
		strings.Contains(string(output), "unexpected-go-call") {
		t.Fatalf("coverage after failed filter: %v\n%s", err, output)
	}
}

func TestBootstrapOutputIsRelativeToCaller(t *testing.T) {
	t.Parallel()
	const headOption = "--head"
	repository := bootstrapRepository(t)
	base := commit(t, repository, "base")
	appendFile(t, repository, "root.go", "const Changed = true\n")
	head := commit(t, repository, "change")
	caller := filepath.Join(repository, "cmd")
	for _, arguments := range [][]string{{"--full"}, {"--base", base, headOption, head}} {
		for _, absolute := range []bool{false, true} {
			output := "manifest.tsv"
			if absolute {
				output = filepath.Join(t.TempDir(), "absolute.tsv")
			}
			args := append([]string{"../scripts/select-affected-gates"}, arguments...)
			run(t, caller, "bash", append(args, "--output", output)...)
			if !absolute {
				output = filepath.Join(caller, output)
			}
			//nolint:gosec // The output is a fixed filename in this test's temporary directories.
			manifest, err := os.ReadFile(output)
			if err != nil || !strings.HasPrefix(string(manifest), "mode\t") {
				t.Fatalf("caller output: %q, %v", manifest, err)
			}
			if _, err = os.Stat(filepath.Join(repository, "manifest.tsv")); !errors.Is(err, os.ErrNotExist) {
				t.Fatalf("unexpected repository-root output: %v", err)
			}
		}
	}
}
