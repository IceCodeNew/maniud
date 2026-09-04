// Package tui presents the application façade without opening runtime or
// journal resources itself.
package tui

import (
	"context"
	"errors"
	"fmt"
	"io"
	"sync/atomic"
	"time"

	tea "charm.land/bubbletea/v2"
	"github.com/IceCodeNew/maniud/internal/application"
	"github.com/IceCodeNew/maniud/internal/llm"
	"github.com/charmbracelet/x/term"
)

const (
	eventQueueCapacity      = 64
	workspaceSuspendTimeout = 30 * time.Second
)

var errInvalidInput = errors.New("TUI input is invalid")

// CatalogState describes whether registered services can be listed.
type CatalogState string

const (
	// CatalogReady identifies a readable service registration.
	CatalogReady CatalogState = "ready"
	// CatalogMissing identifies a host without a registered repository.
	CatalogMissing CatalogState = "missing"
	// CatalogUnavailable identifies registration state that cannot be trusted.
	CatalogUnavailable CatalogState = "unavailable"
)

// SourceBlocker is a stable, privacy-safe reason that a source cannot open.
type SourceBlocker string

const (
	// BlockerNone identifies a source ready for application operations.
	BlockerNone SourceBlocker = ""
	// BlockerInvalid identifies a Compose source that could not pass validation.
	BlockerInvalid SourceBlocker = "compose_source_invalid"
	// BlockerNotFound identifies a source absent from a fresh inventory.
	BlockerNotFound SourceBlocker = "compose_source_not_found"
	// BlockerUnavailable identifies source state that cannot be read safely.
	BlockerUnavailable SourceBlocker = "compose_source_unavailable"
)

// SourceDiagnosticReason is a stable Compose validation category rendered by the TUI.
type SourceDiagnosticReason string

const (
	// DiagnosticYAMLSyntax identifies unreadable YAML syntax.
	DiagnosticYAMLSyntax SourceDiagnosticReason = "yaml_syntax_invalid"
	// DiagnosticYAMLStructure identifies an invalid YAML mapping or value shape.
	DiagnosticYAMLStructure SourceDiagnosticReason = "yaml_structure_invalid"
	// DiagnosticYAMLUnsupported identifies unsupported YAML features.
	DiagnosticYAMLUnsupported SourceDiagnosticReason = "yaml_feature_unsupported"
	// DiagnosticComposeValidation identifies input rejected by Compose validation.
	DiagnosticComposeValidation SourceDiagnosticReason = "compose_validation_failed"
)

// SourceDiagnostic is a privacy-safe projection of a Compose validation failure.
type SourceDiagnostic struct {
	File   string
	Reason SourceDiagnosticReason
	Line   int
	Column int
}

// Service is one registered inventory row. ID is opaque outside Catalog.
type Service struct {
	ID         string
	Location   string
	Project    string
	Name       string
	Runtime    string
	Blocker    SourceBlocker
	Diagnostic SourceDiagnostic
}

// CatalogSnapshot is one fresh registered-service inventory.
type CatalogSnapshot struct {
	State               CatalogState
	Services            []Service
	SuggestedRepository string
}

// RepositorySetupMode selects the owned onboarding path for a desired-state checkout.
type RepositorySetupMode uint8

const (
	// RepositorySetupCreateGitHub creates a private repository through gh.
	RepositorySetupCreateGitHub RepositorySetupMode = iota + 1
	// RepositorySetupExisting clones or reuses an existing Git repository.
	RepositorySetupExisting
)

// RepositorySetupRequest is one confirmed repository onboarding effect.
type RepositorySetupRequest struct {
	Mode     RepositorySetupMode
	Remote   string
	Checkout string
	// Created retries local setup for a GitHub repository created earlier in this TUI session.
	Created bool
}

// RegistrationResult is one privacy-safe repository setup result.
type RegistrationResult struct {
	Snapshot           CatalogSnapshot
	Failure            RepositorySetupFailure
	RecoveryRepository string
}

// RepositorySetupFailure identifies a recoverable repository onboarding failure.
type RepositorySetupFailure string

