package cli

import (
	"bytes"
	"context"
	"crypto/rand"
	"crypto/sha256"
	json "encoding/json/v2"
	"errors"
	"fmt"
	"io"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/IceCodeNew/maniud/containerconfig"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/compose"
	"github.com/IceCodeNew/maniud/internal/tui"
)

type tuiDeploymentDraft struct {
	request    application.Request
	source     compose.Source
	candidate  compose.Source
	repository string
	entry      string
	base       gitTreeState
	fields     []application.DeploymentField
	restore    string
	scope      compose.RepositoryScope
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

type tuiAssistContext struct {
	projection application.AssistProjection
	identity   string
	forbidden  map[string][]string
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

//nolint:funcorder // Source, runtime snapshot, projection, and postflight freeze one network context.
func (workspace *tuiDeploymentWorkspace) assistContext(
	ctx context.Context,
	request application.Request,
	operations tui.Operations,
) (tuiAssistContext, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if operations == nil || workspace.draft != nil || workspace.staged != nil {
		return tuiAssistContext{}, errDeploymentEditInvalid
	}

	return workspace.captureAssistContext(ctx, request, operations)
}

//nolint:funcorder // Acceptance rechecks the same source while an in-memory preview exists.
func (workspace *tuiDeploymentWorkspace) pendingAssistContext(
	ctx context.Context,
	request application.Request,
	operations tui.Operations,
) (tuiAssistContext, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if operations == nil || workspace.staged != nil {
		return tuiAssistContext{}, errDeploymentEditInvalid
	}

	return workspace.captureAssistContext(ctx, request, operations)
}

//nolint:funcorder // Both recommendation phases share one context identity implementation.
func (workspace *tuiDeploymentWorkspace) captureAssistContext(
	ctx context.Context,
	request application.Request,
	operations tui.Operations,
) (tuiAssistContext, error) {
	source, repository, _, base, err := workspace.openRequest(ctx, request)
	if err != nil {
		return tuiAssistContext{}, err
	}
	snapshot, err := operations.Snapshot(ctx, request)
	if err != nil {
		return tuiAssistContext{}, fmt.Errorf("capture deployment snapshot for LLM projection: %w", err)
	}
	projection, err := application.AssistProjectionFor(ctx, request, snapshot)
	if err != nil {
		return tuiAssistContext{}, fmt.Errorf("build LLM deployment projection: %w", err)
	}
	forbidden, err := assistForbiddenValues(ctx, source, request.Service, snapshot)
	if err != nil {
		return tuiAssistContext{}, err
	}
	_, rereadRepository, _, rereadBase, err := workspace.openRequest(ctx, request)
	if err != nil || rereadRepository != repository || rereadBase != base {
		return tuiAssistContext{}, errDeploymentEditInvalid
	}
	stableSnapshot := snapshot
	stableSnapshot.CapturedAt = time.Time{}
	stableSnapshot.DroppedEvents = 0
	snapshotJSON, _ := json.Marshal(&stableSnapshot, json.Deterministic(true))
	payload := strings.Join([]string{
		base.head, base.tree, request.Source.Repository.Digest.String(), projection.Identity, string(snapshotJSON),
	}, "\x00")

	return tuiAssistContext{
		projection: projection, identity: fmt.Sprintf("%x", sha256.Sum256([]byte(payload))),
		forbidden: forbidden,
	}, nil
}

//nolint:cyclop // This inventory collects each allowlisted private Compose/runtime value before network dispatch.
func assistForbiddenValues(
	ctx context.Context,
	source compose.Source,
	service string,
	snapshot application.OperationSnapshot,
) (map[string][]string, error) {
	project, err := compose.Load(ctx, source)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	spec, err := project.ServiceSpec(service)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	forbidden := map[string][]string{
		"command":    append(append([]string(nil), spec.Entrypoint...), spec.Command...),
		"runtime ID": {snapshot.Plan.Observation.ID},
	}
	if spec.Healthcheck != nil {
		forbidden["command"] = append(forbidden["command"], spec.Healthcheck.Test...)
	}
	if source.Repository != nil {
		forbidden["private path"] = append(forbidden["private path"], source.Repository.Root)
	}
	if image, imageErr := project.ImageInput(service); imageErr == nil {
		if registry, found := image.RegistrySource(); found {
			forbidden["image reference"] = append(forbidden["image reference"], registry.String())
		}
	}
	for _, port := range spec.Ports {
		forbidden["port"] = append(forbidden["port"],
			port.HostIP,
			strconv.FormatUint(uint64(port.PublishedPort), 10),
			strconv.FormatUint(uint64(port.TargetPort), 10),
			fmt.Sprintf("%s:%d:%d/%s", port.HostIP, port.PublishedPort, port.TargetPort, port.Protocol),
		)
	}
	for _, mount := range spec.Mounts {
		forbidden["mount"] = append(forbidden["mount"], mount.Source, mount.Target)
	}
	for _, device := range spec.Devices {
		forbidden["device"] = append(forbidden["device"], device.Source, device.Target)
	}
	for _, mount := range snapshot.Plan.Observation.RuntimeMounts {
		forbidden["runtime ID"] = append(forbidden["runtime ID"], mount.Name, mount.Source)
	}

	return forbidden, nil
}

func (workspace *tuiDeploymentWorkspace) Fields(
	ctx context.Context,
	request application.Request,
) ([]tui.DeploymentFieldState, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()

	source, _, _, _, err := workspace.openRequest(ctx, request) //nolint:dogsled // This read needs only the proven source.
	if err != nil {
		return nil, err
	}
	project, err := compose.Load(ctx, source)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	spec, err := project.ServiceSpec(request.Service)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}

	fields := make([]tui.DeploymentFieldState, 0, len(application.DeploymentFields()))
	for _, field := range application.DeploymentFields() {
		fields = append(fields, deploymentFieldState(spec, field))
	}

	return fields, nil
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
		return tui.DeploymentEditPreview{}, errDeploymentEditInvalid
	}

	field, err := application.ParseDeploymentField(fieldID)
	if err != nil {
		return tui.DeploymentEditPreview{}, errDeploymentEditInvalid
	}
	patch, err := parseDeploymentPatch(field, value, unset)
	if err != nil {
		return tui.DeploymentEditPreview{}, err
	}

	return workspace.previewPatches(ctx, request, []application.DeploymentPatch{patch})
}

