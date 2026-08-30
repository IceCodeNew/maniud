// Package changescope computes the conservative Go gate scope between two Git revisions.
package changescope

import (
	"bytes"
	"context"
	"encoding/json/jsontext"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"maps"
	"os"
	"os/exec"
	"path/filepath"
	"slices"
	"strings"

	"golang.org/x/mod/modfile"
)

// Manifest is the typed input consumed by affected gate scripts.
type Manifest struct {
	Mode       string
	Modules    []string
	Packages   map[string][]string
	CommandE2E bool
}

type side struct {
	root     string
	modules  map[string]string // repository directory to module import path
	packages map[string]string // repository directory to package import path
	imports  map[string][]string
	replaces map[string][]string // replaced module directory to dependent module directory
}

type listedPackage struct {
	ImportPath   string
	Dir          string
	Imports      []string
	TestImports  []string
	XTestImports []string
	Standard     bool
}

type selection struct {
	packages   map[string]bool
	modules    map[string]bool
	moduleWide map[string]bool
}

var (
	errChangedPathEmpty  = errors.New("changed path is empty")
	errPathUnclassified  = errors.New("cannot classify changed path")
	errNoModulesSelected = errors.New("selector produced no modules")
	errModuleMissing     = errors.New("missing module directive")
	errPackageOutside    = errors.New("package directory is outside the revision")
)

// Select computes affected modules and the reverse package-import closure from both revisions.
// It deliberately returns an error rather than an empty manifest when a path cannot be classified.
func Select(repository, base, head string, changedPaths []string) (Manifest, error) {
	baseSide, cleanupBase, err := loadSide(repository, base)
	if err != nil {
		return Manifest{}, fmt.Errorf("load base: %w", err)
	}
	defer cleanupBase()
	headSide, cleanupHead, err := loadSide(repository, head)
	if err != nil {
		return Manifest{}, fmt.Errorf("load head: %w", err)
	}
	defer cleanupHead()

	selected := selection{
		packages:   map[string]bool{},
		modules:    map[string]bool{},
		moduleWide: map[string]bool{},
	}
	graphs := []*side{baseSide, headSide}
	if err := selected.classify(changedPaths, graphs); err != nil {
		return Manifest{}, err
	}
	selected.expandImporters(graphs)
	selected.selectPackageModules(graphs)
	selected.selectReplacementDependents(graphs)
	selected.selectModulePackages(graphs)

	return selected.manifest(headSide, baseSide)
}

func (selected *selection) classify(changedPaths []string, graphs []*side) error {
	for _, path := range changedPaths {
		if path == "" {
			return errChangedPathEmpty
		}
		classified := false
		for _, graph := range graphs {
			if pkg, ok := packageAtPath(path, graph.packages); ok {
				selected.packages[pkg] = true
				classified = true
			} else if module, ok := owningKey(path, graph.modules); ok {
				selected.modules[module] = true
				selected.moduleWide[module] = true
				classified = true
			}
		}
		if !classified {
			return fmt.Errorf("%w: %q", errPathUnclassified, path)
		}
	}

	return nil
}

func (selected *selection) expandImporters(graphs []*side) {
	allImports := map[string][]string{}
	for _, graph := range graphs {
		allImports = mergeEdges(allImports, graph.imports)
	}
	for changed := range selected.packages {
		for importer := range reverseClosure(changed, allImports) {
			selected.packages[importer] = true
		}
	}
}

func (selected *selection) selectPackageModules(graphs []*side) {
	for pkg := range selected.packages {
		for _, graph := range graphs {
			if module, ok := moduleForPackage(pkg, graph); ok {
				selected.modules[module] = true
			}
		}
	}
}

func (selected *selection) selectReplacementDependents(graphs []*side) {
	allReplacements := map[string][]string{}
	for _, graph := range graphs {
		allReplacements = mergeEdges(allReplacements, graph.replaces)
	}
	queue := slices.Collect(maps.Keys(selected.modules))
	for len(queue) > 0 {
		module := queue[0]
		queue = queue[1:]
		for _, dependent := range allReplacements[module] {
			if !selected.modules[dependent] {
				selected.modules[dependent] = true
				selected.moduleWide[dependent] = true
				queue = append(queue, dependent)
			}
		}
	}
}

