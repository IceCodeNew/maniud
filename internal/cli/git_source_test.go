package cli

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

const gitRegularContent = "regular\n"

func TestLoadTrackedComposeSourceCapturesCommittedBundle(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "deploy/compose.yaml", []byte(`
services:
  api:
    image: example.com/api:1
    env_file: ../env/api.env
    volumes:
      - ../data:/data:ro
`), 0o600)
	writeGitSourceTestFile(t, root, "env/api.env", []byte("SELECTED=1\n"), 0o600)
	writeGitSourceTestFile(t, root, "data/run.sh", []byte("#!/bin/sh\nexit 0\n"), 0o700)
	commitApplyTestRepository(t, root, "deploy", "env", "data")

	source, err := loadTrackedComposeSource(t.Context(), "deploy/compose.yaml", root, nil, t.TempDir())
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	if source.Repository == nil || len(source.Repository.Files) != 3 ||
		len(source.Repository.RuntimePaths) != 1 ||
		!source.Repository.Files["data/run.sh"].Executable {
		t.Fatalf("loadTrackedComposeSource() = %#v", source.Repository)
	}
}

func TestLoadTrackedComposeSourceRejectsIgnoredSecondaryFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte(`
services:
  api:
    image: example.com/api:1
    env_file: ignored.env
`), 0o600)
	writeGitSourceTestFile(t, root, ".gitignore", []byte("ignored.env\n"), 0o600)
	commitApplyTestRepository(t, root, "compose.yaml", ".gitignore")
	writeGitSourceTestFile(t, root, "ignored.env", []byte("IGNORED=1\n"), 0o600)

	_, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, t.TempDir())
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
}

func TestLoadTrackedComposeSourceRejectsInvalidRuntimeBase(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte("services: {}\n"), 0o600)
	commitApplyTestRepository(t, root, "compose.yaml")

	_, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, "relative")
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadTrackedComposeSource(relative runtime base) error = %v", err)
	}
}

func TestLoadTrackedComposeSourceResolvesRepositoryPathAlias(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte("services: {}\n"), 0o600)
	commitApplyTestRepository(t, root, "compose.yaml")
	alias := filepath.Join(t.TempDir(), "repository")
	if err := os.Symlink(root, alias); err != nil {
		t.Fatal(err)
	}

	source, err := loadTrackedComposeSource(t.Context(), "compose.yaml", alias, nil, t.TempDir())
	if err != nil {
		t.Fatalf("loadTrackedComposeSource(alias) error = %v", err)
	}
	physicalRoot, err := filepath.EvalSymlinks(root)
	if err != nil {
		t.Fatal(err)
	}
	if source.Repository == nil || source.Repository.Root != physicalRoot {
		t.Fatalf("loadTrackedComposeSource(alias) root = %#v", source.Repository)
	}
}

func TestLoadTrackedComposeSourceRequiresCleanRepository(t *testing.T) {
	t.Parallel()

	for _, dirty := range []string{"tracked", "untracked"} {
		t.Run(dirty, func(t *testing.T) {
			t.Parallel()

			root := t.TempDir()
			writeGitSourceTestFile(t, root, "compose.yaml", []byte("name: clean\nservices: {}\n"), 0o600)
			writeGitSourceTestFile(t, root, "tracked", []byte("committed\n"), 0o600)
			commitApplyTestRepository(t, root, "compose.yaml", "tracked")
			writeGitSourceTestFile(t, root, dirty, []byte("changed\n"), 0o600)

			_, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, t.TempDir())
			if !errors.Is(err, compose.ErrInvalidSource) {
				t.Fatalf("loadTrackedComposeSource(%s repository) error = %v", dirty, err)
			}
		})
	}
}

