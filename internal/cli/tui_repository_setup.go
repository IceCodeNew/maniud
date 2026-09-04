package cli

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"time"

	"github.com/IceCodeNew/maniud/internal/tui"
)

const tuiRepositorySetupTimeout = 2 * time.Minute

var (
	errTUIRepositorySetupUnavailable = errors.New("repository setup unavailable")
	errTUIRepositoryCreated          = errors.New("repository created")
	errTUIRepositoryCreateFailed     = errors.New("GitHub repository creation failed")
	errTUIRepositoryCloneFailed      = errors.New("repository clone failed")
	errTUIRepositoryRegistration     = errors.New("repository registration failed")
)

func setupTUIRepository(
	ctx context.Context,
	registrationPath string,
	request tui.RepositorySetupRequest,
) error {
	return setupTUIRepositoryWithHost(
		ctx, registrationPath, request, os.Getenv("GH_HOST"), runTUIRepositoryGH,
	)
}

func setupTUIRepositoryWithHost(
	ctx context.Context,
	registrationPath string,
	request tui.RepositorySetupRequest,
	githubHost string,
	runGH func(context.Context, string, ...string) error,
) error {
	parent, exists, err := inspectTUIRepositoryCheckoutPath(request.Checkout)
	if err != nil {
		return err
	}

	created := false
	switch request.Mode {
	case tui.RepositorySetupCreateGitHub:
		created, err = createTUIRepositoryWithGH(ctx, parent, exists, request, githubHost, runGH)
	case tui.RepositorySetupExisting:
		err = useExistingTUIRepository(ctx, parent, exists, request)
	default:
		return errGitOpsRepositoryInvalid
	}
	if err != nil {
		if created {
			return errors.Join(err, errTUIRepositoryCreated)
		}

		return err
	}

	err = registerTUIRepositoryCheckout(ctx, registrationPath, request)
	if err != nil {
		err = errors.Join(errTUIRepositoryRegistration, err)
		if created {
			return errors.Join(err, errTUIRepositoryCreated)
		}
	}

	return err
}

func createTUIRepositoryWithGH(
	ctx context.Context,
	parent string,
	exists bool,
	request tui.RepositorySetupRequest,
	githubHost string,
	runGH func(context.Context, string, ...string) error,
) (bool, error) {
	remote := tuiGitHubRecoveryRemote(request.Remote, githubHost)
	if remote == "" || runGH == nil {
		return false, errGitOpsRepositoryInvalid
	}
	if exists {
		if !request.Created {
			return false, errGitOpsRepositoryInvalid
		}

		return true, validateTUIRepositoryRemote(ctx, request.Checkout, remote)
	}
	if !request.Created {
		if err := runGH(ctx, parent, "repo", "create", "--private", "--add-readme", request.Remote); err != nil {
			return false, errors.Join(errTUIRepositoryCreateFailed, errTUIRepositorySetupUnavailable)
		}
	}
	if err := runGH(ctx, parent, "repo", "clone", remote, request.Checkout); err != nil {
		return true, errors.Join(errTUIRepositoryCloneFailed, errTUIRepositorySetupUnavailable)
	}

	return true, validateTUIRepositoryRemote(ctx, request.Checkout, remote)
}

func validateTUIRepositoryRemote(ctx context.Context, checkout, expected string) error {
	actual, err := gitRemoteURL(ctx, checkout, gitOpsRemoteName)
	if err != nil || actual != expected {
		return errGitOpsRepositoryInvalid
	}

	return nil
}

func useExistingTUIRepository(
	ctx context.Context,
	parent string,
	exists bool,
	request tui.RepositorySetupRequest,
) error {
	if request.Created || !validGitRemoteURL(request.Remote) {
		return errGitOpsRepositoryInvalid
	}
	if exists {
		return nil
	}
	if _, err := runGit(
		ctx,
		parent,
		"clone", "--quiet", "--origin", gitOpsRemoteName, "--", request.Remote, request.Checkout,
	); err != nil {
		return errors.Join(errTUIRepositoryCloneFailed, errTUIRepositorySetupUnavailable)
	}

	return nil
}

