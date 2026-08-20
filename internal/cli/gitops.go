package cli

import (
	"context"
	"errors"

	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/domain"
)

func executeGitOpsInit(
	ctx context.Context,
	arguments gitOpsInitInvocation,
	environment map[string]string,
) error {
	if !validGitOpsBranch(arguments.branch) {
		return errGitOpsRepositoryInvalid
	}

	root, commit, err := proveGitOpsCheckout(ctx, arguments.repository, arguments.branch)
	if err != nil {
		return classifyGitOpsFailure(err)
	}

	statePath, err := defaultStatePath(environment)
	if err != nil {
		return err
	}

	return writeGitOpsRegistration(gitOpsRegistrationPath(statePath), gitOpsRegistration{
		Version:    gitOpsRegistrationVersion,
		Repository: root,
		Branch:     arguments.branch,
		Remote:     gitOpsRemoteName,
		Commit:     commit,
	})
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