const (
	// RepositorySetupReady identifies a completed repository setup.
	RepositorySetupReady RepositorySetupFailure = ""
	// RepositorySetupInvalidInput rejects an invalid repository or checkout request.
	RepositorySetupInvalidInput RepositorySetupFailure = "repository_setup_invalid_input"
	// RepositorySetupGitHubFailed means gh did not create the requested repository.
	RepositorySetupGitHubFailed RepositorySetupFailure = "github_repository_create_failed"
	// RepositorySetupCloneFailed means the repository could not be cloned locally.
	RepositorySetupCloneFailed RepositorySetupFailure = "repository_clone_failed"
	// RepositorySetupRegistrationFailed means a checkout could not be registered safely.
	RepositorySetupRegistrationFailed RepositorySetupFailure = "repository_registration_failed"
	// RepositorySetupUnavailable means the setup role could not run.
	RepositorySetupUnavailable RepositorySetupFailure = "repository_setup_unavailable"
)

// ServiceDraft is one generated, effect-free service candidate.
type ServiceDraft struct {
	Runtime      string
	Image        string
	Service      string
	ComposePath  string
	Preparation  string
	WarningCount int
	Recovered    bool
}

// StagedService is the exact Git index projection awaiting commit.
type StagedService struct {
	Diff          string
	ComposePath   string
	Preparation   string
	CommitMessage string
}

// CommitOutcome identifies one closed, proven Git commit result.
type CommitOutcome uint8

const (
	// CommitSucceeded means the commit and immutable apply request were proven.
	CommitSucceeded CommitOutcome = iota + 1
	// CommitNeedsUnsignedApproval means signing failed without changing the owned staged state.
	CommitNeedsUnsignedApproval
	// CommitValidationUnavailable means the commit succeeded but no immutable apply request could be prepared.
	CommitValidationUnavailable
	// CommitPreparationRequired means the committed service needs host preparation before validation.
	CommitPreparationRequired
)

// CommitResult is one proven commit outcome or an explicit unsigned fallback request.
type CommitResult struct {
	Request application.Request
	Outcome CommitOutcome
}

// DeploymentFieldState is one editable deployment field and its current value.
// An unavailable field cannot be edited for the selected service.
type DeploymentFieldState = application.DeploymentFieldState

// DeploymentFieldChange is one validated current/proposed field comparison.
type DeploymentFieldChange struct {
	FieldID         string
	CurrentValue    string
	CurrentPresent  bool
	ProposedValue   string
	ProposedPresent bool
}

// DeploymentEditPreview is one validated in-memory Compose candidate and the
// exact Git blob diff that a later file confirmation can stage.
type DeploymentEditPreview struct {
	ComposePath string
	Changes     []DeploymentFieldChange
	Restore     string
	Diff        string
	NoChanges   bool
}

// StagedDeploymentEdit is the exact modified-file Git index projection.
type StagedDeploymentEdit struct {
	Diff          string
	ComposePath   string
	CommitMessage string
}

// DeploymentFailure identifies one recoverable Compose or Git workspace outcome.
type DeploymentFailure string

const (
	// DeploymentPreconditionFailed means the confirmed source or Git state changed.
	DeploymentPreconditionFailed DeploymentFailure = "compose_edit_precondition_failed"
	// DeploymentUnsupportedSource rejects a source that cannot preserve the editor contract.
	DeploymentUnsupportedSource DeploymentFailure = "compose_edit_unsupported_source"
	// DeploymentValidationFailed rejects an invalid field value or candidate.
	DeploymentValidationFailed DeploymentFailure = "compose_edit_validation_failed"
	// DeploymentPublishFailed means staging failed after the original file was restored.
	DeploymentPublishFailed DeploymentFailure = "compose_edit_publish_failed"
	// DeploymentWorktreeUnknown requires manual Git worktree recovery.
	DeploymentWorktreeUnknown DeploymentFailure = "compose_edit_worktree_unknown"
	// DeploymentCommitFailed means Git did not commit the still-owned staged edit.
	DeploymentCommitFailed DeploymentFailure = "git_commit_failed"
	// DeploymentHistoryUnavailable means bounded history could not be read.
	DeploymentHistoryUnavailable DeploymentFailure = "parameter_history_unavailable"
	// DeploymentHistoryEntryInvalid means a selected revision cannot be restored.
	DeploymentHistoryEntryInvalid DeploymentFailure = "parameter_history_entry_invalid"
)

// DeploymentActionError carries no raw Git, filesystem, or Compose error text.
type DeploymentActionError struct {
	Code DeploymentFailure
}

func (err *DeploymentActionError) Error() string {
	if err == nil || err.Code == "" {
		return "deployment action failed"
	}

	return string(err.Code)
}

// DeploymentHistoryEntry is one first-parent commit that changed the selected Compose file.
type DeploymentHistoryEntry struct {
	Revision         string
	Subject          string
	SignaturePresent bool
}

