//nolint:paralleltest // Process-level PATH and TMPDIR fault tests require package-wide serialization.
package changescope

import (
	"bytes"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

const (
	testFullMode   = "full"
	testRootModule = "example.test/root"
)

var errChangescopeTest = errors.New("changescope test failure")

func TestSelectUsesBothPackageGraphsAndLocalReplaceReverseDependents(t *testing.T) {
	repository := newRepository(t)
	write(t, repository, "go.mod", `module example.test/root

go 1.27

require example.test/lib v0.0.0
replace example.test/lib => "./lib"
`)
	write(t, repository, "root.go", "package root\nimport _ \"example.test/lib/leaf\"\n")
	write(t, repository, "consumer/consumer.go", "package consumer\nimport _ \"example.test/root\"\n")
	write(t, repository, "lib/go.mod", "module example.test/lib\n\ngo 1.27\n")
	write(t, repository, "lib/leaf/leaf.go", "package leaf\n")
	write(t, repository, "bridge/go.mod", `module example.test/bridge

go 1.27

require example.test/lib v0.0.0
replace example.test/lib => "../lib"
`)
	write(t, repository, "bridge/bridge.go", "package bridge\n")
	base := commit(t, repository, "base")
	write(t, repository, "lib/leaf/leaf.go", "package leaf\nconst Changed = true\n")
	write(t, repository, "root.go", "package root\n")
	head := commit(t, repository, "head")

	manifest, err := Select(repository, base, head, []string{"lib/leaf/leaf.go"})
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(manifest.Modules, ",") != ".,bridge,lib" {
		t.Fatalf("modules = %v, want root, changed module, and locally replacing dependent", manifest.Modules)
	}
	rootPackages := strings.Join(manifest.Packages["."], ",")
	if !strings.Contains(rootPackages, testRootModule) ||
		!strings.Contains(rootPackages, "example.test/root/consumer") {
		t.Fatalf("root package closure = %q, want base-side importers", rootPackages)
	}
	if got := strings.Join(manifest.Packages["bridge"], ","); got != "example.test/bridge" {
		t.Fatalf("bridge packages = %q, want module-wide local-replace dependent", got)
	}
	if !manifest.CommandE2E {
		t.Fatal("root module selection must enable command E2E")
	}
}

func TestReplacementDependentsCloseAcrossBothGraphs(t *testing.T) {
	selected := selection{
		modules:    map[string]bool{"a": true},
		moduleWide: map[string]bool{},
	}
	selected.selectReplacementDependents([]*side{
		{replaces: map[string][]string{"c": {"d"}}},
		{replaces: map[string][]string{"a": {"c"}}},
	})
	for _, module := range []string{"a", "c", "d"} {
		if !selected.modules[module] {
			t.Fatalf("module %q not selected in cross-revision closure", module)
		}
	}
	for _, module := range []string{"c", "d"} {
		if !selected.moduleWide[module] {
			t.Fatalf("dependent module %q not expanded module-wide", module)
		}
	}
}

func TestSelectKeepsOrdinaryPackageChangesPackageScoped(t *testing.T) {
	repository := newRepository(t)
	write(t, repository, "go.mod", "module example.test/root\n\ngo 1.27\n")
	write(t, repository, "changed/changed.go", "package changed\n")
	write(t, repository, "importer/importer.go", "package importer\nimport _ \"example.test/root/changed\"\n")
	write(t, repository, "unrelated/unrelated.go", "package unrelated\n")
	base := commit(t, repository, "base")
	write(t, repository, "changed/changed.go", "package changed\nconst Changed = true\n")
	head := commit(t, repository, "head")

	manifest, err := Select(repository, base, head, []string{"changed/changed.go"})
	if err != nil {
		t.Fatal(err)
	}
	got := strings.Join(manifest.Packages["."], ",")
	want := "example.test/root/changed,example.test/root/importer"
	if got != want {
		t.Fatalf("packages = %q, want %q", got, want)
	}
}

func TestSelectRejectsEmptyChangedPath(t *testing.T) {
	repository := newRepository(t)
	write(t, repository, "go.mod", "module example.test/root\n\ngo 1.27\n")
	write(t, repository, "root.go", "package root\n")
	revision := commit(t, repository, "base")
	if _, err := Select(repository, revision, revision, []string{""}); err == nil {
		t.Fatal("Select() accepted an empty changed path")
	}
}

func TestSelectExpandsUnclassifiedPackagePathToModule(t *testing.T) {
	repository := newRepository(t)
	write(t, repository, "go.mod", "module example.test/root\n\ngo 1.27\n")
	write(t, repository, "root.go", "package root\n")
	write(t, repository, "unrelated/unrelated.go", "package unrelated\n")
	revision := commit(t, repository, "base")
	manifest, err := Select(repository, revision, revision, []string{"outside/unknown.txt"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(manifest.Packages["."], ","); got != "example.test/root,example.test/root/unrelated" {
		t.Fatalf("module expansion packages = %q", got)
	}
}

func TestSelectKeepsPackagesRemovedWithTheirModule(t *testing.T) {
	repository := newRepository(t)
	write(t, repository, "README", "base\n")
	write(t, repository, "old/go.mod", "module example.test/old\n\ngo 1.27\n")
	write(t, repository, "old/old.go", "package old\n")
	base := commit(t, repository, "base")
	if err := os.RemoveAll(filepath.Join(repository, "old")); err != nil {
		t.Fatal(err)
	}
	write(t, repository, "README", "head\n")
	head := commit(t, repository, "head")

	manifest, err := Select(repository, base, head, []string{"old/old.go"})
	if err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(manifest.Modules, ","); got != "old" {
		t.Fatalf("modules = %q, want removed module", got)
	}
	if got := strings.Join(manifest.Packages["old"], ","); got != "example.test/old" {
		t.Fatalf("packages = %q, want removed package", got)
	}
}

func TestWriteTypedManifest(t *testing.T) {
	manifest := Manifest{
		Mode:       testFullMode,
		Modules:    []string{"."},
		Packages:   map[string][]string{".": {testRootModule}},
		CommandE2E: true,
	}
	var output bytes.Buffer
	if err := Write(&output, manifest); err != nil {
		t.Fatal(err)
	}
	want := "mode\tfull\nmodule\t.\npackage\t.\t" + testRootModule + "\ncommand-e2e\ttrue\n"
	if output.String() != want {
		t.Fatalf("manifest:\n%s\nwant:\n%s", output.String(), want)
	}
}

func TestSelectionRejectsInvalidRevisionsAndUnownedPaths(t *testing.T) {
	repository := newRepository(t)
	write(t, repository, "nested/go.mod", "module example.test/nested\n\ngo 1.27\n")
	write(t, repository, "nested/nested.go", "package nested\n")
	revision := commit(t, repository, "base")
	tests := []struct {
		name    string
		base    string
		head    string
		paths   []string
		message string
	}{
		{name: "base", base: "missing", head: revision, paths: []string{"nested/nested.go"}, message: "load base"},
		{name: "head", base: revision, head: "missing", paths: []string{"nested/nested.go"}, message: "load head"},
		{name: "unowned", base: revision, head: revision, paths: []string{"outside/file.go"}, message: "cannot classify"},
		{name: "empty set", base: revision, head: revision, message: "selector produced no modules"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := Select(repository, test.base, test.head, test.paths)
			if err == nil || !strings.Contains(err.Error(), test.message) {
				t.Fatalf("Select() error = %v, want %q", err, test.message)
			}
		})
	}
}

func TestWriteReportsEveryOutputFailure(t *testing.T) {
	manifest := Manifest{
		Mode:       "affected",
		Modules:    []string{"."},
		Packages:   map[string][]string{".": {testRootModule}},
		CommandE2E: true,
	}
	for failAt := 1; failAt <= 4; failAt++ {
		writer := &failAtChangescopeWriter{failAt: failAt}
		if err := Write(writer, manifest); !errors.Is(err, errChangescopeTest) {
			t.Fatalf("Write(fail at %d) error = %v", failAt, err)
		}
	}
}

//nolint:cyclop,funlen,gocognit // Each subtest exercises a distinct fail-closed loader boundary.
func TestLoaderReportsFilesystemAndPackageFailures(t *testing.T) {
	t.Run("temporary root", func(t *testing.T) {
		t.Setenv("TMPDIR", filepath.Join(t.TempDir(), "missing"))
		if _, cleanup, err := loadSide(t.TempDir(), "HEAD"); err == nil {
			cleanup()
			t.Fatal("loadSide() accepted an unavailable temporary root")
		}
	})

	t.Run("temporary directory", func(t *testing.T) {
		root := t.TempDir()
		file := filepath.Join(root, "file")
		write(t, root, "file", "not a directory\n")
		t.Setenv("TMPDIR", file)
		if _, err := createTemporaryDirectory(); err == nil {
			t.Fatal("createTemporaryDirectory() accepted a file root")
		}
	})

	t.Run("module type", func(t *testing.T) {
		root := t.TempDir()
		if err := os.Mkdir(filepath.Join(root, "go.mod"), 0o700); err != nil {
			t.Fatal(err)
		}
		if err := loadModules(root, emptySide(root)); err == nil {
			t.Fatal("loadModules() accepted a go.mod directory")
		}
	})

	t.Run("module read", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "go.mod", "module example.test/root\n\ngo 1.27\n")
		path := filepath.Join(root, "go.mod")
		if err := os.Chmod(path, 0); err != nil {
			t.Fatal(err)
		}
		t.Cleanup(func() { _ = os.Chmod(path, 0o600) })
		if err := loadModules(root, emptySide(root)); err == nil {
			t.Fatal("loadModules() accepted an unreadable go.mod")
		}
	})

	t.Run("module symlink", func(t *testing.T) {
		root := t.TempDir()
		outside := filepath.Join(t.TempDir(), "outside.mod")
		write(t, filepath.Dir(outside), filepath.Base(outside), "module example.test/outside\n\ngo 1.27\n")
		if err := os.Symlink(outside, filepath.Join(root, "go.mod")); err != nil {
			t.Fatal(err)
		}
		if err := loadModules(root, emptySide(root)); !errors.Is(err, errRevisionSymlink) {
			t.Fatalf("loadModules(module symlink) error = %v", err)
		}
	})

	t.Run("source symlink", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "go.mod", "module example.test/root\n\ngo 1.27\n")
		if err := os.Symlink(filepath.Join(t.TempDir(), "outside.go"), filepath.Join(root, "outside.go")); err != nil {
			t.Fatal(err)
		}
		if err := loadModules(root, emptySide(root)); !errors.Is(err, errRevisionSymlink) {
			t.Fatalf("loadModules(source symlink) error = %v", err)
		}
	})

	t.Run("module walk", func(t *testing.T) {
		if err := loadModules(filepath.Join(t.TempDir(), "missing"), emptySide("")); err == nil {
			t.Fatal("loadModules() accepted a missing root")
		}
	})

	for _, test := range []struct {
		name     string
		contents string
	}{
		{name: "syntax", contents: "not a go.mod\n"},
		{name: "directive", contents: "go 1.27\n"},
	} {
		t.Run(test.name, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "go.mod", test.contents)
			if err := loadModules(root, emptySide(root)); err == nil {
				t.Fatalf("loadModules() accepted %s", test.name)
			}
		})
	}

	for _, replacement := range []string{"..", "../outside", "/outside"} {
		t.Run("replacement "+replacement, func(t *testing.T) {
			root := t.TempDir()
			write(t, root, "go.mod", "module example.test/root\n\ngo 1.27\n\nreplace example.test/outside => "+replacement+"\n")
			if err := loadModules(root, emptySide(root)); !errors.Is(err, errReplacementOutside) {
				t.Fatalf("loadModules(outside replacement) error = %v", err)
			}
		})
	}

	t.Run("go list", func(t *testing.T) {
		root := t.TempDir()
		write(t, root, "go.mod", "module example.test/root\n\ngo 1.27\n")
		write(t, root, "bad.go", "package\n")
		if err := loadPackages(emptySide(root), "."); err == nil {
			t.Fatal("loadPackages() accepted invalid Go source")
		}
	})

	t.Run("load side module", func(t *testing.T) {
		repository := newRepository(t)
		write(t, repository, "go.mod/child", "not a module\n")
		revision := commit(t, repository, "bad module")
		if _, cleanup, err := loadSide(repository, revision); err == nil {
			cleanup()
			t.Fatal("loadSide() accepted an unreadable module path")
		}
	})

	t.Run("load side package", func(t *testing.T) {
		repository := newRepository(t)
		write(t, repository, "go.mod", "module example.test/root\n\ngo 1.27\n")
		write(t, repository, "bad.go", "package\n")
		revision := commit(t, repository, "bad package")
		if _, cleanup, err := loadSide(repository, revision); err == nil {
			cleanup()
			t.Fatal("loadSide() accepted invalid Go source")
		}
	})
}

