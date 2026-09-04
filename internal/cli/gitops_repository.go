package cli

import (
	"context"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/compose"
)

func proveGitOpsCheckout(ctx context.Context, path, branch string) (string, string, string, error) {
	return proveGitOpsCheckoutWithFinalCheck(ctx, path, branch, cleanGitTree)
}

func proveGitOpsCheckoutWithFinalCheck(
	ctx context.Context,
	path string,
	branch string,
	finalState func(context.Context, string) (gitTreeState, error),
) (string, string, string, error) {
	root, state, remote, err := inspectGitOpsCheckout(ctx, path, branch)
	if err != nil {
		return "", "", "", err
	}

	upstream, err := resolveGitObject(ctx, root, gitOpsRemoteName+"/"+branch+"^{commit}")
	if err != nil {
		return "", "", "", errGitOpsRepositoryInvalid
	}
	if err = requireFastForward(ctx, root, upstream, state.head); err != nil {
		return "", "", "", err
	}

	after, err := finalState(ctx, root)
	if err != nil || after != state {
		return "", "", "", errGitOpsRepositoryInvalid
	}

	return root, state.head, remote, nil
}

func inspectGitOpsCheckout(ctx context.Context, path, branch string) (string, gitTreeState, string, error) {
	return inspectGitOpsCheckoutWithState(ctx, path, branch, cleanGitTree)
}

func inspectGitOpsCheckoutWithState(
	ctx context.Context,
	path string,
	branch string,
	stateFor func(context.Context, string) (gitTreeState, error),
) (string, gitTreeState, string, error) {
	root, state, err := inspectLocalGitOpsCheckoutWithState(ctx, path, branch, stateFor)
	if err != nil {
		return "", gitTreeState{}, "", err
	}

	remote, err := gitRemoteURL(ctx, root, gitOpsRemoteName)
	if err != nil || remote == "" {
		return "", gitTreeState{}, "", errGitOpsRepositoryInvalid
	}

	return root, state, remote, nil
}

func inspectLocalGitOpsCheckout(ctx context.Context, path, branch string) (string, gitTreeState, error) {
	return inspectLocalGitOpsCheckoutWithState(ctx, path, branch, cleanGitTree)
}

func inspectLocalGitOpsCheckoutWithState(
	ctx context.Context,
	path string,
	branch string,
	stateFor func(context.Context, string) (gitTreeState, error),
) (string, gitTreeState, error) {
	var empty gitTreeState
	root, err := resolveGitOpsRepository(ctx, path)
	if err != nil {
		return "", empty, err
	}

	state, err := stateFor(ctx, root)
	if err != nil {
		return "", empty, errGitOpsRepositoryInvalid
	}

	current, err := currentGitBranch(ctx, root)
	if err != nil || current != branch {
		return "", empty, errGitOpsRepositoryInvalid
	}

	return root, state, nil
}

func resolveGitOpsRepository(ctx context.Context, path string) (string, error) {
	if !validGitOpsRepositoryPath(path) {
		return "", errGitOpsRepositoryInvalid
	}

	requested, err := filepath.EvalSymlinks(path)
	if err != nil || !filepath.IsAbs(requested) {
		return "", errGitOpsRepositoryInvalid
	}

	output, err := runGit(ctx, requested, "rev-parse", "--show-toplevel")
	if err != nil {
		return "", errGitOpsRepositoryInvalid
	}

	root := filepath.Clean(strings.TrimSpace(string(output)))
	resolved, err := filepath.EvalSymlinks(root)
	if err != nil || resolved != requested {
		return "", errGitOpsRepositoryInvalid
	}
	if err = validateGitProcessConfiguration(ctx, resolved); err != nil {
		return "", errGitOpsRepositoryInvalid
	}

	return resolved, nil
}

func validGitOpsRepositoryPath(path string) bool {
	return path != "" && filepath.IsAbs(path) && filepath.Clean(path) == path
}

func currentGitBranch(ctx context.Context, root string) (string, error) {
	output, err := runGit(ctx, root, "symbolic-ref", "--quiet", "--short", "HEAD")
	value := strings.TrimSpace(string(output))
	if err != nil || !validGitOpsBranch(value) {
		return "", errGitOpsRepositoryInvalid
	}

	return value, nil
}

func gitRemoteURL(ctx context.Context, root, remote string) (string, error) {
	output, err := runGit(ctx, root, "remote", "get-url", remote)
	value := strings.TrimSpace(string(output))
	if err != nil || !validGitRemoteURL(value) {
		return "", errGitOpsRepositoryInvalid
	}

	return value, nil
}

func gitOpsRepositoryScope(
	ctx context.Context,
	registration gitOpsRegistration,
	root string,
) (compose.RepositoryScope, error) {
	remote, err := registeredGitOpsRemoteURL(ctx, root, registration)
	if err != nil {
		return compose.RepositoryScope{}, errGitOpsRepositoryInvalid
	}
	scope, err := compose.NewRepositoryScope(root, remote, registration.Branch)
	if err != nil {
		return compose.RepositoryScope{}, errGitOpsRepositoryInvalid
	}

	return scope, nil
}

func registeredGitOpsRemoteURL(
	ctx context.Context,
	root string,
	registration gitOpsRegistration,
) (string, error) {
	if registration.RemoteURL == "" {
		return "", errGitOpsRepositoryInvalid
	}
	remote, err := gitRemoteURL(ctx, root, registration.Remote)
	if err != nil || remote != registration.RemoteURL {
		return "", errGitOpsRepositoryInvalid
	}

	return registration.RemoteURL, nil
}

func requireFastForward(ctx context.Context, root, ancestor, descendant string) error {
	if ancestor == descendant {
		return nil
	}

	output, err := runGit(ctx, root, "merge-base", "--is-ancestor", ancestor, descendant)
	if err != nil || len(output) != 0 {
		return errGitOpsRepositoryInvalid
	}

	return nil
}
