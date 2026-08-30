package cli

import (
	"context"
	"os"
	"path/filepath"
	"slices"
	"testing"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

func TestDefaultTUICatalogReportsRegistrationState(t *testing.T) {
	t.Parallel()

	missing := defaultTUICatalog(
		map[string]string{homeKey: t.TempDir()},
		func(context.Context, string) (compose.Source, error) { return compose.Source{}, nil },
	)
	if snapshot := missing.Snapshot(t.Context()); snapshot.State != tui.CatalogMissing ||
		snapshot.SuggestedRepository == "" {
		t.Fatalf("missing Snapshot() = %#v", snapshot)
	}

	unavailable := defaultTUICatalog(
		map[string]string{homeKey: testRelativePath},
		func(context.Context, string) (compose.Source, error) { return compose.Source{}, nil },
	)
	if snapshot := unavailable.Snapshot(t.Context()); snapshot.State != tui.CatalogUnavailable {
		t.Fatalf("unavailable Snapshot() = %#v", snapshot)
	}
	withoutHome := defaultTUICatalog(
		map[string]string{xdgStateHomeKey: t.TempDir()},
		func(context.Context, string) (compose.Source, error) { return compose.Source{}, nil },
	)
	if withoutHome.suggestedPath != "" {
		t.Fatalf("catalog without HOME suggested %q", withoutHome.suggestedPath)
	}
}

func TestTUICatalogRegistersDefaultRepository(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	repository := filepath.Join(home, "desired-state")
	catalog := defaultTUICatalog(
		map[string]string{homeKey: home, "XDG_STATE_HOME": filepath.Join(home, "state")},
		func(context.Context, string) (compose.Source, error) { return compose.Source{}, nil },
	)
	result := catalog.Register(t.Context(), repository)
	if result.Blocker != tui.BlockerNone || result.Snapshot.State != tui.CatalogReady {
		t.Fatalf("Register() = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(repository, ".git")); err != nil {
		t.Fatalf("registered repository: %v", err)
	}

	invalid := catalog.Register(t.Context(), testRelativePath)
	if invalid.Blocker != tui.BlockerInvalid {
		t.Fatalf("Register(relative) = %#v", invalid)
	}
	unavailable := (&tuiCatalog{}).Register(t.Context(), filepath.Join(t.TempDir(), "desired-state"))
	if unavailable.Blocker != tui.BlockerUnavailable {
		t.Fatalf("Register(unavailable state) = %#v", unavailable)
	}
}

//nolint:cyclop // This lifecycle test verifies that refresh and fresh-open observe filesystem changes in order.
func TestTUICatalogListsAndFreshlyOpensRegisteredServices(t *testing.T) {
	t.Parallel()
	const blockedServiceFile = "b.yaml"

	root := t.TempDir()
	servicesDirectory := filepath.Join(root, gitOpsServicesDirectory)
	if err := os.Mkdir(servicesDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir(services) error = %v", err)
	}
	for _, name := range []string{blockedServiceFile, "a.yml"} {
		if err := os.WriteFile(filepath.Join(servicesDirectory, name), []byte("fixture"), 0o600); err != nil {
			t.Fatalf("WriteFile(%s) error = %v", name, err)
		}
	}
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	writeTUIRegistration(t, registrationPath, root)
	opened := make([]string, 0, 4)
	catalog := &tuiCatalog{
		registrationPath: registrationPath,
		loadSource: func(_ context.Context, path string) (compose.Source, error) {
			opened = append(opened, filepath.Base(path))
			if filepath.Base(path) == blockedServiceFile {
				return compose.Source{}, compose.ErrInvalidSource
			}

			return testComposeSource(t), nil
		},
	}

	snapshot := catalog.Snapshot(t.Context())
	if snapshot.State != tui.CatalogReady || len(snapshot.Services) != 2 ||
		snapshot.Services[0].ID != "services/a.yml" || snapshot.Services[0].Name != applyServiceValue ||
		snapshot.Services[0].Blocker != tui.BlockerNone || snapshot.Services[1].Blocker != tui.BlockerInvalid {
		t.Fatalf("Snapshot() = %#v", snapshot)
	}
	if !slices.Equal(opened, []string{"a.yml", blockedServiceFile}) {
		t.Fatalf("Snapshot() opened = %q", opened)
	}

	result := catalog.OpenRegistered(t.Context(), snapshot.Services[0].ID)
	if result.Blocker != tui.BlockerNone || len(result.Targets) != 1 ||
		result.Targets[0].Service != applyServiceValue {
		t.Fatalf("OpenRegistered(ready) = %#v", result)
	}
	if result = catalog.OpenRegistered(t.Context(), snapshot.Services[1].ID); result.Blocker != tui.BlockerInvalid {
		t.Fatalf("OpenRegistered(blocked) = %#v", result)
	}
	if result = catalog.OpenRegistered(t.Context(), "services/missing.yaml"); result.Blocker != tui.BlockerNotFound {
		t.Fatalf("OpenRegistered(missing) = %#v", result)
	}
	if err := os.Remove(filepath.Join(servicesDirectory, "a.yml")); err != nil {
		t.Fatalf("Remove(a.yml) error = %v", err)
	}
	if result = catalog.OpenRegistered(t.Context(), snapshot.Services[0].ID); result.Blocker != tui.BlockerNotFound {
		t.Fatalf("OpenRegistered(removed) = %#v", result)
	}
}

func TestTUICatalogOpensPathServiceChoices(t *testing.T) {
	t.Parallel()
	const workerService = "worker"

	requested := ""
	catalog := &tuiCatalog{loadSource: func(_ context.Context, path string) (compose.Source, error) {
		requested = path

		return committedTUIComposeSource(t, []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/api:1
    network_mode: bridge
  worker:
    container_name: example-worker
    image: example.com/worker:1
    network_mode: bridge
`)), nil
	}}

	result := catalog.OpenPath(t.Context(), composeFileValue)
	if requested != composeFileValue || result.Blocker != tui.BlockerNone || len(result.Targets) != 2 ||
		result.Targets[0].Service != applyServiceValue || result.Targets[1].Service != workerService ||
		result.Targets[0].Request.Service != applyServiceValue {
		t.Fatalf("OpenPath() = %#v, requested %q", result, requested)
	}
}

func TestTUICatalogContainsInvalidAndUnavailableSources(t *testing.T) {
	t.Parallel()

	invalid := &tuiCatalog{loadSource: func(context.Context, string) (compose.Source, error) {
		return compose.Source{Content: []byte("invalid"), WorkingDir: t.TempDir()}, nil
	}}
	if result := invalid.OpenPath(t.Context(), composeFileValue); result.Blocker != tui.BlockerInvalid {
		t.Fatalf("OpenPath(invalid) = %#v", result)
	}
	local := &tuiCatalog{loadSource: func(context.Context, string) (compose.Source, error) {
		return testComposeSource(t), nil
	}}
	if result := local.OpenPath(t.Context(), composeFileValue); result.Blocker != tui.BlockerInvalid {
		t.Fatalf("OpenPath(uncommitted) = %#v", result)
	}
	if result := (&tuiCatalog{}).OpenPath(t.Context(), composeFileValue); result.Blocker != tui.BlockerUnavailable {
		t.Fatalf("OpenPath(unavailable) = %#v", result)
	}
	runtimeMissing := &tuiCatalog{loadSource: func(context.Context, string) (compose.Source, error) {
		return committedTUIComposeSource(t, []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/api:1
    network_mode: bridge
x-maniud:
  services:
    worker:
      runtime: podman
`)), nil
	}}
	if result := runtimeMissing.openSource(t.Context(), composeFileValue); result.Blocker != tui.BlockerInvalid {
		t.Fatalf("openSource(missing runtime) = %#v", result)
	}
	empty := &tuiCatalog{loadSource: func(context.Context, string) (compose.Source, error) {
		return committedTUIComposeSource(t, []byte("name: example\nservices: {}\n")), nil
	}}
	if result := empty.openSource(t.Context(), composeFileValue); result.Blocker != tui.BlockerInvalid {
		t.Fatalf("openSource(empty project) = %#v", result)
	}

	root := t.TempDir()
	if err := os.WriteFile(filepath.Join(root, gitOpsServicesDirectory), []byte("not a directory"), 0o600); err != nil {
		t.Fatalf("WriteFile(services) error = %v", err)
	}
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	writeTUIRegistration(t, registrationPath, root)
	catalog := &tuiCatalog{registrationPath: registrationPath, loadSource: invalid.loadSource}
	if snapshot := catalog.Snapshot(t.Context()); snapshot.State != tui.CatalogUnavailable {
		t.Fatalf("Snapshot(blocked directory) = %#v", snapshot)
	}
	if result := catalog.OpenRegistered(t.Context(), "services/api.yaml"); result.Blocker != tui.BlockerUnavailable {
		t.Fatalf("OpenRegistered(blocked directory) = %#v", result)
	}
}

func TestTUICatalogProjectsSafeComposeDiagnostic(t *testing.T) {
	t.Parallel()

	content := []byte("services:\n  api:\n    unexpected: true\n")
	catalog := &tuiCatalog{loadSource: func(context.Context, string) (compose.Source, error) {
		return committedTUIComposeSource(t, content), nil
	}}
	result := catalog.OpenPath(t.Context(), composeFileValue)
	if result.Blocker != tui.BlockerInvalid || result.Diagnostic != (tui.SourceDiagnostic{
		File: composeFileValue, Reason: tui.DiagnosticComposeValidation,
	}) {
		t.Fatalf("OpenPath(invalid Compose) = %#v", result)
	}

	malformed := &tuiCatalog{loadSource: func(context.Context, string) (compose.Source, error) {
		return compose.CaptureRepositorySource(
			t.TempDir(),
			composeFileValue,
			nil,
			func(string) (compose.RepositoryFile, bool, error) {
				return compose.RepositoryFile{Content: []byte("services:\n  api:\n    image: [\n")}, true, nil
			},
			func(string) (compose.RepositoryPathSnapshot, error) {
				return compose.RepositoryPathSnapshot{}, nil
			},
		)
	}}
	result = malformed.OpenPath(t.Context(), composeFileValue)
	if result.Blocker != tui.BlockerInvalid || result.Diagnostic != (tui.SourceDiagnostic{
		File: composeFileValue, Reason: tui.DiagnosticYAMLSyntax, Line: 4, Column: 1,
	}) {
		t.Fatalf("OpenPath(malformed YAML) = %#v", result)
	}
}

func TestTUISourceDiagnosticMapsOnlyStableReasons(t *testing.T) {
	t.Parallel()

	source := committedTUIComposeSource(t, []byte("services: {}\n"))
	tests := []struct {
		name   string
		source compose.Source
		err    error
		want   tui.SourceDiagnostic
	}{
		{
			name: "syntax with source fallback", source: source,
			err: &compose.SourceDiagnosticError{Reason: compose.DiagnosticYAMLSyntax, Line: 4, Column: 1},
			want: tui.SourceDiagnostic{
				File: composeFileValue, Reason: tui.DiagnosticYAMLSyntax, Line: 4, Column: 1,
			},
		},
		{
			name: "structure with diagnostic file",
			err: &compose.SourceDiagnosticError{
				File: "included.yaml", Reason: compose.DiagnosticYAMLStructure, Line: 2, Column: 3,
			},
			want: tui.SourceDiagnostic{
				File: "included.yaml", Reason: tui.DiagnosticYAMLStructure, Line: 2, Column: 3,
			},
		},
		{
			name: "unsupported YAML", source: source,
			err:  &compose.SourceDiagnosticError{Reason: compose.DiagnosticYAMLUnsupported},
			want: tui.SourceDiagnostic{File: composeFileValue, Reason: tui.DiagnosticYAMLUnsupported},
		},
		{
			name: "Compose validation", source: source,
			err:  &compose.SourceDiagnosticError{Reason: compose.DiagnosticComposeValidation},
			want: tui.SourceDiagnostic{File: composeFileValue, Reason: tui.DiagnosticComposeValidation},
		},
		{name: "unknown reason", source: source, err: &compose.SourceDiagnosticError{Reason: "vendor"}},
		{name: "generic error", source: source, err: compose.ErrInvalidSource},
		{name: "missing file", err: &compose.SourceDiagnosticError{Reason: compose.DiagnosticYAMLSyntax}},
	}
	for _, test := range tests {
		if got := tuiSourceDiagnostic(test.source, test.err); got != test.want {
			t.Fatalf("tuiSourceDiagnostic(%s) = %#v, want %#v", test.name, got, test.want)
		}
	}
}

func TestTUICatalogRejectsMultiServiceRegisteredFileAndCancellation(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	servicesDirectory := filepath.Join(root, gitOpsServicesDirectory)
	if err := os.Mkdir(servicesDirectory, 0o700); err != nil {
		t.Fatalf("Mkdir(services) error = %v", err)
	}
	path := filepath.Join(servicesDirectory, "multi.yaml")
	if err := os.WriteFile(path, []byte("fixture"), 0o600); err != nil {
		t.Fatalf("WriteFile(multi) error = %v", err)
	}
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	writeTUIRegistration(t, registrationPath, root)
	catalog := &tuiCatalog{
		registrationPath: registrationPath,
		loadSource: func(context.Context, string) (compose.Source, error) {
			return compose.Source{
				Content: []byte(`name: example
services:
  api:
    container_name: example-api
    image: example.com/api:1
    network_mode: bridge
  worker:
    container_name: example-worker
    image: example.com/worker:1
    network_mode: bridge
`), WorkingDir: t.TempDir()}, nil
		},
	}
	if result := catalog.OpenRegistered(t.Context(), "services/multi.yaml"); result.Blocker != tui.BlockerInvalid {
		t.Fatalf("OpenRegistered(multi) = %#v", result)
	}

	cancelled, cancel := context.WithCancel(t.Context())
	cancel()
	if snapshot := catalog.Snapshot(cancelled); snapshot.State != tui.CatalogUnavailable {
		t.Fatalf("Snapshot(cancelled) = %#v", snapshot)
	}
}

func TestRegisteredTUIServiceID(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	want := tuiTestServicePath
	id, valid := registeredTUIServiceID(root, filepath.Join(root, "services", "api.yaml"))
	if !valid || id != want {
		t.Fatalf("registeredTUIServiceID(valid) = %q, %t", id, valid)
	}
	for _, path := range []string{root, filepath.Dir(root)} {
		if id, valid := registeredTUIServiceID(root, path); valid || id != "" {
			t.Fatalf("registeredTUIServiceID(%q) = %q, %t", path, id, valid)
		}
	}
}

func writeTUIRegistration(t *testing.T, path, root string) {
	t.Helper()

	err := writeGitOpsRegistration(path, gitOpsRegistration{
		Version:        gitOpsRegistrationVersion,
		Repository:     root,
		Branch:         gitOpsTestBranch,
		Remote:         gitOpsRemoteName,
		BaselineCommit: gitOpsTestCommit,
	})
	if err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}
}

