package cli

import (
	"context"
	"errors"
	"fmt"
	"os"
	"path/filepath"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

type gitOpsCheckoutInitializer struct {
	mkdir     func(string, os.FileMode) error
	run       func(context.Context, string, ...string) ([]byte, error)
	resolve   func(context.Context, string, string) (string, error)
	evaluate  func(string) (string, error)
	removeAll func(string) error
}

func executeGitOpsInit(
	ctx context.Context,
	arguments gitOpsInitInvocation,
	environment map[string]string,
) error {
	if !validGitOpsBranch(arguments.branch) {
		return errGitOpsRepositoryInvalid
	}

	statePath, err := defaultStatePath(environment)
	if err != nil {
		return err
	}
	registrationPath := gitOpsRegistrationPath(statePath)

	root, commit, created, err := initializeGitOpsCheckout(ctx, arguments.repository, arguments.branch)
	if err != nil {
		return err
	}
	if created {
		registrationErr := writeGitOpsRegistration(registrationPath, gitOpsRegistration{
			Version: gitOpsRegistrationVersion, Repository: root, Branch: arguments.branch,
			Remote: gitOpsRemoteName, BaselineCommit: commit,
		})
		if registrationErr != nil {
			return errors.Join(registrationErr, os.RemoveAll(root))
		}

		return nil
	}

	existing, readErr := readGitOpsRegistration(registrationPath)
	switch {
	case readErr == nil:
		return reuseInitializedGitOpsCheckout(ctx, existing, arguments.repository, arguments.branch)
	case !errors.Is(readErr, os.ErrNotExist):
		return readErr
	}

	root, commit, err = proveGitOpsCheckout(ctx, arguments.repository, arguments.branch)
	if err != nil {
		return err
	}

	return writeGitOpsRegistration(registrationPath, gitOpsRegistration{
		Version: gitOpsRegistrationVersion, Repository: root, Branch: arguments.branch,
		Remote: gitOpsRemoteName, BaselineCommit: commit,
	})
}

func initializeGitOpsCheckout(ctx context.Context, path, branch string) (string, string, bool, error) {
	return initializeGitOpsCheckoutWith(ctx, path, branch, gitOpsCheckoutInitializer{
		mkdir: os.Mkdir, run: runGit, resolve: resolveGitObject,
		evaluate: filepath.EvalSymlinks, removeAll: os.RemoveAll,
	})
}

func initializeGitOpsCheckoutWith(
	ctx context.Context,
	path string,
	branch string,
	initializer gitOpsCheckoutInitializer,
) (string, string, bool, error) {
	if !validGitOpsRepositoryPath(path) {
		return "", "", false, errGitOpsRepositoryInvalid
	}
	if err := initializer.mkdir(path, gitOpsRegistrationDirMode); os.IsExist(err) {
		return "", "", false, nil
	} else if err != nil {
		return "", "", false, fmt.Errorf("create gitops repository: %w", err)
	}

	return populateGitOpsCheckout(ctx, path, branch, initializer)
}

func populateGitOpsCheckout(
	ctx context.Context,
	path string,
	branch string,
	initializer gitOpsCheckoutInitializer,
) (string, string, bool, error) {
	complete := false
	defer func() {
		if !complete {
			_ = initializer.removeAll(path)
		}
	}()
	if err := initializer.mkdir(
		filepath.Join(path, gitOpsServicesDirectory), gitOpsRegistrationDirMode,
	); err != nil {
		return "", "", false, fmt.Errorf("create gitops services directory: %w", err)
	}
	if _, err := initializer.run(ctx, path, "init", "--quiet", "--initial-branch="+branch); err != nil {
		return "", "", false, errGitOpsRepositoryInvalid
	}
	if _, err := initializer.run(
		ctx,
		path,
		"-c", "user.name=maniud",
		"-c", "user.email=maniud@localhost",
		"-c", "commit.gpgsign=false",
		"commit", "--quiet", "--allow-empty", "-m", "Initialize maniud GitOps repository",
	); err != nil {
		return "", "", false, errGitOpsRepositoryInvalid
	}
	commit, err := initializer.resolve(ctx, path, "HEAD^{commit}")
	if err != nil || !validGitObjectID(commit) {
		return "", "", false, errGitOpsRepositoryInvalid
	}
	resolved, err := initializer.evaluate(path)
	if err != nil || resolved != path {
		return "", "", false, errGitOpsRepositoryInvalid
	}
	complete = true

	return resolved, commit, true, nil
}

func reuseInitializedGitOpsCheckout(
	ctx context.Context,
	registration gitOpsRegistration,
	repository string,
	branch string,
) error {
	if registration.Repository != repository || registration.Branch != branch {
		return errGitOpsRepositoryInvalid
	}
	root, state, err := inspectLocalGitOpsCheckout(ctx, repository, branch)
	if err != nil || requireFastForward(ctx, root, registration.BaselineCommit, state.head) != nil {
		return errGitOpsRepositoryInvalid
	}

	return nil
}

func classifyGitOpsCommandFailure(err error) *domain.FailureError {
	switch {
	case err == nil:
		return nil
	case errors.Is(err, errGitOpsRegistrationExists):
		return domain.ApplyFailed(false)
	case errors.Is(err, errGitOpsRepositoryInvalid),
		errors.Is(err, compose.ErrInvalidSource),
		errors.Is(err, errStateHomeUnavailable),
		errors.Is(err, errStateHomeInvalid):
		return domain.InvalidInput()
	default:
		return domain.CommandUnavailable()
	}
}