//nolint:cyclop // The test proves capture, projection, publication, and ignored-file exclusion together.
func TestLoadTrackedComposeSourceExcludesIgnoredBindContent(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte(`
name: git-source
services:
  api:
    container_name: git-source-api
    image: example.com/api:1
    network_mode: bridge
    volumes:
      - ./data:/data:ro
`), 0o600)
	writeGitSourceTestFile(t, root, ".gitignore", []byte("data/local\n"), 0o600)
	writeGitSourceTestFile(t, root, "data/tracked", []byte("tracked\n"), 0o600)
	commitApplyTestRepository(t, root, "compose.yaml", ".gitignore", "data/tracked")
	writeGitSourceTestFile(t, root, "data/local", []byte("ignored\n"), 0o600)

	source, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, t.TempDir())
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	writeGitSourceTestFile(t, root, "data/tracked", []byte("worktree changed after capture\n"), 0o600)
	project, err := compose.Load(t.Context(), source)
	if err != nil {
		t.Fatalf("compose.Load() error = %v", err)
	}
	input, err := project.ImageInput("api")
	imageSource, registry := input.RegistrySource()
	if err != nil || !registry {
		t.Fatalf("ImageInput() = %t, %v", registry, err)
	}
	digest := domain.Hash([]byte("git source test image"))
	reference, err := imageSource.Pin(digest)
	if err != nil {
		t.Fatalf("Pin() error = %v", err)
	}
	image := domain.ImageIdentity{
		Origin:    domain.ImageOriginRegistry,
		Reference: reference.String(), ReferenceDigest: digest,
		Platform:         domain.Platform{OS: "linux", Architecture: "amd64"},
		PlatformManifest: digest, ImageConfig: digest,
	}
	workload, err := project.Workload("api", image)
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}
	if len(workload.Mounts) != 1 {
		t.Fatalf("Workload() mounts = %#v", workload.Mounts)
	}
	if err := source.MaterializeRuntime(); err != nil {
		t.Fatalf("MaterializeRuntime() error = %v", err)
	}
	if _, err := os.Stat(filepath.Join(workload.Mounts[0].Source, "local")); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("ignored runtime file stat error = %v", err)
	}
	content, err := os.ReadFile(filepath.Join(workload.Mounts[0].Source, "tracked"))
	if err != nil || string(content) != "tracked\n" {
		t.Fatalf("tracked runtime file = %q, %v", content, err)
	}
}

func TestLoadTrackedComposeSourceRejectsSymlinkInBindTree(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte(`
services:
  api:
    image: example.com/api:1
    volumes:
      - ./data:/data:ro
`), 0o600)
	writeGitSourceTestFile(t, root, "target", []byte("target\n"), 0o600)
	if err := os.MkdirAll(filepath.Join(root, "data"), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.Symlink("../target", filepath.Join(root, "data/link")); err != nil {
		t.Fatal(err)
	}
	commitApplyTestRepository(t, root, "compose.yaml", "target", "data/link")

	_, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, t.TempDir())
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
}

func TestLoadTrackedComposeSourceCapturesRegularBindFile(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte(`
services:
  api:
    image: example.com/api:1
    volumes:
      - ./settings:/settings:ro
`), 0o600)
	writeGitSourceTestFile(t, root, "settings", []byte("committed\n"), 0o600)
	commitApplyTestRepository(t, root, "compose.yaml", "settings")

	source, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, t.TempDir())
	if err != nil {
		t.Fatalf("loadTrackedComposeSource() error = %v", err)
	}
	if source.Repository == nil || len(source.Repository.RuntimePaths) != 1 ||
		source.Repository.RuntimePaths[0].Path != "settings" ||
		source.Repository.RuntimePaths[0].Directory {
		t.Fatalf("loadTrackedComposeSource() = %#v", source.Repository)
	}
}

func TestCommittedGitFileContracts(t *testing.T) {
	t.Parallel()

	root, tree := committedGitObjectFixture(t)

	file, found, err := readCommittedGitFile(t.Context(), root, tree, "regular")
	if err != nil || !found || string(file.Content) != gitRegularContent {
		t.Fatalf("readCommittedGitFile(regular) = %#v, %t, %v", file, found, err)
	}
	if _, found, err = readCommittedGitFile(t.Context(), root, tree, "missing"); err != nil || found {
		t.Fatalf("readCommittedGitFile(missing) = %t, %v", found, err)
	}
	if _, _, err = readCommittedGitFile(t.Context(), root, tree, "directory"); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("readCommittedGitFile(directory) error = %v", err)
	}
}

func TestCommittedGitPathContracts(t *testing.T) {
	t.Parallel()

	root, tree := committedGitObjectFixture(t)

	regular, err := readCommittedGitPath(t.Context(), root, tree, "regular")
	if err != nil || regular.Directory || string(regular.Files["regular"].Content) != gitRegularContent {
		t.Fatalf("readCommittedGitPath(regular) = %#v, %v", regular, err)
	}
	directory, err := readCommittedGitPath(t.Context(), root, tree, "directory")
	if err != nil || !directory.Directory || !directory.Files["directory/child"].Executable {
		t.Fatalf("readCommittedGitPath(directory) = %#v, %v", directory, err)
	}
	for _, name := range []string{"missing", "link"} {
		if _, err := readCommittedGitPath(t.Context(), root, tree, name); !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("readCommittedGitPath(%s) error = %v", name, err)
		}
	}
}

