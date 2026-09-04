package compose

import (
	"context"
	"errors"
	"maps"
	"os"
	"path/filepath"
	"reflect"
	"slices"
	"strconv"
	"strings"
	"sync"
	"testing"

	"github.com/IceCodeNew/maniud/internal/domain"
)

const (
	testRepositoryEntry     = "deploy/compose.yaml"
	testRootRepositoryEntry = "compose.yaml"
	testRuntimePath         = "data"
	testRepositoryBranch    = "main"
	testRepositoryRemote    = "https://example.com/repo.git"
)

var errRepositoryFixture = errors.New("repository fixture path is unavailable")

//nolint:cyclop,funlen // The test asserts every captured secondary-file family and projected value.
func TestCaptureRepositorySourceResolvesTrackedSecondaryFiles(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	files := map[string][]byte{
		testRepositoryEntry: []byte(`
name: example
services:
  api:
    container_name: example-api
    image: example.com/team/api:1
    network_mode: bridge
    env_file: ../env/api.env
    environment:
      INLINE: selected
    label_file: ../labels/api.labels
    volumes:
      - ../data:/data:ro
`),
		"env/api.env":       []byte("FROM_FILE=selected\nINLINE=ignored\n"),
		"labels/api.labels": []byte("team=platform\n"),
		"data/runtime.txt":  []byte("tracked\n"),
	}
	source, err := CaptureRepositorySource(
		root,
		testRepositoryEntry,
		nil,
		func(path string) (RepositoryFile, bool, error) {
			content, found := files[path]
			if !found {
				return RepositoryFile{}, false, nil
			}

			return RepositoryFile{Content: content}, true, nil
		}, func(path string) (RepositoryPathSnapshot, error) {
			if path != testRuntimePath {
				return RepositoryPathSnapshot{}, errRepositoryFixture
			}

			return RepositoryPathSnapshot{
				Directory: true,
				Files: map[string]RepositoryFile{
					"data/runtime.txt": {Content: files["data/runtime.txt"]},
				},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	if source.Repository == nil || len(source.Repository.Files) != len(files) ||
		!reflect.DeepEqual(source.Repository.RuntimePaths, []RepositoryPath{{Path: testRuntimePath, Directory: true}}) {
		t.Fatalf("CaptureRepositorySource() = %#v", source)
	}

	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	image := resolvedImageForService(t, project, apiService)
	workload, err := project.Workload(apiService, image)
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}
	if !reflect.DeepEqual(workload.Environment, []string{
		"FROM_FILE=selected", "INLINE=selected",
	}) || !reflect.DeepEqual(workload.Labels, []string{"team=platform"}) {
		t.Fatalf("Workload() environment/labels = %#v / %#v", workload.Environment, workload.Labels)
	}
	if len(workload.Mounts) != 1 || workload.Mounts[0].Source != filepath.Join(root, "data") {
		t.Fatalf("Workload() mounts = %#v", workload.Mounts)
	}
}

func TestRepositoryDigestBindsEntryDocument(t *testing.T) {
	t.Parallel()

	files := map[string]RepositoryFile{
		testRootRepositoryEntry: {Content: []byte("name: first\nservices: {}\n")},
		"compose-alt.yaml":      {Content: []byte("name: second\nservices: {}\n")},
	}
	first := repositoryDigest(testRootRepositoryEntry, files, nil, nil)
	second := repositoryDigest("compose-alt.yaml", files, nil, nil)
	if first == second {
		t.Fatalf("repository digest did not bind the entry document: %s", first)
	}
}

//nolint:cyclop,funlen,goconst,lll // The test proves cloning and digest rebinding across the complete source bundle.
func TestSourceWithEntryContentClonesAndRebindsRepository(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		testRepositoryEntry: []byte("name: before\nservices: {}\n"),
		"deploy/.env":       []byte("SELECTED=before\n"),
	}
	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRepositoryEntry,
		nil,
		repositoryFixtureReader(files),
		func(string) (RepositoryPathSnapshot, error) { return RepositoryPathSnapshot{}, errRepositoryFixture },
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	source.Profiles = []string{"selected"}
	originalDigest := source.Repository.Digest
	replacement := []byte("name: after\nservices: {}\n")

	updated, err := source.WithEntryContent(replacement)
	if err != nil {
		t.Fatalf("WithEntryContent() error = %v", err)
	}
	if updated.Repository == source.Repository || updated.Repository.Digest == originalDigest ||
		!slices.Equal(updated.Content, replacement) ||
		!reflect.DeepEqual(updated.Environment, source.Environment) ||
		!slices.Equal(updated.Profiles, source.Profiles) ||
		!slices.Equal(updated.Repository.Files[testRepositoryEntry].Content, replacement) ||
		!slices.Equal(updated.Repository.Files["deploy/.env"].Content, files["deploy/.env"]) {
		t.Fatalf("WithEntryContent() = %#v", updated)
	}
	replacement[0] = 'x'
	updated.Content[1] = 'x'
	updated.Environment["SELECTED"] = "after"
	updated.Profiles[0] = "after"
	secondary := updated.Repository.Files["deploy/.env"]
	secondary.Content[0] = 'x'
	if string(source.Content) != "name: before\nservices: {}\n" || source.Environment["SELECTED"] != "before" ||
		source.Profiles[0] != "selected" || string(source.Repository.Files["deploy/.env"].Content) != "SELECTED=before\n" {
		t.Fatal("WithEntryContent() shared input storage")
	}

	plain := Source{Content: []byte("before"), WorkingDir: t.TempDir(), Environment: map[string]string{"A": "1"}, Profiles: []string{"p"}}
	plainUpdated, err := plain.WithEntryContent([]byte("after"))
	if err != nil || plainUpdated.Repository != nil || string(plainUpdated.Content) != "after" {
		t.Fatalf("WithEntryContent(non-repository) = %#v, %v", plainUpdated, err)
	}
	invalid := source
	invalid.Repository = new(RepositorySnapshot)
	for _, input := range []struct {
		source  Source
		content []byte
	}{
		{source: source},
		{source: source, content: make([]byte, maxSourceBytes+1)},
		{source: invalid, content: []byte("after")},
	} {
		if _, err = input.source.WithEntryContent(input.content); !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("WithEntryContent(invalid) error = %v", err)
		}
	}
}

