package cli

import (
	"bytes"
	"context"
	"errors"
	"os"
	"path/filepath"
	"slices"
	"sync"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type tuiDeploymentDraft struct {
	request      application.Request
	source       compose.Source
	candidate    compose.Source
	confirmation tuiDeploymentConfirmation
	repository   string
	entry        string
	base         gitTreeState
	fields       []application.DeploymentField
	current      containerconfig.Spec
	proposed     containerconfig.Spec
	restore      string
	scope        compose.RepositoryScope
}

type tuiStagedDeployment struct {
	draft   tuiDeploymentDraft
	proof   tuiStagedProof
	content []byte
}

type tuiDeploymentWorkspace struct {
	mu               sync.Mutex
	registrationPath string
	environment      map[string]string
	runtimeBase      string
	draft            *tuiDeploymentDraft
	staged           *tuiStagedDeployment
}

func publicDeploymentActionError(err error, fallback tui.DeploymentFailure) error {
	code := fallback
	switch {
	case errors.Is(err, errDeploymentWorktreeUnknown):
		code = tui.DeploymentWorktreeUnknown
	case errors.Is(err, errDeploymentPublishFailed):
		code = tui.DeploymentPublishFailed
	}
	action := &tui.DeploymentActionError{Code: code}
	if errors.Is(err, context.Canceled) {
		return errors.Join(action, errDeploymentEditInvalid, context.Canceled)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		return errors.Join(action, errDeploymentEditInvalid, context.DeadlineExceeded)
	}

	return errors.Join(action, errDeploymentEditInvalid)
}

func defaultTUIDeploymentWorkspace(environment map[string]string) *tuiDeploymentWorkspace {
	statePath, err := defaultStatePath(environment)
	if err != nil {
		return &tuiDeploymentWorkspace{environment: environment}
	}

	return &tuiDeploymentWorkspace{
		registrationPath: gitOpsRegistrationPath(statePath),
		environment:      environment,
		runtimeBase:      filepath.Dir(statePath),
	}
}

func (workspace *tuiDeploymentWorkspace) Fields(
	ctx context.Context,
	request application.Request,
) ([]tui.DeploymentFieldState, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	source, _, _, _, err := workspace.openRequest(ctx, request) //nolint:dogsled // This read needs only the proven source.
	if err != nil {
		return nil, publicDeploymentActionError(err, tui.DeploymentPreconditionFailed)
	}
	project, err := compose.Load(ctx, source)
	if err != nil {
		return nil, publicDeploymentActionError(err, tui.DeploymentUnsupportedSource)
	}
	spec, err := project.ServiceSpec(request.Service)
	if err != nil {
		return nil, publicDeploymentActionError(err, tui.DeploymentUnsupportedSource)
	}

	return application.DeploymentFieldStates(spec), nil
}

func (workspace *tuiDeploymentWorkspace) Preview(
	ctx context.Context,
	request application.Request,
	fieldID string,
	value string,
	unset bool,
) (tui.DeploymentEditPreview, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			errDeploymentEditInvalid, tui.DeploymentPreconditionFailed,
		)
	}
	workspace.draft = nil

	field, err := application.ParseDeploymentField(fieldID)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentValidationFailed,
		)
	}
	patch, err := parseDeploymentPatch(field, value, unset)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentValidationFailed,
		)
	}

	return workspace.previewPatches(ctx, request, []application.DeploymentPatch{patch})
}

func (workspace *tuiDeploymentWorkspace) PreviewPatches(
	ctx context.Context,
	request application.Request,
	patches []application.DeploymentPatch,
) (tui.DeploymentEditPreview, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			errDeploymentEditInvalid, tui.DeploymentPreconditionFailed,
		)
	}
	workspace.draft = nil

	return workspace.previewPatches(ctx, request, patches)
}

