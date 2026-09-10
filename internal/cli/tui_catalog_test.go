package cli

import (
	"context"
	"errors"
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

func TestTUICatalogRegistersExistingRepository(t *testing.T) {
	t.Parallel()

	home := t.TempDir()
	source := initGitOpsTestRepository(t)
	remote := filepath.Join(home, "desired-state.git")
	if _, err := runGit(t.Context(), home, "clone", "--quiet", "--bare", "--", source, remote); err != nil {
		t.Fatalf("create bare remote: %v", err)
	}
	remoteURL := "file://" + remote
	checkout := filepath.Join(home, "desired-state")
	catalog := defaultTUICatalog(
		map[string]string{homeKey: home, "XDG_STATE_HOME": filepath.Join(home, "state")},
		func(context.Context, string) (compose.Source, error) { return compose.Source{}, nil },
	)
	request := tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupExisting, Remote: remoteURL, Checkout: checkout,
	}
	result := catalog.Register(t.Context(), request)
	if result.Failure != tui.RepositorySetupReady || result.Snapshot.State != tui.CatalogReady {
		t.Fatalf("Register() = %#v", result)
	}
	if _, err := os.Stat(filepath.Join(checkout, ".git")); err != nil {
		t.Fatalf("registered repository: %v", err)
	}
	if _, err := os.Stat(filepath.Join(checkout, gitOpsServicesDirectory)); err != nil {
		t.Fatalf("registered services directory: %v", err)
	}

	invalid := catalog.Register(t.Context(), tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupExisting, Remote: remoteURL, Checkout: testRelativePath,
	})
	if invalid.Failure != tui.RepositorySetupInvalidInput {
		t.Fatalf("Register(relative) = %#v", invalid)
	}
	catalog.setupRepository = func(context.Context, string, tui.RepositorySetupRequest) error {
		return errGeneratedComposeTest
	}
	if failed := catalog.Register(t.Context(), request); failed.Failure != tui.RepositorySetupUnavailable {
		t.Fatalf("Register(repository failure) = %#v", failed)
	}
	unavailable := (&tuiCatalog{}).Register(t.Context(), request)
	if unavailable.Failure != tui.RepositorySetupUnavailable {
		t.Fatalf("Register(unavailable state) = %#v", unavailable)
	}
}

func TestTUICatalogReportsCreatedRemoteRecovery(t *testing.T) {
	t.Parallel()

	checkout := filepath.Join(t.TempDir(), "desired-state")
	catalog := &tuiCatalog{registrationPath: filepath.Join(t.TempDir(), gitOpsRegistrationName)}
	catalog.setupRepository = func(context.Context, string, tui.RepositorySetupRequest) error {
		return errors.Join(errTUIRepositoryCloneFailed, errTUIRepositoryCreated)
	}
	created := catalog.Register(t.Context(), tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupCreateGitHub, Remote: tuiTestGitHubRepository, Checkout: checkout,
	})
	if created.Failure != tui.RepositorySetupCloneFailed ||
		created.RecoveryRepository != tuiTestGitHubRepository {
		t.Fatalf("Register(created remote) = %#v", created)
	}
}

func TestTUICatalogReportsCreatedEnterpriseRemoteRecovery(t *testing.T) {
	t.Parallel()

	checkout := filepath.Join(t.TempDir(), "desired-state")
	catalog := &tuiCatalog{
		registrationPath: filepath.Join(t.TempDir(), gitOpsRegistrationName),
		githubHost:       "github.example.com",
	}
	catalog.setupRepository = func(context.Context, string, tui.RepositorySetupRequest) error {
		return errors.Join(errTUIRepositoryRegistration, errTUIRepositoryCreated)
	}
	created := catalog.Register(t.Context(), tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupCreateGitHub, Remote: tuiTestGitHubRepository, Checkout: checkout,
	})
	if created.Failure != tui.RepositorySetupRegistrationFailed ||
		created.RecoveryRepository != tuiTestGitHubRepository {
		t.Fatalf("Register(created enterprise remote) = %#v", created)
	}
}