// selectModulePackages prevents module inputs and local-replace dependents
// from turning non-Go resource changes into an empty green result.
func (selected *selection) selectModulePackages(graphs []*side) {
	for _, graph := range graphs {
		for pkg := range graph.imports {
			if module, ok := moduleForPackage(pkg, graph); ok && selected.moduleWide[module] {
				selected.packages[pkg] = true
			}
		}
	}
}

func (selected *selection) manifest(graphs ...*side) (Manifest, error) {
	manifest := Manifest{Mode: "affected", Packages: map[string][]string{}}
	manifest.Modules = slices.Sorted(maps.Keys(selected.modules))
	for pkg := range selected.packages {
		for _, graph := range graphs {
			if module, ok := moduleForPackage(pkg, graph); ok && selected.modules[module] {
				manifest.Packages[module] = append(manifest.Packages[module], pkg)

				break
			}
		}
	}
	for module := range manifest.Packages {
		slices.Sort(manifest.Packages[module])
		manifest.Packages[module] = slices.Compact(manifest.Packages[module])
	}
	manifest.CommandE2E = selected.modules["."]
	if len(manifest.Modules) == 0 {
		return Manifest{}, errNoModulesSelected
	}

	return manifest, nil
}

// Write emits a stable typed TSV manifest.
func Write(w io.Writer, manifest Manifest) error {
	if _, err := fmt.Fprintf(w, "mode\t%s\n", manifest.Mode); err != nil {
		return fmt.Errorf("write manifest mode: %w", err)
	}
	for _, module := range manifest.Modules {
		if _, err := fmt.Fprintf(w, "module\t%s\n", module); err != nil {
			return fmt.Errorf("write manifest module: %w", err)
		}
		for _, pkg := range manifest.Packages[module] {
			if _, err := fmt.Fprintf(w, "package\t%s\t%s\n", module, pkg); err != nil {
				return fmt.Errorf("write manifest package: %w", err)
			}
		}
	}
	if _, err := fmt.Fprintf(w, "command-e2e\t%t\n", manifest.CommandE2E); err != nil {
		return fmt.Errorf("write manifest command gate: %w", err)
	}

	return nil
}

func loadSide(repository, revision string) (*side, func(), error) {
	temporary, err := createTemporaryDirectory()
	if err != nil {
		return nil, func() {}, err
	}
	cleanup := func() { _ = os.RemoveAll(temporary) }
	if err := extractRevision(repository, revision, temporary); err != nil {
		cleanup()

		return nil, func() {}, err
	}
	graph := &side{
		root:     temporary,
		modules:  map[string]string{},
		packages: map[string]string{},
		imports:  map[string][]string{},
		replaces: map[string][]string{},
	}
	if err := loadModules(temporary, graph); err != nil {
		cleanup()

		return nil, func() {}, err
	}
	for moduleDirectory := range graph.modules {
		if err := loadPackages(graph, moduleDirectory); err != nil {
			cleanup()

			return nil, func() {}, err
		}
	}

	return graph, cleanup, nil
}

func createTemporaryDirectory() (string, error) {
	root, err := filepath.EvalSymlinks(os.TempDir())
	if err != nil {
		return "", fmt.Errorf("resolve temporary root: %w", err)
	}
	temporary, err := os.MkdirTemp(root, "maniud-changescope-")
	if err != nil {
		return "", fmt.Errorf("create temporary directory: %w", err)
	}

	return temporary, nil
}

func extractRevision(repository, revision, destination string) error {
	// The executable names are fixed; Git receives only opaque revisions and paths.
	//nolint:gosec // The executable name is fixed and Git treats revisions and paths as arguments.
	archive := exec.CommandContext(context.Background(), "git", "-C", repository, "archive", "--format=tar", revision)
	//nolint:gosec // The executable name and tar extraction arguments are fixed.
	extract := exec.CommandContext(context.Background(), "tar", "-xf", "-", "-C", destination)
	pipe, _ := archive.StdoutPipe()
	extract.Stdin = pipe
	var archiveStderr, extractStderr bytes.Buffer
	archive.Stderr = &archiveStderr
	extract.Stderr = &extractStderr
	if err := extract.Start(); err != nil {
		return fmt.Errorf("start archive extraction: %w", err)
	}
	if err := archive.Run(); err != nil {
		_ = extract.Wait()

		return fmt.Errorf("git archive: %w: %s", err, archiveStderr.String())
	}
	if err := extract.Wait(); err != nil {
		return fmt.Errorf("extract archive: %w: %s", err, extractStderr.String())
	}

	return nil
}

