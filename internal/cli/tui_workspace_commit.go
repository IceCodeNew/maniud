package cli

import (
	"bytes"
	"context"
	"path/filepath"
	"strings"

	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
)

func resolveUnsettledTUICommit(
	cancelErr error,
	commitErr error,
	unsigned bool,
	unchanged bool,
) (bool, error) {
	if cancelErr != nil {
		return false, cancelErr
	}
	if !unsigned && unchanged {
		return true, nil
	}
	if commitErr != nil {
		return false, commitErr
	}

	return false, nil
}

func commitTUIStagedProof(
	ctx context.Context,
	staged tuiStagedProof,
	message string,
	unsigned bool,
) error {
	if unsigned {
		_, err := runGit(
			ctx,
			staged.repository,
			"-c", "user.name=maniud",
			"-c", "user.email=maniud@localhost",
			"-c", "commit.gpgsign=false",
			"commit", "--quiet", "--no-gpg-sign", "--no-verify", "-m", message,
		)

		return err
	}
	if err := validateGitProcessConfiguration(ctx, staged.repository); err != nil {
		return err
	}
	_, err := runGitWithUserConfig(
		ctx, staged.repository, "commit", "--quiet", "-S", "--no-verify", "-m", message,
	)

	return err
}

func validTUICommitMessage(message string) bool {
	return message != "" && len(message) <= maximumTUICommitMessage &&
		strings.TrimSpace(message) == message && !strings.ContainsAny(message, "\x00\r\n")
}

func verifyTUIStagedState(ctx context.Context, staged tuiStagedService) bool {
	if !verifyTUIBaseState(ctx, staged.draft) ||
		!exactStagedPaths(ctx, staged.draft.repository, staged.paths) {
		return false
	}
	tree, err := writeGitTree(ctx, staged.draft.repository)

	return err == nil && tree == staged.expectedTree
}

func (staged tuiStagedService) proof() tuiStagedProof {
	return tuiStagedProof{
		repository: staged.draft.repository, base: staged.draft.base, paths: staged.paths,
		indexStatus: "A", expectedTree: staged.expectedTree,
	}
}

func verifyTUIStagedProof(ctx context.Context, staged tuiStagedProof) bool {
	head, err := resolveGitObject(ctx, staged.repository, "HEAD^{commit}")
	if err != nil || head != staged.base.head ||
		!exactStagedPathStatusWithAttributes(
			ctx, staged.repository, staged.paths, staged.indexStatus, staged.attributeSource,
		) {
		return false
	}
	tree, err := writeGitTree(ctx, staged.repository)

	return err == nil && tree == staged.expectedTree
}

func proveTUICommit(
	ctx context.Context,
	staged tuiStagedService,
	message string,
	requireSignature bool,
) (string, bool) {
	head, err := resolveGitObject(ctx, staged.draft.repository, "HEAD^{commit}")
	if err != nil {
		return "", false
	}
	if head == staged.draft.base.head {
		return "", verifyTUIStagedState(ctx, staged)
	}
	if !proveNewTUICommit(ctx, staged, head, message, requireSignature) {
		return "", false
	}

	return head, false
}

func proveTUIStagedCommit(
	ctx context.Context,
	staged tuiStagedProof,
	message string,
	requireSignature bool,
) (string, bool) {
	head, err := resolveGitObject(ctx, staged.repository, "HEAD^{commit}")
	if err != nil {
		return "", false
	}
	if head == staged.base.head {
		return "", verifyTUIStagedProof(ctx, staged)
	}
	if !proveNewTUIStagedCommit(ctx, staged, head, message, requireSignature) {
		return "", false
	}

	return head, false
}

func proveNewTUICommit(
	ctx context.Context,
	staged tuiStagedService,
	head string,
	message string,
	requireSignature bool,
) bool {
	if !matchesTUICommit(ctx, staged, head, message, requireSignature) {
		return false
	}
	branch, err := currentGitBranch(ctx, staged.draft.repository)
	if err != nil || branch != staged.draft.branch {
		return false
	}
	state, err := gitTreeAllowingTUIDrafts(ctx, staged.draft.repository)

	return err == nil && state.head == head && state.tree == staged.expectedTree
}

func proveNewTUIStagedCommit(
	ctx context.Context,
	staged tuiStagedProof,
	head string,
	message string,
	requireSignature bool,
) bool {
	if !matchesTUIStagedCommit(ctx, staged, head, message, requireSignature) {
		return false
	}
	state, err := cleanGitTreeWithAttributeSource(ctx, staged.repository, staged.attributeSource)

	return err == nil && state.head == head && state.tree == staged.expectedTree
}