func TestSourceWithEntryContentRejectsReplacementBeyondRepositoryBudget(t *testing.T) {
	t.Parallel()

	entryContent := []byte("a")
	files := map[string]RepositoryFile{
		testRootRepositoryEntry: {Content: entryContent},
		"data/remainder":        {Content: make([]byte, maxSourceBytes-1)},
	}
	for index := range maximumRepositoryBytes/maxSourceBytes - 1 {
		files["data/full-"+strconv.Itoa(index)] = RepositoryFile{Content: make([]byte, maxSourceBytes)}
	}
	snapshot := &RepositorySnapshot{
		Root:  t.TempDir(),
		Entry: testRootRepositoryEntry,
		Files: files,
	}
	snapshot.Digest = repositoryDigest(snapshot.Entry, snapshot.Files, nil, nil)
	source := Source{
		Content: entryContent, WorkingDir: snapshot.Root, Environment: map[string]string{}, Repository: snapshot,
	}
	if !validRepositorySnapshot(source) {
		t.Fatal("repository budget fixture is invalid")
	}
	if _, err := source.WithEntryContent([]byte("ab")); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("WithEntryContent(over repository budget) error = %v", err)
	}
}

func TestRepositoryProvenanceBindsCanonicalGitInputs(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "srv", "maniud")
	remote := "https://example.com/team/desired.git"
	scope, err := NewRepositoryScope(root, remote, testRepositoryBranch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	if !scope.Valid() || scope.Version != RepositoryProvenanceVersion ||
		scope.Digest.String() != "sha256:18af3c4f7199d3dcaa57fd12af93af2a2b368dcf37752da6391ec0685db2f934" {
		t.Fatalf("NewRepositoryScope() = %#v", scope)
	}

	source := domain.Hash([]byte("committed source"))
	provenance, err := scope.Bind(testRepositoryEntry, source)
	if err != nil {
		t.Fatalf("RepositoryScope.Bind() error = %v", err)
	}
	if !provenance.ValidFor(source) || provenance.Scope != scope.Digest ||
		provenance.Location.String() != "sha256:413f0373caea180d73b610357e5967796f7f6a299fb0130846796bd6cdb00fb1" {
		t.Fatalf("RepositoryScope.Bind() = %#v", provenance)
	}

	assertRepositoryScopeInputs(t, scope, []struct {
		root   string
		remote string
		branch string
	}{
		{root: filepath.Join(string(filepath.Separator), "srv", "other"), remote: remote, branch: testRepositoryBranch},
		{root: root, remote: "https://example.com/team/other.git", branch: testRepositoryBranch},
		{root: root, remote: "https://example.com/team/desired.git", branch: "release"},
	})
}