//nolint:funcorder // Preview and PreviewPatches share this private transaction boundary.
func (workspace *tuiDeploymentWorkspace) previewPatches(
	ctx context.Context,
	request application.Request,
	patches []application.DeploymentPatch,
) (tui.DeploymentEditPreview, error) {
	source, repository, entry, base, err := workspace.openRequest(ctx, request)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}
	candidate, err := prepareDeploymentEdit(ctx, source, request.Service, patches)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentUnsupportedSource,
		)
	}
	if len(candidate.fields) == 0 {
		workspace.draft = nil

		return tui.DeploymentEditPreview{NoChanges: true}, nil
	}
	scope, err := workspace.requestScope(ctx, request, source)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}
	draft := tuiDeploymentDraft{
		request: request, source: source, candidate: candidate.source, repository: repository,
		entry: entry, base: base, fields: candidate.fields,
		current: candidate.current, proposed: candidate.proposed, scope: scope,
	}

	preview, err := workspace.confirmDeploymentPreview(ctx, draft)
	if err != nil {
		return tui.DeploymentEditPreview{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}

	return preview, nil
}

//nolint:funcorder // Preview and restore share this private pre-mutation confirmation boundary.
func (workspace *tuiDeploymentWorkspace) confirmDeploymentPreview(
	ctx context.Context,
	draft tuiDeploymentDraft,
) (tui.DeploymentEditPreview, error) {
	confirmation, noChanges, err := prepareDeploymentConfirmation(ctx, draft)
	if err != nil {
		return tui.DeploymentEditPreview{}, err
	}
	if noChanges {
		workspace.draft = nil

		return tui.DeploymentEditPreview{NoChanges: true}, nil
	}
	draft.confirmation = confirmation
	workspace.draft = &draft

	return publicDeploymentPreview(draft), nil
}

func parseDeploymentPatch(
	field application.DeploymentField,
	value string,
	unset bool,
) (application.DeploymentPatch, error) {
	if len(value) > maximumTUICommitMessage {
		return application.DeploymentPatch{}, errDeploymentEditInvalid
	}
	patch, err := application.ParseDeploymentPatch(field.ID(), value, unset)
	if err != nil {
		return application.DeploymentPatch{}, errDeploymentEditInvalid
	}

	return patch, nil
}

func publicDeploymentPreview(draft tuiDeploymentDraft) tui.DeploymentEditPreview {
	return tui.DeploymentEditPreview{
		ComposePath: draft.entry,
		Changes:     deploymentFieldChanges(draft.current, draft.proposed, draft.fields),
		Restore:     draft.restore,
		Diff:        string(draft.confirmation.diff),
	}
}

func deploymentFieldChanges(
	current containerconfig.Spec,
	proposed containerconfig.Spec,
	fields []application.DeploymentField,
) []tui.DeploymentFieldChange {
	selected := make(map[application.DeploymentField]struct{}, len(fields))
	for _, field := range fields {
		selected[field] = struct{}{}
	}
	currentStates := application.DeploymentFieldStates(current)
	proposedStates := application.DeploymentFieldStates(proposed)
	changes := make([]tui.DeploymentFieldChange, 0, len(fields))
	for index, field := range application.DeploymentFields() {
		if _, valid := selected[field]; !valid {
			continue
		}
		before := currentStates[index]
		after := proposedStates[index]
		if before.Value == after.Value && before.Present == after.Present {
			continue
		}
		changes = append(changes, tui.DeploymentFieldChange{
			FieldID: field.ID(), CurrentValue: before.Value, CurrentPresent: before.Present,
			ProposedValue: after.Value, ProposedPresent: after.Present,
		})
	}

	return changes
}

//nolint:cyclop,funcorder // This private request proof stays before the transaction methods that consume it.
func (workspace *tuiDeploymentWorkspace) openRequest(
	ctx context.Context,
	request application.Request,
) (compose.Source, string, string, gitTreeState, error) {
	if request.Service == "" || request.Source.Repository == nil {
		return compose.Source{}, "", "", gitTreeState{}, errDeploymentEditInvalid
	}
	snapshot := request.Source.Repository
	if !filepath.IsAbs(snapshot.Root) || filepath.Clean(snapshot.Root) != snapshot.Root ||
		!validGitTreePath(snapshot.Entry) {
		return compose.Source{}, "", "", gitTreeState{}, errDeploymentEditInvalid
	}
	path := filepath.Join(snapshot.Root, filepath.FromSlash(snapshot.Entry))
	source, err := loadTrackedComposeSource(
		ctx, path, snapshot.Root, workspace.environment, workspace.runtimeBase,
	)
	if err != nil || source.Repository == nil || source.Repository.Root != snapshot.Root ||
		source.Repository.Entry != snapshot.Entry || source.Repository.Digest != snapshot.Digest ||
		!bytes.Equal(source.Content, request.Source.Content) {
		return compose.Source{}, "", "", gitTreeState{}, errDeploymentEditInvalid
	}
	state, err := cleanGitTree(ctx, snapshot.Root)
	if err != nil {
		return compose.Source{}, "", "", gitTreeState{}, errDeploymentEditInvalid
	}

	return source, snapshot.Root, snapshot.Entry, state, nil
}