func (workspace *tuiDeploymentWorkspace) PreviewRecommendation(
	ctx context.Context,
	request application.Request,
	changes []tui.LLMChange,
) (tui.DeploymentEditPreview, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged != nil || len(changes) == 0 || len(changes) > len(application.DeploymentFields()) {
		return tui.DeploymentEditPreview{}, errDeploymentEditInvalid
	}
	patches := make([]application.DeploymentPatch, 0, len(changes))
	seen := make(map[string]struct{}, len(changes))
	for _, change := range changes {
		if _, duplicate := seen[change.FieldID]; duplicate {
			return tui.DeploymentEditPreview{}, errDeploymentEditInvalid
		}
		patch, err := application.ParseDeploymentPatch(change.FieldID, change.Value, change.Unset)
		if err != nil {
			return tui.DeploymentEditPreview{}, errDeploymentEditInvalid
		}
		seen[change.FieldID] = struct{}{}
		patches = append(patches, patch)
	}

	return workspace.previewPatches(ctx, request, patches)
}

//nolint:funcorder // Preview and PreviewRecommendation share this private transaction boundary.
func (workspace *tuiDeploymentWorkspace) previewPatches(
	ctx context.Context,
	request application.Request,
	patches []application.DeploymentPatch,
) (tui.DeploymentEditPreview, error) {
	source, repository, entry, base, err := workspace.openRequest(ctx, request)
	if err != nil {
		return tui.DeploymentEditPreview{}, err
	}
	candidate, err := prepareDeploymentEdit(ctx, source, request.Service, patches)
	if err != nil || len(candidate.fields) == 0 {
		return tui.DeploymentEditPreview{}, errors.Join(err, errDeploymentEditInvalid)
	}
	scope, err := workspace.requestScope(ctx, request, source)
	if err != nil {
		return tui.DeploymentEditPreview{}, err
	}
	draft := tuiDeploymentDraft{
		request: request, source: source, candidate: candidate.source, repository: repository,
		entry: entry, base: base, fields: candidate.fields, scope: scope,
	}
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

//nolint:cyclop,funlen // Every closed application field has one stable display projection.
func deploymentFieldState(
	spec containerconfig.Spec,
	field application.DeploymentField,
) tui.DeploymentFieldState {
	result := tui.DeploymentFieldState{ID: field.ID(), AllowsUnset: field.AllowsUnset(), Available: true}
	switch field {
	case application.DeploymentCPUs:
		result.Value, result.Present = spec.CPUs, spec.CPUs != ""
	case application.DeploymentMemory:
		result.Value, result.Present = strconv.FormatInt(spec.MemoryBytes, 10), spec.MemoryBytes != 0
	case application.DeploymentPIDs:
		result.Value, result.Present = deploymentOptionalInt64(spec.PidsLimit)
	case application.DeploymentRestart:
		result.Value, result.Present = spec.Restart, spec.Restart != ""
	case application.DeploymentSharedMemory:
		result.Value, result.Present = strconv.FormatInt(spec.SharedMemoryBytes, 10), spec.SharedMemoryBytes != 0
	case application.DeploymentStopGrace:
		if spec.StopTimeout != nil {
			result.Value = (time.Duration(*spec.StopTimeout) * time.Second).String()
			result.Present = true
		}
	case application.DeploymentInit:
		result.Value, result.Present = deploymentOptionalBool(spec.Init)
	case application.DeploymentReadOnly:
		result.Value, result.Present = deploymentOptionalBool(spec.ReadOnly)
	case application.DeploymentNoNewPrivileges:
		result.Present = spec.NoNewPrivileges
		result.Available = !spec.NoNewPrivileges
		if result.Present {
			result.Value = "true"
		}
	case application.DeploymentHealthInterval:
		result.Value, result.Present, result.Available = deploymentHealthString(spec.Healthcheck, func(
			health *containerconfig.Healthcheck,
		) string {
			return health.Interval
		})
	case application.DeploymentHealthTimeout:
		result.Value, result.Present, result.Available = deploymentHealthString(spec.Healthcheck, func(
			health *containerconfig.Healthcheck,
		) string {
			return health.Timeout
		})
	case application.DeploymentHealthRetries:
		result.Available = spec.Healthcheck != nil && !spec.Healthcheck.Disabled
		if result.Available {
			result.Value, result.Present = deploymentOptionalInt(spec.Healthcheck.Retries)
		}
	case application.DeploymentHealthStartPeriod:
		result.Value, result.Present, result.Available = deploymentHealthString(spec.Healthcheck, func(
			health *containerconfig.Healthcheck,
		) string {
			return health.StartPeriod
		})
	case application.DeploymentHealthStartInterval:
		result.Value, result.Present, result.Available = deploymentHealthString(spec.Healthcheck, func(
			health *containerconfig.Healthcheck,
		) string {
			return health.StartInterval
		})
	default:
		result.Available = false
	}

	return result
}

func deploymentOptionalInt64(value *int64) (string, bool) {
	if value == nil {
		return "", false
	}

	return strconv.FormatInt(*value, 10), true
}

func deploymentOptionalInt(value *int) (string, bool) {
	if value == nil {
		return "", false
	}

	return strconv.Itoa(*value), true
}

func deploymentOptionalBool(value *bool) (string, bool) {
	if value == nil {
		return "", false
	}

	return strconv.FormatBool(*value), true
}

func deploymentHealthString(
	health *containerconfig.Healthcheck,
	selectValue func(*containerconfig.Healthcheck) string,
) (string, bool, bool) {
	if health == nil || health.Disabled {
		return "", false, false
	}
	value := selectValue(health)

	return value, value != "", true
}

func publicDeploymentPreview(draft tuiDeploymentDraft) tui.DeploymentEditPreview {
	fieldIDs := make([]string, len(draft.fields))
	for index, field := range draft.fields {
		fieldIDs[index] = field.ID()
	}

	return tui.DeploymentEditPreview{
		ComposePath: draft.entry, FieldIDs: fieldIDs, Restore: draft.restore,
	}
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
		return tui.StagedDeploymentEdit{}, errDeploymentEditInvalid
	}

	draft := *workspace.draft
	staged, diff, err := stageDeploymentEdit(ctx, draft)
	if err != nil {
		return tui.StagedDeploymentEdit{}, err
	}
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

//nolint:cyclop // Each linear branch rejects or rescues one transaction proof boundary.
func stageDeploymentEditWith(
	ctx context.Context,
	draft tuiDeploymentDraft,
	replace func(string, string, []byte, []byte) (bool, error),
	diffStaged func(context.Context, string, []string) ([]byte, error),
	writeTree func(context.Context, string) (string, error),
) (tuiStagedDeployment, []byte, error) {
	if !deploymentAttributesSafe(ctx, draft.repository, draft.base.tree, draft.entry) {
		return tuiStagedDeployment{}, nil, errDeploymentEditInvalid
	}
	state, err := cleanGitTreeWithAttributeSource(ctx, draft.repository, draft.base.tree)
	if err != nil || state != draft.base {
		return tuiStagedDeployment{}, nil, errDeploymentEditInvalid
	}
	rollback := func(stageErr error) (tuiStagedDeployment, []byte, error) {
		rescueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
		defer cancel()

		return tuiStagedDeployment{}, nil, errors.Join(stageErr, rollbackDeploymentEdit(rescueCtx, draft))
	}
	published, err := replace(
		draft.repository, draft.entry, draft.source.Content, draft.candidate.Content,
	)
	if err != nil {
		if published {
			return rollback(err)
		}

		return tuiStagedDeployment{}, nil, err
	}
	paths := []string{draft.entry}
	if _, err = runGit(
		ctx, draft.repository, "--attr-source="+draft.base.tree, "add", "--", draft.entry,
	); err != nil {
		return rollback(err)
	}
	if !exactStagedPathStatusWithAttributes(
		ctx, draft.repository, paths, "M", draft.base.tree,
	) {
		return rollback(errDeploymentEditInvalid)
	}
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

	return tuiStagedDeployment{draft: draft, proof: proof, content: content}, diff, nil
}

func deploymentAttributesSafe(ctx context.Context, repository, tree, entry string) bool {
	output, err := runGit(
		ctx, repository, "--attr-source="+tree, "check-attr", "-z", "--all", "--", entry,
	)
	if err != nil {
		return false
	}

	return deploymentAttributeOutputSafe(output, entry)
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
	source, err := draft.candidate.WithSemanticallyEquivalentEntryContent(content)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	project, err := compose.Load(ctx, source)
	if err != nil {
		return nil, errDeploymentEditInvalid
	}
	if _, err = project.ServiceSpec(draft.request.Service); err != nil {
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

type deploymentEntryOperations struct {
	openRoot      func(string) (*os.Root, error)
	lstat         func(*os.Root, string) (os.FileInfo, error)
	readFile      func(*os.Root, string) ([]byte, error)
	openFile      func(*os.Root, string, os.FileMode) (*os.File, error)
	writeFile     func(*os.File, []byte) (int, error)
	syncFile      func(*os.File) error
	closeFile     func(*os.File) error
	rename        func(*os.Root, string, string) error
	openDirectory func(*os.Root, string) (*os.File, error)
	remove        func(*os.Root, string) error
	closeRoot     func(*os.Root) error
}

func defaultDeploymentEntryOperations() deploymentEntryOperations {
	return deploymentEntryOperations{
		openRoot: os.OpenRoot,
		lstat:    (*os.Root).Lstat,
		readFile: (*os.Root).ReadFile,
		openFile: func(root *os.Root, name string, mode os.FileMode) (*os.File, error) {
			return root.OpenFile(name, os.O_WRONLY|os.O_CREATE|os.O_EXCL, mode)
		},
		writeFile: (*os.File).Write,
		syncFile:  (*os.File).Sync,
		closeFile: (*os.File).Close,
		rename:    (*os.Root).Rename,
		openDirectory: func(root *os.Root, name string) (*os.File, error) {
			return root.Open(name)
		},
		remove:    (*os.Root).Remove,
		closeRoot: (*os.Root).Close,
	}
}

func replaceDeploymentEntry(
	repository string,
	entry string,
	before []byte,
	after []byte,
) (published bool, returnErr error) {
	return replaceDeploymentEntryWithOperations(
		repository, entry, before, after, defaultDeploymentEntryOperations(),
	)
}

//nolint:cyclop,funlen // Each injected filesystem step contributes to the same atomic publication proof.
func replaceDeploymentEntryWithOperations(
	repository string,
	entry string,
	before []byte,
	after []byte,
	operations deploymentEntryOperations,
) (published bool, returnErr error) {
	root, err := operations.openRoot(repository)
	if err != nil {
		return false, errDeploymentEditInvalid
	}
	defer func() { returnErr = errors.Join(returnErr, operations.closeRoot(root)) }()
	name := filepath.FromSlash(entry)
	info, err := operations.lstat(root, name)
	if err != nil || !info.Mode().IsRegular() {
		return false, errDeploymentEditInvalid
	}
	current, err := operations.readFile(root, name)
	if err != nil || !bytes.Equal(current, before) {
		return false, errDeploymentEditInvalid
	}
	directory := filepath.Dir(name)
	temporary := filepath.Join(directory, "."+filepath.Base(name)+".maniud-"+rand.Text())
	file, err := operations.openFile(root, temporary, info.Mode().Perm())
	if err != nil {
		return false, fmt.Errorf("create deployment edit: %w", err)
	}
	defer func() {
		if !published {
			returnErr = errors.Join(returnErr, operations.remove(root, temporary))
		}
	}()
	written, writeErr := operations.writeFile(file, after)
	if writeErr == nil && written != len(after) {
		writeErr = io.ErrShortWrite
	}
	syncErr := operations.syncFile(file)
	closeErr := operations.closeFile(file)
	if writeErr != nil || syncErr != nil || closeErr != nil {
		return false, errors.Join(writeErr, syncErr, closeErr)
	}
	if err = operations.rename(root, temporary, name); err != nil {
		return false, fmt.Errorf("publish deployment edit: %w", err)
	}
	published = true
	directoryFile, err := operations.openDirectory(root, directory)
	if err != nil {
		return true, fmt.Errorf("open deployment edit directory: %w", err)
	}
	syncErr = operations.syncFile(directoryFile)
	closeErr = operations.closeFile(directoryFile)
	if syncErr != nil || closeErr != nil {
		return true, errors.Join(syncErr, closeErr)
	}
	current, err = operations.readFile(root, name)
	if err != nil || !bytes.Equal(current, after) {
		return true, errDeploymentEditInvalid
	}

	return true, nil
}

func rollbackDeploymentEdit(ctx context.Context, draft tuiDeploymentDraft) error {
	_, resetErr := runGit(
		ctx, draft.repository, "--attr-source="+draft.base.tree,
		"reset", "--quiet", "HEAD", "--", draft.entry,
	)
	_, replaceErr := replaceDeploymentEntry(
		draft.repository, draft.entry, draft.candidate.Content, draft.source.Content,
	)
	state, stateErr := cleanGitTreeWithAttributeSource(ctx, draft.repository, draft.base.tree)
	if stateErr == nil && state != draft.base {
		stateErr = errDeploymentEditInvalid
	}

	return errors.Join(resetErr, replaceErr, stateErr)
}

func (workspace *tuiDeploymentWorkspace) Commit(
	ctx context.Context,
	message string,
	unsigned bool,
) (tui.DeploymentCommitResult, error) {
	return workspace.commitWith(ctx, message, unsigned, commitTUIStagedProof)
}

//nolint:funcorder // The private commit phase stays beside the exported transaction entrypoint.
func (workspace *tuiDeploymentWorkspace) commitWith(
	ctx context.Context,
	message string,
	unsigned bool,
	commit func(context.Context, tuiStagedProof, string, bool) error,
) (tui.DeploymentCommitResult, error) {
	workspace.mu.Lock()
	defer workspace.mu.Unlock()
	if workspace.staged == nil || !validTUICommitMessage(message) {
		return tui.DeploymentCommitResult{}, errDeploymentEditInvalid
	}
	staged := *workspace.staged
	if !verifyTUIStagedProof(ctx, staged.proof) ||
		!deploymentTreeContains(ctx, staged.proof, staged.draft.entry, staged.content) {
		return tui.DeploymentCommitResult{}, errDeploymentEditInvalid
	}
	if !unsigned {
		if err := validateGitProcessConfiguration(ctx, staged.draft.repository); err != nil {
			return tui.DeploymentCommitResult{}, err
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
) (tui.DeploymentCommitResult, error) {
	proofCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
	defer cancel()
	committedHead, unchanged := proveTUIStagedCommit(proofCtx, staged.proof, message, !unsigned)
	if committedHead != "" && deploymentTreeContains(
		proofCtx, staged.proof, staged.draft.entry, staged.content,
	) {
		workspace.staged = nil
		workspace.draft = nil
		request, requestErr := workspace.committedRequest(proofCtx, staged, committedHead)
		if requestErr != nil {
			return tui.DeploymentCommitResult{Committed: true, ValidationUnavailable: true}, nil
		}

		return tui.DeploymentCommitResult{Request: request, Committed: true}, nil
	}
	if err := ctx.Err(); err != nil {
		return tui.DeploymentCommitResult{}, fmt.Errorf("commit deployment edit: %w", err)
	}
	if !unsigned && unchanged {
		return tui.DeploymentCommitResult{NeedsUnsignedApproval: true}, nil
	}
	if commitErr != nil {
		return tui.DeploymentCommitResult{}, commitErr
	}

	return tui.DeploymentCommitResult{}, errDeploymentEditInvalid
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
	if !verifyTUIStagedProof(ctx, staged.proof) ||
		!deploymentTreeContains(ctx, staged.proof, staged.draft.entry, staged.content) {
		return errDeploymentEditInvalid
	}
	rescueCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), gitCommandTimeout)
	defer cancel()
	if err := rollbackDeploymentEdit(rescueCtx, staged.draft); err != nil {
		return err
	}
	workspace.staged = nil
	workspace.draft = nil

	return nil
}