// LLMConfiguration is the privacy-safe effective provider configuration.
type LLMConfiguration struct {
	Provider      string
	Model         string
	Endpoint      string
	Timeout       string
	Origin        string
	KeySource     string
	KeyConfigured bool
	Complete      bool
	Identity      string
	Warnings      []string
}

// LLMSettings is one TUI configuration draft. An empty APIKey preserves the
// current protected value; ClearAPIKey explicitly removes it from XDG config.
type LLMSettings struct {
	Provider    string
	Model       string
	Endpoint    string
	Timeout     string
	APIKey      string
	ClearAPIKey bool
}

// LLMResult is one bounded completion result. Token is opaque to the TUI and
// can accept one choice from the current pending result.
type LLMResult = llm.Result

// Stable configuration outcomes owned by the Assistant role. Provider action
// outcomes use llm.ErrorCode directly.
const (
	LLMConfigPathInvalid       llm.ErrorCode = "llm_config_path_invalid"
	LLMConfigReloadFailed      llm.ErrorCode = "config_reload_failed"
	LLMConfigSaveStale         llm.ErrorCode = "config_save_stale"
	LLMConfigSaveUnknown       llm.ErrorCode = "config_save_outcome_unknown"
	LLMConfigSavedReloadFailed llm.ErrorCode = "config_saved_reload_failed"
)

// Target is one validated service that can enter the operation façade.
type Target struct {
	Project string
	Service string
	Runtime string
	Request application.Request
}

// OpenResult is a typed, privacy-safe source lookup result.
type OpenResult struct {
	Targets    []Target
	Blocker    SourceBlocker
	Diagnostic SourceDiagnostic
}

// Catalog owns registered inventory and Compose source lookup for the TUI.
type Catalog interface {
	Snapshot(ctx context.Context) CatalogSnapshot
	OpenRegistered(ctx context.Context, id string) OpenResult
	OpenPath(ctx context.Context, path string) OpenResult
	Register(ctx context.Context, request RepositorySetupRequest) RegistrationResult
}

// ServiceWorkspace owns the generated file and Git transaction behind Add service.
type ServiceWorkspace interface {
	Preview(ctx context.Context, input string) (ServiceDraft, error)
	Stage(ctx context.Context) (StagedService, error)
	Commit(ctx context.Context, message string, unsigned bool) (CommitResult, error)
	Suspend(ctx context.Context) error
}

// DeploymentWorkspace owns manual Compose edits and their Git history transaction.
type DeploymentWorkspace interface {
	Fields(ctx context.Context, request application.Request) ([]DeploymentFieldState, error)
	Preview(
		ctx context.Context,
		request application.Request,
		fieldID string,
		value string,
		unset bool,
	) (DeploymentEditPreview, error)
	PreviewRestore(
		ctx context.Context,
		request application.Request,
		revision string,
	) (DeploymentEditPreview, error)
	Stage(ctx context.Context) (StagedDeploymentEdit, error)
	Commit(ctx context.Context, message string, unsigned bool) (CommitResult, error)
	Discard(ctx context.Context) error
	History(ctx context.Context, request application.Request) ([]DeploymentHistoryEntry, error)
}

// DeploymentPatchWorkspace previews one multi-field application patch set.
type DeploymentPatchWorkspace interface {
	PreviewPatches(
		ctx context.Context,
		request application.Request,
		patches []application.DeploymentPatch,
	) (DeploymentEditPreview, error)
}

// Assistant owns resolved configuration, provider networking, bounded chat,
// and stale-context validation outside the TUI model.
type Assistant interface {
	Configuration(ctx context.Context) (LLMConfiguration, error)
	Save(ctx context.Context, settings LLMSettings) (LLMConfiguration, error)
	Recommend(
		ctx context.Context,
		request application.Request,
		configurationIdentity string,
		question string,
	) (LLMResult, error)
	Accept(ctx context.Context, token string, choice int) error
	Close()
}

// Operations is the mutation façade consumed by the TUI.
type Operations interface {
	DryRun(ctx context.Context, request application.Request) (application.Plan, error)
	Apply(ctx context.Context, request application.Request) (application.Plan, error)
	ResolveHealth(
		ctx context.Context,
		request application.Request,
		resolution application.HealthResolution,
	) (application.Plan, error)
	Snapshot(ctx context.Context, request application.Request) (application.OperationSnapshot, error)
	Evidence(snapshot application.OperationSnapshot) (application.EvidenceBundle, error)
}