//nolint:funcorder // This private proof helper stays beside openRequest, which consumes the same source contract.
func (workspace *tuiDeploymentWorkspace) requestScope(
	ctx context.Context,
	request application.Request,
	source compose.Source,
) (compose.RepositoryScope, error) {
	if request.Repository == (compose.RepositoryProvenance{}) {
		return compose.RepositoryScope{}, nil
	}
	registration, err := readGitOpsRegistration(workspace.registrationPath)
	if err != nil || source.Repository == nil || registration.Repository != source.Repository.Root {
		return compose.RepositoryScope{}, errDeploymentEditInvalid
	}
	scope, err := gitOpsRepositoryScope(ctx, registration, source.Repository.Root)
	if err != nil {
		return compose.RepositoryScope{}, errDeploymentEditInvalid
	}
	provenance, err := scope.Bind(source.Repository.Entry, source.Repository.Digest)
	if err != nil || provenance != request.Repository {
		return compose.RepositoryScope{}, errDeploymentEditInvalid
	}

	return scope, nil
}

func (workspace *tuiDeploymentWorkspace) Stage(
	ctx context.Context,
) (tui.StagedDeploymentEdit, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.draft == nil || workspace.staged != nil {
		return tui.StagedDeploymentEdit{}, publicDeploymentActionError(
			errDeploymentEditInvalid, tui.DeploymentPreconditionFailed,
		)
	}

	draft := *workspace.draft
	staged, diff, err := stageDeploymentEdit(ctx, draft)
	if err != nil {
		return tui.StagedDeploymentEdit{}, publicDeploymentActionError(
			err, tui.DeploymentPreconditionFailed,
		)
	}
	workspace.draft = nil
	workspace.staged = &staged
	message := "Update " + draft.request.Service + " deployment"
	if draft.restore != "" {
		message = "Restore " + draft.request.Service + " deployment"
	}

	return tui.StagedDeploymentEdit{Diff: string(diff), ComposePath: draft.entry, CommitMessage: message}, nil
}

func stageDeploymentEdit(
	ctx context.Context,
	draft tuiDeploymentDraft,
) (tuiStagedDeployment, []byte, error) {
	return stageDeploymentEditWith(ctx, draft, replaceDeploymentEntry, stagedTUIDiff, writeGitTree)
}

func stageDeploymentEditWith(
	ctx context.Context,
	draft tuiDeploymentDraft,
	replace func(string, string, []byte, []byte) (bool, error),
	diffStaged func(context.Context, string, []string) ([]byte, error),
	writeTree func(context.Context, string) (string, error),
) (tuiStagedDeployment, []byte, error) {
	if draft.confirmation.expectedTree == "" {
		return tuiStagedDeployment{}, nil, errDeploymentEditInvalid
	}
	state, err := cleanGitTreeWithAttributeSource(ctx, draft.repository, draft.base.tree)
	if err != nil || state != draft.base {
		return tuiStagedDeployment{}, nil, errDeploymentEditInvalid
	}
	rollback := func(stageErr error) (tuiStagedDeployment, []byte, error) {
		rescueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
		defer cancel()
		rollbackErr := rollbackDeploymentEdit(rescueCtx, draft)
		if rollbackErr != nil {
			return tuiStagedDeployment{}, nil, errors.Join(
				errDeploymentWorktreeUnknown, stageErr, rollbackErr,
			)
		}

		return tuiStagedDeployment{}, nil, errors.Join(errDeploymentPublishFailed, stageErr)
	}
	if err = stageConfirmedDeploymentWith(
		ctx, draft, replace, defaultDeploymentGitFileOperations(),
	); err != nil {
		return tuiStagedDeployment{}, nil, err
	}
	paths := []string{draft.entry}
	diff, err := diffStaged(ctx, draft.repository, paths)
	if err != nil {
		return rollback(err)
	}
	expectedTree, err := writeTree(ctx, draft.repository)
	if err != nil {
		return rollback(err)
	}
	proof := tuiStagedProof{
		repository: draft.repository, base: draft.base, paths: paths,
		indexStatus: "M", expectedTree: expectedTree, attributeSource: draft.base.tree,
	}
	content, err := validatedStagedDeploymentContent(ctx, draft, proof)
	if err != nil {
		return rollback(errDeploymentEditInvalid)
	}
	if !deploymentConfirmationMatches(draft.confirmation, content, diff, expectedTree) {
		return rollback(errDeploymentEditInvalid)
	}

	return tuiStagedDeployment{draft: draft, proof: proof, content: content}, diff, nil
}

