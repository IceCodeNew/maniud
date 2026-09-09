package changescope

import (
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

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