func assertRepositoryScopeInputs(
	t *testing.T,
	scope RepositoryScope,
	inputs []struct {
		root   string
		remote string
		branch string
	},
) {
	t.Helper()

	for _, input := range inputs {
		other, scopeErr := NewRepositoryScope(input.root, input.remote, input.branch)
		if scopeErr != nil || other.Digest == scope.Digest {
			t.Fatalf("NewRepositoryScope(%q, %q, %q) = %#v, %v", input.root, input.remote, input.branch, other, scopeErr)
		}
	}
}

func TestRepositoryProvenanceRejectsIncompleteInputs(t *testing.T) {
	t.Parallel()

	root := filepath.Join(string(filepath.Separator), "srv", "maniud")
	assertInvalidRepositoryScopeInputs(t, []struct {
		root   string
		remote string
		branch string
	}{
		{root: contractRelative, remote: testRepositoryRemote, branch: testRepositoryBranch},
		{root: root + "/../maniud", remote: testRepositoryRemote, branch: testRepositoryBranch},
		{root: root, remote: "", branch: testRepositoryBranch},
		{root: root, remote: testRepositoryRemote, branch: ""},
		{root: root, remote: "bad\x00remote", branch: testRepositoryBranch},
		{root: root, remote: testRepositoryRemote, branch: string([]byte{0xff})},
		{root: root, remote: strings.Repeat("x", maxSourceBytes+1), branch: testRepositoryBranch},
	})

	scope, err := NewRepositoryScope(root, testRepositoryRemote, testRepositoryBranch)
	if err != nil {
		t.Fatalf("NewRepositoryScope() error = %v", err)
	}
	assertInvalidRepositoryProvenance(t, scope)
}

func assertInvalidRepositoryScopeInputs(
	t *testing.T,
	inputs []struct {
		root   string
		remote string
		branch string
	},
) {
	t.Helper()

	for _, input := range inputs {
		scope, err := NewRepositoryScope(input.root, input.remote, input.branch)
		if !errors.Is(err, ErrInvalidSource) || scope != (RepositoryScope{}) {
			t.Fatalf("NewRepositoryScope(%q, %q, %q) = %#v, %v", input.root, input.remote, input.branch, scope, err)
		}
	}
}

func assertInvalidRepositoryProvenance(t *testing.T, scope RepositoryScope) {
	t.Helper()

	if _, locationErr := scope.Location("../outside.yaml"); !errors.Is(locationErr, ErrInvalidSource) {
		t.Fatalf("RepositoryScope.Location(escaping) error = %v", locationErr)
	}
	provenance, bindErr := scope.Bind(testRepositoryEntry, domain.Digest{})
	if !errors.Is(bindErr, ErrInvalidSource) || provenance != (RepositoryProvenance{}) {
		t.Fatalf("RepositoryScope.Bind(empty source) = %#v, %v", provenance, bindErr)
	}
	if _, locationErr := (RepositoryScope{}).Location(testRepositoryEntry); !errors.Is(locationErr, ErrInvalidSource) {
		t.Fatalf("RepositoryScope.Location(invalid scope) error = %v", locationErr)
	}
	if (RepositoryScope{Version: RepositoryProvenanceVersion, Digest: domain.Digest{}}).Valid() ||
		(RepositoryScope{Version: RepositoryProvenanceVersion + 1, Digest: scope.Digest}).Valid() {
		t.Fatal("RepositoryScope.Valid() accepted incomplete or unknown evidence")
	}

	provenance, bindErr = scope.Bind(testRepositoryEntry, domain.Hash([]byte("source")))
	if bindErr != nil {
		t.Fatalf("RepositoryScope.Bind() error = %v", bindErr)
	}
	if provenance.ValidFor(domain.Hash([]byte("different"))) ||
		(RepositoryProvenance{}).ValidFor(provenance.Source) {
		t.Fatal("RepositoryProvenance.ValidFor() accepted incomplete or mismatched evidence")
	}
}