func deploymentAttributeOutputSafe(output []byte, entry string) bool {
	fields, valid := splitNullTerminated(output)
	if !valid || len(fields)%3 != 0 {
		return false
	}
	for index := 0; index < len(fields); index += 3 {
		if string(fields[index]) != entry ||
			string(fields[index+1]) == "filter" &&
				string(fields[index+2]) != "unset" && string(fields[index+2]) != "unspecified" {
			return false
		}
	}

	return true
}

func validatedStagedDeploymentContent(
	ctx context.Context,
	draft tuiDeploymentDraft,
	proof tuiStagedProof,
) ([]byte, error) {
	file, found, err := readCommittedGitFile(ctx, proof.repository, proof.expectedTree, draft.entry)
	if err != nil || !found {
		return nil, errDeploymentEditInvalid
	}
	content := file.Content
	if err = validateDeploymentContent(ctx, draft, content); err != nil {
		return nil, errDeploymentEditInvalid
	}

	return content, nil
}

func deploymentTreeContains(
	ctx context.Context,
	proof tuiStagedProof,
	entry string,
	content []byte,
) bool {
	treeEntry, found, err := readGitTreeEntry(ctx, proof.repository, proof.expectedTree, entry)
	if err != nil || !found || !treeEntry.regularFile() {
		return false
	}
	actual, err := readGitBlob(ctx, proof.repository, treeEntry.object)

	return err == nil && bytes.Equal(actual, content)
}

func rollbackDeploymentEdit(ctx context.Context, draft tuiDeploymentDraft) (returnErr error) {
	return rollbackDeploymentEditWith(
		ctx, draft, replaceDeploymentEntry, defaultDeploymentGitFileOperations(),
	)
}