func TestModuleAndPackageDecodersHandleSupportedInputs(t *testing.T) {
	root := t.TempDir()
	write(t, root, "go.mod", `module example.test/root

go 1.27

replace (
	example.test/local => "./lib"
	example.test/remote v1.0.0 => example.test/other v1.1.0
)
`)
	write(t, root, "lib/go.mod", "module example.test/local\n\ngo 1.27\n")
	graph := emptySide(root)
	if err := loadModules(root, graph); err != nil {
		t.Fatal(err)
	}
	if graph.modules["."] != "example.test/root" || graph.modules["lib"] != "example.test/local" ||
		strings.Join(graph.replaces["lib"], ",") != "." || len(graph.replaces) != 1 {
		t.Fatalf("module graph = %#v, replacements %#v", graph.modules, graph.replaces)
	}

	output := fmt.Sprintf(
		"{\"ImportPath\":\"fmt\",\"Dir\":%q,\"Standard\":true}\n"+
			"{\"ImportPath\":\"example.test/root\",\"Dir\":%q,"+
			"\"Imports\":[\"one\"],\"TestImports\":[\"two\"],\"XTestImports\":[\"three\"]}\n",
		root,
		root,
	)
	if err := decodePackages(graph, []byte(output)); err != nil {
		t.Fatal(err)
	}
	if got := strings.Join(graph.imports["example.test/root"], ","); got != "one,two,three" {
		t.Fatalf("decoded imports = %q", got)
	}
	if err := decodePackages(graph, []byte("{")); err == nil {
		t.Fatal("decodePackages() accepted malformed JSON")
	}
	outside := fmt.Sprintf("{\"ImportPath\":\"outside\",\"Dir\":%q}\n", filepath.Dir(root))
	if err := decodePackages(graph, []byte(outside)); err == nil {
		t.Fatal("decodePackages() accepted an outside package directory")
	}
}