func TestCaptureRepositorySourceRejectsUntrackedOrEscapingReferences(t *testing.T) {
	t.Parallel()

	for _, content := range [][]byte{
		[]byte("services:\n  api:\n    env_file: /outside.env\n"),
		[]byte("services:\n  api:\n    env_file: ../../outside.env\n"),
		[]byte("services:\n  api:\n    env_file: ${ENV_FILE}\n"),
		[]byte("services:\n  api:\n    env_file: missing.env\n"),
		[]byte("services:\n  api:\n    env_file: {path: missing.env, required: true}\n"),
		[]byte("services:\n  api:\n    env_file: {path: missing.env, required: yes}\n"),
		[]byte("services:\n  api:\n    label_file: missing.labels\n"),
		[]byte("include:\n  - missing.yaml\nservices: {}\n"),
		[]byte("services:\n  api:\n    extends:\n      file: missing.yaml\n      service: base\n"),
		[]byte("configs:\n  settings:\n    file: missing.json\nservices: {}\n"),
		[]byte("secrets:\n  token:\n    file: missing.secret\nservices: {}\n"),
		[]byte("services:\n  api:\n    volumes:\n      - ./missing:/data\n"),
		[]byte("include:\n  - https://example.com/compose.yaml\nservices: {}\n"),
	} {
		_, err := CaptureRepositorySource(
			t.TempDir(),
			testRootRepositoryEntry,
			nil,
			func(path string) (RepositoryFile, bool, error) {
				if path == testRootRepositoryEntry {
					return RepositoryFile{Content: content}, true, nil
				}

				return RepositoryFile{}, false, nil
			}, func(string) (RepositoryPathSnapshot, error) {
				return RepositoryPathSnapshot{}, errRepositoryFixture
			},
		)
		if !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("CaptureRepositorySource(%q) error = %v", content, err)
		}
	}
}

func TestCaptureRepositorySourcePreservesSafeParseDiagnostic(t *testing.T) {
	t.Parallel()

	_, err := CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		nil,
		repositoryFixtureReader(map[string][]byte{
			testRootRepositoryEntry: []byte("services:\n  api:\n    image: [\n"),
		}),
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	diagnostic, ok := errors.AsType[*SourceDiagnosticError](err)
	if !ok || diagnostic.File != testRootRepositoryEntry || diagnostic.Reason != DiagnosticYAMLSyntax ||
		diagnostic.Line != 4 || diagnostic.Column != 1 {
		t.Fatalf("CaptureRepositorySource() diagnostic = %#v, error %v", diagnostic, err)
	}
	_, _, diagnostic = repositoryDocumentReferencesDetailed([]byte(`services:
  base: &base
    image: busybox
  api: *base
`), ".")
	if diagnostic == nil || diagnostic.Reason != DiagnosticYAMLUnsupported ||
		diagnostic.Line != 4 || diagnostic.Column != 8 {
		t.Fatalf("unsupported YAML diagnostic = %#v", diagnostic)
	}
}