//nolint:cyclop,funlen // Index lock ownership, worktree compensation, and publication form one rollback transaction.
func rollbackDeploymentEditWith(
	ctx context.Context,
	draft tuiDeploymentDraft,
	replace func(string, string, []byte, []byte) (bool, error),
	operations deploymentGitFileOperations,
) (returnErr error) {
	indexPath, err := absoluteGitPath(ctx, draft.repository, "index")
	if err != nil {
		return errDeploymentEditInvalid
	}
	originalDigest, err := createDeploymentIndexLockWithOperations(indexPath, operations)
	if err != nil {
		return err
	}
	lockPath := indexPath + ".lock"
	lockOwned := true
	defer func() {
		if !lockOwned {
			return
		}
		if removeErr := operations.remove(lockPath); removeErr != nil && !errors.Is(removeErr, os.ErrNotExist) {
			returnErr = errors.Join(returnErr, removeErr)
		}
	}()

	head, err := resolveGitObject(ctx, draft.repository, "HEAD^{commit}")
	if err != nil || head != draft.base.head {
		return errDeploymentEditInvalid
	}
	indexTree, err := deploymentIndexTree(ctx, draft.repository, lockPath)
	if err != nil {
		return err
	}
	baseEntry, baseFound, baseErr := readGitTreeEntry(ctx, draft.repository, draft.base.tree, draft.entry)
	confirmedEntry, confirmedFound, confirmedErr := readGitTreeEntry(
		ctx, draft.repository, draft.confirmation.expectedTree, draft.entry,
	)
	indexEntry, indexFound, indexErr := readGitTreeEntry(ctx, draft.repository, indexTree, draft.entry)
	if baseErr != nil || confirmedErr != nil || indexErr != nil || !baseFound || !confirmedFound || !indexFound {
		return errors.Join(errDeploymentEditInvalid, baseErr, confirmedErr, indexErr)
	}
	indexChanged := indexEntry == confirmedEntry
	indexTargetDrifted := !indexChanged && indexEntry != baseEntry
	if indexChanged {
		if _, err = runGitProcess(
			ctx, draft.repository, false, nil, []string{"GIT_INDEX_FILE=" + lockPath},
			"reset", "--quiet", draft.base.tree, "--", draft.entry,
		); err != nil {
			return err
		}
		indexTree, err = deploymentIndexTree(ctx, draft.repository, lockPath)
		resetEntry, resetFound, resetErr := readGitTreeEntry(ctx, draft.repository, indexTree, draft.entry)
		if err != nil || resetErr != nil || !resetFound || resetEntry != baseEntry {
			return errors.Join(errDeploymentEditInvalid, err, resetErr)
		}
	}

	_, replaceErr := replace(
		draft.repository, draft.entry, draft.candidate.Content, draft.source.Content,
	)
	if indexTargetDrifted {
		return errors.Join(errDeploymentEditInvalid, replaceErr)
	}
	if replaceErr != nil {
		return replaceErr
	}
	currentDigest, digestErr := deploymentIndexDigestWithOperations(indexPath, operations)
	head, headErr := resolveGitObject(ctx, draft.repository, "HEAD^{commit}")
	if digestErr != nil || currentDigest != originalDigest || headErr != nil || head != draft.base.head {
		_, compensateErr := replace(
			draft.repository, draft.entry, draft.source.Content, draft.candidate.Content,
		)

		return errors.Join(errDeploymentEditInvalid, digestErr, headErr, compensateErr)
	}
	if indexChanged {
		if err = operations.rename(lockPath, indexPath); err != nil {
			_, compensateErr := replace(
				draft.repository, draft.entry, draft.source.Content, draft.candidate.Content,
			)

			return errors.Join(err, compensateErr)
		}
		lockOwned = false
	}

	return nil
}

func (workspace *tuiDeploymentWorkspace) Commit(
	ctx context.Context,
	message string,
	unsigned bool,
) (tui.CommitResult, error) {
	return workspace.commitWith(ctx, message, unsigned, commitTUIStagedProof)
}

//nolint:funcorder // The private commit phase stays beside the exported transaction entrypoint.
func (workspace *tuiDeploymentWorkspace) commitWith(
	ctx context.Context,
	message string,
	unsigned bool,
	commit func(context.Context, tuiStagedProof, string, bool) error,
) (tui.CommitResult, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged == nil || !validTUICommitMessage(message) {
		return tui.CommitResult{}, publicDeploymentActionError(
			errDeploymentEditInvalid, tui.DeploymentPreconditionFailed,
		)
	}
	staged := *workspace.staged
	if !verifyTUIStagedProof(ctx, staged.proof) ||
		!deploymentTreeContains(ctx, staged.proof, staged.draft.entry, staged.content) {
		return tui.CommitResult{}, publicDeploymentActionError(
			errDeploymentEditInvalid, tui.DeploymentPreconditionFailed,
		)
	}
	if !unsigned {
		if err := validateGitProcessConfiguration(ctx, staged.draft.repository); err != nil {
			return tui.CommitResult{Outcome: tui.CommitNeedsUnsignedApproval}, nil
		}
	}

	commitErr := commit(ctx, staged.proof, message, unsigned)

	return workspace.settleCommit(ctx, staged, message, unsigned, commitErr)
}

