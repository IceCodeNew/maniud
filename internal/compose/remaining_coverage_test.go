package compose

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	composetypes "github.com/compose-spec/compose-go/v2/types"

	"github.com/IceCodeNew/maniud/internal/composeext/maniud"
	"github.com/IceCodeNew/maniud/internal/domain"
	"github.com/IceCodeNew/maniud/internal/runtimeargv"
)

const (
	remainingComposeEntry = "compose.yaml"
	remainingRuntimePath  = "data"
	remainingRuntimeFile  = "data/file"
	remainingChildCompose = "child.yaml"
)

func TestRemainingPureValidationBranches(t *testing.T) {
	t.Parallel()

	if _, valid := internalRuntime(maniud.Runtime("invalid")); valid {
		t.Fatal("unknown runtime provenance accepted")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("invalid internal runtime did not trip the invariant")
			}
		}()
		extensionRuntime(domain.RuntimeKind("invalid"))
	}()
	if _, valid := internalManiudExtension(maniud.Extension{Services: map[string]maniud.Service{
		apiService: {Runtime: maniud.Runtime("invalid")},
	}}); valid {
		t.Fatal("extension with unknown runtime provenance accepted")
	}
	if _, valid := internalManiudExtension(maniud.Extension{Services: map[string]maniud.Service{
		apiService: {Runtime: maniud.RuntimeDocker, ArchiveProof: &maniud.ArchiveProof{}},
	}}); valid {
		t.Fatal("extension with invalid archive proof accepted")
	}
	if _, err := encodeManiudService("", maniud.Service{}); err == nil {
		t.Fatal("invalid typed extension encoded")
	}
	func() {
		defer func() {
			if recover() == nil {
				t.Fatal("invalid runtime provenance did not trip the invariant")
			}
		}()
		mustEncodeManiudRuntime("", maniud.Runtime("invalid"))
	}()

	if _, err := workloadSpecFromService(composetypes.ServiceConfig{
		HealthCheck: &composetypes.HealthCheckConfig{Disable: true, Test: []string{"NONE"}},
	}, domain.Platform{}, "", ""); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("invalid workload service error = %v", err)
	}

	if got := runtimeSnapshotDirectories(map[string]RepositoryFile{
		"a/x": {}, "b/y": {},
	}); !reflect.DeepEqual(got, []string{".", "a", "b"}) {
		t.Fatalf("runtime directories = %q", got)
	}
}

func TestComposeExtensionBoundaries(t *testing.T) {
	t.Parallel()

	cycle := map[string]any{}
	cycle[maniud.Key] = cycle
	for _, document := range []map[string]any{
		cycle,
		{maniud.Key: map[string]any{"services": map[string]any{}}, "x-other": true},
		{"x-other": true},
	} {
		if _, valid := decodeComposeExtensions(document); valid {
			t.Fatalf("decodeComposeExtensions(%#v) accepted", document)
		}
	}
}

func TestRenderRuntimeRejectsEnvironmentFileAtOutputDirectory(t *testing.T) {
	t.Parallel()

	workingDirectory := composeTestWorkingDirectory
	projection, err := runtimeargv.Parse([]string{
		composeDockerRuntime, runtimeTestCreateCommand, "--env-file=" + workingDirectory, "example.test/image:1",
	}, "service", workingDirectory)
	if err != nil {
		t.Fatal(err)
	}
	digest, err := domain.ParseDigest("sha256:" + strings.Repeat("b", 64))
	if err != nil {
		t.Fatal(err)
	}
	image := domain.ImageIdentity{
		Origin: domain.ImageOriginRegistry, Reference: "example.test/image:1@" + digest.String(),
		ReferenceDigest: digest, Platform: projection.Platform(),
	}
	if _, err := RenderRuntime(context.Background(), projection, image, workingDirectory); err == nil {
		t.Fatal("environment file equal to output directory accepted")
	}
}