func loadModules(root string, graph *side) error {
	err := filepath.WalkDir(root, func(path string, entry os.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.Name() != "go.mod" {
			return nil
		}
		relative, _ := filepath.Rel(root, filepath.Dir(path))
		relative = filepath.ToSlash(relative)
		// WalkDir supplies paths rooted in the private extracted revision.
		//nolint:gosec // The path comes from WalkDir under the private extracted revision.
		contents, err := os.ReadFile(path)
		if err != nil {
			return fmt.Errorf("read module file: %w", err)
		}
		file, err := modfile.Parse(path, contents, nil)
		if err != nil {
			return fmt.Errorf("parse module file: %w", err)
		}
		if file.Module == nil {
			return fmt.Errorf("%w: %s", errModuleMissing, relative)
		}
		graph.modules[relative] = file.Module.Mod.Path
		for _, replacement := range file.Replace {
			if !modfile.IsDirectoryPath(replacement.New.Path) {
				continue
			}
			target := filepath.Clean(filepath.Join(relative, replacement.New.Path))
			target = filepath.ToSlash(target)
			graph.replaces[target] = append(graph.replaces[target], relative)
		}

		return nil
	})
	if err != nil {
		return fmt.Errorf("load revision modules: %w", err)
	}

	return nil
}

func loadPackages(graph *side, moduleDirectory string) error {
	command := exec.CommandContext(context.Background(), "go", "list", "-json", "./...")
	command.Dir = filepath.Join(graph.root, moduleDirectory)
	command.Env = append(os.Environ(), "GOWORK=off", "CGO_ENABLED=0")
	output, err := command.Output()
	if err != nil {
		return fmt.Errorf("go list %s: %w", moduleDirectory, err)
	}

	return decodePackages(graph, output)
}

func decodePackages(graph *side, output []byte) error {
	decoder := jsontext.NewDecoder(bytes.NewReader(output))
	for {
		var pkg listedPackage
		if err := json.UnmarshalDecode(decoder, &pkg); errors.Is(err, io.EOF) {
			break
		} else if err != nil {
			return fmt.Errorf("decode listed package: %w", err)
		}
		if pkg.Standard {
			continue
		}
		relative, _ := filepath.Rel(graph.root, pkg.Dir)
		if relative == ".." || strings.HasPrefix(relative, ".."+string(filepath.Separator)) {
			return fmt.Errorf("%w: %q", errPackageOutside, pkg.Dir)
		}
		graph.packages[filepath.ToSlash(relative)] = pkg.ImportPath
		graph.imports[pkg.ImportPath] = append(append(pkg.Imports, pkg.TestImports...), pkg.XTestImports...)
	}

	return nil
}

func packageAtPath(path string, packages map[string]string) (string, bool) {
	directory := filepath.ToSlash(filepath.Dir(path))
	pkg, ok := packages[directory]

	return pkg, ok
}

func owningKey[T any](path string, values map[string]T) (string, bool) {
	directory := filepath.ToSlash(filepath.Dir(path))
	for {
		if _, ok := values[directory]; ok {
			return directory, true
		}
		if directory == "." {
			return "", false
		}
		directory = filepath.ToSlash(filepath.Dir(directory))
	}
}

func moduleForPackage(pkg string, graph *side) (string, bool) {
	selected := ""
	selectedPath := ""
	for directory, path := range graph.modules {
		if (pkg == path || strings.HasPrefix(pkg, path+"/")) && len(path) > len(selectedPath) {
			selected, selectedPath = directory, path
		}
	}

	return selected, selectedPath != ""
}

func mergeEdges(graphs ...map[string][]string) map[string][]string {
	result := map[string][]string{}
	for _, graph := range graphs {
		for pkg, imports := range graph {
			result[pkg] = append(result[pkg], imports...)
		}
	}

	return result
}

func reverseClosure(changed string, imports map[string][]string) map[string]bool {
	result := map[string]bool{changed: true}
	for grew := true; grew; {
		grew = false
		for pkg, dependencies := range imports {
			if result[pkg] {
				continue
			}
			for _, dependency := range dependencies {
				if result[dependency] {
					result[pkg], grew = true, true

					break
				}
			}
		}
	}

	return result
}