//nolint:funcorder // Settlement immediately follows the private commit phase that invokes it.
func (workspace *tuiDeploymentWorkspace) settleCommit(
	ctx context.Context,
	staged tuiStagedDeployment,
	message string,
	unsigned bool,
	commitErr error,
) (tui.CommitResult, error) {
	proofCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
	defer cancel()
	committedHead, unchanged := proveTUIStagedCommit(proofCtx, staged.proof, message, !unsigned)
	committed := committedHead != "" && deploymentTreeContains(
		proofCtx, staged.proof, staged.draft.entry, staged.content,
	)
	if committed {
		workspace.staged = nil
		workspace.draft = nil
		request, requestErr := workspace.committedRequest(proofCtx, staged, committedHead)
		if requestErr != nil {
			return tui.CommitResult{Outcome: tui.CommitValidationUnavailable}, nil
		}

		return tui.CommitResult{Request: request, Outcome: tui.CommitSucceeded}, nil
	}
	cancelErr := ctx.Err()
	needsUnsigned, outcomeErr := resolveUnsettledTUICommit(cancelErr, commitErr, unsigned, unchanged)
	if outcomeErr != nil {
		if !unchanged {
			return tui.CommitResult{}, publicDeploymentActionError(
				outcomeErr, tui.DeploymentWorktreeUnknown,
			)
		}

		return tui.CommitResult{}, publicDeploymentActionError(
			outcomeErr, tui.DeploymentCommitFailed,
		)
	}
	if needsUnsigned {
		return tui.CommitResult{Outcome: tui.CommitNeedsUnsignedApproval}, nil
	}

	code := tui.DeploymentCommitFailed
	if !unchanged {
		code = tui.DeploymentWorktreeUnknown
	}

	return tui.CommitResult{}, publicDeploymentActionError(errDeploymentEditInvalid, code)
}

//nolint:funcorder // Readback stays next to Commit because it proves that method's only successful result.
func (workspace *tuiDeploymentWorkspace) committedRequest(
	ctx context.Context,
	staged tuiStagedDeployment,
	head string,
) (application.Request, error) {
	source, err := compose.CaptureRepositorySource(
		staged.draft.repository,
		staged.draft.entry,
		workspace.environment,
		func(name string) (compose.RepositoryFile, bool, error) {
			return readCommittedGitFile(ctx, staged.draft.repository, staged.proof.expectedTree, name)
		},
		func(name string) (compose.RepositoryPathSnapshot, error) {
			return readCommittedGitPath(ctx, staged.draft.repository, staged.proof.expectedTree, name)
		},
	)
	if err != nil {
		return application.Request{}, errDeploymentEditInvalid
	}
	source, err = compose.PinRepositoryRuntime(source, workspace.runtimeBase)
	if err != nil {
		return application.Request{}, errDeploymentEditInvalid
	}
	project, err := compose.Load(ctx, source)
	if err != nil {
		return application.Request{}, errDeploymentEditInvalid
	}
	if _, err = project.ServiceSpec(staged.draft.request.Service); err != nil {
		return application.Request{}, errDeploymentEditInvalid
	}
	state, err := cleanGitTreeWithAttributeSource(
		ctx, staged.draft.repository, staged.proof.attributeSource,
	)
	if err != nil || state != (gitTreeState{head: head, tree: staged.proof.expectedTree}) {
		return application.Request{}, errDeploymentEditInvalid
	}
	request := application.Request{Source: source, Service: staged.draft.request.Service}
	if staged.draft.scope.Valid() {
		request.Repository, err = staged.draft.scope.Bind(staged.draft.entry, source.Repository.Digest)
	}

	return request, errors.Join(err)
}

func (workspace *tuiDeploymentWorkspace) Discard(ctx context.Context) error {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged == nil {
		workspace.draft = nil

		return nil
	}
	staged := *workspace.staged
	if staged.proof.repository != staged.draft.repository || staged.proof.base != staged.draft.base ||
		!slices.Equal(staged.proof.paths, []string{staged.draft.entry}) || staged.proof.indexStatus != "M" ||
		staged.proof.attributeSource != staged.draft.base.tree ||
		staged.proof.expectedTree != staged.draft.confirmation.expectedTree ||
		!deploymentTreeContains(ctx, staged.proof, staged.draft.entry, staged.content) {
		return publicDeploymentActionError(errDeploymentEditInvalid, tui.DeploymentWorktreeUnknown)
	}
	rescueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
	defer cancel()
	if err := rollbackDeploymentEdit(rescueCtx, staged.draft); err != nil {
		return publicDeploymentActionError(err, tui.DeploymentWorktreeUnknown)
	}
	workspace.staged = nil
	workspace.draft = &staged.draft

	return nil
}
