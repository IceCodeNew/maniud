//nolint:paralleltest // Subtests intentionally mutate and reset one shared fixture repository.
package changescope

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

func TestBootstrapGuardWidensUnsafeChanges(t *testing.T) {
	repository := bootstrapRepository(t)
	base := commit(t, repository, "base")
	tests := []struct {
		name   string
		change func()
	}{
		{"selector", func() { appendFile(t, repository, "internal/changescope/changescope.go", "\n// changed\n") }},
		{"selector-entry", func() {
			write(
				t,
				repository,
				"internal/changescope/cmd/changescope/main.go",
				"package main\nfunc main() { panic(\"must not run\") }\n",
			)
		}},
		{"workflow", func() { write(t, repository, ".github/workflows/checks.yml", "name: changed\n") }},
		{"go-mod", func() { appendFile(t, repository, "go.mod", "\n// changed\n") }},
		{"tool-version", func() { write(t, repository, ".agents/tool-versions.sh", "CAPSLOCK_VERSION=v1\n") }},
		{"build-tag", func() { write(t, repository, "tagged.go", "//go:build darwin\n\npackage root\n") }},
		{"delete", func() {
			if err := os.Remove(filepath.Join(repository, "root.go")); err != nil {
				t.Fatal(err)
			}
		}},
		{"rename", func() {
			if err := os.Rename(filepath.Join(repository, "root.go"), filepath.Join(repository, "renamed.go")); err != nil {
				t.Fatal(err)
			}
		}},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			run(t, repository, "git", "reset", "--hard", base)
			invokeBootstrapChange(t, repository, test.change, testFullMode)
		})
	}
	t.Run("removed-build-tag", func(t *testing.T) {
		run(t, repository, "git", "reset", "--hard", base)
		write(t, repository, "tagged.go", "//go:build darwin\n\npackage root\n")
		commit(t, repository, "tagged base")
		write(t, repository, "tagged.go", "package root\n")
		commit(t, repository, "remove tag")
		invokeBootstrap(t, repository, "HEAD^", "HEAD", testFullMode)
	})
	t.Run("new-ref", func(t *testing.T) {
		invokeBootstrap(t, repository, strings.Repeat("0", 40), "HEAD", testFullMode)
	})
	t.Run("missing-object", func(t *testing.T) {
		invokeBootstrap(t, repository, strings.Repeat("f", 40), "HEAD", testFullMode)
	})
}

func TestBootstrapGuardAllowsClassifiedChangeAndRejectsShallowHistory(t *testing.T) {
	repository := bootstrapRepository(t)
	base := commit(t, repository, "base")
	appendFile(t, repository, "root.go", "const Changed = true\n")
	head := commit(t, repository, "ordinary")
	invokeBootstrap(t, repository, base, head, "affected")

	clone := filepath.Join(t.TempDir(), "clone")
	run(t, t.TempDir(), "git", "clone", "--quiet", "--depth=1", "file://"+repository, clone)
	invokeBootstrap(t, clone, base, "HEAD", testFullMode)
}

func TestCommandE2EManifestFailsClosed(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "manifest.tsv")
	for _, contents := range []string{
		"mode\taffected\n",
		"command-e2e\tunknown\n",
		"command-e2e\tfalse\ncommand-e2e\ttrue\n",
	} {
		write(t, filepath.Dir(manifest), filepath.Base(manifest), contents)
		command := exec.CommandContext(t.Context(), "bash", "../../scripts/check-command-e2e")
		command.Env = append(os.Environ(), "MANIUD_GATE_MANIFEST="+manifest)
		if output, err := command.CombinedOutput(); err == nil {
			t.Fatalf("check-command-e2e accepted manifest %q: %s", contents, output)
		}
	}
	write(t, filepath.Dir(manifest), filepath.Base(manifest), "command-e2e\tfalse\n")
	command := exec.CommandContext(t.Context(), "bash", "../../scripts/check-command-e2e")
	command.Env = append(os.Environ(), "MANIUD_GATE_MANIFEST="+manifest)
	if output, err := command.CombinedOutput(); err != nil || string(output) != "command E2E not selected\n" {
		t.Fatalf("check-command-e2e false manifest: %v\n%s", err, output)
	}
}

