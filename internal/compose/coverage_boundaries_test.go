package compose

import (
	"errors"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"
)

const (
	composeTestWorkingDirectory = "/work"
)

var errMaterializeTest = errors.New("materialize test failure")

//nolint:cyclop,funlen // Each branch verifies one independent exact-snapshot invariant.
func TestMaterializeRuntimeExactSnapshotAndFailClosed(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	content := []byte("services: {}\n")
	files := map[string]RepositoryFile{
		remainingComposeEntry: {Content: content},
		"data/a.txt":          {Content: []byte("a\n")},
		"data/bin/run":        {Content: []byte("#!/bin/sh\n"), Executable: true},
	}
	snapshot := &RepositorySnapshot{Root: t.TempDir(), Entry: remainingComposeEntry, Files: files,
		RuntimePaths: []RepositoryPath{{Path: "data", Directory: true}}}
	snapshot.Digest = repositoryDigest(snapshot.Entry, snapshot.Files, snapshot.RuntimePaths)
	source := Source{Content: content, WorkingDir: snapshot.Root, Repository: snapshot}
	pinned, err := PinRepositoryRuntime(source, base)
	if err != nil || pinned.runtimeBase != base {
		t.Fatalf("PinRepositoryRuntime() = %#v, %v", pinned, err)
	}
	if err := pinned.MaterializeRuntime(); err != nil {
		t.Fatalf("MaterializeRuntime() error = %v", err)
	}
	baseRoot, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := baseRoot.Close(); closeErr != nil {
			t.Errorf("close base root: %v", closeErr)
		}
	})
	if err := pinned.materializeRuntime(baseRoot, func(string) (os.FileInfo, error) {
		return nil, errMaterializeTest
	}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("materialize lstat failure = %v", err)
	}
	root := repositoryRuntimeRoot(base, snapshot.Digest)
	for name, want := range files {
		if name == snapshot.Entry {
			continue
		}
		runtimeRoot, openErr := os.OpenRoot(root)
		if openErr != nil {
			t.Fatal(openErr)
		}
		t.Cleanup(func() {
			if closeErr := runtimeRoot.Close(); closeErr != nil {
				t.Errorf("close runtime root: %v", closeErr)
			}
		})
		got, readErr := runtimeRoot.ReadFile(filepath.FromSlash(name))
		info, statErr := runtimeRoot.Stat(filepath.FromSlash(name))
		mode := runtimeFileMode
		if want.Executable {
			mode = runtimeExecutableMode
		}
		if readErr != nil || statErr != nil || !reflect.DeepEqual(got, want.Content) || info.Mode().Perm() != mode {
			t.Fatalf("materialized %s = %q mode %o, errors %v/%v", name, got, info.Mode().Perm(), readErr, statErr)
		}
	}
	if err := pinned.MaterializeRuntime(); err != nil {
		t.Fatalf("idempotent materialize: %v", err)
	}
	if err := os.WriteFile(filepath.Join(root, "extra"), []byte("x"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := pinned.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("extra entry error = %v", err)
	}
	if err := os.Remove(filepath.Join(root, "extra")); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(filepath.Join(root, "data", "a.txt"), 0o600); err != nil {
		t.Fatal(err)
	}
	if err := pinned.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("mode drift error = %v", err)
	}
}

//nolint:cyclop // Each branch verifies one independent fail-closed boundary.
func TestMaterializeRuntimeRejectsInvalidBoundaries(t *testing.T) {
	t.Parallel()

	var source Source
	if err := source.MaterializeRuntime(); err != nil {
		t.Fatal(err)
	}
	if _, err := PinRepositoryRuntime(source, t.TempDir()); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	source.Repository = &RepositorySnapshot{RuntimePaths: []RepositoryPath{{Path: "data", Directory: true}}}
	if err := source.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if _, err := PinRepositoryRuntime(source, "relative"); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	root, err := os.OpenRoot(t.TempDir())
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})
	if err := root.Mkdir(repositoryRuntimeDirectory, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimeParent(root); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if err := syncRootDirectory(root, "missing"); !errors.Is(err, ErrInvalidSource) {
		t.Fatal(err)
	}
	if validMaterializedRuntime(root, "missing", &RepositorySnapshot{}) {
		t.Fatal("missing runtime accepted")
	}
}

func TestRuntimeEnvironmentFiles(t *testing.T) {
	t.Parallel()

	if got, err := runtimeEnvironmentFiles(nil, composeTestWorkingDirectory); err != nil || got != nil {
		t.Fatal(got, err)
	}
	if _, err := runtimeEnvironmentFiles(
		[]string{composeTestWorkingDirectory},
		composeTestWorkingDirectory,
	); err == nil {
		t.Fatal("directory env accepted")
	}
	got, err := runtimeEnvironmentFiles([]string{"/work/a.env"}, composeTestWorkingDirectory)
	if err != nil || !reflect.DeepEqual(got, []string{"a.env"}) {
		t.Fatal(got, err)
	}
}

//nolint:cyclop // The table verifies independent malformed repository-document shapes.
func TestRepositoryCollectorMalformedShapes(t *testing.T) {
	t.Parallel()

	var docs []repositoryDocument
	invalidIncludes := []any{
		[]any{},
		[]any{1},
		[]any{map[string]any{repositoryPathKey: "a", "extra": true}},
		[]any{map[string]any{repositoryPathKey: nil}},
	}
	for _, raw := range invalidIncludes {
		if collectIncludes(raw, ".", &docs) {
			t.Fatalf("include accepted %#v", raw)
		}
	}
	invalidPaths := []any{map[string]any{"extra": 1}}
	if collectResourceFiles("bad", ".", &docs) ||
		collectExtends("bad", ".", &docs) ||
		collectPathList(invalidPaths, ".", &docs) {
		t.Fatal("collector accepted malformed")
	}
	if _, ok := repositoryPaths(nil, "."); ok {
		t.Fatal("nil paths")
	}
	if collectBindMounts("bad", ".", new([]string)) || collectBindMounts([]any{1}, ".", new([]string)) {
		t.Fatal("bind shape")
	}
	for _, raw := range []any{"", "/abs", "a:b", "$A", "~user/a", 1} {
		if _, ok := resolveRepositoryPath(raw, "."); ok {
			t.Fatalf("path accepted %#v", raw)
		}
	}
	if _, ok := repositoryEnvironment(nil, RepositoryFile{Content: []byte("bad='")}); ok {
		t.Fatal("bad dotenv")
	}
	if _, ok := repositoryDefaultEnvironment(".", map[string]string{composeDisableEnvFile: "maybe"}); ok {
		t.Fatal("bad bool")
	}
	value, ok := repositoryDefaultEnvironment(".", map[string]string{composeDisableEnvFile: "false"})
	if !ok || value == "" {
		t.Fatalf("false disable flag = %q, %t", value, ok)
	}
	if strings.TrimSpace(resolveRepositoryDefaultEnv(".")) == "" {
		t.Fatal("default env")
	}
}
