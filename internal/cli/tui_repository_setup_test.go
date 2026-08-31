package cli

import (
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/IceCodeNew/maniud/internal/tui"
)

const tuiTestGitHubRepository = "owner/desired-state"

func TestTUIRepositorySetupCreatesPrivateGitHubRepositoryWithGH(t *testing.T) {
	t.Parallel()

	remote := newTUISetupRemote(t)
	root := t.TempDir()
	checkout := filepath.Join(root, "desired-state")
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	request := tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupCreateGitHub, Remote: tuiTestGitHubRepository, Checkout: checkout,
	}
	var calls [][]string
	err := setupTUIRepositoryWith(
		t.Context(), registrationPath, request,
		func(ctx context.Context, directory string, arguments ...string) error {
			calls = append(calls, slices.Clone(arguments))
			if directory != root {
				t.Fatalf("gh directory = %q, want %q", directory, root)
			}
			if len(arguments) > 1 && arguments[1] == "clone" {
				_, cloneErr := runGit(
					ctx, directory,
					"clone", "--quiet", "--origin", gitOpsRemoteName, "--", remote, checkout,
				)

				return cloneErr
			}

			return nil
		},
	)
	if err != nil {
		t.Fatalf("setupTUIRepositoryWith(create) error = %v", err)
	}
	wantCalls := [][]string{
		{"repo", "create", "--private", "--add-readme", tuiTestGitHubRepository},
		{"repo", "clone", tuiTestGitHubRepository, checkout},
	}
	if !slices.EqualFunc(calls, wantCalls, slices.Equal) {
		t.Fatalf("gh calls = %q, want %q", calls, wantCalls)
	}
	assertTUISetupRegistration(t, registrationPath, checkout)
}

func TestTUIRepositorySetupUsesGitForExistingRepository(t *testing.T) {
	t.Parallel()

	remote := newTUISetupRemote(t)
	checkout := filepath.Join(t.TempDir(), "desired-state")
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	request := tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupExisting, Remote: remote, Checkout: checkout,
	}
	ghCalled := false
	runGH := func(context.Context, string, ...string) error {
		ghCalled = true

		return errTUIRepositorySetupUnavailable
	}
	if err := setupTUIRepositoryWith(t.Context(), registrationPath, request, runGH); err != nil {
		t.Fatalf("setupTUIRepositoryWith(existing) error = %v", err)
	}
	if ghCalled {
		t.Fatal("existing repository setup invoked gh")
	}
	if err := setupTUIRepositoryWith(t.Context(), registrationPath, request, runGH); err != nil {
		t.Fatalf("setupTUIRepositoryWith(reuse) error = %v", err)
	}
	assertTUISetupRegistration(t, registrationPath, checkout)

	otherRemote := newTUISetupRemote(t)
	request.Remote = otherRemote
	if err := setupTUIRepositoryWith(t.Context(), registrationPath, request, runGH); !errors.Is(
		err,
		errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("setupTUIRepositoryWith(remote mismatch) error = %v", err)
	}
}