func TestRepositorySetupFailureClassification(t *testing.T) {
	t.Parallel()

	for _, test := range []struct {
		err  error
		want tui.RepositorySetupFailure
	}{
		{errTUIRepositoryCreateFailed, tui.RepositorySetupGitHubFailed},
		{errTUIRepositoryCloneFailed, tui.RepositorySetupCloneFailed},
		{errTUIRepositoryRegistration, tui.RepositorySetupRegistrationFailed},
		{errGitOpsRepositoryInvalid, tui.RepositorySetupInvalidInput},
		{errGitOpsRegistrationExists, tui.RepositorySetupInvalidInput},
		{compose.ErrInvalidSource, tui.RepositorySetupInvalidInput},
		{errGeneratedComposeTest, tui.RepositorySetupUnavailable},
	} {
		if got := repositorySetupFailure(test.err); got != test.want {
			t.Fatalf("repositorySetupFailure(%v) = %q, want %q", test.err, got, test.want)
		}
	}
}

func TestTUIGitHubRecoveryRemoteRejectsMalformedHost(t *testing.T) {
	t.Parallel()

	for _, host := range []string{
		"https://github.example.com",
		"user@github.example.com",
		"github.example.com/path",
		"github.example.com?query",
		"github.example.com#fragment",
	} {
		if remote := tuiGitHubRecoveryRemote(tuiTestGitHubRepository, host); remote != "" {
			t.Fatalf("tuiGitHubRecoveryRemote(%q) = %q", host, remote)
		}
	}
}

//nolint:cyclop // This lifecycle test verifies that refresh and fresh-open observe repository changes in order.
func TestTUICatalogListsAndFreshlyOpensRegisteredServices(t *testing.T) {
	t.Parallel()
	const blockedServiceFile = "b.yaml"
	const composeContent = `name: example
services:
  api:
    container_name: example-api
    image: example.com/api:1
    network_mode: bridge
`

	root := initGitOpsTestRepository(t)
	writeGitOpsTestCommit(t, root, "services/a.yml", composeContent, "add a")
	writeGitOpsTestCommit(t, root, "services/"+blockedServiceFile, "fixture", "add b")
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

			return committedTUIComposeSourceAt(
				t,
				root,
				filepath.ToSlash(filepath.Join(gitOpsServicesDirectory, filepath.Base(path))),
				[]byte(composeContent),
			), nil
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
		result.Targets[0].Service != applyServiceValue ||
		!result.Targets[0].Request.Repository.ValidFor(result.Targets[0].Request.Source.Repository.Digest) {
		t.Fatalf("OpenRegistered(ready) = %#v", result)
	}
	if result = catalog.OpenRegistered(t.Context(), snapshot.Services[1].ID); result.Blocker != tui.BlockerInvalid {
		t.Fatalf("OpenRegistered(blocked) = %#v", result)
	}
	if result = catalog.OpenRegistered(t.Context(), "services/missing.yaml"); result.Blocker != tui.BlockerNotFound {
		t.Fatalf("OpenRegistered(missing) = %#v", result)
	}
	removeAndCommitTUIService(t, root, "services/a.yml")
	if result = catalog.OpenRegistered(t.Context(), snapshot.Services[0].ID); result.Blocker != tui.BlockerNotFound {
		t.Fatalf("OpenRegistered(removed) = %#v", result)
	}
}