func committedTUIComposeSource(t *testing.T, content []byte) compose.Source {
	t.Helper()

	source, err := compose.CaptureRepositorySource(
		t.TempDir(),
		composeFileValue,
		nil,
		func(path string) (compose.RepositoryFile, bool, error) {
			if path == composeFileValue {
				return compose.RepositoryFile{Content: content}, true, nil
			}

			return compose.RepositoryFile{}, false, nil
		},
		func(string) (compose.RepositoryPathSnapshot, error) {
			return compose.RepositoryPathSnapshot{}, nil
		},
	)
	if err != nil {
		t.Fatalf("CaptureRepositorySource() error = %v", err)
	}

	return source
}

func TestTUICatalogRegistrationClassifiesInvalidFile(t *testing.T) {
	t.Parallel()

	path := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	if err := os.WriteFile(path, []byte("invalid"), gitOpsRegistrationMode); err != nil {
		t.Fatalf("WriteFile(registration) error = %v", err)
	}
	catalog := &tuiCatalog{registrationPath: path}
	if snapshot := catalog.Snapshot(t.Context()); snapshot.State != tui.CatalogUnavailable {
		t.Fatalf("Snapshot(invalid registration) = %#v", snapshot)
	}
	if result := catalog.OpenRegistered(t.Context(), "services/api.yaml"); result.Blocker != tui.BlockerUnavailable {
		t.Fatalf("OpenRegistered(invalid registration) = %#v", result)
	}
}