//nolint:cyclop,funlen // Assertions cover independent setup input and dependency failures.
func TestTUIRepositorySetupContainsInvalidAndUnavailableInputs(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	checkout := filepath.Join(root, "desired-state")
	registrationPath := filepath.Join(t.TempDir(), gitOpsRegistrationName)
	validCreate := tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupCreateGitHub, Remote: tuiTestGitHubRepository, Checkout: checkout,
	}
	if err := os.Mkdir(checkout, 0o700); err != nil {
		t.Fatalf("Mkdir(checkout) error = %v", err)
	}
	if err := setupTUIRepositoryWith(
		t.Context(), registrationPath, validCreate,
		func(context.Context, string, ...string) error { return nil },
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("setupTUIRepositoryWith(existing create path) error = %v", err)
	}
	validCreate.Checkout = filepath.Join(root, "new-checkout")
	if err := setupTUIRepositoryWith(
		t.Context(), registrationPath, validCreate,
		func(context.Context, string, ...string) error { return errTUIRepositorySetupUnavailable },
	); !errors.Is(err, errTUIRepositorySetupUnavailable) {
		t.Fatalf("setupTUIRepositoryWith(create unavailable) error = %v", err)
	}
	ghCalls := 0
	if err := setupTUIRepositoryWith(
		t.Context(), registrationPath, validCreate,
		func(context.Context, string, ...string) error {
			ghCalls++
			if ghCalls == 2 {
				return errTUIRepositorySetupUnavailable
			}

			return nil
		},
	); !errors.Is(err, errTUIRepositorySetupUnavailable) || ghCalls != 2 {
		t.Fatalf("setupTUIRepositoryWith(clone unavailable) error = %v, calls = %d", err, ghCalls)
	}
	validCreate.Remote = testInvalidValue
	if err := setupTUIRepositoryWith(t.Context(), registrationPath, validCreate, nil); !errors.Is(
		err,
		errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("setupTUIRepositoryWith(invalid GitHub repository) error = %v", err)
	}

	for _, request := range []tui.RepositorySetupRequest{
		{Checkout: validCreate.Checkout},
		{Mode: tui.RepositorySetupExisting, Remote: "ssh://example.com/repo", Checkout: validCreate.Checkout},
		{Mode: tui.RepositorySetupExisting, Remote: "https://example.com/repo", Checkout: testRelativePath},
	} {
		if err := setupTUIRepositoryWith(t.Context(), registrationPath, request, nil); !errors.Is(
			err,
			errGitOpsRepositoryInvalid,
		) {
			t.Fatalf("setupTUIRepositoryWith(%#v) error = %v", request, err)
		}
	}
	missingRemote := "file://" + filepath.Join(root, "missing.git")
	if err := setupTUIRepositoryWith(t.Context(), registrationPath, tui.RepositorySetupRequest{
		Mode: tui.RepositorySetupExisting, Remote: missingRemote, Checkout: filepath.Join(root, "missing-clone"),
	}, nil); !errors.Is(err, errTUIRepositorySetupUnavailable) {
		t.Fatalf("setupTUIRepositoryWith(existing clone unavailable) error = %v", err)
	}

	missingParent := filepath.Join(root, "missing", "checkout")
	if _, _, err := inspectTUIRepositoryCheckoutPath(missingParent); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("inspectTUIRepositoryCheckoutPath(missing parent) error = %v", err)
	}
	symlinkParent := filepath.Join(root, "parent-link")
	if err := os.Symlink(root, symlinkParent); err != nil {
		t.Fatalf("Symlink(parent) error = %v", err)
	}
	if _, _, err := inspectTUIRepositoryCheckoutPath(
		filepath.Join(symlinkParent, "checkout"),
	); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("inspectTUIRepositoryCheckoutPath(symlink parent) error = %v", err)
	}
	blockedParent := filepath.Join(root, "blocked")
	if err := os.Mkdir(blockedParent, 0o700); err != nil {
		t.Fatalf("Mkdir(blocked parent) error = %v", err)
	}
	if err := os.Chmod(blockedParent, 0); err != nil {
		t.Fatalf("Chmod(blocked parent) error = %v", err)
	}
	_, _, inspectErr := inspectTUIRepositoryCheckoutPath(filepath.Join(blockedParent, "checkout"))
	if err := os.Chmod(blockedParent, 0o700); err != nil { //nolint:gosec // Restore private directory access.
		t.Fatalf("restore blocked parent mode error = %v", err)
	}
	if inspectErr == nil {
		t.Skip("filesystem allowed Lstat through an unreadable parent")
	}
	if !errors.Is(inspectErr, errGitOpsRepositoryInvalid) {
		t.Fatalf("inspectTUIRepositoryCheckoutPath(unreadable parent) error = %v", inspectErr)
	}
}

