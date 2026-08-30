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
	if snapshot := missing.Snapshot(t.Context()); snapshot.State != tui.CatalogMissing {
		t.Fatalf("missing Snapshot() = %#v", snapshot)
	}

	unavailable := defaultTUICatalog(
		map[string]string{homeKey: "relative"},
		func(context.Context, string) (compose.Source, error) { return compose.Source{}, nil },
	)
	if snapshot := unavailable.Snapshot(t.Context()); snapshot.State != tui.CatalogUnavailable {
		t.Fatalf("unavailable Snapshot() = %#v", snapshot)
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
	want := "services/api.yaml"
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