func inspectTUIRepositoryCheckoutPath(path string) (string, bool, error) {
	if !validGitOpsRepositoryPath(path) {
		return "", false, errGitOpsRepositoryInvalid
	}
	parent := filepath.Dir(path)
	physicalParent, err := filepath.EvalSymlinks(parent)
	if err != nil || physicalParent != parent {
		return "", false, errGitOpsRepositoryInvalid
	}
	_, err = os.Lstat(path)
	switch {
	case err == nil:
		return parent, true, nil
	case errors.Is(err, os.ErrNotExist):
		return parent, false, nil
	default:
		return "", false, errGitOpsRepositoryInvalid
	}
}

func registerTUIRepositoryCheckout(
	ctx context.Context,
	registrationPath string,
	request tui.RepositorySetupRequest,
) error {
	root, err := resolveGitOpsRepository(ctx, request.Checkout)
	if err != nil || root != request.Checkout {
		return errGitOpsRepositoryInvalid
	}
	branch, err := currentGitBranch(ctx, root)
	if err != nil {
		return errGitOpsRepositoryInvalid
	}
	if request.Mode == tui.RepositorySetupExisting {
		remote, remoteErr := gitRemoteURL(ctx, root, gitOpsRemoteName)
		if remoteErr != nil || remote != request.Remote {
			return errGitOpsRepositoryInvalid
		}
	}
	root, commit, remote, err := proveGitOpsCheckout(ctx, root, branch)
	if err != nil {
		return errGitOpsRepositoryInvalid
	}
	if err = ensureTUIRepositoryServicesDirectory(root); err != nil {
		return err
	}

	return writeGitOpsRegistration(registrationPath, gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: root, Branch: branch,
		Remote: gitOpsRemoteName, RemoteURL: remote, BaselineCommit: commit,
	})
}

func ensureTUIRepositoryServicesDirectory(rootPath string) (returnErr error) {
	root, err := os.OpenRoot(rootPath)
	if err != nil {
		return errGitOpsRepositoryInvalid
	}
	defer func() { returnErr = errors.Join(returnErr, root.Close()) }()
	info, err := root.Lstat(gitOpsServicesDirectory)
	switch {
	case errors.Is(err, os.ErrNotExist):
		if err = root.Mkdir(gitOpsServicesDirectory, gitOpsRegistrationDirMode); err != nil {
			return errGitOpsRepositoryInvalid
		}
	case err != nil || info == nil || !info.IsDir() || info.Mode()&os.ModeSymlink != 0:
		return errGitOpsRepositoryInvalid
	}

	return nil
}

func validGitHubRepository(value string) bool {
	owner, repository, found := strings.Cut(value, "/")

	return found && owner != "" && repository != "" && !strings.Contains(repository, "/") &&
		len(value) <= 200 && validGitHubName(owner, false) && validGitHubName(repository, true)
}

func validGitHubName(value string, allowDot bool) bool {
	if strings.HasPrefix(value, "-") || strings.HasSuffix(value, "-") {
		return false
	}
	allowed := "abcdefghijklmnopqrstuvwxyzABCDEFGHIJKLMNOPQRSTUVWXYZ0123456789-"
	if allowDot {
		allowed += "._"
	}
	for _, character := range value {
		if !strings.ContainsRune(allowed, character) {
			return false
		}
	}

	return true
}

func runTUIRepositoryGH(ctx context.Context, directory string, arguments ...string) error {
	path, err := exec.LookPath("gh")
	if err != nil {
		return errTUIRepositorySetupUnavailable
	}
	ctx, cancel := context.WithTimeout(ctx, tuiRepositorySetupTimeout)
	defer cancel()
	command := exec.CommandContext(ctx, path, arguments...) //nolint:gosec // gh and every production subcommand are fixed.
	command.Dir = directory
	command.Env = tuiRepositoryGHEnvironment()
	if err = command.Run(); err != nil {
		return errTUIRepositorySetupUnavailable
	}

	return nil
}

func tuiRepositoryGHEnvironment() []string {
	environment := []string{"GH_PROMPT_DISABLED=1"}
	for _, name := range []string{
		"PATH", "HOME", "XDG_CONFIG_HOME", "GH_CONFIG_DIR", "GH_HOST", "GH_TOKEN", "GITHUB_TOKEN",
		"HTTP_PROXY", "HTTPS_PROXY", "NO_PROXY", "SSL_CERT_FILE", "SSL_CERT_DIR",
	} {
		if value, found := os.LookupEnv(name); found {
			environment = append(environment, name+"="+value)
		}
	}

	return environment
}