func TestExtractRevisionReportsProcessFailures(t *testing.T) {
	repository := newRepository(t)
	write(t, repository, "file", "content\n")
	revision := commit(t, repository, "base")

	t.Run("start", func(t *testing.T) {
		bin := t.TempDir()
		linkCommand(t, bin, "git")
		t.Setenv("PATH", bin)
		if err := extractRevision(repository, revision, t.TempDir()); err == nil {
			t.Fatal("extractRevision() started without tar")
		}
	})

	t.Run("archive", func(t *testing.T) {
		if err := extractRevision(repository, "missing", t.TempDir()); err == nil {
			t.Fatal("extractRevision() accepted an invalid revision")
		}
	})

	t.Run("extract", func(t *testing.T) {
		bin := t.TempDir()
		linkCommand(t, bin, "git")
		writeExecutable(t, filepath.Join(bin, "tar"), "#!/bin/sh\n/bin/cat >/dev/null\nexit 7\n")
		t.Setenv("PATH", bin)
		if err := extractRevision(repository, revision, t.TempDir()); err == nil {
			t.Fatal("extractRevision() accepted a failing tar")
		}
	})
}

func TestOwnershipHelpersRejectMissingOwners(t *testing.T) {
	if _, ok := owningKey("outside/file.go", map[string]string{"nested": "module"}); ok {
		t.Fatal("owningKey() found an unrelated module")
	}
	if _, ok := moduleForPackage("example.test/other", &side{modules: map[string]string{".": "example.test/root"}}); ok {
		t.Fatal("moduleForPackage() found an unrelated module")
	}
	selected := selection{packages: map[string]bool{"example.test/changed": true}}
	selected.expandImporters([]*side{{imports: map[string][]string{
		"example.test/importer": {"example.test/unrelated", "example.test/changed"},
	}}})
	if !selected.packages["example.test/importer"] {
		t.Fatal("expandImporters() omitted an importer after an unrelated dependency")
	}
}