func matchesTUICommit(
	ctx context.Context,
	staged tuiStagedService,
	head string,
	message string,
	requireSignature bool,
) bool {
	return matchesTUIStagedCommit(ctx, staged.proof(), head, message, requireSignature)
}

func matchesTUIStagedCommit(
	ctx context.Context,
	staged tuiStagedProof,
	head string,
	message string,
	requireSignature bool,
) bool {
	parents, err := runGit(ctx, staged.repository, "rev-list", "--parents", "-n", "1", head)
	fields := strings.Fields(string(parents))
	if err != nil || len(fields) != 2 || fields[0] != head || fields[1] != staged.base.head {
		return false
	}
	tree, err := resolveGitObject(ctx, staged.repository, head+"^{tree}")
	if err != nil || tree != staged.expectedTree {
		return false
	}
	if !gitCommitMatches(ctx, staged.repository, head, message, requireSignature) {
		return false
	}

	return true
}

func gitCommitMatches(ctx context.Context, repository, head, message string, requireSignature bool) bool {
	commit, err := runGit(ctx, repository, "cat-file", "commit", head)
	header, body, found := bytes.Cut(commit, []byte("\n\n"))
	if err != nil || !found || string(body) != message+"\n" {
		return false
	}

	return !requireSignature || bytes.Contains(header, []byte("\ngpgsig "))
}

func gitCommitSignaturePresent(ctx context.Context, repository, head string) (bool, error) {
	commit, err := runGit(ctx, repository, "cat-file", "commit", head)
	header, _, _ := bytes.Cut(commit, []byte("\n\n"))

	return bytes.Contains(header, []byte("\ngpgsig ")), err
}

//nolint:cyclop // Readback keeps immutable source, checkout, runtime, and provenance proofs ordered.
func committedTUIRequest(
	ctx context.Context,
	staged tuiStagedService,
	head string,
	environment map[string]string,
	runtimeBase string,
) (application.Request, error) {
	entry := filepath.ToSlash(staged.draft.generated.path)
	if !validGitObjectID(head) || !validGitObjectID(staged.expectedTree) ||
		!staged.draft.repositoryScope.Valid() || !onTUIBranch(ctx, staged.draft) {
		return application.Request{}, compose.ErrInvalidSource
	}
	source, err := compose.CaptureRepositorySource(
		staged.draft.repository,
		entry,
		environment,
		func(name string) (compose.RepositoryFile, bool, error) {
			return readCommittedGitFile(ctx, staged.draft.repository, staged.expectedTree, name)
		},
		func(name string) (compose.RepositoryPathSnapshot, error) {
			return readCommittedGitPath(ctx, staged.draft.repository, staged.expectedTree, name)
		},
	)
	if err != nil {
		return application.Request{}, compose.ErrInvalidSource
	}
	after, err := gitTreeAllowingTUIDrafts(ctx, staged.draft.repository)
	if err != nil || after != (gitTreeState{head: head, tree: staged.expectedTree}) ||
		!onTUIBranch(ctx, staged.draft) {
		return application.Request{}, compose.ErrInvalidSource
	}
	source, err = compose.PinRepositoryRuntime(source, runtimeBase)
	if err != nil {
		return application.Request{}, compose.ErrInvalidSource
	}
	provenance, err := bindApplyRepositorySource(
		staged.draft.repository,
		staged.draft.generated.absolutePath,
		staged.draft.repositoryScope,
		source,
	)
	if err != nil {
		return application.Request{}, compose.ErrInvalidSource
	}

	return application.Request{
		Source: source, Service: staged.draft.generated.service, Repository: provenance,
	}, nil
}

func onTUIBranch(ctx context.Context, draft tuiServiceDraft) bool {
	branch, err := currentGitBranch(ctx, draft.repository)

	return err == nil && branch == draft.branch
}

func commitInstructions(draft tuiServiceDraft) []string {
	var instructions []string
	if draft.generated.preparationAbsolute != "" {
		instructions = append(instructions, "sudo sh "+shellArgument(draft.generated.preparationAbsolute))
	}
	instructions = append(
		instructions,
		"git -C "+shellArgument(draft.repository)+" push "+gitOpsRemoteName+" "+shellArgument(draft.branch),
		"maniud tui",
	)

	return instructions
}

func shellArgument(value string) string {
	return "'" + strings.ReplaceAll(value, "'", "'\"'\"'") + "'"
}