func removeAndCommitTUIService(t *testing.T, root, path string) {
	t.Helper()

	if err := os.Remove(filepath.Join(root, filepath.FromSlash(path))); err != nil {
		t.Fatalf("Remove(%s) error = %v", filepath.Base(path), err)
	}
	if _, err := runGit(t.Context(), root, "add", "--update", "--", path); err != nil {
		t.Fatalf("git add removal error = %v", err)
	}
	if _, err := runGit(
		t.Context(), root,
		"-c", "user.name=Maniud Tests",
		"-c", "user.email=maniud@example.invalid",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "-m", "remove service",
	); err != nil {
		t.Fatalf("git commit removal error = %v", err)
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

	const composeContent = `name: example
services:
  api:
    container_name: example-api
    image: example.com/api:1
    network_mode: bridge
  worker:
    container_name: example-worker
    image: example.com/worker:1
    network_mode: bridge
`
	root := initGitOpsTestRepository(t)
	writeGitOpsTestCommit(t, root, "services/multi.yaml", composeContent, "add services")
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	writeTUIRegistration(t, registrationPath, root)
	catalog := &tuiCatalog{
		registrationPath: registrationPath,
		loadSource: func(context.Context, string) (compose.Source, error) {
			return committedTUIComposeSourceAt(
				t, root, "services/multi.yaml", []byte(composeContent),
			), nil
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

func TestTUICatalogContainsRegisteredRepositoryFailures(t *testing.T) {
	t.Parallel()

	t.Run("missing remote", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsTestRepository(t)
		registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
		writeTUIRegistration(t, registrationPath, root)
		if _, err := runGit(t.Context(), root, "remote", "remove", gitOpsRemoteName); err != nil {
			t.Fatalf("git remote remove error = %v", err)
		}
		catalog := &tuiCatalog{registrationPath: registrationPath}
		if result := catalog.OpenRegistered(t.Context(), tuiTestServicePath); result.Blocker != tui.BlockerUnavailable {
			t.Fatalf("OpenRegistered(missing remote) = %#v", result)
		}
	})

	t.Run("services path is a file", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsTestRepository(t)
		writeGitOpsTestCommit(t, root, gitOpsServicesDirectory, "not a directory\n", "block services")
		registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
		writeTUIRegistration(t, registrationPath, root)
		catalog := &tuiCatalog{registrationPath: registrationPath}
		if result := catalog.OpenRegistered(t.Context(), tuiTestServicePath); result.Blocker != tui.BlockerUnavailable {
			t.Fatalf("OpenRegistered(blocked services path) = %#v", result)
		}
	})

	t.Run("source outside repository", func(t *testing.T) {
		t.Parallel()

		root := initGitOpsTestRepository(t)
		scope, err := compose.NewRepositoryScope(root, root, gitOpsTestBranch)
		if err != nil {
			t.Fatalf("NewRepositoryScope() error = %v", err)
		}
		catalog := &tuiCatalog{loadSource: func(context.Context, string) (compose.Source, error) {
			return committedTUIComposeSource(t, []byte(
				"name: example\nservices:\n  api:\n    container_name: example-api\n"+
					"    image: example.com/api:1\n    network_mode: bridge\n",
			)), nil
		}}
		result := catalog.openRepositorySource(
			t.Context(), filepath.Join(root, composeFileValue), root, scope,
		)
		if result.Blocker != tui.BlockerInvalid {
			t.Fatalf("openRepositorySource(outside source) = %#v", result)
		}
	})
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
		Version: gitOpsRegistrationVersion, Repository: root, Branch: gitOpsTestBranch,
		Remote: gitOpsRemoteName, RemoteURL: root, BaselineCommit: gitOpsTestCommit,
	})
	if err != nil {
		t.Fatalf("writeGitOpsRegistration() error = %v", err)
	}
}

func committedTUIComposeSource(t *testing.T, content []byte) compose.Source {
	t.Helper()

	return committedTUIComposeSourceAt(t, t.TempDir(), composeFileValue, content)
}

func committedTUIComposeSourceAt(t *testing.T, root, entry string, content []byte) compose.Source {
	t.Helper()

	source, err := compose.CaptureRepositorySource(
		root,
		entry,
		nil,
		func(path string) (compose.RepositoryFile, bool, error) {
			if path == entry {
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