func TestCaptureRepositorySourceHonorsOptionalEnvironmentFiles(t *testing.T) {
	t.Parallel()

	entry := []byte(`
name: optional-environment
services:
  api:
    container_name: optional-environment-api
    image: example.com/api:1
    network_mode: bridge
    env_file:
      - path: optional.env
        required: false
`)
	for _, present := range []bool{false, true} {
		t.Run(strconv.FormatBool(present), func(t *testing.T) {
			t.Parallel()

			files := map[string][]byte{testRootRepositoryEntry: entry}
			if present {
				files["optional.env"] = []byte("OPTIONAL=present\n")
			}
			source, err := CaptureRepositorySource(
				t.TempDir(), testRootRepositoryEntry,
				map[string]string{composeDisableEnvFile: "true"},
				repositoryFixtureReader(files),
				func(string) (RepositoryPathSnapshot, error) {
					return RepositoryPathSnapshot{}, errRepositoryFixture
				},
			)
			if err != nil {
				t.Fatalf("CaptureRepositorySource() error = %v", err)
			}
			_, captured := source.Repository.Files["optional.env"]
			if captured != present {
				t.Fatalf("optional environment captured = %t, want %t", captured, present)
			}
			project, err := Load(context.Background(), source)
			if err != nil {
				t.Fatalf("Load() error = %v", err)
			}
			workload, err := project.Workload(apiService, resolvedImageForService(t, project, apiService))
			if err != nil {
				t.Fatalf("Workload() error = %v", err)
			}
			want := []string(nil)
			if present {
				want = []string{"OPTIONAL=present"}
			}
			if !slices.Equal(workload.Environment, want) {
				t.Fatalf("environment = %q, want %q", workload.Environment, want)
			}
		})
	}
}

//nolint:funlen // The fixture keeps include and extends path-base semantics in one source graph.
func TestCaptureRepositorySourceResolvesIncludeAndExtendsBases(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		testRepositoryEntry: []byte(`
name: repository
include:
  - path:
      - ../includes/base.yaml
      - ../includes/override.yaml
    project_directory: ../project
    env_file: ../env/include.env
services:
  api:
    image: example.com/api:1
    extends:
      file: ../extends/base.yaml
      service: base
    env_file: ../env/root.env
    label_file: ../labels/root.labels
configs:
  settings:
    file: ../config/settings.json
secrets:
  token:
    file: ../secrets/token
`),
		"includes/base.yaml": []byte(`
services:
  worker:
    image: example.com/worker:1
    env_file: worker.env
`),
		"includes/override.yaml": []byte(`
services:
  worker:
    label_file: labels/worker.labels
`),
		"extends/base.yaml": []byte(`
services:
  base:
    image: example.com/base:1
    env_file: nested/base.env
`),
		"env/include.env":              []byte("SELECTED=1\n"),
		"env/root.env":                 []byte("ROOT=1\n"),
		"labels/root.labels":           []byte("root=true\n"),
		"config/settings.json":         []byte("{}\n"),
		"secrets/token":                []byte("test-only\n"),
		"project/worker.env":           []byte("WORKER=1\n"),
		"project/labels/worker.labels": []byte("worker=true\n"),
		"extends/nested/base.env":      []byte("BASE=1\n"),
	}

	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRepositoryEntry,
		nil,
		repositoryFixtureReader(files),
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	got := slices.Sorted(maps.Keys(source.Repository.Files))
	want := slices.Sorted(maps.Keys(files))
	if !slices.Equal(got, want) {
		t.Fatalf("CaptureRepositorySource() files = %q, want %q", got, want)
	}
	if _, err := Load(context.Background(), source); err != nil {
		t.Fatalf("Load(captured includes and extends) error = %v", err)
	}
}

func TestCaptureRepositorySourceIncludesTrackedDefaultEnvironment(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		testRootRepositoryEntry: []byte("name: root\ninclude:\n  - child/compose.yaml\nservices: {}\n"),
		"child/compose.yaml": []byte(`
services:
  api:
    image: ${API_IMAGE}
`),
		"child/.env": []byte("API_IMAGE=example.com/api:1\n"),
	}
	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		nil,
		repositoryFixtureReader(files),
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	if _, found := source.Repository.Files["child/.env"]; !found {
		t.Fatalf("CaptureRepositorySource() files = %q", slices.Sorted(maps.Keys(source.Repository.Files)))
	}
	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	if names := project.ServiceNames(); !slices.Equal(names, []string{apiService}) {
		t.Fatalf("ServiceNames() = %q", names)
	}
}