//nolint:cyclop,funlen // Subtests keep independent checkout proof failures visible in one table-like fixture.
func TestTUIRepositorySetupContainsCheckoutRegistrationFailures(t *testing.T) {
	t.Parallel()

	newCheckout := func(t *testing.T) (string, string) {
		t.Helper()
		remote := newTUISetupRemote(t)
		checkout := filepath.Join(t.TempDir(), "checkout")
		if _, err := runGit(
			t.Context(), filepath.Dir(checkout),
			"clone", "--quiet", "--origin", gitOpsRemoteName, "--", remote, checkout,
		); err != nil {
			t.Fatalf("git clone error = %v", err)
		}

		return remote, checkout
	}

	t.Run("missing checkout", func(t *testing.T) {
		t.Parallel()
		if err := registerTUIRepositoryCheckout(
			t.Context(),
			filepath.Join(t.TempDir(), "registration"),
			tui.RepositorySetupRequest{
				Mode: tui.RepositorySetupExisting, Remote: "file:///missing", Checkout: filepath.Join(t.TempDir(), "missing"),
			},
		); !errors.Is(err, errGitOpsRepositoryInvalid) {
			t.Fatalf("registerTUIRepositoryCheckout(missing) error = %v", err)
		}
	})

	t.Run("detached head", func(t *testing.T) {
		t.Parallel()
		remote, checkout := newCheckout(t)
		if _, err := runGit(t.Context(), checkout, "checkout", "--quiet", "--detach"); err != nil {
			t.Fatalf("git checkout --detach error = %v", err)
		}
		if err := registerTUIRepositoryCheckout(
			t.Context(),
			filepath.Join(t.TempDir(), "registration"),
			tui.RepositorySetupRequest{Mode: tui.RepositorySetupExisting, Remote: remote, Checkout: checkout},
		); !errors.Is(err, errGitOpsRepositoryInvalid) {
			t.Fatalf("registerTUIRepositoryCheckout(detached) error = %v", err)
		}
	})

	t.Run("unproven checkout", func(t *testing.T) {
		t.Parallel()
		remote, checkout := newCheckout(t)
		if _, err := runGit(t.Context(), checkout, "remote", "remove", gitOpsRemoteName); err != nil {
			t.Fatalf("git remote remove error = %v", err)
		}
		if err := registerTUIRepositoryCheckout(
			t.Context(),
			filepath.Join(t.TempDir(), "registration"),
			tui.RepositorySetupRequest{Mode: tui.RepositorySetupCreateGitHub, Remote: remote, Checkout: checkout},
		); !errors.Is(err, errGitOpsRepositoryInvalid) {
			t.Fatalf("registerTUIRepositoryCheckout(unproven) error = %v", err)
		}
	})

	t.Run("invalid services path", func(t *testing.T) {
		t.Parallel()
		remote, checkout := newCheckout(t)
		if err := os.WriteFile(filepath.Join(checkout, gitOpsServicesDirectory), []byte("file"), 0o600); err != nil {
			t.Fatalf("WriteFile(services) error = %v", err)
		}
		if err := registerTUIRepositoryCheckout(
			t.Context(),
			filepath.Join(t.TempDir(), "registration"),
			tui.RepositorySetupRequest{Mode: tui.RepositorySetupExisting, Remote: remote, Checkout: checkout},
		); !errors.Is(err, errGitOpsRepositoryInvalid) {
			t.Fatalf("registerTUIRepositoryCheckout(services file) error = %v", err)
		}
	})

	t.Run("unwritable services path", func(t *testing.T) {
		t.Parallel()
		remote, checkout := newCheckout(t)
		if err := os.Chmod(checkout, 0o500); err != nil { //nolint:gosec // The test removes parent write access.
			t.Fatalf("Chmod(checkout) error = %v", err)
		}
		registerErr := registerTUIRepositoryCheckout(
			t.Context(),
			filepath.Join(t.TempDir(), "registration"),
			tui.RepositorySetupRequest{
				Mode: tui.RepositorySetupExisting, Remote: remote, Checkout: checkout,
			},
		)
		if err := os.Chmod(checkout, 0o700); err != nil { //nolint:gosec // Restore private directory access.
			t.Fatalf("restore checkout mode error = %v", err)
		}
		if registerErr == nil {
			t.Skip("filesystem allowed services creation in a read-only checkout")
		}
		if !errors.Is(registerErr, errGitOpsRepositoryInvalid) {
			t.Fatalf("registerTUIRepositoryCheckout(read-only) error = %v", registerErr)
		}
	})
}