var _ Operations = (*application.ApplyFacade)(nil)

// IsTerminal reports whether one file descriptor is an interactive terminal.
func IsTerminal(descriptor uintptr) bool {
	return term.IsTerminal(descriptor)
}

// EventStream accepts bounded application events and exposes dropped-event
// counts to the application snapshot.
type EventStream struct {
	queue    chan eventMsg
	sequence atomic.Uint64
	dropped  atomic.Uint64
}

// NewEventStream creates one process-local event stream for a TUI session.
func NewEventStream() *EventStream {
	return &EventStream{queue: make(chan eventMsg, eventQueueCapacity)}
}

// TryPublish implements application.EventSink without blocking an operation.
func (stream *EventStream) TryPublish(event application.Event) bool {
	observation := eventMsg{sequence: stream.sequence.Load(), event: event}
	select {
	case stream.queue <- observation:
		return true
	default:
		stream.dropped.Add(1)

		return false
	}
}

// DroppedEvents implements application.EventDropCounter.
func (stream *EventStream) DroppedEvents() uint64 {
	return stream.dropped.Load()
}

func (stream *EventStream) bind(sequence uint64) {
	stream.sequence.Store(sequence)
}

func (stream *EventStream) wait(ctx context.Context) tea.Cmd {
	return func() tea.Msg {
		if ctx.Err() != nil {
			return contextDoneMsg{}
		}
		select {
		case observation := <-stream.queue:
			return messageForEvent(ctx, observation)
		case <-ctx.Done():
			return contextDoneMsg{}
		}
	}
}

//nolint:ireturn // Bubble Tea commands communicate through the tea.Msg extension contract.
func messageForEvent(ctx context.Context, observation eventMsg) tea.Msg {
	if ctx.Err() != nil {
		return contextDoneMsg{}
	}

	return observation
}

// Result is the bounded presentation value returned after Bubble Tea restores
// the terminal. Export is empty unless the user requested a session export.
type Result struct {
	Export string
}

// RunWithAssistant opens one session with the optional LLM capability enabled.
func RunWithAssistant(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog Catalog,
	workspace ServiceWorkspace,
	deployments DeploymentWorkspace,
	assistant Assistant,
	operations Operations,
	events *EventStream,
	options Options,
) (Result, error) {
	valid := catalog != nil && workspace != nil && deployments != nil && operations != nil && events != nil
	if assistant != nil {
		_, supportsPatches := deployments.(DeploymentPatchWorkspace)
		valid = valid && supportsPatches
	}
	if !valid {
		if assistant != nil {
			assistant.Close()
		}

		return Result{}, errInvalidInput
	}

	return run(ctx, input, output, catalog, workspace, deployments, assistant, operations, events, options)
}

func run(
	ctx context.Context,
	input io.Reader,
	output io.Writer,
	catalog Catalog,
	workspace ServiceWorkspace,
	deployments DeploymentWorkspace,
	assistant Assistant,
	operations Operations,
	events *EventStream,
	options Options,
) (Result, error) {
	runCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	state := newModelWithCapabilities(
		runCtx, catalog, workspace, deployments, assistant, operations, events, options,
	)
	_, err := tea.NewProgram(
		state,
		tea.WithInput(input),
		tea.WithOutput(output),
		tea.WithWindowSize(defaultWidth, defaultHeight),
	).Run()
	state.page = nil
	cancel()
	if assistant != nil {
		assistant.Close()
	}
	suspendCtx, cancelSuspend := context.WithTimeout(context.WithoutCancel(ctx), workspaceSuspendTimeout)
	suspendErr := workspace.Suspend(suspendCtx)
	cancelSuspend()
	discardCtx, cancelDiscard := context.WithTimeout(context.WithoutCancel(ctx), workspaceSuspendTimeout)
	discardErr := deployments.Discard(discardCtx)
	cancelDiscard()
	cleanupErr := errors.Join(suspendErr, discardErr)
	if err != nil {
		return state.result, errors.Join(fmt.Errorf("run TUI: %w", err), cleanupErr)
	}
	if state.err != nil {
		return state.result, errors.Join(fmt.Errorf("run TUI operation: %w", state.err), cleanupErr)
	}
	if err = ctx.Err(); err != nil {
		return state.result, errors.Join(fmt.Errorf("run TUI context: %w", err), cleanupErr)
	}

	return state.result, errors.Join(cleanupErr)
}