type failAtChangescopeWriter struct {
	writes int
	failAt int
}

func (writer *failAtChangescopeWriter) Write(value []byte) (int, error) {
	writer.writes++
	if writer.writes == writer.failAt {
		return 0, errChangescopeTest
	}

	return len(value), nil
}

func emptySide(root string) *side {
	return &side{
		root:     root,
		modules:  map[string]string{},
		packages: map[string]string{},
		imports:  map[string][]string{},
		replaces: map[string][]string{},
	}
}

func linkCommand(t *testing.T, directory, name string) {
	t.Helper()
	path, err := exec.LookPath(name)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink(path, filepath.Join(directory, name)); err != nil {
		t.Fatal(err)
	}
}

func writeExecutable(t *testing.T, path, contents string) {
	t.Helper()
	//nolint:gosec // Process-failure fixtures must be executable by the current user.
	if err := os.WriteFile(path, []byte(contents), 0o700); err != nil {
		t.Fatal(err)
	}
}

func newRepository(t *testing.T) string {
	t.Helper()
	repository := t.TempDir()
	run(t, repository, "git", "init", "--quiet", "--initial-branch=main")

	return repository
}

func write(t *testing.T, repository, name, contents string) {
	t.Helper()
	path := filepath.Join(repository, name)
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, []byte(contents), 0o600); err != nil {
		t.Fatal(err)
	}
}

func commit(t *testing.T, repository, message string) string {
	t.Helper()
	run(t, repository, "git", "add", ".")
	run(
		t,
		repository,
		"git",
		"-c", "user.name=test",
		"-c", "user.email=test@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", message,
	)

	return strings.TrimSpace(run(t, repository, "git", "rev-parse", "HEAD"))
}

func run(t *testing.T, directory, name string, arguments ...string) string {
	t.Helper()
	//nolint:gosec // Tests invoke fixed local tools with fixture-controlled arguments.
	command := exec.CommandContext(t.Context(), name, arguments...)
	command.Dir = directory
	output, err := command.CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v\n%s", name, arguments, err, output)
	}

	return string(output)
}