func TestCaptureRepositorySourceReusesTrackedDefaultEnvironment(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		testRootRepositoryEntry: []byte("services:\n  api:\n    image: example.com/api:1\n    env_file: .env\n"),
		composeDefaultEnvFile:   []byte("SELECTED=1\n"),
	}
	reads := 0
	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		nil,
		func(path string) (RepositoryFile, bool, error) {
			reads++
			content, found := files[path]

			return RepositoryFile{Content: content}, found, nil
		},
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	if reads != len(files) || source.Environment["SELECTED"] != "1" {
		t.Fatalf("CaptureRepositorySource() reads = %d, source = %#v", reads, source)
	}
}

func TestCaptureRepositorySourceRequiresTrackedBindSource(t *testing.T) {
	t.Parallel()

	for _, content := range []string{
		"services:\n  api:\n    volumes:\n      - ../data:/data:ro\n",
		"services:\n  api:\n    volumes:\n      - type: bind\n        source: ../data\n        target: /data\n",
	} {
		_, err := CaptureRepositorySource(
			t.TempDir(),
			testRepositoryEntry,
			nil,
			repositoryFixtureReader(map[string][]byte{testRepositoryEntry: []byte(content)}),
			func(path string) (RepositoryPathSnapshot, error) {
				if path != testRuntimePath {
					t.Fatalf("runtime path = %q, want data", path)
				}

				return RepositoryPathSnapshot{}, errRepositoryFixture
			},
		)
		if !errors.Is(err, ErrInvalidSource) {
			t.Fatalf("CaptureRepositorySource() error = %v", err)
		}
	}
}

func TestCaptureRepositorySourceLeavesAbsoluteBindsOnRuntimeHost(t *testing.T) {
	t.Parallel()

	content := []byte(`
name: external-storage
services:
  api:
    container_name: external-storage-api
    image: example.com/team/api:1
    network_mode: bridge
    volumes:
      - type: bind
        source: /srv/example/config
        target: /config
        read_only: true
      - type: bind
        source: /srv/example/data
        target: /data
`)
	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		nil,
		repositoryFixtureReader(map[string][]byte{testRootRepositoryEntry: content}),
		func(path string) (RepositoryPathSnapshot, error) {
			t.Fatalf("absolute bind %q was treated as a repository path", path)

			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	if source.Repository == nil || len(source.Repository.RuntimePaths) != 0 {
		t.Fatalf("CaptureRepositorySource() = %#v", source)
	}
	project, err := Load(context.Background(), source)
	if err != nil {
		t.Fatalf("Load() error = %v", err)
	}
	workload, err := project.Workload(apiService, resolvedImageForService(t, project, apiService))
	if err != nil {
		t.Fatalf("Workload() error = %v", err)
	}
	want := []domain.Mount{
		{Kind: domain.MountBind, Source: "/srv/example/config", Target: "/config", ReadOnly: true},
		{Kind: domain.MountBind, Source: "/srv/example/data", Target: runtimeTestDataTarget},
	}
	if !reflect.DeepEqual(workload.Mounts, want) {
		t.Fatalf("Workload() mounts = %#v, want %#v", workload.Mounts, want)
	}
}

func TestCaptureRepositorySourceIgnoresAmbientProjectEnvironment(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		testRootRepositoryEntry: []byte("services:\n  api:\n    image: ${IMAGE}\n"),
		composeDefaultEnvFile:   []byte("IMAGE=example.com/api:1\nBASE=one\nEXPANDED=${BASE}-two\n"),
	}
	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		map[string]string{"IMAGE": "example.com/api:2", "SECRET": "ambient"},
		repositoryFixtureReader(files),
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	if len(source.Repository.Files) != len(files) || len(source.Environment) != 3 ||
		source.Environment["IMAGE"] != "example.com/api:1" || source.Environment["EXPANDED"] != "one-two" {
		t.Fatalf("CaptureRepositorySource() = %#v", source)
	}
	changed := source
	changed.Environment = maps.Clone(source.Environment)
	changed.Environment["IMAGE"] = "example.com/api:2"
	if _, err = Load(context.Background(), changed); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("Load(changed environment) error = %v", err)
	}
}

