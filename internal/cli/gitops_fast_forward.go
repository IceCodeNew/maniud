package cli

import (
	"context"
	"os"
)

type gitOpsCheckoutSelection struct {
	root         string
	commit       string
	awaitingPush bool
}

func fastForwardGitOpsCheckout(
	ctx context.Context,
	path string,
	branch string,
	registeredCommit string,
) (gitOpsCheckoutSelection, error) {
	root, before, err := registeredGitOpsCheckout(ctx, path, branch, registeredCommit)
	if err != nil {
		return gitOpsCheckoutSelection{}, err
	}

	upstream, err := fetchGitOpsCommit(ctx, root, branch)
	if err != nil {
		return gitOpsCheckoutSelection{}, errGitOpsRepositoryInvalid
	}
	if requireFastForward(ctx, root, before, upstream) == nil {
		if err = advanceGitOpsCheckout(ctx, root, before, upstream); err != nil {
			return gitOpsCheckoutSelection{}, err
		}

		return gitOpsCheckoutSelection{root: root, commit: upstream}, nil
	}
	if requireFastForward(ctx, root, upstream, before) == nil {
		return gitOpsCheckoutSelection{root: root, commit: before, awaitingPush: true}, nil
	}

	return gitOpsCheckoutSelection{}, errGitOpsRepositoryInvalid
}

func registeredGitOpsCheckout(
	ctx context.Context,
	path string,
	branch string,
	registeredCommit string,
) (string, string, error) {
	if !validGitObjectID(registeredCommit) {
		return "", "", errGitOpsRepositoryInvalid
	}

	root, before, err := inspectGitOpsCheckoutWithState(
		ctx, path, branch, gitTreeAllowingTUIDrafts,
	)
	if err != nil || requireFastForward(ctx, root, registeredCommit, before.head) != nil {
		return "", "", errGitOpsRepositoryInvalid
	}

	return root, before.head, nil
}

func fetchGitOpsCommit(ctx context.Context, root, branch string) (string, error) {
	return fetchGitOpsCommitWithResolver(ctx, root, branch, resolveGitObject)
}

func fetchGitOpsCommitWithResolver(
	ctx context.Context,
	root string,
	branch string,
	resolve func(context.Context, string, string) (string, error),
) (string, error) {
	remoteBranch := gitOpsRemoteName + "/" + branch
	refspec := "refs/heads/" + branch + ":refs/remotes/" + remoteBranch
	remoteURL, err := gitRemoteURL(ctx, root, gitOpsRemoteName)
	if err != nil {
		return "", errGitOpsRepositoryInvalid
	}
	if _, err := runGit(
		ctx,
		root,
		"fetch", "--quiet", "--no-tags", "--no-recurse-submodules", remoteURL, refspec,
	); err != nil {
		return "", errGitOpsRepositoryInvalid
	}

	upstream, err := resolve(ctx, root, remoteBranch+"^{commit}")
	if err != nil {
		return "", errGitOpsRepositoryInvalid
	}

	return upstream, nil
}

func advanceGitOpsCheckout(ctx context.Context, root, before, upstream string) error {
	if upstream != before {
		if _, err := runGit(
			ctx,
			root,
			"-c", "core.hooksPath="+os.DevNull,
			"merge", "--quiet", "--ff-only", upstream,
		); err != nil {
			return errGitOpsRepositoryInvalid
		}
	}

	after, err := gitTreeAllowingTUIDrafts(ctx, root)
	if err != nil || after.head != upstream {
		return errGitOpsRepositoryInvalid
	}

	return nil
}