func TestImageInputReportsAdapterValidationFailure(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    security_opt: [label=x]
`))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := project.ImageInput(apiService); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("ImageInput() error = %v", err)
	}
}

func TestWorkloadRejectsInvalidRepositoryPathMapping(t *testing.T) {
	t.Parallel()

	project, err := Load(context.Background(), testSource(t, `
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    volumes: [/repository/data:/data]
`))
	if err != nil {
		t.Fatal(err)
	}
	image := resolvedImageForService(t, project, apiService)
	project.pathFrom = "/repository"
	project.pathTo = "relative"
	if _, err := project.Workload(apiService, image); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Workload() error = %v", err)
	}
}

//nolint:cyclop,funlen // The table covers independent snapshot bounds and materialization failures.
func TestRepositorySnapshotValidationAndMaterializationFailures(t *testing.T) {
	root := t.TempDir()
	topLevelInvalid := remainingSnapshotSource(root, map[string]RepositoryFile{
		remainingComposeEntry: {Content: []byte("services: {}\n")},
	}, nil)
	topLevelInvalid.Repository.Digest = domain.Digest{}
	if validRepositorySnapshot(topLevelInvalid) {
		t.Fatal("zero-digest snapshot accepted")
	}
	if _, err := Load(context.Background(), topLevelInvalid); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Load(invalid repository snapshot) error = %v", err)
	}

	invalidSources := []Source{
		remainingSnapshotSource(root, map[string]RepositoryFile{
			remainingComposeEntry: {Content: []byte("services: {}\n")},
			"../bad":              {Content: []byte("x")},
		}, nil),
		remainingSnapshotSource(root, map[string]RepositoryFile{
			remainingComposeEntry: {Content: []byte("services: {}\n")},
			"large":               {Content: make([]byte, maxSourceBytes+1)},
		}, nil),
		remainingSnapshotSource(root, remainingMaximumByteFiles(), nil),
		remainingSnapshotSource(root, map[string]RepositoryFile{
			remainingComposeEntry: {Content: []byte("services: {}\n")},
		}, []RepositoryPath{{Path: "z"}, {Path: "a"}}),
		remainingSnapshotSource(root, map[string]RepositoryFile{
			remainingComposeEntry: {Content: []byte("services: {}\n")},
		}, []RepositoryPath{{Path: "../bad"}}),
	}
	for index, source := range invalidSources {
		if validRepositorySnapshot(source) {
			t.Fatalf("invalid snapshot %d accepted", index)
		}
		if _, cleanup, err := materializeSource(source); !errors.Is(err, ErrInvalidSource) {
			cleanup()
			t.Fatalf("materialize invalid snapshot %d error = %v", index, err)
		}
	}

	executable := remainingSnapshotSource(root, map[string]RepositoryFile{
		remainingComposeEntry: {Content: []byte("services: {}\n"), Executable: true},
	}, nil)
	materialized, cleanup, err := materializeSource(executable)
	if err != nil {
		t.Fatal(err)
	}
	defer cleanup()
	info, err := os.Stat(materialized.filename)
	if err != nil || info.Mode().Perm() != materializedExecutableMode {
		t.Fatalf("materialized executable mode = %v, %v", info, err)
	}

	conflict := remainingSnapshotSource(root, map[string]RepositoryFile{
		remainingComposeEntry: {Content: []byte("services: {}\n")},
		"conflict":            {Content: []byte("file")},
		"conflict/child":      {Content: []byte("child")},
	}, nil)
	if _, conflictCleanup, err := materializeSource(conflict); !errors.Is(err, ErrInvalidSource) {
		conflictCleanup()
		t.Fatalf("file/directory conflict error = %v", err)
	}

	missingTemp := filepath.Join(root, "missing-temp")
	t.Setenv("TMPDIR", missingTemp)
	if _, tempCleanup, err := materializeSource(executable); !errors.Is(err, ErrInvalidSource) {
		tempCleanup()
		t.Fatalf("unavailable temporary directory error = %v", err)
	}

	pinned := executable
	pinned.runtimeBase = filepath.Join(root, "missing-runtime-base")
	pinned.Repository.RuntimePaths = []RepositoryPath{{Path: remainingComposeEntry}}
	pinned.Repository.Digest = repositoryDigest(
		pinned.Repository.Entry,
		pinned.Repository.Files,
		pinned.Repository.RuntimePaths,
		pinned.Environment,
	)
	if err := pinned.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("missing runtime base error = %v", err)
	}
	if pinned.repositoryRoot() != repositoryRuntimeRoot(pinned.runtimeBase, pinned.Repository.Digest) {
		t.Fatal("pinned repository root was not selected")
	}

	invalidParentBase := t.TempDir()
	if err := os.WriteFile(
		filepath.Join(invalidParentBase, repositoryRuntimeDirectory),
		[]byte("not a directory"),
		0o600,
	); err != nil {
		t.Fatal(err)
	}
	invalidParent := executable
	invalidParent.runtimeBase = invalidParentBase
	if err := invalidParent.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("invalid runtime parent error = %v", err)
	}

	longName := strings.Repeat("x", 300)
	longPath := remainingSnapshotSource(root, map[string]RepositoryFile{
		remainingComposeEntry: {Content: []byte("services: {}\n")},
		longName:              {Content: []byte("x")},
	}, []RepositoryPath{{Path: longName}})
	longPath.runtimeBase = t.TempDir()
	if err := longPath.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("oversized runtime path error = %v", err)
	}
}

//nolint:cyclop,funlen // Each branch exercises a distinct real descriptor or root failure.
func TestMaterializeRuntimeFilesystemFailureBranches(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	if err := root.Close(); err != nil {
		t.Fatal(err)
	}
	if err := ensureRuntimeParent(root); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed root parent error = %v", err)
	}
	if _, err := createRuntimeTemporary(root); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed root temporary error = %v", err)
	}
	if _, err := prepareRuntimeSnapshot(root, &RepositorySnapshot{}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed root preparation error = %v", err)
	}

	nestedSnapshot := &RepositorySnapshot{
		Files:        map[string]RepositoryFile{"data/nested/file": {Content: []byte("x")}},
		RuntimePaths: []RepositoryPath{{Path: remainingRuntimePath, Directory: true}},
	}
	if err := writeRuntimeSnapshot(root, "temporary", nestedSnapshot); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed root directory error = %v", err)
	}
	if err := writeRuntimeSnapshot(root, "temporary", &RepositorySnapshot{}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed root chmod error = %v", err)
	}

	openRoot, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := openRoot.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})
	if err := writeRuntimeSnapshot(openRoot, "missing", &RepositorySnapshot{
		Files:        map[string]RepositoryFile{"file": {Content: []byte("x")}},
		RuntimePaths: []RepositoryPath{{Path: "file"}},
	}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("missing temporary directory error = %v", err)
	}
	if _, err := prepareRuntimeSnapshot(openRoot, &RepositorySnapshot{
		Files:        map[string]RepositoryFile{strings.Repeat("x", 300): {Content: []byte("x")}},
		RuntimePaths: []RepositoryPath{{Path: strings.Repeat("x", 300)}},
	}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("runtime write preparation error = %v", err)
	}
	if err := writeRuntimeFile(root, "file", RepositoryFile{}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed root file error = %v", err)
	}

	descriptor, err := os.CreateTemp(base, "closed-file-")
	if err != nil {
		t.Fatal(err)
	}
	if err := descriptor.Close(); err != nil {
		t.Fatal(err)
	}
	if err := writeRuntimeDescriptor(descriptor, RepositoryFile{}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed descriptor error = %v", err)
	}
	if err := syncRuntimeDirectory(descriptor); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("closed directory descriptor error = %v", err)
	}
	if err := openRoot.Mkdir("wrong-mode", 0o755); err != nil {
		t.Fatal(err)
	}
	if validMaterializedRuntime(openRoot, "wrong-mode", &RepositorySnapshot{}) {
		t.Fatal("wrong-mode runtime directory accepted")
	}
	if validMaterializedRuntime(root, ".", &RepositorySnapshot{}) {
		t.Fatal("closed root runtime accepted")
	}
}

//nolint:cyclop // The test covers success, idempotent collision, and conflicting collision.
func TestPublishRuntimeSnapshotHandlesExistingDestinations(t *testing.T) {
	t.Parallel()

	base := t.TempDir()
	root, err := os.OpenRoot(base)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if closeErr := root.Close(); closeErr != nil {
			t.Errorf("close root: %v", closeErr)
		}
	})
	if err := root.Mkdir(repositoryRuntimeDirectory, runtimePrivateMode); err != nil {
		t.Fatal(err)
	}
	snapshot := &RepositorySnapshot{
		Files:        map[string]RepositoryFile{remainingRuntimeFile: {Content: []byte("x")}},
		RuntimePaths: []RepositoryPath{{Path: remainingRuntimePath, Directory: true}},
	}
	for _, name := range []string{"first", "same", "conflict"} {
		if err := root.Mkdir(name, runtimePrivateMode); err != nil {
			t.Fatal(err)
		}
		if err := writeRuntimeSnapshot(root, name, snapshot); err != nil {
			t.Fatalf("write %s: %v", name, err)
		}
	}
	final := filepath.Join(repositoryRuntimeDirectory, "final")
	if err := publishRuntimeSnapshot(root, "first", final, snapshot); err != nil {
		t.Fatal(err)
	}
	if err := publishRuntimeSnapshot(root, "same", final, snapshot); err != nil {
		t.Fatalf("idempotent publish error = %v", err)
	}
	if err := root.Mkdir(filepath.Join(repositoryRuntimeDirectory, "invalid"), runtimePrivateMode); err != nil {
		t.Fatal(err)
	}
	if err := publishRuntimeSnapshot(
		root,
		"conflict",
		filepath.Join(repositoryRuntimeDirectory, "invalid"),
		snapshot,
	); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("conflicting publish error = %v", err)
	}
}

//nolint:cyclop,funlen // Independent readers exercise each global repository budget and overlap rule.
func TestCaptureRepositorySourceRemainingBudgetsAndOverlap(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	if _, err := CaptureRepositorySource(root, remainingComposeEntry, nil, nil, nil); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("nil readers error = %v", err)
	}

	self := []byte("include:\n  - compose.yaml\nservices: {}\n")
	if _, err := CaptureRepositorySource(
		root,
		remainingComposeEntry,
		nil,
		func(name string) (RepositoryFile, bool, error) {
			if name == remainingComposeEntry {
				return RepositoryFile{Content: self}, true, nil
			}

			return RepositoryFile{}, false, nil
		},
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	); err != nil {
		t.Fatalf("self include error = %v", err)
	}

	manyReferences := strings.Builder{}
	manyReferences.WriteString("services:\n  api:\n    env_file:\n")
	for index := range maximumRepositoryFiles {
		fmt.Fprintf(&manyReferences, "      - env/%03d.env\n", index)
	}
	if _, err := CaptureRepositorySource(
		root,
		remainingComposeEntry,
		map[string]string{composeDisableEnvFile: contractTrue},
		func(name string) (RepositoryFile, bool, error) {
			if name == remainingComposeEntry {
				return RepositoryFile{Content: []byte(manyReferences.String())}, true, nil
			}

			return RepositoryFile{Content: []byte("A=1\n")}, true, nil
		},
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("repository file budget error = %v", err)
	}

	overlapDocument := []byte(`
configs:
  same:
    file: data/file
services:
  api:
    volumes:
      - ./data/file:/data:ro
`)
	for _, mismatch := range []bool{false, true} {
		_, err := CaptureRepositorySource(
			root,
			remainingComposeEntry,
			map[string]string{composeDisableEnvFile: contractTrue},
			func(name string) (RepositoryFile, bool, error) {
				switch name {
				case remainingComposeEntry:
					return RepositoryFile{Content: overlapDocument}, true, nil
				case remainingRuntimeFile:
					return RepositoryFile{Content: []byte("same")}, true, nil
				default:
					return RepositoryFile{}, false, nil
				}
			},
			func(string) (RepositoryPathSnapshot, error) {
				content := []byte("same")
				if mismatch {
					content = []byte("different")
				}

				return RepositoryPathSnapshot{Files: map[string]RepositoryFile{
					remainingRuntimeFile: {Content: content},
				}}, nil
			},
		)
		if mismatch && !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("overlap mismatch error = %v", err)
		}
		if !mismatch && err != nil {
			t.Fatalf("identical overlap error = %v", err)
		}
	}

	if _, err := captureRuntimeDirectory(root, remainingMaximumRuntimeFiles()); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("runtime file budget error = %v", err)
	}
	if _, err := captureRuntimeDirectory(root, remainingMaximumRuntimeByteFiles()); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("runtime byte budget error = %v", err)
	}
	if _, err := captureRuntimeDirectory(root, map[string]RepositoryFile{
		remainingRuntimeFile: {Content: make([]byte, maxSourceBytes+1)},
	}); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("oversized runtime file error = %v", err)
	}

	malformedEnvironment := map[string][]byte{
		remainingComposeEntry: []byte("services: {}\n"),
		composeDefaultEnvFile: []byte("bad='"),
	}
	if _, err := CaptureRepositorySource(
		root,
		remainingComposeEntry,
		nil,
		repositoryFixtureReader(malformedEnvironment),
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("malformed default environment error = %v", err)
	}
}

func TestRepositoryCollectorRemainingMalformedForms(t *testing.T) {
	t.Parallel()

	if _, _, diagnostic := repositoryDocumentReferencesDetailed([]byte("services: [\n"), "."); diagnostic == nil {
		t.Fatal("malformed YAML accepted")
	}
	if _, _, diagnostic := repositoryDocumentReferencesDetailed([]byte("[]\n"), "."); diagnostic == nil {
		t.Fatal("sequence document accepted")
	}
	var documents []repositoryDocument
	if collectIncludes([]any{map[string]any{
		repositoryPathKey: remainingChildCompose, "project_directory": "/absolute",
	}}, ".", &documents) {
		t.Fatal("absolute include project directory accepted")
	}
	documents = nil
	if !collectIncludes([]any{map[string]any{
		repositoryPathKey: remainingChildCompose, "env_file": []any{"child.env"},
	}}, ".", &documents) {
		t.Fatal("explicit include environment rejected")
	}
	documents = nil
	if !collectIncludes([]any{map[string]any{
		repositoryPathKey: remainingChildCompose,
	}}, ".", &documents) || len(documents) != 2 || !documents[1].optional {
		t.Fatalf("default include environment = %#v", documents)
	}
	if _, valid := repositoryPaths([]any{"ok", "/absolute"}, "."); valid {
		t.Fatal("invalid path list accepted")
	}
	var mounts []string
	if collectBindMounts([]any{"too:many:volume:parts"}, ".", &mounts) {
		t.Fatal("malformed short volume accepted")
	}
}

func remainingSnapshotSource(
	root string,
	files map[string]RepositoryFile,
	runtimePaths []RepositoryPath,
) Source {
	snapshot := &RepositorySnapshot{
		Root: root, Entry: remainingComposeEntry, Files: files, RuntimePaths: runtimePaths,
	}
	snapshot.Digest = repositoryDigest(snapshot.Entry, files, runtimePaths, nil)

	return Source{
		Content:    files[remainingComposeEntry].Content,
		WorkingDir: root,
		Repository: snapshot,
	}
}

func remainingMaximumByteFiles() map[string]RepositoryFile {
	files := map[string]RepositoryFile{
		remainingComposeEntry: {Content: []byte("services: {}\n")},
	}
	for index := range maximumRepositoryBytes/maxSourceBytes + 1 {
		files[fmt.Sprintf("large/%02d", index)] = RepositoryFile{Content: make([]byte, maxSourceBytes)}
	}

	return files
}

func remainingMaximumRuntimeFiles() map[string]RepositoryFile {
	files := make(map[string]RepositoryFile, maximumRepositoryFiles)
	for index := range maximumRepositoryFiles {
		files[fmt.Sprintf("data/%03d", index)] = RepositoryFile{Content: []byte("x")}
	}

	return files
}

func remainingMaximumRuntimeByteFiles() map[string]RepositoryFile {
	files := make(map[string]RepositoryFile, maximumRepositoryBytes/maxSourceBytes+1)
	for index := range maximumRepositoryBytes/maxSourceBytes + 1 {
		files[fmt.Sprintf("data/%02d", index)] = RepositoryFile{Content: make([]byte, maxSourceBytes)}
	}

	return files
}

func captureRuntimeDirectory(root string, files map[string]RepositoryFile) (Source, error) {
	content := []byte("services:\n  api:\n    volumes:\n      - ./data:/data:ro\n")

	return CaptureRepositorySource(
		root,
		remainingComposeEntry,
		map[string]string{composeDisableEnvFile: contractTrue},
		func(name string) (RepositoryFile, bool, error) {
			if name == remainingComposeEntry {
				return RepositoryFile{Content: content}, true, nil
			}

			return RepositoryFile{}, false, nil
		},
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{Directory: true, Files: files}, nil
		},
	)
}