func TestCommittedGitObjectFailures(t *testing.T) {
	t.Parallel()

	root, tree := committedGitObjectFixture(t)
	invalidObject := strings.Repeat("f", 40)

	entry, found, err := readGitTreeEntry(t.Context(), root, tree, "regular")
	if err != nil || !found {
		t.Fatalf("readGitTreeEntry(regular) = %#v, %t, %v", entry, found, err)
	}
	if _, err := readCommittedGitRegularPath(t.Context(), root, "regular", gitTreeEntry{
		mode: "100644", kind: "blob", object: invalidObject, path: "regular",
	}); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("readCommittedGitRegularPath(invalid object) error = %v", err)
	}
	_, err = readCommittedGitDirectory(t.Context(), root, invalidObject, "directory")
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("readCommittedGitDirectory(invalid tree) error = %v", err)
	}
	_, _, err = readGitTreeEntry(t.Context(), root, invalidObject, "regular")
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("readGitTreeEntry(invalid tree) error = %v", err)
	}
	if _, err := readGitBlob(t.Context(), root, invalidObject); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("readGitBlob(invalid object) error = %v", err)
	}
	if _, err := resolveGitObject(t.Context(), root, "missing"); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("resolveGitObject(missing) error = %v", err)
	}
}

func TestCommittedGitReadIgnoresReplaceRefs(t *testing.T) {
	t.Parallel()

	root, tree := committedGitObjectFixture(t)
	entry, found, err := readGitTreeEntry(t.Context(), root, tree, "regular")
	if err != nil || !found {
		t.Fatalf("readGitTreeEntry(regular) = %#v, %t, %v", entry, found, err)
	}
	writeGitSourceTestFile(t, root, "replacement", []byte("replaced\n"), 0o600)
	replacementOutput, err := runGit(t.Context(), root, "hash-object", "-w", "replacement")
	replacement := strings.TrimSpace(string(replacementOutput))
	if err != nil || !validGitObjectID(replacement) {
		t.Fatalf("hash replacement object = %q, %v", replacement, err)
	}
	if _, err = runGit(t.Context(), root, "replace", entry.object, replacement); err != nil {
		t.Fatalf("git replace error = %v", err)
	}

	content, err := readGitBlob(t.Context(), root, entry.object)
	if err != nil || string(content) != gitRegularContent {
		t.Fatalf("readGitBlob(replaced object) = %q, %v", content, err)
	}
}

func committedGitObjectFixture(t *testing.T) (string, string) {
	t.Helper()

	root := t.TempDir()
	writeGitSourceTestFile(t, root, "regular", []byte(gitRegularContent), 0o600)
	writeGitSourceTestFile(t, root, "directory/child", []byte("child\n"), 0o700)
	if err := os.Symlink("regular", filepath.Join(root, "link")); err != nil {
		t.Fatal(err)
	}
	commitApplyTestRepository(t, root, "regular", "directory/child", "link")
	state, err := cleanGitTree(t.Context(), root)
	if err != nil {
		t.Fatalf("cleanGitTree() error = %v", err)
	}

	return root, state.tree
}

func TestGitMetadataParsersRejectMalformedValues(t *testing.T) {
	t.Parallel()

	object := strings.Repeat("a", 40)
	valid := []byte("100644 blob " + object + "\tpath\x00")
	entries, ok := parseGitTreeEntries(valid)
	if !ok || len(entries) != 1 || entries[0].path != "path" {
		t.Fatalf("parseGitTreeEntries(valid) = %#v, %t", entries, ok)
	}
	for _, value := range [][]byte{
		nil,
		[]byte("100644 blob " + object + "\tpath"),
		[]byte("malformed\x00"),
		[]byte("100644 blob\tpath\x00"),
		[]byte("100644 blob invalid\tpath\x00"),
		[]byte("100644 blob " + object + "\t../path\x00"),
	} {
		if _, ok := parseGitTreeEntries(value); ok {
			t.Fatalf("parseGitTreeEntries(%q) succeeded", value)
		}
	}

	for _, value := range []string{"", strings.Repeat("a", 39), strings.Repeat("a", 63), strings.Repeat("A", 40)} {
		if validGitObjectID(value) {
			t.Fatalf("validGitObjectID(%q) = true", value)
		}
	}
	if !validGitObjectID(strings.Repeat("f", 64)) {
		t.Fatal("validGitObjectID(sha256) = false")
	}
}

func TestGitSourcePathAndHeadBoundaries(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	for _, path := range []string{"", invalidPathValue} {
		if _, _, _, err := locateCleanGitSource(t.Context(), path, root); !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("locateCleanGitSource(%q) error = %v", path, err)
		}
	}
	if _, _, _, err := locateCleanGitSource(t.Context(), "missing.yaml", root); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("locateCleanGitSource(missing) error = %v", err)
	}
	if path, valid := normalizedComposeSourcePath("compose.yaml", "relative"); valid || path != "" {
		t.Fatalf("normalizedComposeSourcePath(relative cwd) = %q, %t", path, valid)
	}
	if _, err := runGit(t.Context(), root, "init", "--quiet"); err != nil {
		t.Fatalf("git init error = %v", err)
	}
	if _, err := cleanGitTree(t.Context(), root); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("cleanGitTree(unborn HEAD) error = %v", err)
	}
}