//nolint:cyclop // Assertions cover independent repository-name and services-directory boundaries.
func TestTUIRepositorySetupValidatesServicesDirectoryAndGitHubName(t *testing.T) {
	t.Parallel()

	for value, want := range map[string]bool{
		"owner/repository": true,
		"Owner/repo.name":  true,
		"owner/repo_name":  true,
		"owner":            false,
		"owner/repo/extra": false,
		"owner_/repo":      false,
		"-owner/repo":      false,
		"owner-/repo":      false,
		"owner/-repo":      false,
		"owner/répo":       false,
		strings.Repeat("a", 100) + "/" + strings.Repeat("b", 101): false,
	} {
		if got := validGitHubRepository(value); got != want {
			t.Fatalf("validGitHubRepository(%q) = %t, want %t", value, got, want)
		}
	}

	root := t.TempDir()
	if err := ensureTUIRepositoryServicesDirectory(root); err != nil {
		t.Fatalf("ensureTUIRepositoryServicesDirectory(create) error = %v", err)
	}
	if err := ensureTUIRepositoryServicesDirectory(root); err != nil {
		t.Fatalf("ensureTUIRepositoryServicesDirectory(existing) error = %v", err)
	}
	fileRoot := t.TempDir()
	if err := os.WriteFile(filepath.Join(fileRoot, gitOpsServicesDirectory), []byte("file"), 0o600); err != nil {
		t.Fatalf("WriteFile(services) error = %v", err)
	}
	if err := ensureTUIRepositoryServicesDirectory(fileRoot); !errors.Is(err, errGitOpsRepositoryInvalid) {
		t.Fatalf("ensureTUIRepositoryServicesDirectory(file) error = %v", err)
	}
	if err := ensureTUIRepositoryServicesDirectory(filepath.Join(root, testMissingName)); !errors.Is(
		err,
		errGitOpsRepositoryInvalid,
	) {
		t.Fatalf("ensureTUIRepositoryServicesDirectory(missing root) error = %v", err)
	}
	readOnlyRoot := t.TempDir()
	if err := os.Chmod(readOnlyRoot, 0o500); err != nil { //nolint:gosec // The test removes directory write access.
		t.Fatalf("Chmod(read-only root) error = %v", err)
	}
	createErr := ensureTUIRepositoryServicesDirectory(readOnlyRoot)
	if err := os.Chmod(readOnlyRoot, 0o700); err != nil { //nolint:gosec // Restore private directory access.
		t.Fatalf("restore root mode error = %v", err)
	}
	if createErr == nil {
		t.Skip("filesystem allowed directory creation in a read-only directory")
	}
	if !errors.Is(createErr, errGitOpsRepositoryInvalid) {
		t.Fatalf("ensureTUIRepositoryServicesDirectory(read-only) error = %v", createErr)
	}
}

func TestRunTUIRepositoryGHUsesBoundedEnvironment(t *testing.T) {
	directory := t.TempDir()
	script := filepath.Join(directory, "gh")
	//nolint:gosec // This private temporary executable is the controlled gh test fixture.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 0\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(gh) error = %v", err)
	}
	t.Setenv("PATH", directory)
	t.Setenv("GH_TOKEN", "test-token")
	t.Setenv("UNRELATED_VALUE", "not-forwarded")
	if err := runTUIRepositoryGH(t.Context(), directory, "repo", "create"); err != nil {
		t.Fatalf("runTUIRepositoryGH(success) error = %v", err)
	}
	environment := tuiRepositoryGHEnvironment()
	if !slices.Contains(environment, "GH_PROMPT_DISABLED=1") ||
		!slices.Contains(environment, "GH_TOKEN=test-token") ||
		slices.Contains(environment, "UNRELATED_VALUE=not-forwarded") {
		t.Fatalf("tuiRepositoryGHEnvironment() = %q", environment)
	}
	//nolint:gosec // This private temporary executable is the controlled failing gh fixture.
	if err := os.WriteFile(script, []byte("#!/bin/sh\nexit 1\n"), 0o700); err != nil {
		t.Fatalf("WriteFile(failing gh) error = %v", err)
	}
	if err := runTUIRepositoryGH(t.Context(), directory, "repo", "create"); !errors.Is(
		err,
		errTUIRepositorySetupUnavailable,
	) {
		t.Fatalf("runTUIRepositoryGH(failure) error = %v", err)
	}
	if err := os.Remove(script); err != nil {
		t.Fatalf("Remove(gh) error = %v", err)
	}
	if err := runTUIRepositoryGH(t.Context(), directory, "repo", "create"); !errors.Is(
		err,
		errTUIRepositorySetupUnavailable,
	) {
		t.Fatalf("runTUIRepositoryGH(missing) error = %v", err)
	}
}

func newTUISetupRemote(t *testing.T) string {
	t.Helper()

	source := initGitOpsTestRepository(t)
	parent := t.TempDir()
	remote := filepath.Join(parent, "desired-state.git")
	if _, err := runGit(t.Context(), parent, "clone", "--quiet", "--bare", "--", source, remote); err != nil {
		t.Fatalf("create setup remote: %v", err)
	}

	return "file://" + remote
}

func assertTUISetupRegistration(t *testing.T, registrationPath, checkout string) {
	t.Helper()

	registration, err := readGitOpsRegistration(registrationPath)
	if err != nil || registration.Repository != checkout || registration.Remote != gitOpsRemoteName ||
		registration.Branch == "" || !validGitObjectID(registration.BaselineCommit) {
		t.Fatalf("registration = %#v, %v", registration, err)
	}
	if _, err := os.Stat(filepath.Join(checkout, gitOpsServicesDirectory)); err != nil {
		t.Fatalf("services directory: %v", err)
	}
}