func TestRunGoModulesExpandsOnlyAffectedPackagePatterns(t *testing.T) {
	manifest := filepath.Join(t.TempDir(), "manifest.tsv")
	tests := []struct {
		name     string
		contents string
		want     string
		wantOK   bool
	}{
		{
			name: "affected",
			contents: "mode\taffected\nmodule\t.\n" +
				"package\t.\tgithub.com/IceCodeNew/maniud/internal/changescope\n" +
				"package\t.\tgithub.com/IceCodeNew/maniud/internal/changescope/cmd/changescope\n",
			want: "==> .: printf %s\\n ./internal/changescope ./internal/changescope/cmd/changescope\n" +
				"./internal/changescope\n./internal/changescope/cmd/changescope\n",
			wantOK: true,
		},
		{
			name:     testFullMode,
			contents: "mode\tfull\nmodule\t.\npackage\t.\tpackage/one\n",
			want:     "==> .: printf %s\\n ./...\n./...\n",
			wantOK:   true,
		},
		{name: "empty affected", contents: "mode\taffected\nmodule\t.\n"},
		{name: "outside affected", contents: "mode\taffected\nmodule\t.\npackage\t.\texample.test/outside\n"},
		{name: "invalid", contents: "mode\tunknown\nmodule\t.\n"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			write(t, filepath.Dir(manifest), filepath.Base(manifest), test.contents)
			command := exec.CommandContext(
				t.Context(),
				"bash",
				"../../scripts/run-go-modules",
				"printf",
				"%s\\n",
				"./...",
			)
			command.Env = append(os.Environ(), "MANIUD_GATE_MANIFEST="+manifest)
			output, err := command.CombinedOutput()
			if (err == nil) != test.wantOK || (test.wantOK && string(output) != test.want) {
				t.Fatalf("run-go-modules: %v\n%s\nwant success %t and output %q", err, output, test.wantOK, test.want)
			}
		})
	}
}

func bootstrapRepository(t *testing.T) string {
	t.Helper()
	repository := newRepository(t)
	write(t, repository, "go.mod", "module github.com/IceCodeNew/maniud\n\ngo 1.27\n")
	write(t, repository, "root.go", "package root\n")
	write(t, repository, "cmd/tool/main.go", "package main\nfunc main() {}\n")
	write(t, repository, "internal/changescope/changescope.go", "package changescope\n")
	write(t, repository, "internal/changescope/cmd/changescope/main.go", `package main

import "os"

func main() {
	for index, argument := range os.Args {
		if argument == "--output" && index+1 < len(os.Args) {
			manifest := []byte(
				"mode\taffected\nmodule\t.\n" +
					"package\t.\tgithub.com/IceCodeNew/maniud\ncommand-e2e\ttrue\n",
			)
			_ = os.WriteFile(os.Args[index+1], manifest, 0o600)
			return
		}
	}
	os.Exit(2)
}
`)
	write(t, repository, "plugins/runtime/runtime.go", "package runtime\n")
	copyFile(t, "../../scripts/select-affected-gates", filepath.Join(repository, "scripts/select-affected-gates"))

	return repository
}

func invokeBootstrapChange(t *testing.T, repository string, change func(), mode string) {
	t.Helper()
	change()
	commit(t, repository, "change")
	invokeBootstrap(t, repository, "HEAD^", "HEAD", mode)
}

func invokeBootstrap(t *testing.T, repository, base, head, mode string) {
	t.Helper()
	manifest := filepath.Join(t.TempDir(), "manifest.tsv")
	run(t, repository, "bash", "scripts/select-affected-gates", "--base", base, "--head", head, "--output", manifest)
	contents, err := os.ReadFile(manifest) //nolint:gosec // The path belongs to this test.
	if err != nil {
		t.Fatal(err)
	}
	if !strings.HasPrefix(string(contents), "mode\t"+mode+"\n") {
		t.Fatalf("manifest mode:\n%s\nwant %s", contents, mode)
	}
	if mode == testFullMode && strings.Contains(string(contents), "github.com/IceCodeNew/maniud/cmd/tool") {
		t.Fatalf("full coverage manifest included command package:\n%s", contents)
	}
}

func appendFile(t *testing.T, repository, name, contents string) {
	t.Helper()
	// The repository and relative fixture path belong to this test.
	//nolint:gosec // The path joins a test-owned temporary repository and fixture path.
	file, err := os.OpenFile(filepath.Join(repository, name), os.O_APPEND|os.O_WRONLY, 0)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := file.WriteString(contents); err != nil {
		_ = file.Close()
		t.Fatal(err)
	}
	if err := file.Close(); err != nil {
		t.Fatal(err)
	}
}

func copyFile(t *testing.T, source, destination string) {
	t.Helper()
	contents, err := os.ReadFile(source) //nolint:gosec // The caller supplies a repository-owned fixture path.
	if err != nil {
		t.Fatal(err)
	}
	if err := os.MkdirAll(filepath.Dir(destination), 0o700); err != nil {
		t.Fatal(err)
	}
	//nolint:gosec // The copied gate script must remain executable in the fixture.
	if err := os.WriteFile(destination, contents, 0o700); err != nil {
		t.Fatal(err)
	}
}
