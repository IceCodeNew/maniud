package cli

import (
	"context"
	"errors"
	"slices"
	"strconv"
	"strings"
	"unicode/utf8"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

const (
	maximumDeploymentHistory = 100
	deploymentHistoryFields  = 2
)

func (workspace *tuiDeploymentWorkspace) History(
	ctx context.Context,
	request application.Request,
) ([]tui.DeploymentHistoryEntry, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged != nil {
		return nil, publicDeploymentActionError(
			errDeploymentEditInvalid, tui.DeploymentPreconditionFailed,
		)
	}
	_, repository, entry, _, err := workspace.openRequest(ctx, request)
	if err != nil {
		return nil, publicDeploymentActionError(err, tui.DeploymentPreconditionFailed)
	}

	history, err := deploymentHistory(ctx, repository, entry)
	if err != nil {
		return nil, publicDeploymentActionError(err, tui.DeploymentHistoryUnavailable)
	}

	return history, nil
}

func deploymentHistory(
	ctx context.Context,
	repository string,
	entry string,
) ([]tui.DeploymentHistoryEntry, error) {
	return deploymentHistoryWith(ctx, repository, entry, runGit, gitCommitSignaturePresent)
}

func deploymentHistoryWith(
	ctx context.Context,
	repository string,
	entry string,
	logHistory func(context.Context, string, ...string) ([]byte, error),
	signaturePresent func(context.Context, string, string) (bool, error),
) ([]tui.DeploymentHistoryEntry, error) {
	output, err := logHistory(
		ctx,
		repository,
		"log", "-z", "--first-parent", "-n", strconv.Itoa(maximumDeploymentHistory),
		"--format=%H%x00%s", "--", entry,
	)
	if err != nil || len(output) == 0 {
		return nil, errDeploymentEditInvalid
	}
	history, err := parseDeploymentHistory(output)
	if err != nil {
		return nil, err
	}
	for index := range history {
		history[index].SignaturePresent, err = signaturePresent(
			ctx, repository, history[index].Revision,
		)
		if err != nil {
			return nil, errDeploymentEditInvalid
		}
	}

	return history, nil
}

func parseDeploymentHistory(output []byte) ([]tui.DeploymentHistoryEntry, error) {
	fields, valid := splitNullTerminated(output)
	if !valid || len(fields)%deploymentHistoryFields != 0 ||
		len(fields) > maximumDeploymentHistory*deploymentHistoryFields {
		return nil, errDeploymentEditInvalid
	}
	history := make([]tui.DeploymentHistoryEntry, 0, len(fields)/deploymentHistoryFields)
	for index := 0; index < len(fields); index += deploymentHistoryFields {
		revision := string(fields[index])
		subject := string(fields[index+1])
		if !validGitObjectID(revision) ||
			subject == "" || !utf8.ValidString(subject) || strings.ContainsAny(subject, "\x00\r\n") {
			return nil, errDeploymentEditInvalid
		}
		history = append(history, tui.DeploymentHistoryEntry{
			Revision: revision, Subject: subject,
		})
	}

	return history, nil
}

func (workspace *tuiDeploymentWorkspace) PreviewRestore(
	ctx context.Context,
	request application.Request,
	revision string,
) (tui.DeploymentEditPreview, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			errDeploymentEditInvalid, tui.DeploymentPreconditionFailed,
		)
	}
	workspace.draft = nil
	source, repository, entry, base, err := workspace.openRequest(ctx, request)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}
	candidate, proposed, err := workspace.deploymentRestoreCandidate(
		ctx, request, source, repository, entry, base, revision,
	)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentHistoryEntryInvalid,
		)
	}
	current, err := deploymentServiceSpec(ctx, source, request.Service)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}
	scope, err := workspace.requestScope(ctx, request, source)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}
	draft := tuiDeploymentDraft{
		request: request, source: source, candidate: candidate, repository: repository,
		entry: entry, base: base, fields: application.DeploymentFields(),
		current: current, proposed: proposed, restore: revision, scope: scope,
	}

	preview, err := workspace.confirmDeploymentPreview(ctx, draft)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}

	return preview, nil
}