func TestBoundedGitOutput(t *testing.T) {
	t.Parallel()

	output := new(boundedGitOutput)
	content := make([]byte, maximumGitOutputBytes)
	written, err := output.Write(content)
	if err != nil || written != len(content) {
		t.Fatalf("Write(maximum) = %d, %v", written, err)
	}
	if written, err = output.Write([]byte("x")); written != 0 || !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("Write(excess) = %d, %v", written, err)
	}
}

//nolint:paralleltest // This test must own PATH while exercising deterministic Git process failures.
func TestGitLocationFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte("services: {}\n"), 0o600)
	gitPath := installFakeGit(t)

	for _, test := range []struct {
		name string
		body string
	}{
		{name: "Git failure", body: `exit 1`},
		{name: "relative root", body: `printf 'relative\n'`},
		{name: "outside root", body: `printf '/different\n'`},
	} {
		writeFakeGit(t, gitPath, test.body)
		_, _, _, err := locateCleanGitSource(t.Context(), "compose.yaml", root)
		if !errors.Is(err, compose.ErrInvalidSource) {
			t.Fatalf("locateCleanGitSource(%s) error = %v", test.name, err)
		}
	}
}

//nolint:paralleltest // This test must own PATH while exercising deterministic Git process failures.
func TestGitProcessFailureBoundaries(t *testing.T) {
	root := t.TempDir()
	writeGitSourceTestFile(t, root, "compose.yaml", []byte("services: {}\n"), 0o600)
	gitPath := installFakeGit(t)
	object := strings.Repeat("a", 40)
	tree := strings.Repeat("b", 40)

	writeFakeGit(t, gitPath, `
case "$*" in
  *"status --porcelain"*) exit 0 ;;
  *"HEAD^{commit}"*) printf '%s\n' `+object+` ;;
  *) exit 1 ;;
esac
`)
	if _, err := cleanGitTree(t.Context(), root); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("cleanGitTree(tree failure) error = %v", err)
	}

	writeFakeGit(t, gitPath, `printf 'malformed\000'`)
	if _, _, err := readGitTreeEntry(t.Context(), root, tree, "path"); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("readGitTreeEntry(malformed) error = %v", err)
	}

	writeFakeGit(t, gitPath, `
case "$*" in
  *"ls-tree -r"*) printf '100644 blob `+object+`\tdata/file\000' ;;
  *) exit 1 ;;
esac
`)
	if _, err := readCommittedGitDirectory(t.Context(), root, tree, "data"); !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("readCommittedGitDirectory(blob failure) error = %v", err)
	}

	counter := filepath.Join(t.TempDir(), "counter")
	writeFakeGit(t, gitPath, fmt.Sprintf(`
case "$*" in
  *"rev-parse --show-toplevel"*) printf '%%s\n' %q ;;
  *"status --porcelain"*) exit 0 ;;
  *"HEAD^{commit}"*)
    if test -f %q; then printf '%s\n'; else : > %q; printf '%s\n'; fi ;;
  *"rev-parse --verify"*) printf '%s\n' ;;
  *"ls-tree -z"*"compose.yaml"*) printf '100644 blob %s\tcompose.yaml\000' ;;
  *"ls-tree -z"*) exit 0 ;;
  *"cat-file blob"*) printf 'services: {}\n' ;;
  *) exit 1 ;;
esac
`, root, counter, strings.Repeat("c", 40), counter, object, tree, strings.Repeat("d", 40)))
	_, err := loadTrackedComposeSource(t.Context(), "compose.yaml", root, nil, t.TempDir())
	if !errors.Is(err, compose.ErrInvalidSource) {
		t.Fatalf("loadTrackedComposeSource(drift) error = %v", err)
	}
}

func installFakeGit(t *testing.T) string {
	t.Helper()

	directory := t.TempDir()
	path := filepath.Join(directory, "git")
	t.Setenv("PATH", directory+string(os.PathListSeparator)+os.Getenv("PATH"))

	return path
}

func writeFakeGit(t *testing.T, path, body string) {
	t.Helper()

	content := []byte("#!/bin/sh\n" + body + "\n")
	if err := os.WriteFile(path, content, 0o600); err != nil {
		t.Fatal(err)
	}
	if err := os.Chmod(path, 0o700); err != nil { //nolint:gosec // The test-controlled fake Git must be executable.
		t.Fatal(err)
	}
}

func writeGitSourceTestFile(t *testing.T, root, name string, content []byte, mode os.FileMode) {
	t.Helper()

	path := filepath.Join(root, filepath.FromSlash(name))
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(path, content, mode); err != nil {
		t.Fatal(err)
	}
}