func TestCaptureRepositorySourceHonorsDisabledProjectEnvironment(t *testing.T) {
	t.Parallel()

	files := map[string][]byte{
		testRootRepositoryEntry: []byte("services: {}\n"),
		composeDefaultEnvFile:   []byte("PRIVATE=ignored\n"),
	}
	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		map[string]string{composeDisableEnvFile: contractTrue},
		repositoryFixtureReader(files),
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	if len(source.Repository.Files) != 1 || source.Environment["PRIVATE"] != "" {
		t.Fatalf("CaptureRepositorySource() = %#v", source)
	}

	_, err = CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		map[string]string{composeDisableEnvFile: "invalid"},
		repositoryFixtureReader(files),
		func(string) (RepositoryPathSnapshot, error) {
			return RepositoryPathSnapshot{}, errRepositoryFixture
		},
	)
	if !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("CaptureRepositorySource(invalid disable value) error = %v", err)
	}
}

func TestMaterializeRepositoryRuntimePublishesOneExactSnapshot(t *testing.T) {
	t.Parallel()

	source, runtimeBase := testRepositoryRuntimeSource(t)

	const publishers = 8
	errorsByPublisher := make([]error, publishers)
	var wait sync.WaitGroup
	for index := range publishers {
		wait.Go(func() {
			errorsByPublisher[index] = source.MaterializeRuntime()
		})
	}
	wait.Wait()
	for index, publishErr := range errorsByPublisher {
		if publishErr != nil {
			t.Fatalf("MaterializeRuntime() publisher %d error = %v", index, publishErr)
		}
	}

	runtimeRoot := repositoryRuntimeRoot(runtimeBase, source.Repository.Digest)
	content, err := os.ReadFile(filepath.Join(runtimeRoot, "data", "run")) //nolint:gosec // Test root is private.
	if err != nil || string(content) != "#!/bin/sh\nexit 0\n" {
		t.Fatalf("materialized runtime file = %q, %v", content, err)
	}
	info, err := os.Stat(filepath.Join(runtimeRoot, "data", "run"))
	if err != nil || info.Mode().Perm() != runtimeExecutableMode {
		t.Fatalf("materialized runtime mode = %v, %v", info, err)
	}
}

func TestMaterializeRepositoryRuntimeRejectsTampering(t *testing.T) {
	t.Parallel()

	source, runtimeBase := testRepositoryRuntimeSource(t)
	if err := source.MaterializeRuntime(); err != nil {
		t.Fatalf("MaterializeRuntime() error = %v", err)
	}
	runtimeRoot := repositoryRuntimeRoot(runtimeBase, source.Repository.Digest)
	if err := os.WriteFile(filepath.Join(runtimeRoot, "extra"), []byte("unexpected\n"), 0o400); err != nil {
		t.Fatal(err)
	}
	if err := source.MaterializeRuntime(); !errors.Is(err, ErrInvalidSource) {
		t.Fatalf("MaterializeRuntime(extra file) error = %v", err)
	}
}

func testRepositoryRuntimeSource(t *testing.T) (Source, string) {
	t.Helper()

	files := map[string][]byte{
		testRootRepositoryEntry: []byte("services:\n  api:\n    volumes:\n      - ./data:/data:ro\n"),
		"data/run":              []byte("#!/bin/sh\nexit 0\n"),
	}
	source, err := CaptureRepositorySource(
		t.TempDir(),
		testRootRepositoryEntry,
		nil,
		repositoryFixtureReader(files),
		func(path string) (RepositoryPathSnapshot, error) {
			if path != testRuntimePath {
				return RepositoryPathSnapshot{}, errRepositoryFixture
			}

			return RepositoryPathSnapshot{
				Directory: true,
				Files: map[string]RepositoryFile{
					"data/run": {Content: files["data/run"], Executable: true},
				},
			}, nil
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}
	runtimeBase := t.TempDir()
	source, err = PinRepositoryRuntime(source, runtimeBase)
	if err != nil {
		t.Fatalf("PinRepositoryRuntime() error = %v", err)
	}

	return source, runtimeBase
}

func repositoryFixtureReader(files map[string][]byte) TrackedFileReader {
	return func(path string) (RepositoryFile, bool, error) {
		content, found := files[path]
		if !found {
			return RepositoryFile{}, false, nil
		}

		return RepositoryFile{Content: content}, true, nil
	}
}