func (workspace *tuiDeploymentWorkspace) deploymentRestoreCandidate(
	ctx context.Context,
	request application.Request,
	source compose.Source,
	repository string,
	entry string,
	base gitTreeState,
	revision string,
) (compose.Source, containerconfig.Spec, error) {
	history, err := deploymentHistory(ctx, repository, entry)
	if err != nil || !slices.ContainsFunc(history, func(item tui.DeploymentHistoryEntry) bool {
		return item.Revision == revision
	}) {
		return compose.Source{}, containerconfig.Spec{}, errors.Join(errDeploymentEditInvalid, err)
	}
	tree, err := resolveGitObject(ctx, repository, revision+"^{tree}")
	if err != nil {
		return compose.Source{}, containerconfig.Spec{}, err
	}
	treeEntry, found, err := readGitTreeEntry(ctx, repository, tree, entry)
	if err != nil || !found || !treeEntry.regularFile() {
		return compose.Source{}, containerconfig.Spec{}, errors.Join(errDeploymentEditInvalid, err)
	}
	content, err := readGitBlob(ctx, repository, treeEntry.object)
	if err != nil {
		return compose.Source{}, containerconfig.Spec{}, err
	}
	candidate, err := captureDeploymentRestore(
		ctx, source, base.tree, content, workspace.environment, workspace.runtimeBase,
	)
	if err != nil {
		return compose.Source{}, containerconfig.Spec{}, err
	}
	proposed, err := deploymentServiceSpec(ctx, candidate, request.Service)
	if err != nil {
		return compose.Source{}, containerconfig.Spec{}, err
	}

	return candidate, proposed, nil
}

func deploymentServiceSpec(
	ctx context.Context,
	source compose.Source,
	service string,
) (containerconfig.Spec, error) {
	project, err := compose.Load(ctx, source)
	if err != nil {
		return containerconfig.Spec{}, errDeploymentEditInvalid
	}
	spec, err := project.ServiceSpec(service)
	if err != nil {
		return containerconfig.Spec{}, errDeploymentEditInvalid
	}

	return spec, nil
}

func captureDeploymentRestore(
	ctx context.Context,
	source compose.Source,
	currentTree string,
	content []byte,
	environment map[string]string,
	runtimeBase string,
) (compose.Source, error) {
	if source.Repository == nil || !validGitObjectID(currentTree) {
		return compose.Source{}, errDeploymentEditInvalid
	}
	snapshot := source.Repository
	currentEntry, found := snapshot.Files[snapshot.Entry]
	if !found {
		return compose.Source{}, errDeploymentEditInvalid
	}
	candidate, err := compose.CaptureRepositorySource(
		snapshot.Root,
		snapshot.Entry,
		environment,
		func(name string) (compose.RepositoryFile, bool, error) {
			if name == snapshot.Entry {
				return compose.RepositoryFile{
					Content: slices.Clone(content), Executable: currentEntry.Executable,
				}, true, nil
			}

			return readCommittedGitFile(ctx, snapshot.Root, currentTree, name)
		},
		func(name string) (compose.RepositoryPathSnapshot, error) {
			if name == snapshot.Entry {
				return compose.RepositoryPathSnapshot{Files: map[string]compose.RepositoryFile{
					name: {Content: slices.Clone(content), Executable: currentEntry.Executable},
				}}, nil
			}

			return readCommittedGitPath(ctx, snapshot.Root, currentTree, name)
		},
	)
	if err != nil {
		return compose.Source{}, errDeploymentEditInvalid
	}
	candidate, err = compose.PinRepositoryRuntime(candidate, runtimeBase)
	if err != nil {
		return compose.Source{}, errDeploymentEditInvalid
	}

	return candidate, nil
}
