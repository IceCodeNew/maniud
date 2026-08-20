package cli

import (
	"context"
	"os"
)

func fastForwardGitOpsCheckout(
	ctx context.Context,
	path string,
	branch string,
	registeredCommit string,
) (string, string, error) {
	root, before, err := registeredGitOpsCheckout(ctx, path, branch, registeredCommit)
	if err != nil {
		return "", "", err
	}

	upstream, err := fetchGitOpsCommit(ctx, root, branch)
	if err != nil || requireFastForward(ctx, root, before, upstream) != nil {
		return "", "", errGitOpsRepositoryInvalid
	}
	if err = advanceGitOpsCheckout(ctx, root, before, upstream); err != nil {
		return "", "", errGitOpsRepositoryInvalid
	}

	return root, upstream, nil
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

	root, before, err := inspectGitOpsCheckout(ctx, path, branch)
	if err != nil || requireFastForward(ctx, root, registeredCommit, before.head) != nil {
		return "", "", errGitOpsRepositoryInvalid
	}

	return root, before.head, nil
}

func fetchGitOpsCommit(ctx context.Context, root, branch string) (string, error) {
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

	upstream, err := resolveGitObject(ctx, root, remoteBranch+"^{commit}")
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

	after, err := cleanGitTree(ctx, root)
	if err != nil || after.head != upstream {
		return errGitOpsRepositoryInvalid
	}

	return nil
}
